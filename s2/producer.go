package s2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Producer provides per-record append semantics on top of a batched AppendSession.
//   - submit(record) returns a [RecordSubmitFuture] that resolves once the record
//     has been accepted (written to the batcher). Backpressure is applied
//     automatically via the batcher when the [AppendSession] is at capacity.
//   - ticket.Ack() returns an IndexedAppendAck that resolves once the record is durable.
//   - Flush() establishes a reusable durability boundary without closing the producer.
type Producer struct {
	batcher      *Batcher
	session      appendSessionAPI
	ctx          context.Context
	cancel       context.CancelFunc
	ackWG        sync.WaitGroup
	consumerDone chan struct{}
	errMu        sync.RWMutex
	terminalErr  error
	closing      atomic.Bool
	closeOnce    sync.Once
	closeDone    chan struct{}
	closeErr     error
}

type appendSessionAPI interface {
	Submit(input *AppendInput) (*SubmitFuture, error)
	Close() error
}

// Create a new [Producer].
func NewProducer(ctx context.Context, batcher *Batcher, session *AppendSession) *Producer {
	return newProducerWithSession(ctx, batcher, session)
}

func newProducerWithSession(ctx context.Context, batcher *Batcher, session appendSessionAPI) *Producer {
	if ctx == nil {
		ctx = context.Background()
	}
	prodCtx, cancel := context.WithCancel(ctx)

	p := &Producer{
		batcher:      batcher,
		session:      session,
		ctx:          prodCtx,
		cancel:       cancel,
		consumerDone: make(chan struct{}),
		closeDone:    make(chan struct{}),
	}

	go p.consumeBatches()

	return p
}

// Submit a single record for appending.
// Returns a [RecordSubmitFuture] that resolves to a RecordSubmitTicket once the record has been
// accepted. Blocks if the underlying [AppendSession] is at capacity.
func (p *Producer) Submit(record AppendRecord) (*RecordSubmitFuture, error) {
	if err := p.terminalError(); err != nil {
		return nil, err
	}
	if p.closing.Load() {
		return nil, ErrSessionClosed
	}
	if err := p.ctx.Err(); err != nil {
		return nil, p.recordTerminalError(err)
	}

	ticketCh := make(chan *RecordSubmitTicket, 1)
	resultCh := make(chan *producerOutcome, 1)

	if err := p.batcher.Add(record, resultCh); err != nil {
		if errors.Is(err, ErrSessionClosed) {
			if terminalErr := p.terminalError(); terminalErr != nil {
				return nil, terminalErr
			}
		}
		return nil, err
	}
	ticketCh <- &RecordSubmitTicket{ackCh: resultCh}

	return &RecordSubmitFuture{ticketCh: ticketCh}, nil
}

// Flush forces buffered records to be emitted and waits until every record
// ordered before the barrier is durably acknowledged or one fails. Records
// ordered after the barrier are excluded, and the Producer remains usable
// after a successful Flush.
//
// Submit and Flush are ordered by the Batcher: a Submit that has completed
// before Flush begins is covered, while calls made concurrently may land on
// either side. An empty Flush succeeds without issuing an append. Canceling ctx
// stops this call's wait, while ordering and draining the barrier continue in
// the background. If a covered append fails, the error is terminal, closes the
// Producer, and is returned by subsequent Submit, Flush, and Close calls.
// Unlike [Batcher.Flush], this method waits for durable acknowledgments.
func (p *Producer) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.terminalError(); err != nil {
		return p.waitForTerminalDrain(ctx, err)
	}
	if p.closing.Load() {
		return ErrSessionClosed
	}
	if err := p.ctx.Err(); err != nil {
		terminalErr := p.recordTerminalError(err)
		return p.waitForTerminalDrain(ctx, terminalErr)
	}

	barrier := make(chan error, 1)
	ordered := make(chan error, 1)
	go func() {
		err := p.batcher.flushBarrier(barrier)
		if err != nil && !errors.Is(err, ErrSessionClosed) {
			err = p.recordTerminalError(err)
		}
		ordered <- err
	}()

	select {
	case err := <-ordered:
		if err == nil {
			break
		}
		if errors.Is(err, ErrSessionClosed) {
			if terminalErr := p.terminalError(); terminalErr != nil {
				return p.waitForTerminalDrain(ctx, terminalErr)
			}
			return err
		}
		return p.waitForTerminalDrain(ctx, err)
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-barrier:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Producer) consumeBatches() {
	defer close(p.consumerDone)

	for batch := range p.batcher.Batches() {
		if batch.Input != nil {
			p.processBatch(batch)
		}
		if batch.barrier != nil {
			p.ackWG.Wait()
			batch.barrier <- p.terminalError()
		}
	}
	p.ackWG.Wait()
}

