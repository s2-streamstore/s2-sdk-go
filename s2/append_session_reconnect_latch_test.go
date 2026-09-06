package s2

import (
	"context"
	"sync"
	"testing"
	"time"
)

// runReadAcksViaErrorsChCloseDrain forces AppendSession.readAcks through the
// errorsCh-close drain branch of its select. readAcksLoop closes errorsCh
// before acksCh (defer order is LIFO), so an advice ack can still be buffered
// in acksCh when the errorsCh-closed case becomes the only ready case. This
// helper reproduces that shutdown race deterministically: errorsCh is closed
// while acksCh is empty, so the drain is the sole ready case; the buffered
// acks are then drained and acksCh is closed to end the range.
func runReadAcksViaErrorsChCloseDrain(t *testing.T, r *AppendSession, ts *transportAppendSession, acks ...*AppendAck) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.readAcks(ts)
	}()
	close(ts.errorsCh)
	for _, ack := range acks {
		ts.acksCh <- ack
	}
	close(ts.acksCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readAcks did not return after draining acksCh")
	}
}

// newHandBuiltAppendSession constructs an AppendSession without running the
// pump goroutine, so individual methods (readAcks, handleReconnectAdvice) can
// be driven directly and their effects observed.
func newHandBuiltAppendSession(stream *StreamClient, opts *AppendSessionOptions) *AppendSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &AppendSession{
		streamClient: stream,
		options:      opts,
		capacity:     newCapacityTracker(1024, 1),
		sessionRefs:  make(map[*transportAppendSession]int),
		pumpCtx:      ctx,
		pumpCancel:   cancel,
		closeDone:    make(chan struct{}),
		wakeup:       make(chan struct{}, 1),
	}
}

// TestAppendSession_ReconnectAdviceHandledOnErrorsChCloseDrain verifies that a
// reconnect-advice ack drained through the errorsCh-close branch (rather than
// consumed via the acksCh case) still triggers the full advice path: halfClose,
// reconnectAccepted, and the deferred handleReconnectAdvice that records an
// advised reconnect and clears currentSession. Without the fix the drain
// calls handleAck only, so handleReconnectAdvice never runs and
// advisedReconnects stays at zero; this test asserts the recorded count (the
// discriminating invariant) rather than the transport-level holdInputsForReconnect.
func TestAppendSession_ReconnectAdviceHandledOnErrorsChCloseDrain(t *testing.T) {
	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	r := newHandBuiltAppendSession(stream, &AppendSessionOptions{RetryConfig: DefaultRetryConfig})
	defer r.pumpCancel()

	dead := newTransportSession(stream, &signalWriteCloser{})
	dead.reconnectAdvised.Store(true)
	r.sessionMu.Lock()
	r.currentSession = dead
	r.sessionMu.Unlock()

	adviceAck := &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}
	runReadAcksViaErrorsChCloseDrain(t, r, dead, adviceAck)

	r.stateMu.RLock()
	advisedCount := r.advisedReconnects.count
	r.stateMu.RUnlock()
	if advisedCount != 1 {
		t.Fatalf("expected handleReconnectAdvice to record one advised reconnect after drain, got %d (advice was dropped by the close branch)", advisedCount)
	}

	r.sessionMu.RLock()
	current := r.currentSession
	r.sessionMu.RUnlock()
	if current != nil {
		t.Fatalf("expected currentSession cleared by handleReconnectAdvice after drain, got %p", current)
	}

	if dead.reconnectDeclined.Load() {
		t.Fatalf("expected reconnect accepted (not declined) on fresh advised-reconnect budget")
	}
	if !dead.ReconnectAdvised() {
		t.Fatalf("reconnectAdvised should still be latched on the transport")
	}
}

// TestAppendSession_ReconnectAdviceDeclineHandledOnErrorsChCloseDrain
// verifies the decline path is also mirrored into the drain. When the advised
// reconnect budget is exhausted, the drain must call declineReconnect (and
// wake the pump) so holdInputsForReconnect releases; otherwise the pump stays
// pinned to the dead transport exactly as the accept-path bug.
func TestAppendSession_ReconnectAdviceDeclineHandledOnErrorsChCloseDrain(t *testing.T) {
	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	r := newHandBuiltAppendSession(stream, &AppendSessionOptions{RetryConfig: DefaultRetryConfig})
	defer r.pumpCancel()

	// Pre-exhaust the advised reconnect budget so shouldReconnectOnAdvice
	// returns false and the drain takes the decline path.
	r.stateMu.Lock()
	r.advisedReconnects = advisedReconnects{count: maxAdvisedReconnects, last: time.Now()}
	r.stateMu.Unlock()

	dead := newTransportSession(stream, &signalWriteCloser{})
	dead.reconnectAdvised.Store(true)
	r.sessionMu.Lock()
	r.currentSession = dead
	r.sessionMu.Unlock()

	adviceAck := &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}
	runReadAcksViaErrorsChCloseDrain(t, r, dead, adviceAck)

	if !dead.reconnectDeclined.Load() {
		t.Fatalf("expected declineReconnect to run on the drain's decline path; reconnectDeclined is false")
	}
	if dead.holdInputsForReconnect() {
		t.Fatalf("holdInputsForReconnect should be false after declineReconnect; pump would latch on dead transport")
	}

	// The decline path must wake the pump so it can move off the dead
	// transport. With an empty inflight queue handleACK is a no-op, so this
	// wakeup is the only one produced by the drain.
	select {
	case <-r.wakeup:
	case <-time.After(time.Second):
		t.Fatalf("decline path did not wake the pump")
	}

	r.sessionMu.RLock()
	current := r.currentSession
	r.sessionMu.RUnlock()
	if current != dead {
		t.Fatalf("decline path should leave currentSession in place (handleReconnectAdvice does not run), got %p", current)
	}

	r.stateMu.RLock()
	advisedCount := r.advisedReconnects.count
	r.stateMu.RUnlock()
	if advisedCount != maxAdvisedReconnects {
		t.Fatalf("decline path must not consume the advised-reconnect budget, got %d", advisedCount)
	}
}

