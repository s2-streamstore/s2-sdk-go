package s2

import (
	"context"
	"sync"
	"time"
)

// Accumulates AppendRecords and emits them as batches based on
// configurable size, count, or time thresholds.
type Batcher struct {
	opts *BatchingOptions

	mu              sync.Mutex
	buffer          []AppendRecord
	bufferBytes     uint64
	recordMeta      []recordMeta
	timer           *time.Timer
	closed          bool
	nextMatchSeqNum *uint64

	batchesCh chan *BatchOutput
	ctx       context.Context
	cancel    context.CancelFunc
}

// BatchOutput represents a flushed batch ready for appending.
type BatchOutput struct {
	// Input contains the records to append.
	Input      *AppendInput
	recordMeta []recordMeta
}

type recordMeta struct {
	index    int
	resultCh chan *producerOutcome
}

func applyBatchingDefaults(opts *BatchingOptions) *BatchingOptions {
	if opts == nil {
		opts = &BatchingOptions{}
	}
	result := *opts
	if result.Linger == 0 {
		result.Linger = 5 * time.Millisecond
	}
	if result.MaxRecords <= 0 || result.MaxRecords > MaxBatchRecords {
		result.MaxRecords = MaxBatchRecords
	}
	if result.MaxMeteredBytes == 0 || result.MaxMeteredBytes > MaxBatchMeteredBytes {
		result.MaxMeteredBytes = MaxBatchMeteredBytes
	}
	if result.ChannelBuffer <= 0 {
		result.ChannelBuffer = 100
	}
	return &result
}

// Create a new [Batcher] that accumulates AppendRecords and emits them as batches based on
// configurable size, count, or time thresholds.
func NewBatcher(ctx context.Context, opts *BatchingOptions) *Batcher {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = applyBatchingDefaults(opts)
	batchCtx, cancel := context.WithCancel(ctx)

	var nextMatchSeqNum *uint64
	if opts.MatchSeqNum != nil {
		v := *opts.MatchSeqNum
		nextMatchSeqNum = &v
	}

	return &Batcher{
		opts:            opts,
		batchesCh:       make(chan *BatchOutput, opts.ChannelBuffer),
		ctx:             batchCtx,
		cancel:          cancel,
		nextMatchSeqNum: nextMatchSeqNum,
	}
}

// Adds a record to the current batch.
func (b *Batcher) Add(record AppendRecord, resultCh chan *producerOutcome) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return ErrSessionClosed
	}

	cloned := cloneAppendRecords([]AppendRecord{record})
	rec := cloned[0]

	recordBytes := MeteredPayloadBytes(rec)
	if recordBytes > b.opts.MaxMeteredBytes {
		return &S2Error{Message: "record exceeds max batch bytes"}
	}

	if len(b.buffer) > 0 && (len(b.buffer)+1 > b.opts.MaxRecords || b.bufferBytes+recordBytes > b.opts.MaxMeteredBytes) {
		b.flushLocked()
	}

	index := len(b.buffer)
	b.buffer = append(b.buffer, rec)
	b.bufferBytes += recordBytes

	if resultCh != nil {
		b.recordMeta = append(b.recordMeta, recordMeta{index: index, resultCh: resultCh})
	}

	if len(b.buffer) >= b.opts.MaxRecords || b.bufferBytes >= b.opts.MaxMeteredBytes {
		b.flushLocked()
	} else if b.opts.Linger > 0 && b.timer == nil {
		b.timer = time.AfterFunc(b.opts.Linger, b.flushFromTimer)
	}

	return nil
}

func (b *Batcher) flushLocked() {
	if len(b.buffer) == 0 {
		return
	}

	records := make([]AppendRecord, len(b.buffer))
	copy(records, b.buffer)
	b.buffer = b.buffer[:0]
	b.bufferBytes = 0

	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}

	var matchSeqNum *uint64
	if b.nextMatchSeqNum != nil {
		v := *b.nextMatchSeqNum
		matchSeqNum = &v
		*b.nextMatchSeqNum += uint64(len(records))
	}

	input := &AppendInput{
		Records:      records,
		MatchSeqNum:  matchSeqNum,
		FencingToken: b.opts.FencingToken,
	}

	meta := b.recordMeta
	b.recordMeta = nil

	select {
	case b.batchesCh <- &BatchOutput{Input: input, recordMeta: meta}:
	case <-b.ctx.Done():
	}
}

func (b *Batcher) flushFromTimer() {
	b.mu.Lock()
	b.timer = nil
	if !b.closed {
		b.flushLocked()
	}
	b.mu.Unlock()
}

// Batches returns a receive-only channel of flushed batches.
func (b *Batcher) Batches() <-chan *BatchOutput {
	return b.batchesCh
}

// Flush forces the current batch to be emitted immediately.
func (b *Batcher) Flush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
}

// Flushes any remaining records and closes the batcher.
func (b *Batcher) Close() {
	b.cancel() // Cancel first to unblock any flushLocked waiting on channel send

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.flushLocked()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()

	close(b.batchesCh)
}