// ownedSubmitter is implemented by sessions that can skip the defensive deep
// clone for inputs the caller exclusively owns.
type ownedSubmitter interface {
	submitOwned(input *AppendInput) (*SubmitFuture, error)
}

func (p *Producer) processBatch(batch *BatchOutput) {
	if err := p.terminalError(); err != nil {
		p.dispatchBatchError(batch.recordMeta, err)
		return
	}

	// The batcher deep-cloned every record on Add and this batch is its sole
	// reference, so the session does not need to clone them again.
	var (
		future *SubmitFuture
		err    error
	)
	if owned, ok := p.session.(ownedSubmitter); ok {
		future, err = owned.submitOwned(batch.Input)
	} else {
		future, err = p.session.Submit(batch.Input)
	}
	if err != nil {
		p.resolveSynchronousBatchError(batch.recordMeta, err)
		return
	}

	ticket, err := p.waitForBatchSubmission(future)
	if err != nil {
		p.resolveSynchronousBatchError(batch.recordMeta, err)
		return
	}

	p.ackWG.Add(1)
	go func() {
		defer p.ackWG.Done()

		ack, err := p.waitForBatchAck(ticket)
		if err != nil {
			p.dispatchBatchError(batch.recordMeta, p.recordTerminalError(err))
			return
		}

		p.resolveBatchAck(batch.recordMeta, ack)
	}()
}

func (p *Producer) waitForBatchSubmission(future *SubmitFuture) (*BatchSubmitTicket, error) {
	select {
	case ticket := <-future.ticketCh:
		return ticket, nil
	case err := <-future.errCh:
		return nil, err
	case <-p.ctx.Done():
		// Prefer a result that was already available when termination raced the
		// select. This preserves the AppendSession's exact terminal cause.
		select {
		case ticket := <-future.ticketCh:
			return ticket, nil
		case err := <-future.errCh:
			return nil, err
		default:
			return nil, p.ctx.Err()
		}
	}
}

func (p *Producer) waitForBatchAck(ticket *BatchSubmitTicket) (*AppendAck, error) {
	result := func(outcome *inflightResult, ok bool) (*AppendAck, error) {
		if !ok || outcome == nil {
			return nil, fmt.Errorf("batch submit ticket resolved without a payload")
		}
		return outcome.ack, outcome.err
	}

	select {
	case outcome, ok := <-ticket.ackCh:
		return result(outcome, ok)
	case <-p.ctx.Done():
		// Drain a result that was already ready before falling back to the
		// Producer's sticky terminal cause, matching ordered session shutdown.
		select {
		case outcome, ok := <-ticket.ackCh:
			return result(outcome, ok)
		default:
			return nil, p.ctx.Err()
		}
	}
}

func (p *Producer) resolveBatchAck(meta []recordMeta, ack *AppendAck) {
	for _, m := range meta {
		indexedAck := &IndexedAppendAck{
			batchAck: ack,
			index:    uint64(m.index),
		}
		select {
		case m.resultCh <- &producerOutcome{indexedAck: indexedAck}:
			close(m.resultCh)
		default:
		}
	}
}