// TestAppendSession_RecoversAfterDrainedAdviceAckEndToEnd reproduces the full
// shutdown race end-to-end through the pump and the session factory: the advice
// ack for the first record is delivered through the errorsCh-close drain, the
// deferral handleReconnectAdvice must clear the dead transport, and a
// subsequent submit must open a fresh transport and resolve. Without the fix
// the drain processes the advice ack as a plain ack, currentSession stays pinned
// to the dead transport, holdInputsForReconnect latches, every later submit
// hangs, and Close never returns.
func TestAppendSession_RecoversAfterDrainedAdviceAckEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	stream := newTestStreamClientForAppend(retryCfg)

	writeSignal1 := make(chan struct{}, 1)
	writeSignal2 := make(chan struct{}, 1)

	var (
		mu  sync.Mutex
		e1  *transportAppendSession
		e2  *transportAppendSession
		nth int
	)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		mu.Lock()
		nth++
		call := nth
		mu.Unlock()
		switch call {
		case 1:
			ts := newTransportSession(stream, &signalWriteCloser{signal: writeSignal1})
			mu.Lock()
			e1 = ts
			mu.Unlock()
			return ts, nil
		default:
			ts := newTransportSession(stream, &signalWriteCloser{signal: writeSignal2})
			mu.Lock()
			e2 = ts
			mu.Unlock()
			return ts, nil
		}
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("AppendSession: %v", err)
	}

	// First append is sent on the first transport, then the server's
	// reconnect-advice ack for it is delivered through the errorsCh-close drain.
	future1, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("a")}}})
	if err != nil {
		t.Fatalf("submit 1 failed: %v", err)
	}
	ticket1, err := future1.Wait(ctx)
	if err != nil {
		t.Fatalf("wait 1 failed: %v", err)
	}

	select {
	case <-writeSignal1:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first write")
	}
	mu.Lock()
	first := e1
	mu.Unlock()
	if first == nil {
		t.Fatal("first transport was never created")
	}

	// Latch the advice as handleFrame would, then force the drain by closing
	// errorsCh while acksCh is empty.
	first.reconnectAdvised.Store(true)
	close(first.errorsCh)
	first.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}
	close(first.acksCh)

	if _, err := ticket1.Ack(ctx); err != nil {
		t.Fatalf("first ack should resolve through the drain, got %v", err)
	}

	// The deferral handleReconnectAdvice must clear the dead transport. If
	// the advice was dropped by the drain (the bug), currentSession stays
	// pinned and this never becomes nil.
	if !waitForCondition(time.Second, func() bool {
		session.sessionMu.RLock()
		c := session.currentSession
		session.sessionMu.RUnlock()
		return c == nil
	}) {
		t.Fatal("currentSession was not cleared after advice ack drained through the close branch; reconnect advice was dropped")
	}

	// Second append must open a fresh transport and resolve. With the bug
	// present, getSession sees the pinned dead transport, holdInputsForReconnect
	// latches, and this never resolves.
	future2, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("b")}}})
	if err != nil {
		t.Fatalf("submit 2 failed: %v", err)
	}
	ticket2, err := future2.Wait(ctx)
	if err != nil {
		t.Fatalf("wait 2 failed: %v", err)
	}

	select {
	case <-writeSignal2:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second write on a fresh transport")
	}
	mu.Lock()
	second := e2
	mu.Unlock()
	if second == nil {
		t.Fatal("second transport was never created; pump never reconnected after the drained advice")
	}
	if second == first {
		t.Fatal("expected a fresh transport after reconnect advice, got the dead one")
	}

	second.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 1},
		End:   StreamPosition{SeqNum: 2},
		Tail:  StreamPosition{SeqNum: 2},
	}
	if _, err := ticket2.Ack(ctx); err != nil {
		t.Fatalf("second ack failed: %v", err)
	}

	// Close must return: with the bug, the held second entry (or the pinned
	// dead transport keeping the queue non-empty) keeps Close() blocked on
	// closeDone forever.
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return; AppendSession is hung after reconnect advice was dropped by the close branch")
	}
}

func waitForCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return cond()
		}
		time.Sleep(time.Millisecond)
	}
}
