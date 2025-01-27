package s2bentobox

import (
	"context"
	"errors"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

var ErrInputClosed = errors.New("input closed")

type SeqNumCache interface {
	Get(ctx context.Context, stream string) (uint64, error)
	Set(ctx context.Context, stream string, seqNum uint64) error
}

type InputStreams interface {
	list(ctx context.Context, client *s2.BasinClient) ([]string, error)
}

type StaticInputStreams struct {
	Streams []string
}

func (s StaticInputStreams) list(context.Context, *s2.BasinClient) ([]string, error) { //nolint:unparam
	return s.Streams, nil
}

type PrefixedInputStreams struct {
	Prefix string
}

func (p PrefixedInputStreams) list(ctx context.Context, client *s2.BasinClient) ([]string, error) {
	var (
		hasMore    = true
		streams    []string
		startAfter string
	)

	for hasMore {
		list, err := client.ListStreams(ctx, &s2.ListStreamsRequest{Prefix: p.Prefix, StartAfter: startAfter})
		if err != nil {
			return nil, err
		}

		for _, stream := range list.Streams {
			if stream.DeletedAt != nil {
				// The stream is deleted.
				continue
			}

			streams = append(streams, stream.Name)
			startAfter = stream.Name
		}

		hasMore = list.HasMore
	}

	return streams, nil
}

type InputConfig struct {
	*Config
	Streams            InputStreams
	MaxInFlight        int
	UpdateListInterval time.Duration
	Cache              SeqNumCache
	Logger             Logger
}

type recvOutput struct {
	Stream string
	Batch  *s2.SequencedRecordBatch
	Err    error
}

type Input struct {
	config               *InputConfig
	recvCh               <-chan recvOutput
	streamsManagerCloser <-chan struct{}
	cancelSession        context.CancelFunc
	closeWorker          chan<- string
}

func ConnectInput(ctx context.Context, config *InputConfig) (*Input, error) {
	client, err := newBasinClient(config.Config)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)

	streamsManagerCloser := make(chan struct{})
	closeWorker := make(chan string, config.MaxInFlight)
	recvCh := make(chan recvOutput, config.MaxInFlight)

	go streamsManagerWorker(
		sessionCtx,
		client,
		config,
		config.UpdateListInterval,
		recvCh,
		closeWorker,
		streamsManagerCloser,
	)

	return &Input{
		config:               config,
		recvCh:               recvCh,
		streamsManagerCloser: streamsManagerCloser,
		cancelSession:        cancelSession,
		closeWorker:          closeWorker,
	}, nil
}

type streamWorker struct {
	cancel context.CancelFunc
	closer <-chan struct{}
}

func (sw *streamWorker) IsClosed() bool {
	select {
	case <-sw.closer:
		return true
	default:
		return false
	}
}

func (sw *streamWorker) Close() {
	sw.cancel()
}

func (sw *streamWorker) Wait() {
	<-sw.closer
}

