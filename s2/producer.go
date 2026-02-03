package s2

import (
	"context"
	"fmt"
	"sync"
)

// Producer provides per-record append semantics on top of a batched AppendSession.
//   - submit(record) returns a [RecordSubmitFuture] that resolves once the record
//     has been accepted (written to the batcher). Backpressure is applied
//     automatically via the batcher when the [AppendSession] is at capacity.
//   - ticket.Ack() returns an IndexedAppendAck that resolves once the record is durable.
type Producer struct {
	batcher *Batcher
	session *AppendSession
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Create a new [Producer].
func NewProducer(ctx context.Context, batcher *Batcher, session *AppendSession) *Producer {
	if ctx == nil {
		ctx = context.Background()
	}
	prodCtx, cancel := context.WithCancel(ctx)

	p := &Producer{
		batcher: batcher,
		session: session,
		ctx:     prodCtx,
		cancel:  cancel,
	}

	p.wg.Add(1)
	go p.consumeBatches()

	return p
}

// Submit a single record for appending.
// Returns a [RecordSubmitFuture] that resolves to a RecordSubmitTicket once the record has been
// accepted. Blocks if the underlying [AppendSession] is at capacity.
func (p *Producer) Submit(record AppendRecord) (*RecordSubmitFuture, error) {
	ticketCh := make(chan *RecordSubmitTicket, 1)
	errCh := make(chan error, 1)

	resultCh := make(chan *producerOutcome, 1)

	go func() {
		if err := p.batcher.Add(record, resultCh); err != nil {
			errCh <- err
			return
		}
		ticketCh <- &RecordSubmitTicket{ackCh: resultCh}
	}()

	return &RecordSubmitFuture{ticketCh: ticketCh, errCh: errCh}, nil
}

func (p *Producer) consumeBatches() {
	defer p.wg.Done()

	for batch := range p.batcher.Batches() {
		p.processBatch(batch)
	}
}

func (p *Producer) processBatch(batch *BatchOutput) {
	future, err := p.session.Submit(batch.Input)
	if err != nil {
		p.resolveBatchError(batch.recordMeta, err)
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		ticket, err := future.Wait(p.ctx)
		if err != nil {
			p.resolveBatchError(batch.recordMeta, err)
			return
		}

		ack, err := ticket.Ack(p.ctx)
		if err != nil {
			p.resolveBatchError(batch.recordMeta, err)
			return
		}

		p.resolveBatchAck(batch.recordMeta, ack)
	}()
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

func (p *Producer) resolveBatchError(meta []recordMeta, err error) {
	for _, m := range meta {
		select {
		case m.resultCh <- &producerOutcome{err: err}:
			close(m.resultCh)
		default:
		}
	}
}

// Stops the producer, flushes pending batches, and waits for completion.
func (p *Producer) Close() error {
	p.batcher.Close()
	p.wg.Wait()
	p.cancel()
	return nil
}

// Represents a pending single-record submission to a [Producer].
type RecordSubmitFuture struct {
	ticketCh <-chan *RecordSubmitTicket
	errCh    <-chan error
}

// Blocks until the record is accepted and returns a [RecordSubmitTicket].
func (f *RecordSubmitFuture) Wait(ctx context.Context) (*RecordSubmitTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case ticket := <-f.ticketCh:
		return ticket, nil
	case err := <-f.errCh:
		return nil, err
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
