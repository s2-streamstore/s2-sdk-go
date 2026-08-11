package s2

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"
)

const flushTestTimeout = 5 * time.Second

type controlledAppendCall struct {
	input    *AppendInput
	ticketCh chan *BatchSubmitTicket
	errCh    chan error
	ackCh    chan *inflightResult
}

func (c *controlledAppendCall) accept() {
	c.ticketCh <- &BatchSubmitTicket{ackCh: c.ackCh}
}

func (c *controlledAppendCall) reject(err error) {
	c.errCh <- err
}

func (c *controlledAppendCall) resolve(ack *AppendAck, err error) {
	c.ackCh <- &inflightResult{ack: ack, err: err}
	close(c.ackCh)
}

type controlledAppendSession struct {
	calls chan *controlledAppendCall
}

func newControlledAppendSession() *controlledAppendSession {
	return &controlledAppendSession{calls: make(chan *controlledAppendCall, 16)}
}

func (s *controlledAppendSession) Submit(input *AppendInput) (*SubmitFuture, error) {
	call := &controlledAppendCall{
		input:    input,
		ticketCh: make(chan *BatchSubmitTicket, 1),
		errCh:    make(chan error, 1),
		ackCh:    make(chan *inflightResult, 1),
	}
	s.calls <- call
	return &SubmitFuture{ticketCh: call.ticketCh, errCh: call.errCh}, nil
}

func (s *controlledAppendSession) Close() error { return nil }

func submitRecord(t *testing.T, producer *Producer, body string) *RecordSubmitTicket {
	t.Helper()
	future, err := producer.Submit(AppendRecord{Body: []byte(body)})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ticket, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	return ticket
}

func startFlush(producer *Producer, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- producer.Flush(ctx) }()
	return done
}

func nextAppendCall(t *testing.T, session *controlledAppendSession) *controlledAppendCall {
	t.Helper()
	select {
	case call := <-session.calls:
		return call
	case <-time.After(flushTestTimeout):
		t.Fatal("append call timed out")
		return nil
	}
}

func waitFlush(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(flushTestTimeout):
		t.Fatal("flush timed out")
		return nil
	}
}