func streamsManagerWorker(
	ctx context.Context,
	client *s2.BasinClient,
	config *InputConfig,
	updateDuration time.Duration,
	recvCh chan<- recvOutput,
	closeWorker <-chan string,
	streamsManagerCloser chan<- struct{},
) {
	defer close(streamsManagerCloser)

	var (
		existingWorkers    = make(map[string]streamWorker)
		ticker             = time.NewTicker(updateDuration)
		exitNotifier       = make(chan struct{}, 1)
		updateListNotifier = make(chan struct{}, 1)
	)

	defer ticker.Stop()

	notifyOnce(updateListNotifier)

outerLoop:
	for {
		select {
		case <-ctx.Done():
			break outerLoop

		case <-exitNotifier:
			for stream, worker := range existingWorkers {
				if worker.IsClosed() {
					delete(existingWorkers, stream)
					// Maybe the worker exited due to an unexpected error. Let's list immediately.
					notifyOnce(updateListNotifier)
				}
			}

		case stream := <-closeWorker:
			if worker, found := existingWorkers[stream]; found {
				worker.Close()
			}

		case <-updateListNotifier:
		case <-ticker.C:
		}

		newStreams, err := config.Streams.list(ctx, client)
		if err != nil {
			config.Logger.
				With("error", err).
				Warn("Failed to list streams")
			notifyOnce(updateListNotifier)

			continue
		}

		newStreamsSet := make(map[string]struct{})
		for _, stream := range newStreams {
			newStreamsSet[stream] = struct{}{}

			if _, found := existingWorkers[stream]; !found {
				workerCtx, cancelWorker := context.WithCancel(ctx)
				workerCloser := make(chan struct{})
				worker := streamWorker{
					cancel: cancelWorker,
					closer: workerCloser,
				}
				existingWorkers[stream] = worker
				go receiverWorker(workerCtx, client, config, stream, recvCh, workerCloser, exitNotifier)
			}
		}

		for stream, worker := range existingWorkers {
			if _, found := newStreamsSet[stream]; !found {
				// Cancel the worker that's not in the new list.
				config.Logger.With("stream", stream).Warn("Not reading from S2 source anymore")
				worker.Close()
				delete(existingWorkers, stream)
			}
		}
	}

	// Close and wait for all to exit.
	for _, worker := range existingWorkers {
		worker.Close()
		worker.Wait()
	}
}

func receiverWorker(
	ctx context.Context,
	client *s2.BasinClient,
	config *InputConfig,
	stream string,
	recvCh chan<- recvOutput,
	closer chan<- struct{},
	exitNotifier chan<- struct{},
) {
	defer notifyOnce(exitNotifier)
	defer close(closer)

	startSeqNum, err := config.Cache.Get(ctx, stream)
	if err != nil {
		config.Logger.
			With(
				"error", err,
				"stream", stream,
			).
			Warn("Failed to get last sequence number from cache. Setting start seq num to 0")

		startSeqNum = 0
	}

	receiver, err := client.StreamClient(stream).ReadSession(ctx, &s2.ReadSessionRequest{
		StartSeqNum: startSeqNum,
	})
	if err != nil {
		config.Logger.
			With(
				"error", err,
				"stream", stream,
			).
			Error("Failed to initialize receiver")

		return
	}

	config.Logger.With("stream", stream).Info("Reading from S2 source")
	defer config.Logger.With("stream", stream).Debug("Exiting S2 source worker")

	for {
		output, err := receiver.Recv()
		if err != nil {
			select {
			case recvCh <- recvOutput{Stream: stream, Err: err}:
			case <-ctx.Done():
			}

			return
		}

		if batch, ok := output.(s2.ReadOutputBatch); ok {
			select {
			case recvCh <- recvOutput{Stream: stream, Batch: batch.SequencedRecordBatch}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (i *Input) ReadBatch(ctx context.Context) (*s2.SequencedRecordBatch, string, error) {
	select {
	case output := <-i.recvCh:
		if output.Err != nil {
			return nil, output.Stream, output.Err
		}

		return output.Batch, output.Stream, nil

	case <-ctx.Done():
		return nil, "", ctx.Err()

	case <-i.streamsManagerCloser:
		if err := waitForClosers(ctx, i.streamsManagerCloser); err != nil {
			return nil, "", err
		}

		return nil, "", ErrInputClosed
	}
}

func (i *Input) AckFunc(stream string, batch *s2.SequencedRecordBatch) func(context.Context, error) error {
	return func(ctx context.Context, err error) error {
		if err == nil && batch != nil && len(batch.Records) > 0 {
			// Update the cache with the last sequence number
			lastSeqNum := batch.Records[len(batch.Records)-1].SeqNum

			return i.config.Cache.Set(ctx, stream, lastSeqNum)
		}

		// Close a specific worker and let it restart.
		i.closeWorker <- stream

		return nil
	}
}

func (i *Input) Close(ctx context.Context) error {
	i.cancelSession()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-i.streamsManagerCloser:
		return waitForClosers(ctx, i.streamsManagerCloser)
	}
}