func (p *Producer) resolveSynchronousBatchError(meta []recordMeta, err error) {
	// Establish the failure before waiting so cancellation releases any earlier
	// ticket waits. recordTerminalError preserves a cause already observed by an
	// earlier batch and fans the chosen cause out to the remaining tickets.
	terminalErr := p.recordTerminalError(err)
	p.ackWG.Wait()
	p.dispatchBatchError(meta, terminalErr)
}

func (p *Producer) dispatchBatchError(meta []recordMeta, err error) {
	for _, m := range meta {
		select {
		case m.resultCh <- &producerOutcome{err: err}:
			close(m.resultCh)
		default:
		}
	}
}

func (p *Producer) terminalError() error {
	p.errMu.RLock()
	defer p.errMu.RUnlock()
	return p.terminalErr
}

func (p *Producer) recordTerminalError(err error) error {
	if err == nil {
		return nil
	}

	p.errMu.Lock()
	first := p.terminalErr == nil
	if first {
		p.terminalErr = err
	}
	terminalErr := p.terminalErr
	p.errMu.Unlock()

	if first {
		// Closing seals records that were already accepted into the Batcher and
		// makes later submissions fail. Canceling the producer context also
		// releases every outstanding ticket wait so they receive this same cause.
		p.closing.Store(true)
		p.cancel()
		// This must be asynchronous because the consumer may need to drain a
		// bounded batch channel for the batcher close to finish.
		go func() { _ = p.batcher.close() }()
	}

	return terminalErr
}

func (p *Producer) waitForTerminalDrain(ctx context.Context, terminalErr error) error {
	select {
	case <-p.consumerDone:
		return terminalErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stops the producer, flushes pending batches, and waits for in-flight acks.
// Close blocks until in-flight work completes or the producer context is
// canceled. It returns a terminal append error when one closed the Producer.
// Close does not close the supplied AppendSession.
func (p *Producer) Close() error {
	p.closeOnce.Do(func() {
		p.closing.Store(true)
		if err := p.batcher.close(); err != nil {
			p.recordTerminalError(err)
		}
		<-p.consumerDone

		// Check before calling cancel so a parent-context cancellation remains
		// distinguishable from this method's normal cleanup cancellation.
		if err := p.ctx.Err(); err != nil && p.terminalError() == nil {
			p.recordTerminalError(err)
		}
		p.cancel()
		p.closeErr = p.terminalError()
		close(p.closeDone)
	})
	<-p.closeDone
	return p.closeErr
}

// Represents a pending single-record submission to a [Producer].
type RecordSubmitFuture struct {
	ticketCh <-chan *RecordSubmitTicket
}

// Blocks until the record is accepted and returns a [RecordSubmitTicket].
func (f *RecordSubmitFuture) Wait(ctx context.Context) (*RecordSubmitTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case ticket := <-f.ticketCh:
		return ticket, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Returned after a record is accepted by the [Producer].
// Use [RecordSubmitTicket.Ack] to wait for the record to become durable.
type RecordSubmitTicket struct {
	ackCh <-chan *producerOutcome
}

// Blocks until the record is durable and returns the [IndexedAppendAck].
func (t *RecordSubmitTicket) Ack(ctx context.Context) (*IndexedAppendAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case outcome, ok := <-t.ackCh:
		if !ok || outcome == nil {
			return nil, fmt.Errorf("record submit ticket resolved without a payload")
		}
		if outcome.err != nil {
			return nil, outcome.err
		}
		return outcome.indexedAck, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Represents the acknowledgment for a single record within a batch.
type IndexedAppendAck struct {
	batchAck *AppendAck
	index    uint64
}

// Returns the underlying batch [AppendAck].
func (a *IndexedAppendAck) BatchAppendAck() *AppendAck {
	return a.batchAck
}

// Returns the sequence number assigned to this specific record.
func (a *IndexedAppendAck) SeqNum() uint64 {
	return a.batchAck.Start.SeqNum + a.index
}

type producerOutcome struct {
	indexedAck *IndexedAppendAck
	err        error
}
