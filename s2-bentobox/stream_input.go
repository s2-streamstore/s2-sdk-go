package bentobox

import (
	"context"
	"errors"
	"fmt"
	"sync"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
	"github.com/tidwall/btree"
)

var errCannotAckBatch = errors.New("cannot acknowledge batch")

type batchAckStatus struct {
	Acked       bool
	FirstSeqNum uint64
	LastSeqNum  uint64
}

type toAckMap struct {
	stream string
	logger Logger

	mu    sync.Mutex
	inner *btree.Map[uint64, batchAckStatus]
	cache *seqNumCache
}

func newToAckMap(stream string, cache *seqNumCache, logger Logger) *toAckMap {
	return &toAckMap{
		stream: stream,
		logger: logger,
		inner:  btree.NewMap[uint64, batchAckStatus](2),
		cache:  cache,
	}
}

func (tam *toAckMap) Add(records []s2.SequencedRecord) {
	if len(records) == 0 {
		return
	}

	tam.mu.Lock()
	defer tam.mu.Unlock()

	firstSeqNum := records[0].SeqNum
	lastSeqNum := records[len(records)-1].SeqNum

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

func (tam *toAckMap) MarkDone(ctx context.Context, records []s2.SequencedRecord, updateCache bool) error {
	if len(records) == 0 {
		return nil
	}

	tam.mu.Lock()
	defer tam.mu.Unlock()

	seqNum := records[0].SeqNum

	batchStatus, ok := tam.inner.Get(seqNum)
	if !ok {
		return errCannotAckBatch
	}

	batchStatus.Acked = true
	tam.inner.Set(seqNum, batchStatus)

	var lastAcked *batchAckStatus

	for {
		_, first, ok := tam.inner.Min()
		if !ok || !first.Acked {
			// Immediately break after finding the first one that's not acked.
			break
		}

		if lastAcked != nil && first.FirstSeqNum != lastAcked.LastSeqNum+1 {
			// We only want to ack continuous batches. Ensures that everything
			// has been received.
			break
		}

		// Pop this batch
		tam.inner.Delete(first.FirstSeqNum)
		lastAcked = &first
	}

	if lastAcked != nil && updateCache {
		nextSeqNum := lastAcked.LastSeqNum + 1
		tam.logger.With("stream", tam.stream, "start_seq_num", nextSeqNum).Debug("Updating cached sequence number")
		return tam.cache.Set(ctx, tam.stream, nextSeqNum)
	}

	return nil
}

type streamInput struct {
	Stream       string
	session      *s2.ReadSession
	cache        *seqNumCache
	toAck        *toAckMap
	nacks        chan []s2.SequencedRecord
	Logger       Logger
	closeSession func()
	closedCh     chan struct{}
	closeOnce    sync.Once
}

func connectStreamInput(
	ctx context.Context,
	basin *s2.BasinClient,
	cache *seqNumCache,
	logger Logger,
	stream string,
	maxInflight int,
	inputStartSeqNum InputStartSeqNum,
) (*streamInput, error) {
	streamClient := basin.Stream(s2.StreamName(stream))

	var opts s2.ReadOptions

	// Try getting the sequence number from cache
	startSeqNum, err := cache.Get(ctx, stream)
	if err != nil {
		if inputStartSeqNum == InputStartSeqNumLatest {
			opts.TailOffset = s2.Int64(0)
		} else {
			opts.SeqNum = s2.Uint64(0)
		}
	} else {
		opts.SeqNum = s2.Uint64(startSeqNum)
	}

	logger.With("stream", stream, "start_seq_num", startSeqNum).Debug("Starting to read")

	streamCtx, closeSession := context.WithCancel(ctx)

	session, err := streamClient.ReadSession(streamCtx, &opts)
	if err != nil {
		closeSession()
		return nil, err
	}

	return &streamInput{
		Stream:       stream,
		session:      session,
		cache:        cache,
		toAck:        newToAckMap(stream, cache, logger),
		nacks:        make(chan []s2.SequencedRecord, maxInflight),
		Logger:       logger,
		closeSession: closeSession,
		closedCh:     make(chan struct{}),
	}, nil
}

func (si *streamInput) isClosed() bool {
	select {
	case <-si.closedCh:
		return true
	default:
		return false
	}
}

func (si *streamInput) ReadBatch(ctx context.Context) ([]s2.SequencedRecord, AckFunc, error) {
	// Check for nacks first
	select {
	case records := <-si.nacks:
		return si.handleBatch(ctx, records)
	default:
	}

	// Check context
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-si.closedCh:
		return nil, nil, ErrInputClosed
	default:
	}

	// Try reading from session (blocking)
	if si.session.Next() {
		record := si.session.Record()
		return si.handleBatch(ctx, []s2.SequencedRecord{record})
	}

	if err := si.session.Err(); err != nil {
		return nil, nil, err
	}

	return nil, nil, ErrInputClosed
}

func (si *streamInput) handleBatch(_ context.Context, records []s2.SequencedRecord) ([]s2.SequencedRecord, AckFunc, error) {
	if len(records) == 0 {
		return records, func(context.Context, error) error { return nil }, nil
	}

	si.toAck.Add(records)

	ackFunc := func(c context.Context, e error) error {
		if e == nil {
			return si.ack(c, records)
		}
		return si.nack(records)
	}

	return records, ackFunc, nil
}

func (si *streamInput) nack(records []s2.SequencedRecord) error {
	si.Logger.With("stream", si.Stream).Debug("Nacking batch")

	select {
	case si.nacks <- records:
		return nil
	case <-si.closedCh:
		return ErrInputClosed
	}
}

func (si *streamInput) ack(ctx context.Context, records []s2.SequencedRecord) error {
	withLog := []any{"stream", si.Stream}
	if len(records) > 0 {
		firstSeqNum := records[0].SeqNum
		lastSeqNum := records[len(records)-1].SeqNum
		withLog = append(withLog, "range", fmt.Sprintf("%d..=%d", firstSeqNum, lastSeqNum))
	}

	return si.toAck.MarkDone(ctx, records, !si.isClosed())
}

func (si *streamInput) Close(ctx context.Context) error {
	si.closeOnce.Do(func() {
		close(si.closedCh)
		si.closeSession()
		si.session.Close()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
