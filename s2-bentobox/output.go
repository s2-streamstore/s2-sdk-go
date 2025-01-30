package s2bentobox

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// Errors.
var (
	ErrOutputClosed          = errors.New("output closed")
	ErrAppendRecordBatchFull = errors.New("append record batch is full")
)

type OutputConfig struct {
	*Config
	Stream       string
	MaxInFlight  int
	FencingToken []byte
}

type Output struct {
	closed atomic.Bool
	sendCh chan<- sendInput

	ackStreamCloser    <-chan struct{}
	appendWorkerCloser <-chan struct{}
	cancelSession      context.CancelFunc
}

func ConnectOutput(ctx context.Context, config *OutputConfig) (*Output, error) {
	client, err := newStreamClient(config.Config, config.Stream)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)

	sender, receiver, err := client.AppendSession(sessionCtx)
	if err != nil {
		cancelSession()

		return nil, err
	}

	var (
		sendCh             = make(chan sendInput, config.MaxInFlight)
		replyQ             = make(chan chan<- error, config.MaxInFlight)
		ackStreamCloser    = make(chan struct{})
		appendWorkerCloser = make(chan struct{})
	)

	go ackStream(receiver, replyQ, ackStreamCloser)
	go appendWorker(sender, sendCh, replyQ, config, appendWorkerCloser)

	return &Output{
		sendCh:             sendCh,
		ackStreamCloser:    ackStreamCloser,
		appendWorkerCloser: appendWorkerCloser,
		cancelSession:      cancelSession,
	}, nil
}

type sendInput struct {
	batch *s2.AppendRecordBatch
	reply chan<- error
}

func ackStream(
	receiver s2.Receiver[*s2.AppendOutput],
	replyQ <-chan chan<- error,
	ackStreamCloser chan<- struct{},
) {
	defer close(ackStreamCloser)

	for reply := range replyQ {
		_, err := receiver.Recv()
		reply <- err

		if err != nil {
			// End the stream since it's basically poisoned.
			return
		}
	}
}

func appendWorker(
	sender s2.Sender[*s2.AppendInput],
	sendCh <-chan sendInput,
	replyQ chan<- chan<- error,
	config *OutputConfig,
	appendWorkerCloser chan<- struct{},
) {
	defer close(appendWorkerCloser)
	defer close(replyQ)
	defer func(sender s2.Sender[*s2.AppendInput]) {
		_ = sender.CloseSend()
	}(sender)

	for s := range sendCh {
		input := s2.AppendInput{
			Records:      s.batch,
			FencingToken: config.FencingToken,
			MatchSeqNum:  nil,
		}

		if err := sender.Send(&input); err != nil {
			if errors.Is(err, io.EOF) {
				// Stream is closed. Exit immediately.
				return
			}

			// TODO: Add a retry limit here. This may keep on happening due to a connection error too.
			s.reply <- err

			continue
		}

		replyQ <- s.reply
	}
}

func (o *Output) waitForClosers(ctx context.Context, done error) error {
	if err := waitForClosers(ctx, o.ackStreamCloser, o.appendWorkerCloser); err != nil {
		return err
	}

	return done
}

func (o *Output) WriteBatch(ctx context.Context, batch *s2.AppendRecordBatch) error {
	reply := make(chan error, 1)

	select {
	case o.sendCh <- sendInput{
		batch: batch,
		reply: reply,
	}: // OK

	case <-ctx.Done():
		return ctx.Err()

	case <-o.appendWorkerCloser:
		return o.waitForClosers(ctx, ErrOutputClosed)
	}

	select {
	case err := <-reply:
		return err

	case <-ctx.Done():
		return ctx.Err()

	case <-o.appendWorkerCloser:
		return o.waitForClosers(ctx, ErrOutputClosed)
	}
}

func (o *Output) Close(ctx context.Context) error {
	if !o.closed.Load() {
		close(o.sendCh)
		o.closed.Store(true)
	}

	// Cancel the session for abandoning the requests.
	o.cancelSession()

	return o.waitForClosers(ctx, nil)
}
