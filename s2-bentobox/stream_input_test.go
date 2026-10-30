package bentobox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
)

func newTestStreamInput() *streamInput {
	cache := newSeqNumCache(nil, silentLogger{})
	return &streamInput{
		Stream:   "demo",
		cache:    cache,
		toAck:    newToAckMap("demo", cache, 0, silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}
}

// Calling the returned AckFunc more than once with an error must not enqueue
// the same batch for redelivery more than once.
func TestAckFuncDoubleNackEnqueuesOnce(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	boom := errors.New("boom")
	_ = ackFunc(context.Background(), boom)
	_ = ackFunc(context.Background(), boom)

	if got := len(si.nacks); got != 1 {
		t.Fatalf("expected batch enqueued for redelivery exactly once, got %d", got)
	}
}

// Once a batch is acked, a subsequent invocation with an error must not cause a
// redelivery (which would reprocess already-committed records).
func TestAckFuncAckThenNackDoesNotRedeliver(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	if err := ackFunc(context.Background(), nil); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	_ = ackFunc(context.Background(), errors.New("boom"))

	if got := len(si.nacks); got != 0 {
		t.Fatalf("expected no redelivery after ack, got %d", got)
	}
}

// Subsequent invocations must return the same result as the first.
func TestAckFuncReturnsCachedResult(t *testing.T) {
	si := newTestStreamInput()
	records := []s2.SequencedRecord{{SeqNum: 0}}

	_, ackFunc, err := si.handleBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("handleBatch: %v", err)
	}

	first := ackFunc(context.Background(), nil)
	second := ackFunc(context.Background(), nil)

	if first != nil {
		t.Fatalf("first ack returned error: %v", first)
	}
	if second != first {
		t.Fatalf("expected cached result %v on repeat, got %v", first, second)
	}
}

// fakeCache is a durable SeqNumCache that records the last persisted value.
type fakeCache struct {
	mu     sync.Mutex
	values map[string]uint64
}

func newFakeCache() *fakeCache { return &fakeCache{values: map[string]uint64{}} }

func (f *fakeCache) Get(_ context.Context, stream string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[stream], nil
}

func (f *fakeCache) Set(_ context.Context, stream string, seqNum uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[stream] = seqNum
	return nil
}

// Reproduces issue #294: an ack that completes after the streamInput is closed
// returns success but does not persist the sequence position, so a re-add of
// the stream resumes from the stale position and re-delivers acked records.
func TestAckAfterCloseDoesNotUpdateCache(t *testing.T) {
	const stream = "demo"
	inner := newFakeCache()
	cache := newSeqNumCache(inner, silentLogger{})
	si := &streamInput{
		Stream:   stream,
		cache:    cache,
		toAck:    newToAckMap(stream, cache, 0, silentLogger{}),
		nacks:    make(chan []s2.SequencedRecord, 8),
		Logger:   silentLogger{},
		closedCh: make(chan struct{}),
	}

	records := []s2.SequencedRecord{{SeqNum: 100}, {SeqNum: 101}, {SeqNum: 102}}
	si.toAck.Add(records)

	// Simulate the streamInput having been closed (session restart / removal /
	// shutdown) while this batch's ack is still pending in the Bento pipeline.
	close(si.closedCh)

	if err := si.ack(context.Background(), records); err != nil {
		t.Fatalf("ack returned error: %v", err)
	}

	got, _ := inner.Get(context.Background(), stream)
	if got != 103 {
		t.Fatalf("expected durable cache to advance to 103 after ack, got %d", got)
	}
}

// fakeReadSession is a controllable stand-in for *s2.ReadSession used to drive
// the streamInput pump in tests without a real S2 server.
//
// It yields pending records one at a time via Next/Record. When pending is
// exhausted it either blocks inside Next() until Close is called (the idle
// stream case, blockOnIdle=true) or returns false immediately. Err returns a
// preset terminal error. This lets tests reproduce the exact scenario from the
// bug report: a read parked in session.Next() on an idle stream while a nack
// is pending.
type fakeReadSession struct {
	mu          sync.Mutex
	pending     []s2.SequencedRecord
	current     s2.SequencedRecord
	err         error
	blockOnIdle bool
	closed      bool
	done        chan struct{}
	idle        chan struct{}
}

func newFakeReadSession(records []s2.SequencedRecord) *fakeReadSession {
	return &fakeReadSession{
		pending:     records,
		blockOnIdle: true,
		done:        make(chan struct{}),
		idle:        make(chan struct{}, 1),
	}
}

