package s2bentobox

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/s2-streamstore/s2-sdk-go/s2"
	"github.com/tidwall/btree"
)

type (
	Stream  = string
	AckFunc = func(ctx context.Context, err error) error
)

var errCannotAckBatch = errors.New("cannot acknowledge batch")

type rOutput struct {
	Output s2.ReadOutput
	Err    error
}

type batchAckStatus struct {
	Acked       bool
	FirstSeqNum uint64
	LastSeqNum  uint64
}

type toAckMap struct {
	stream Stream
	logger Logger

	mu    sync.Mutex
	inner *btree.Map[uint64, batchAckStatus]
	cache *seqNumCache
}

func newToAckMap(stream Stream, cache *seqNumCache, logger Logger) *toAckMap {
	return &toAckMap{
		stream: stream,
		logger: logger,
		inner:  btree.NewMap[uint64, batchAckStatus](2),
		cache:  cache,
	}
}

func (tam *toAckMap) Add(batch *s2.SequencedRecordBatch) {
	if len(batch.Records) == 0 {
		return
	}

	tam.mu.Lock()
	defer tam.mu.Unlock()

	firstSeqNum := batch.Records[0].SeqNum
	lastSeqNum := batch.Records[len(batch.Records)-1].SeqNum

	tam.inner.Set(firstSeqNum, batchAckStatus{
		FirstSeqNum: firstSeqNum,
		LastSeqNum:  lastSeqNum,
		Acked:       false,
	})
}

func (tam *toAckMap) Len() int {
	tam.mu.Lock()
	defer tam.mu.Unlock()

	return tam.inner.Len()
}

func (tam *toAckMap) MarkDone(ctx context.Context, batch *s2.SequencedRecordBatch, updateCache bool) error {
	if len(batch.Records) == 0 {
		return nil
	}

	tam.mu.Lock()
	defer tam.mu.Unlock()

	seqNum := batch.Records[0].SeqNum

	batchStatus, ok := tam.inner.Get(seqNum)
	if !ok {
		return errCannotAckBatch
	}

	batchStatus.Acked = true
	tam.inner.Set(seqNum, batchStatus)

	var (
		lastAckedBatch batchAckStatus
		isSet          bool
	)

	for _, value := range tam.inner.Values() {
		if !value.Acked {
			// Immediately break after finding the first one that's not acked.
			break
		}

		if isSet && value.FirstSeqNum != lastAckedBatch.LastSeqNum+1 {
			// We only want to ack continuous batches. Ensures that everything
			// has been received.
			break
		}

		_, ackedBatch, ok := tam.inner.PopMin()
		if !ok {
			// Should not happen but let it be.
			break
		}

		isSet = true
		lastAckedBatch = ackedBatch
	}

	if isSet && updateCache {
		nextSeqNum := lastAckedBatch.LastSeqNum + 1
		tam.logger.With("stream", tam.stream, "start_seq_num", nextSeqNum).Debug("Updating cached sequence number")

		return tam.cache.Set(ctx, tam.stream, nextSeqNum)
	}

	return nil
}

type streamInput struct {
	Stream       Stream
	InputStream  <-chan rOutput
	StreamCloser <-chan struct{}
	Cache        *seqNumCache
	CloseStream  context.CancelFunc
	ToAck        *toAckMap
	Nacks        chan *s2.SequencedRecordBatch
	Logger       Logger
}

func connectStreamInput(
	ctx context.Context,
	client *s2.BasinClient,
	cache *seqNumCache,
	logger Logger,
	stream string,
	maxInflight int,
	inputStartSeqNum InputStartSeqNum,
) (*streamInput, error) {
	streamClient := client.StreamClient(stream)

	var start s2.ReadStart

	// Try getting the sequence number from cache.
	startSeqNum, err := cache.Get(ctx, stream)
	if err != nil {
		if inputStartSeqNum == InputStartSeqNumLatest {
			start = s2.ReadStartTailOffset(0)
		} else {
			start = s2.ReadStartSeqNum(0)
		}
	} else {
		// Cache found!
		start = s2.ReadStartSeqNum(startSeqNum)
	}

	logger.With("stream", stream, "start", start.String()).Debug("Starting to read")

	// Open a read session.
	streamCtx, closeStream := context.WithCancel(ctx)

	receiver, err := streamClient.ReadSession(streamCtx, &s2.ReadSessionRequest{
		Start: start,
	})
	if err != nil {
		closeStream()

		return nil, err
	}

	streamCloser := make(chan struct{})
	inputStream := make(chan rOutput, maxInflight)

	go streamInputLoop(streamCtx, receiver, inputStream, streamCloser)

	return &streamInput{
		Stream:       stream,
		Logger:       logger,
		InputStream:  inputStream,
		StreamCloser: streamCloser,
		Cache:        cache,
		CloseStream:  closeStream,
		ToAck:        newToAckMap(stream, cache, logger),
		Nacks:        make(chan *s2.SequencedRecordBatch, maxInflight),
	}, nil
}