func requireFlushPending(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("flush returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func readyOutcome(t *testing.T, ticket *RecordSubmitTicket) *producerOutcome {
	t.Helper()
	select {
	case outcome := <-ticket.ackCh:
		if outcome == nil {
			t.Fatal("ticket resolved without an outcome")
		}
		return outcome
	default:
		t.Fatal("ticket was unresolved after flush")
		return nil
	}
}

func appendAck(start, end uint64) *AppendAck {
	return &AppendAck{
		Start: StreamPosition{SeqNum: start},
		End:   StreamPosition{SeqNum: end},
		Tail:  StreamPosition{SeqNum: end},
	}
}

func TestProducer_FlushWaitsForCoveredBatches(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    2,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := newControlledAppendSession()
	producer := newProducerWithSession(ctx, batcher, session)

	tickets := []*RecordSubmitTicket{
		submitRecord(t, producer, "a"),
		submitRecord(t, producer, "b"),
		submitRecord(t, producer, "c"),
	}
	flushDone := startFlush(producer, ctx)

	first := nextAppendCall(t, session)
	if len(first.input.Records) != 2 {
		t.Fatalf("first batch has %d records, want 2", len(first.input.Records))
	}
	first.accept()
	second := nextAppendCall(t, session)
	if len(second.input.Records) != 1 {
		t.Fatalf("partial batch has %d records, want 1", len(second.input.Records))
	}
	second.accept()

	firstAck := appendAck(41, 43)
	first.resolve(firstAck, nil)
	requireFlushPending(t, flushDone)
	secondAck := appendAck(43, 44)
	second.resolve(secondAck, nil)
	if err := waitFlush(t, flushDone); err != nil {
		t.Fatalf("flush: %v", err)
	}

	for i, ticket := range tickets {
		outcome := readyOutcome(t, ticket)
		if outcome.err != nil || outcome.indexedAck.SeqNum() != uint64(41+i) {
			t.Fatalf("ticket %d outcome: %+v", i, outcome)
		}
		wantBatch := firstAck
		if i == 2 {
			wantBatch = secondAck
		}
		if outcome.indexedAck.BatchAppendAck() != wantBatch {
			t.Fatalf("ticket %d references the wrong batch ack", i)
		}
	}

	if err := producer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestProducer_FlushBoundaryIsReusable(t *testing.T) {
	ctx := context.Background()
	matchSeqNum := uint64(0)
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    100,
		Linger:        time.Hour,
		ChannelBuffer: 10,
		MatchSeqNum:   &matchSeqNum,
	})
	session := newControlledAppendSession()
	producer := newProducerWithSession(ctx, batcher, session)

	if err := waitFlush(t, startFlush(producer, ctx)); err != nil {
		t.Fatalf("empty flush: %v", err)
	}
	select {
	case call := <-session.calls:
		t.Fatalf("empty flush appended: %+v", call.input)
	default:
	}

	firstTicket := submitRecord(t, producer, "first")
	firstFlush := startFlush(producer, ctx)
	firstCall := nextAppendCall(t, session)
	if firstCall.input.MatchSeqNum == nil || *firstCall.input.MatchSeqNum != 0 {
		t.Fatalf("first match seq num: %v", firstCall.input.MatchSeqNum)
	}
	firstCall.accept()

	secondTicket := submitRecord(t, producer, "second")
	batcher.Flush()
	firstCall.resolve(appendAck(0, 1), nil)

	var secondCall *controlledAppendCall
	select {
	case err := <-firstFlush:
		if err != nil {
			t.Fatalf("first flush: %v", err)
		}
	case secondCall = <-session.calls:
		secondCall.accept()
		if err := waitFlush(t, firstFlush); err != nil {
			t.Fatalf("first flush waited for later work: %v", err)
		}
	case <-time.After(flushTestTimeout):
		t.Fatal("first flush timed out")
	}

	if outcome := readyOutcome(t, firstTicket); outcome.err != nil {
		t.Fatalf("first ticket: %v", outcome.err)
	}
	select {
	case outcome := <-secondTicket.ackCh:
		t.Fatalf("first flush covered the later ticket: %+v", outcome)
	default:
	}

	secondFlush := startFlush(producer, ctx)
	if secondCall == nil {
		secondCall = nextAppendCall(t, session)
		secondCall.accept()
	}
	if secondCall.input.MatchSeqNum == nil || *secondCall.input.MatchSeqNum != 1 {
		t.Fatalf("second match seq num: %v", secondCall.input.MatchSeqNum)
	}
	secondCall.resolve(appendAck(1, 2), nil)
	if err := waitFlush(t, secondFlush); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if outcome := readyOutcome(t, secondTicket); outcome.err != nil {
		t.Fatalf("second ticket: %v", outcome.err)
	}

	if err := producer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestProducer_FlushPropagatesTerminalAckError(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := newControlledAppendSession()
	producer := newProducerWithSession(ctx, batcher, session)
	tickets := []*RecordSubmitTicket{
		submitRecord(t, producer, "fails"),
		submitRecord(t, producer, "covered"),
	}

	flushDone := startFlush(producer, ctx)
	first := nextAppendCall(t, session)
	first.accept()
	second := nextAppendCall(t, session)
	second.accept()
	conditionErr := newSeqNumMismatchError(http.StatusPreconditionFailed, 0)
	first.resolve(nil, conditionErr)

	if err := waitFlush(t, flushDone); err != conditionErr {
		t.Fatalf("flush error: %v", err)
	}
	for i, ticket := range tickets {
		if err := readyOutcome(t, ticket).err; err != conditionErr {
			t.Fatalf("ticket %d error: %v", i, err)
		}
	}
	if err := waitFlush(t, startFlush(producer, ctx)); err != conditionErr {
		t.Fatalf("repeated flush error: %v", err)
	}
	if _, err := producer.Submit(AppendRecord{}); err != conditionErr {
		t.Fatalf("submit after failure: %v", err)
	}
	if err := producer.Close(); err != conditionErr {
		t.Fatalf("close error: %v", err)
	}
}

func TestProducer_SynchronousFailureReleasesCoveredTickets(t *testing.T) {
	ctx := context.Background()
	terminalErr := errors.New("acceptance failed")
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 10,
	})
	session := newControlledAppendSession()
	producer := newProducerWithSession(ctx, batcher, session)
	tickets := []*RecordSubmitTicket{
		submitRecord(t, producer, "ack pending"),
		submitRecord(t, producer, "acceptance fails"),
	}

	flushDone := startFlush(producer, ctx)
	first := nextAppendCall(t, session)
	first.accept()
	second := nextAppendCall(t, session)
	second.reject(terminalErr)

	if err := waitFlush(t, flushDone); err != terminalErr {
		t.Fatalf("flush error: %v", err)
	}
	for i, ticket := range tickets {
		if err := readyOutcome(t, ticket).err; err != terminalErr {
			t.Fatalf("ticket %d error: %v", i, err)
		}
	}
	if err := producer.Close(); err != terminalErr {
		t.Fatalf("close error: %v", err)
	}
}

func TestProducer_FlushCancellationWhileBackpressured(t *testing.T) {
	ctx := context.Background()
	batcher := NewBatcher(ctx, &BatchingOptions{
		MaxRecords:    1,
		Linger:        time.Hour,
		ChannelBuffer: 1,
	})
	session := newControlledAppendSession()
	producer := newProducerWithSession(ctx, batcher, session)
	tickets := []*RecordSubmitTicket{
		submitRecord(t, producer, "consumer blocked"),
		submitRecord(t, producer, "channel full"),
	}
	first := nextAppendCall(t, session)

	flushCtx, cancelFlush := context.WithCancel(ctx)
	flushDone := startFlush(producer, flushCtx)
	deadline := time.Now().Add(flushTestTimeout)
	for batcher.mu.TryLock() {
		batcher.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("barrier did not reach backpressure")
		}
		runtime.Gosched()
	}
	cancelFlush()
	if err := waitFlush(t, flushDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled flush: %v", err)
	}

	first.accept()
	first.resolve(appendAck(0, 1), nil)
	second := nextAppendCall(t, session)
	second.accept()
	second.resolve(appendAck(1, 2), nil)
	if err := waitFlush(t, startFlush(producer, ctx)); err != nil {
		t.Fatalf("flush after backpressure: %v", err)
	}
	for i, ticket := range tickets {
		outcome := readyOutcome(t, ticket)
		if outcome.err != nil {
			t.Fatalf("ticket %d outcome: %+v", i, outcome)
		}
	}
	if err := producer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