func (s *fakeReadSession) Next() bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if len(s.pending) > 0 {
		s.current = s.pending[0]
		s.pending = s.pending[1:]
		s.mu.Unlock()
		return true
	}
	blockOnIdle := s.blockOnIdle
	s.mu.Unlock()
	if !blockOnIdle {
		return false
	}
	// Signal once that the session has gone idle (parked inside Next()), then
	// wait until Close unblocks the read.
	select {
	case s.idle <- struct{}{}:
	default:
	}
	<-s.done
	return false
}

func (s *fakeReadSession) Record() s2.SequencedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *fakeReadSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeReadSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	return nil
}

// newPumpedStreamInput builds a streamInput wired to a fake session and starts
// the pump goroutine, mirroring what connectStreamInput does for a real
// session. Cleanup closes the input (and so drains the pump).
func newPumpedStreamInput(t *testing.T, session readSession) *streamInput {
	t.Helper()
	cache := newSeqNumCache(nil, silentLogger{})
	si := &streamInput{
		Stream:       "demo",
		session:      session,
		cache:        cache,
		toAck:        newToAckMap("demo", cache, 0, silentLogger{}),
		nacks:        make(chan []s2.SequencedRecord, 8),
		records:      make(chan []s2.SequencedRecord),
		Logger:       silentLogger{},
		closeSession: func() {},
		closedCh:     make(chan struct{}),
		sessionDone:  make(chan struct{}),
	}
	go si.pump()
	t.Cleanup(func() {
		_ = si.Close(context.Background())
	})
	return si
}

type readBatchResult struct {
	batch []s2.SequencedRecord
	err   error
}

// callReadBatch runs ReadBatch on a goroutine and returns the result via a
// buffered channel so callers can assert it parks / unparks with a timeout.
func callReadBatch(si *streamInput, ctx context.Context) <-chan readBatchResult {
	out := make(chan readBatchResult, 1)
	go func() {
		b, _, e := si.ReadBatch(ctx)
		out <- readBatchResult{b, e}
	}()
	return out
}

