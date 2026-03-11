package s2

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProducer_PerRecordAckOrdering(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        5 * time.Millisecond,
		ChannelBuffer: 10,
	})
	session := &fakeAppendSession{}
	producer := newProducerWithSession(ctx, batcher, session)

	f1, err := producer.Submit(AppendRecord{Body: []byte("a")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	f2, err := producer.Submit(AppendRecord{Body: []byte("b")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	t1, err := f1.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	t2, err := f2.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	ack1, err := t1.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	ack2, err := t2.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if ack1.SeqNum() != 0 || ack2.SeqNum() != 1 {
		t.Fatalf("expected seq_nums 0 and 1, got %d and %d", ack1.SeqNum(), ack2.SeqNum())
	}
	_ = producer.Close()
}

func TestProducer_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        5 * time.Millisecond,
		ChannelBuffer: 10,
	})
	session := &fakeAppendSession{submitErr: ErrSessionClosed}
	producer := newProducerWithSession(ctx, batcher, session)

	future, err := producer.Submit(AppendRecord{Body: []byte("a")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if _, err := ticket.Ack(ctx); err == nil {
		t.Fatalf("expected error from ack")
	}
	_ = producer.Close()
}

func TestProducer_PreservesOrderAcrossBatches(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    2,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := &fakeAppendSession{}
	producer := newProducerWithSession(ctx, batcher, session)

	f1, err := producer.Submit(AppendRecord{Body: []byte("a")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	f2, err := producer.Submit(AppendRecord{Body: []byte("b")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	f3, err := producer.Submit(AppendRecord{Body: []byte("c")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	batcher.Flush()

	t1, err := f1.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	t2, err := f2.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	t3, err := f3.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	ack1, err := t1.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	ack2, err := t2.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	ack3, err := t3.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if ack1.SeqNum() != 0 || ack2.SeqNum() != 1 || ack3.SeqNum() != 2 {
		t.Fatalf("expected seq_nums 0,1,2 got %d,%d,%d", ack1.SeqNum(), ack2.SeqNum(), ack3.SeqNum())
	}

	_ = producer.Close()
}

func TestProducer_ConcurrentSubmitsAreGapless(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    3,
		Linger:        5 * time.Millisecond,
		ChannelBuffer: 100,
	})
	session := &fakeAppendSession{}
	producer := newProducerWithSession(ctx, batcher, session)

	const total = 20
	var wg sync.WaitGroup
	seqs := make([]uint64, 0, total)
	var mu sync.Mutex
	tickets := make(chan *RecordSubmitTicket, total)

	for range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			future, err := producer.Submit(AppendRecord{Body: []byte("x")})
			if err != nil {
				t.Errorf("submit failed: %v", err)
				return
			}
			ticket, err := future.Wait(ctx)
			if err != nil {
				t.Errorf("wait failed: %v", err)
				return
			}
			tickets <- ticket
		}()
	}

	wg.Wait()
	batcher.Flush()
	close(tickets)

	for ticket := range tickets {
		ack, err := ticket.Ack(ctx)
		if err != nil {
			t.Fatalf("ack failed: %v", err)
		}
		mu.Lock()
		seqs = append(seqs, ack.SeqNum())
		mu.Unlock()
	}
	_ = producer.Close()

	if len(seqs) != total {
		t.Fatalf("expected %d seqs, got %d", total, len(seqs))
	}

	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i := range total {
		if seqs[i] != uint64(i) {
			t.Fatalf("expected seq %d at index %d, got %d", i, i, seqs[i])
		}
	}
}

type blockingSubmitSession struct {
	release     chan struct{}
	started     chan struct{}
	inFlight    int
	maxInFlight int
	nextSeq     uint64
	mu          sync.Mutex
}

func (s *blockingSubmitSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.mu.Unlock()

	select {
	case s.started <- struct{}{}:
	default:
	}

	<-s.release

	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()

	batchSize := len(input.Records)
	start := s.nextSeq
	s.nextSeq += uint64(batchSize)
	ack := &AppendAck{
		Start: StreamPosition{SeqNum: start},
		End:   StreamPosition{SeqNum: start + uint64(batchSize)},
		Tail:  StreamPosition{SeqNum: start + uint64(batchSize)},
	}

	ticketCh := make(chan *BatchSubmitTicket, 1)
	errCh := make(chan error, 1)
	ackCh := make(chan *inflightResult, 1)
	ticketCh <- &BatchSubmitTicket{ackCh: ackCh}
	ackCh <- &inflightResult{ack: ack}
	close(ackCh)

	return &SubmitFuture{ticketCh: ticketCh, errCh: errCh}, nil
}

func (s *blockingSubmitSession) Close() error {
	return nil
}

func TestProducer_SubmitSerializesAppendSessionCalls(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := &blockingSubmitSession{
		release: make(chan struct{}),
		started: make(chan struct{}, 2),
	}
	producer := newProducerWithSession(ctx, batcher, session)

	f1, err := producer.Submit(AppendRecord{Body: []byte("a")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	f2, err := producer.Submit(AppendRecord{Body: []byte("b")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	select {
	case <-session.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for first submit")
	}

	select {
	case <-session.started:
		t.Fatalf("expected submits to be serialized")
	default:
	}

	close(session.release)

	t1, err := f1.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	t2, err := f2.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if _, err := t1.Ack(ctx); err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if _, err := t2.Ack(ctx); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	if session.maxInFlight != 1 {
		t.Fatalf("expected max in-flight submit to be 1, got %d", session.maxInFlight)
	}

	_ = producer.Close()
}

type delayedAckSession struct {
	ackGate chan struct{}
	nextSeq uint64
}

func (s *delayedAckSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	batchSize := len(input.Records)
	start := s.nextSeq
	s.nextSeq += uint64(batchSize)

	ack := &AppendAck{
		Start: StreamPosition{SeqNum: start},
		End:   StreamPosition{SeqNum: start + uint64(batchSize)},
		Tail:  StreamPosition{SeqNum: start + uint64(batchSize)},
	}

	ticketCh := make(chan *BatchSubmitTicket, 1)
	errCh := make(chan error, 1)
	ackCh := make(chan *inflightResult, 1)
	ticketCh <- &BatchSubmitTicket{ackCh: ackCh}

	go func() {
		<-s.ackGate
		ackCh <- &inflightResult{ack: ack}
		close(ackCh)
	}()

	return &SubmitFuture{ticketCh: ticketCh, errCh: errCh}, nil
}

func (s *delayedAckSession) Close() error {
	return nil
}

type delayedAcceptanceSession struct {
	release chan struct{}
	started chan struct{}
	submits atomic.Int32
	seqMu   sync.Mutex
	nextSeq uint64
}

func (s *delayedAcceptanceSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	s.submits.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}

	ticketCh := make(chan *BatchSubmitTicket, 1)
	errCh := make(chan error, 1)

	go func() {
		<-s.release

		s.seqMu.Lock()
		start := s.nextSeq
		s.nextSeq += uint64(len(input.Records))
		end := s.nextSeq
		s.seqMu.Unlock()

		ackCh := make(chan *inflightResult, 1)
		ticketCh <- &BatchSubmitTicket{ackCh: ackCh}
		ackCh <- &inflightResult{
			ack: &AppendAck{
				Start: StreamPosition{SeqNum: start},
				End:   StreamPosition{SeqNum: end},
				Tail:  StreamPosition{SeqNum: end},
			},
		}
		close(ackCh)
	}()

	return &SubmitFuture{ticketCh: ticketCh, errCh: errCh}, nil
}

func (s *delayedAcceptanceSession) Close() error {
	return nil
}

func TestProducer_CloseDrains(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := &delayedAckSession{ackGate: make(chan struct{})}
	producer := newProducerWithSession(ctx, batcher, session)

	future, err := producer.Submit(AppendRecord{Body: []byte("a")})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = producer.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("close returned before ack")
	case <-time.After(50 * time.Millisecond):
	}

	close(session.ackGate)

	if _, err := ticket.Ack(ctx); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("close did not drain in time")
	}
}

func TestProducer_SubmitWaitsForAcceptanceBeforeNextBatch(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 1,
	})
	session := &delayedAcceptanceSession{
		release: make(chan struct{}),
		started: make(chan struct{}, 10),
	}
	producer := newProducerWithSession(ctx, batcher, session)

	submitDone := make(chan struct{})
	go func() {
		defer close(submitDone)
		for range 5 {
			if _, err := producer.Submit(AppendRecord{Body: []byte("x")}); err != nil {
				return
			}
		}
	}()

	select {
	case <-session.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for first session submit")
	}

	select {
	case <-session.started:
		t.Fatalf("expected producer to block on first batch acceptance before submitting next batch")
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-submitDone:
		t.Fatalf("expected submit loop to block before acceptance is released")
	default:
	}

	close(session.release)

	select {
	case <-submitDone:
	case <-time.After(time.Second):
		t.Fatalf("submit loop did not complete after acceptance release")
	}

	if err := producer.Close(); err != nil {
		t.Fatalf("producer close failed: %v", err)
	}
}

func TestProducer_OversizedRecordRejected(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxMeteredBytes: 10,
		Linger:          time.Hour,
		ChannelBuffer:   10,
	})
	session := &fakeAppendSession{}
	producer := newProducerWithSession(ctx, batcher, session)

	if _, err := producer.Submit(AppendRecord{Body: make([]byte, 100)}); err == nil {
		t.Fatalf("expected error for oversized record")
	}

	_ = producer.Close()
}