func streamInputLoop(
	ctx context.Context,
	receiver s2.Receiver[s2.ReadOutput],
	inputStream chan<- rOutput,
	closer chan<- struct{},
) {
	defer close(closer)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		oput, err := receiver.Recv()
		if err != nil {
			// Encountered an error.
			select {
			case inputStream <- rOutput{Err: err}:
			case <-ctx.Done():
			}

			return
		}

		select {
		case inputStream <- rOutput{Output: oput}:
		case <-ctx.Done():
			return
		}
	}
}

func (si *streamInput) isClosed() bool {
	select {
	case <-si.StreamCloser:
		return true
	default:
		return false
	}
}

func (si *streamInput) ReadBatch(ctx context.Context) (*s2.SequencedRecordBatch, AckFunc, error) {
	var readOutput s2.ReadOutput

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()

	case r := <-si.InputStream:
		if r.Err != nil {
			return nil, nil, r.Err
		}

		readOutput = r.Output

	case batch := <-si.Nacks:
		readOutput = s2.ReadOutputBatch{SequencedRecordBatch: batch}

	case <-si.StreamCloser:
		return nil, nil, ErrInputClosed
	}

	var startSeqNum uint64

	switch output := readOutput.(type) {
	case s2.ReadOutputBatch:
		ackFunc := func(c context.Context, e error) error {
			if len(output.Records) == 0 {
				return nil
			}

			if e == nil {
				return si.ack(c, output.SequencedRecordBatch)
			}

			return si.nack(output.SequencedRecordBatch)
		}

		if len(output.Records) > 0 {
			si.ToAck.Add(output.SequencedRecordBatch)
		}

		return output.SequencedRecordBatch, ackFunc, nil

	// The following are terminal messages appearing immediately in the stream
	// that will be received once. It's safe to update the next sequence number
	// in the cache here since no other routine will have anything to ack any
	// records from this stream.
	case s2.ReadOutputNextSeqNum:
		startSeqNum = uint64(output)
	}

	if si.ToAck.Len() > 0 {
		panic("input instance shouldn't have any messages to acknowledge")
	}

	// Close the input to to restart with the updated sequence number.
	if err := si.Close(ctx); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInputClosed, err)
	}

	// Update the cache for next sequence number.
	if err := si.Cache.Set(ctx, si.Stream, startSeqNum); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInputClosed, err)
	}

	return nil, nil, ErrInputClosed
}

func (si *streamInput) nack(batch *s2.SequencedRecordBatch) error {
	si.Logger.With("stream", si.Stream).Debug("Nacking batch")

	// The batch is still to be acknowledged.
	select {
	case si.Nacks <- batch:
		return nil

	case <-si.StreamCloser:
		return ErrInputClosed
	}
}

func (si *streamInput) ack(ctx context.Context, batch *s2.SequencedRecordBatch) error {
	withLog := []any{"stream", si.Stream}

	if len(batch.Records) > 0 {
		firstSeqNum := batch.Records[0].SeqNum
		lastSeqNum := batch.Records[len(batch.Records)-1].SeqNum
		withLog = append(withLog, "range", fmt.Sprintf("%d..=%d", firstSeqNum, lastSeqNum))
	}

	si.Logger.With(withLog...).Debug("Acknowledging batch")

	return si.ToAck.MarkDone(ctx, batch, !si.isClosed())
}

func (si *streamInput) Close(ctx context.Context) error {
	si.CloseStream()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-si.StreamCloser:
		return nil
	}
}