// Regression test for the idle-stream nack stall: a batch that Bento nacks
// must be redelivered promptly even while the read pump is parked inside
// session.Next() on an idle stream. With the bug, the second ReadBatch
// blocked until a new record arrived; here it must return as soon as the
// ack function enqueues the nacked batch.
func TestStreamInputNackRedeliveredWhileSessionIdle(t *testing.T) {
	sess := newFakeReadSession([]s2.SequencedRecord{{SeqNum: 0}})
	si := newPumpedStreamInput(t, sess)

	// First ReadBatch returns the only record and its ack function. The pump
	// then parks inside Next() (idle stream).
	batch, ackFunc, err := si.ReadBatch(context.Background())
	if err != nil {
		t.Fatalf("first ReadBatch: %v", err)
	}
	if len(batch) != 1 || batch[0].SeqNum != 0 {
		t.Fatalf("first ReadBatch returned unexpected batch: %+v", batch)
	}

	// Wait for the pump to actually be parked in the idle Next() call.
	select {
	case <-sess.idle:
	case <-time.After(time.Second):
		t.Fatal("pump never went idle")
	}

	// Start a second ReadBatch; it must park because the session is idle and
	// nothing has been nacked yet.
	second := callReadBatch(si, context.Background())
	select {
	case r := <-second:
		t.Fatalf("second ReadBatch returned before any nack: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	// Bento nacks the first batch. The ack function enqueues it for redelivery.
	if err := ackFunc(context.Background(), errors.New("boom")); err != nil {
		t.Fatalf("nack: %v", err)
	}

	// The parked ReadBatch must wake on the nack and return the redelivered
	// batch promptly. With the bug this hangs until a new record arrives.
	select {
	case r := <-second:
		if r.err != nil {
			t.Fatalf("redelivered ReadBatch returned error: %v", r.err)
		}
		if len(r.batch) != 1 || r.batch[0].SeqNum != 0 {
			t.Fatalf("expected nacked batch (seq 0) redelivered, got %+v", r.batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nacked batch was not redelivered while the stream was idle")
	}
}

// Records produced by the session must still surface through ReadBatch when
// the stream is active (no regression in the happy path).
func TestStreamInputDeliversRecordsFromSession(t *testing.T) {
	sess := newFakeReadSession([]s2.SequencedRecord{{SeqNum: 0}, {SeqNum: 1}})
	si := newPumpedStreamInput(t, sess)

	for want := range uint64(2) {
		readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		batch, ackFunc, err := si.ReadBatch(readCtx)
		cancel()
		if err != nil {
			t.Fatalf("ReadBatch[%d]: %v", want, err)
		}
		if len(batch) != 1 || batch[0].SeqNum != want {
			t.Fatalf("ReadBatch[%d] returned unexpected batch: %+v", want, batch)
		}
		if err := ackFunc(context.Background(), nil); err != nil {
			t.Fatalf("ack[%d]: %v", want, err)
		}
	}
}

// When the session ends with a terminal error, ReadBatch must surface it (not
// hang), and it must keep surfacing the same error for subsequent calls.
func TestStreamInputSurfacesSessionTerminalError(t *testing.T) {
	boom := errors.New("session boom")
	sess := newFakeReadSession(nil)
	sess.blockOnIdle = false
	sess.err = boom
	si := newPumpedStreamInput(t, sess)

	deadline := time.After(time.Second)

	// First call surfaces the terminal error rather than parking.
	out := callReadBatch(si, context.Background())
	select {
	case r := <-out:
		if !errors.Is(r.err, boom) {
			t.Fatalf("expected terminal error %v, got %v", boom, r.err)
		}
	case <-deadline:
		t.Fatal("ReadBatch parked instead of surfacing the terminal error")
	}

	// Subsequent calls keep resurfacing it (no hang after the first consume).
	out2 := callReadBatch(si, context.Background())
	select {
	case r := <-out2:
		if !errors.Is(r.err, boom) {
			t.Fatalf("expected terminal error to persist, got %v", r.err)
		}
	case <-deadline:
		t.Fatal("second ReadBatch parked instead of resurfacing the terminal error")
	}
}

// When the session ends cleanly (Next returns false with no error), ReadBatch
// must return ErrInputClosed so the recv loop treats it as a "restart source".
func TestStreamInputReturnsErrInputClosedWhenSessionEndsCleanly(t *testing.T) {
	sess := newFakeReadSession(nil)
	sess.blockOnIdle = false
	si := newPumpedStreamInput(t, sess)

	deadline := time.After(time.Second)
	for i := range 2 {
		out := callReadBatch(si, context.Background())
		select {
		case r := <-out:
			if !errors.Is(r.err, ErrInputClosed) {
				t.Fatalf("call %d: expected ErrInputClosed, got %v", i, r.err)
			}
		case <-deadline:
			t.Fatalf("call %d parked instead of returning ErrInputClosed", i)
		}
	}
}

// A cancelled context passed to ReadBatch must be honoured even while the read
// is parked on an idle stream. The original implementation ignored the caller's
// context once it parked inside session.Next().
func TestStreamInputReadBatchReturnsOnContextCancel(t *testing.T) {
	sess := newFakeReadSession([]s2.SequencedRecord{{SeqNum: 0}})
	si := newPumpedStreamInput(t, sess)

	batch, ackFunc, err := si.ReadBatch(context.Background())
	if err != nil {
		t.Fatalf("first ReadBatch: %v", err)
	}
	if len(batch) != 1 || batch[0].SeqNum != 0 {
		t.Fatalf("first ReadBatch returned unexpected batch: %+v", batch)
	}

	select {
	case <-sess.idle:
	case <-time.After(time.Second):
		t.Fatal("pump never went idle")
	}

	readCtx, cancel := context.WithCancel(context.Background())
	parked := callReadBatch(si, readCtx)
	select {
	case r := <-parked:
		t.Fatalf("ReadBatch returned before context cancel: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case r := <-parked:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadBatch did not return after context cancellation")
	}

	// Ack the first batch so cleanup does not leave it pending.
	if err := ackFunc(context.Background(), nil); err != nil {
		t.Fatalf("ack: %v", err)
	}
}

// Closing the input while ReadBatch is parked on an idle stream must wake the
// parked call with ErrInputClosed and must let Close return (pump exits).
func TestStreamInputReadBatchReturnsErrInputClosedOnClose(t *testing.T) {
	sess := newFakeReadSession([]s2.SequencedRecord{{SeqNum: 0}})
	si := newPumpedStreamInput(t, sess)

	batch, _, err := si.ReadBatch(context.Background())
	if err != nil {
		t.Fatalf("first ReadBatch: %v", err)
	}
	if len(batch) != 1 || batch[0].SeqNum != 0 {
		t.Fatalf("first ReadBatch returned unexpected batch: %+v", batch)
	}

	select {
	case <-sess.idle:
	case <-time.After(time.Second):
		t.Fatal("pump never went idle")
	}

	parked := callReadBatch(si, context.Background())
	select {
	case r := <-parked:
		t.Fatalf("ReadBatch returned before close: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	if err := si.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case r := <-parked:
		if !errors.Is(r.err, ErrInputClosed) {
			t.Fatalf("expected ErrInputClosed after close, got %v", r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked ReadBatch did not return after Close")
	}
}
