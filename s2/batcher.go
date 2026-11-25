package s2

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
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
	timerSeq        uint64
	closed          bool
	closeErr        error
	nextMatchSeqNum *uint64

	batchesCh chan *BatchOutput
	// Set by Close before it takes mu, which a flush blocked on a full
	// Batches may hold until the consumer drains.
	closing   atomic.Bool
	closeOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
}

// BatchOutput represents a flushed batch ready for appending.
type BatchOutput struct {
	// Input contains the records to append.
	Input      *AppendInput
	recordMeta []recordMeta
	barrier    chan<- error
}

type recordMeta struct {
	index    int
	resultCh chan *producerOutcome
	permit   *producerPermit
}

func (m recordMeta) resolve(outcome *producerOutcome) {
	if m.resultCh == nil {
		return
	}
	select {
	case m.resultCh <- outcome:
		close(m.resultCh)
	default:
	}
}

func (b *Batcher) flushForBackpressure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrSessionClosed
	}
	return b.flushLocked()
}

func (b *Batcher) shutdownCause() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closeErr != nil {
		return b.closeErr
	}
	if b.closed || b.closing.Load() {
		return ErrSessionClosed
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	return ErrSessionClosed
}

func applyBatchingDefaults(opts *BatchingOptions) *BatchingOptions {
	if opts == nil {
		opts = &BatchingOptions{}
	}
	result := *opts
	// Batch inputs reference this token without re-cloning it per batch, so
	// detach it from the caller's pointer.
	result.FencingToken = cloneStringPtr(result.FencingToken)
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
//
// Panics with a validation error if opts.Linger is negative.
func NewBatcher(ctx context.Context, opts *BatchingOptions) *Batcher {
	if ctx == nil {
		ctx = context.Background()
	}
	// Spec C2: linger must be ≥ 0. A negative linger would never flush.
	if opts != nil && opts.Linger < 0 {
		panic(newValidationError("linger must not be negative"))
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
	return b.addWithPermit(record, resultCh, nil)
}

func (b *Batcher) addWithPermit(record AppendRecord, resultCh chan *producerOutcome, permit *producerPermit) error {
	// Checked before mu, which a flush blocked on a full Batches may hold.
	if b.closing.Load() {
		return ErrSessionClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed || b.closing.Load() {
		return ErrSessionClosed
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}

	cloned := cloneAppendRecords([]AppendRecord{record})
	rec := cloned[0]

	recordBytes := MeteredPayloadBytes(rec)
	if recordBytes > b.opts.MaxMeteredBytes {
		return newValidationError("record exceeds max batch bytes")
	}

	if b.nextMatchSeqNum != nil && uint64(len(b.buffer))+1 > math.MaxUint64-*b.nextMatchSeqNum {
		return newValidationError("match_seq_num would overflow uint64")
	}

	if len(b.buffer) > 0 && (len(b.buffer)+1 > b.opts.MaxRecords || b.bufferBytes+recordBytes > b.opts.MaxMeteredBytes) {
		if err := b.flushLocked(); err != nil {
			return err
		}
	}

	index := len(b.buffer)
	b.buffer = append(b.buffer, rec)
	b.bufferBytes += recordBytes

	if resultCh != nil {
		b.recordMeta = append(b.recordMeta, recordMeta{index: index, resultCh: resultCh, permit: permit})
	}

	if len(b.buffer) >= b.opts.MaxRecords || b.bufferBytes >= b.opts.MaxMeteredBytes {
		return b.flushLocked()
	} else if b.opts.Linger > 0 && b.timer == nil {
		b.timerSeq++
		timerSeq := b.timerSeq
		b.timer = time.AfterFunc(b.opts.Linger, func() {
			b.flushFromTimer(timerSeq)
		})
	}

	return nil
}

func (b *Batcher) flushLocked() error {
	output := b.takeBatchLocked()
	if output == nil {
		return nil
	}
	if err := b.send(output); err != nil {
		if b.closeErr == nil {
			b.closeErr = err
		}
		return err
	}
	if b.nextMatchSeqNum != nil {
		*b.nextMatchSeqNum += uint64(len(output.Input.Records))
	}
	return nil
}

func (b *Batcher) takeBatchLocked() *BatchOutput {
	if len(b.buffer) == 0 {
		return nil
	}

	records := make([]AppendRecord, len(b.buffer))
	copy(records, b.buffer)
	clear(b.buffer)
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
	}

	input := &AppendInput{
		Records:      records,
		MatchSeqNum:  matchSeqNum,
		FencingToken: b.opts.FencingToken,
	}

	meta := b.recordMeta
	b.recordMeta = nil

	return &BatchOutput{Input: input, recordMeta: meta}
}

func (b *Batcher) send(output *BatchOutput) error {
	select {
	case b.batchesCh <- output:
		return nil
	case <-b.ctx.Done():
		err := b.ctx.Err()
		releaseProducerPermits(output.recordMeta)
		for _, m := range output.recordMeta {
			m.resolve(&producerOutcome{err: err})
		}
		return err
	}
}

// flushBarrier atomically emits the current partial batch and orders a
// producer barrier after it. Holding mu across both sends prevents a
// concurrent Add from landing between the batch and its barrier.
func (b *Batcher) flushBarrier(barrier chan<- error) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return ErrSessionClosed
	}
	if err := b.flushLocked(); err != nil {
		return err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}

	select {
	case b.batchesCh <- &BatchOutput{barrier: barrier}:
		return nil
	case <-b.ctx.Done():
		return b.ctx.Err()
	}
}

func (b *Batcher) flushFromTimer(timerSeq uint64) {
	b.mu.Lock()
	if b.timer == nil || b.timerSeq != timerSeq {
		b.mu.Unlock()
		return
	}
	b.timer = nil
	if !b.closed {
		b.flushLocked()
	}
	b.mu.Unlock()
}

// Batches returns a receive-only channel of flushed batches. It is closed after
// [Batcher.Close] once the final batch has been received.
func (b *Batcher) Batches() <-chan *BatchOutput {
	return b.batchesCh
}

// Flush forces the current batch to be emitted immediately. It does not wait
// for the batch to become durable; use [Producer.Flush] for a durable barrier.
func (b *Batcher) Flush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
}

// Waits until the final batch is received or dropped and Batches is closed.
func (b *Batcher) close() error {
	b.closeOnce.Do(func() {
		// Set before mu, which a flush blocked on a full Batches may hold, so
		// that Add fails fast instead of queuing behind it.
		b.closing.Store(true)
		b.finishClose()
	})

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeErr
}

func (b *Batcher) finishClose() {
	b.mu.Lock()
	b.closed = true
	output := b.takeBatchLocked()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()

	if output != nil {
		if err := b.send(output); err != nil {
			b.mu.Lock()
			if b.closeErr == nil {
				b.closeErr = err
			}
			b.mu.Unlock()
		}
	}
	b.cancel()
	close(b.batchesCh)
}

// Flushes any remaining records and closes the batcher. Calls to Add after
// Close starts fail.
//
// Close returns once the final batch has been received from Batches (or fits
// in its buffer), and Batches is then closed. Close holds no lock while it
// waits, so other calls do not block behind it. To abandon a batcher whose
// consumer has stopped, cancel the context passed to [NewBatcher]: the final
// batch is then dropped and Close returns.
func (b *Batcher) Close() {
	_ = b.close()
}
