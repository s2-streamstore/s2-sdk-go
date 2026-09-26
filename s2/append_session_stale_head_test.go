package s2

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingLogHandler blocks the first Handle call until release is closed, so
// a test can deterministically widen the checkTimeouts TOCTOU window between
// the inflightMu.RUnlock (head snapshot) and the session-claim action.
type blockingLogHandler struct {
	triggered chan struct{}
	release   chan struct{}
	blocked   atomic.Bool
}

func (h *blockingLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *blockingLogHandler) Handle(_ context.Context, _ slog.Record) error {
	if h.blocked.CompareAndSwap(false, true) {
		h.triggered <- struct{}{}
		<-h.release
	}
	return nil
}

func (h *blockingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *blockingLogHandler) WithGroup(_ string) slog.Handler      { return h }

// newStaleHeadTimeoutSession builds a white-box AppendSession with H1 (past its
// deadline) and H2 (fresh, well within its budget) both sent on transport.
// H2 has a strictly later attemptStart than H1, mirroring two distinct
// submitInflightBatches passes.
func newStaleHeadTimeoutSession(t *testing.T, retryCfg *RetryConfig, logger *slog.Logger) (*AppendSession, *transportAppendSession, *inflightEntry, *inflightEntry) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream := newTestStreamClientForAppend(retryCfg)
	stream.basinClient.requestTimeout = 5 * time.Second
	stream.logger = logger

	transport := &transportAppendSession{
		streamClient:  stream,
		acksCh:        make(chan *AppendAck, appendAckChannelBuffer),
		errorsCh:      make(chan error, 1),
		closed:        make(chan struct{}),
		requestWriter: &signalWriteCloser{},
	}
	t.Cleanup(func() { transport.Close() })

	session := &AppendSession{
		streamClient:   stream,
		options:        &AppendSessionOptions{RetryConfig: retryCfg},
		capacity:       newCapacityTracker(1024, 2),
		sessionRefs:    make(map[*transportAppendSession]int),
		currentSession: transport,
		pumpCtx:        ctx,
		pumpCancel:     cancel,
		closeDone:      make(chan struct{}),
		wakeup:         make(chan struct{}, 1),
	}

	h1 := &inflightEntry{
		input:          &AppendInput{Records: []AppendRecord{{Body: []byte("h1")}}},
		expectedCount:  1,
		meteredBytes:   1,
		requestTimeout: 5 * time.Second,
		attemptStart:   time.Now().Add(-10 * time.Second),
		resultCh:       make(chan *inflightResult, 1),
	}
	h2 := &inflightEntry{
		input:          &AppendInput{Records: []AppendRecord{{Body: []byte("h2")}}},
		expectedCount:  1,
		meteredBytes:   1,
		requestTimeout: 5 * time.Second,
		attemptStart:   time.Now(),
		resultCh:       make(chan *inflightResult, 1),
	}
	h1.sentOnSessions = []*transportAppendSession{transport}
	h2.sentOnSessions = []*transportAppendSession{transport}
	session.inflightQueue = []*inflightEntry{h1, h2}
	session.sessionRefs[transport] = 2
	transport.markWriteSignalled()
	transport.markWriteSignalled()

	return session, transport, h1, h2
}

// TestAppendSession_StaleHeadTimeoutTearsDownJustAckedSession reproduces the
// reported race: checkTimeouts snapshots H1 (timed out), releases inflightMu,
// then handleAck pops H1 in the window. Before the fix, the stale snapshot
// tore down the just-acked session and failed H2 with REQUEST_TIMEOUT before
// H2's own budget elapsed. After the fix, checkTimeouts re-validates the head
// atomically and bails on the stale snapshot.
func TestAppendSession_StaleHeadTimeoutTearsDownJustAckedSession(t *testing.T) {
	for _, policy := range []AppendRetryPolicy{AppendRetryPolicyAll, AppendRetryPolicyNoSideEffects} {
		t.Run(string(policy), func(t *testing.T) {
			retryCfg := &RetryConfig{
				MaxAttempts:       3,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: policy,
			}
			triggered := make(chan struct{})
			release := make(chan struct{})
			logger := slog.New(&blockingLogHandler{triggered: triggered, release: release})

			session, transport, h1, h2 := newStaleHeadTimeoutSession(t, retryCfg, logger)

			done := make(chan struct{})
			go func() {
				session.checkTimeouts()
				close(done)
			}()

			select {
			case <-triggered:
			case <-time.After(2 * time.Second):
				t.Fatal("checkTimeouts did not reach logError in time")
			}

			// H1's ACK arrives while checkTimeouts is in the TOCTOU window.
			session.handleAck(transport, &AppendAck{
				Start: StreamPosition{SeqNum: 0},
				End:   StreamPosition{SeqNum: 1},
				Tail:  StreamPosition{SeqNum: 2},
			})

			close(release)

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("checkTimeouts did not complete")
			}

			select {
			case result := <-h1.resultCh:
				if result == nil || result.err != nil {
					t.Fatalf("expected H1 to succeed, got %+v", result)
				}
			default:
				t.Fatal("expected H1 to have an acknowledgement result")
			}

			select {
			case result := <-h2.resultCh:
				t.Fatalf("BUG: H2 was wrongly failed with %v; H1 was already acknowledged "+
					"and H2's own timeout budget (attemptStart=%v, requestTimeout=%v) had not elapsed",
					result.err, h2.attemptStart, h2.requestTimeout)
			default:
			}

			session.sessionMu.RLock()
			current := session.currentSession
			session.sessionMu.RUnlock()
			if current != transport {
				t.Fatalf("BUG: session was torn down based on a stale head timeout; "+
					"currentSession = %p, want %p", current, transport)
			}
			if session.closed {
				t.Fatal("BUG: session was closed based on a stale head timeout")
			}
		})
	}
}

// TestAppendSession_StaleHeadTimeoutNoOpWhenQueueDrained covers the empty-queue
// re-validation branch: both H1 and H2 are acknowledged (queue drained) while
// checkTimeouts is in the TOCTOU window, so the stale H1 snapshot must not
// tear down the live session.
func TestAppendSession_StaleHeadTimeoutNoOpWhenQueueDrained(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}
	triggered := make(chan struct{})
	release := make(chan struct{})
	logger := slog.New(&blockingLogHandler{triggered: triggered, release: release})

	session, transport, h1, h2 := newStaleHeadTimeoutSession(t, retryCfg, logger)

	done := make(chan struct{})
	go func() {
		session.checkTimeouts()
		close(done)
	}()

	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not reach logError in time")
	}

	// Both H1 and H2 are acknowledged while checkTimeouts is in the window.
	session.handleAck(transport, &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 2},
	})
	session.handleAck(transport, &AppendAck{
		Start: StreamPosition{SeqNum: 1},
		End:   StreamPosition{SeqNum: 2},
		Tail:  StreamPosition{SeqNum: 2},
	})

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not complete")
	}

	for i, entry := range []*inflightEntry{h1, h2} {
		select {
		case result := <-entry.resultCh:
			if result == nil || result.err != nil {
				t.Fatalf("expected entry %d to succeed, got %+v", i, result)
			}
		default:
			t.Fatalf("expected entry %d to have an acknowledgement result", i)
		}
	}

	session.sessionMu.RLock()
	current := session.currentSession
	session.sessionMu.RUnlock()
	if current != transport {
		t.Fatalf("BUG: session torn down after queue drained; currentSession = %p, want %p",
			current, transport)
	}
	if session.closed {
		t.Fatal("BUG: session closed after queue drained based on stale timeout")
	}
}

// TestAppendSession_LegitimateHeadTimeoutStillTearsDownSession confirms the fix
// does not suppress genuine head timeouts: when the head is still the current,
// uncompleted, timed-out entry at re-validation time, the session is torn down
// and routed through retry/failure as before.
func TestAppendSession_LegitimateHeadTimeoutStillTearsDownSession(t *testing.T) {
	for _, policy := range []AppendRetryPolicy{AppendRetryPolicyAll, AppendRetryPolicyNoSideEffects} {
		t.Run(string(policy), func(t *testing.T) {
			retryCfg := &RetryConfig{
				MaxAttempts:       3,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: policy,
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			session, _, h1, h2 := newStaleHeadTimeoutSession(t, retryCfg, logger)

			// No ACK is delivered: the head is genuinely still timed-out.
			session.checkTimeouts()

			session.sessionMu.RLock()
			current := session.currentSession
			session.sessionMu.RUnlock()
			if current != nil {
				t.Fatalf("legitimate timeout should have cleared currentSession, got %p", current)
			}

			switch policy {
			case AppendRetryPolicyAll:
				if session.closed {
					t.Fatal("legitimate retryable timeout under All must not close the session")
				}
				session.stateMu.RLock()
				attempt := session.currentAttempt
				session.stateMu.RUnlock()
				if attempt != 1 {
					t.Fatalf("legitimate timeout under All should schedule one retry, got currentAttempt=%d", attempt)
				}
				select {
				case result := <-h1.resultCh:
					t.Fatalf("H1 should still be in flight pending retry, got %+v", result)
				default:
				}
				select {
				case result := <-h2.resultCh:
					t.Fatalf("H2 should still be in flight pending retry, got %+v", result)
				default:
				}
			case AppendRetryPolicyNoSideEffects:
				if !session.closed {
					t.Fatal("legitimate timeout of a sent non-idempotent head under NoSideEffects should close the session")
				}
				for i, entry := range []*inflightEntry{h1, h2} {
					select {
					case result := <-entry.resultCh:
						if result == nil || result.err == nil {
							t.Fatalf("entry %d should have failed with REQUEST_TIMEOUT, got %+v", i, result)
						}
						var s2Err *S2Error
						if !errors.As(result.err, &s2Err) || s2Err.Code != "REQUEST_TIMEOUT" {
							t.Fatalf("entry %d: expected REQUEST_TIMEOUT S2Error, got %v", i, result.err)
						}
					default:
						t.Fatalf("entry %d was not failed by the legitimate timeout", i)
					}
				}
			}
		})
	}
}

// TestAppendSession_TimeoutNoOpOnClosedSession confirms the closed-session guard
// on the timeout path: once the session is already closed, a head timeout
// must not re-enter the teardown/retry machinery.
func TestAppendSession_TimeoutNoOpOnClosedSession(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	session, transport, h1, _ := newStaleHeadTimeoutSession(t, retryCfg, logger)

	session.closedMu.Lock()
	session.closed = true
	session.closedMu.Unlock()
	beforeAttempt := session.currentAttempt

	session.checkTimeouts()

	session.sessionMu.RLock()
	current := session.currentSession
	session.sessionMu.RUnlock()
	if current != transport {
		t.Fatalf("timeout on a closed session should not touch currentSession; got %p, want %p",
			current, transport)
	}
	if session.currentAttempt != beforeAttempt {
		t.Fatalf("timeout on a closed session should not consume a retry attempt; got %d, want %d",
			session.currentAttempt, beforeAttempt)
	}
	select {
	case result := <-h1.resultCh:
		t.Fatalf("timeout on a closed session should not fail inflight entries; got %+v", result)
	default:
	}
}

// TestAppendSession_TimeoutAfterHeadCompletedInPlace covers the third
// re-validation branch: the head entry is CAS-completed in place (e.g. by a
// concurrent failAllInflight/Close) without being popped, so the queue still
// holds it at index 0 but its completed flag is set. The stale timeout must
// bail rather than tear the session down a second time. (G10)
func TestAppendSession_TimeoutAfterHeadCompletedInPlace(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	triggered := make(chan struct{})
	release := make(chan struct{})
	logger := slog.New(&blockingLogHandler{triggered: triggered, release: release})

	session, transport, h1, _ := newStaleHeadTimeoutSession(t, retryCfg, logger)

	done := make(chan struct{})
	go func() {
		session.checkTimeouts()
		close(done)
	}()

	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not reach logError in time")
	}

	// Mark the head completed in place (as failAllInflight/Close would),
	// without popping it from the queue.
	atomic.StoreInt32(&h1.completed, 1)

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not complete")
	}

	session.sessionMu.RLock()
	current := session.currentSession
	session.sessionMu.RUnlock()
	if current != transport {
		t.Fatalf("BUG: session torn down for a head that was already completed in place; "+
			"currentSession = %p, want %p", current, transport)
	}
	if session.closed {
		t.Fatal("BUG: session closed for a head that was already completed in place")
	}
	session.stateMu.RLock()
	attempt := session.currentAttempt
	session.stateMu.RUnlock()
	if attempt != 0 {
		t.Fatalf("BUG: stale timeout for a completed-in-place head consumed a retry attempt; got %d", attempt)
	}
}

// slowWriter performs genuine formatting work on each Write so the real
// slog.TextHandler has a non-trivial TOCTOU window without any channel
// blocking crutch.
type slowWriter struct{ buf []byte }

func (w *slowWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for i := range 100 {
		w.buf = append(w.buf, byte(i%256))
		w.buf = w.buf[:len(w.buf)-1]
	}
	return len(p), nil
}

// TestAppendSession_StaleHeadTimeoutRace_StressTest exercises the actual
// checkTimeouts and handleAck entry points concurrently with a real
// slog.TextHandler (no blocking crutch). It counts the STALE signature:
// iterations where H1 was acknowledged (err==nil) AND H2 was failed (err!=nil).
// A stale timeout is the only way H1 can be acked while H2 is failed in this
// harness (a legitimate timeout CAS-fails H1, so H1.err!=nil there). The stale
// signature MUST be 0 after the fix. The total H2-failure count (which
// includes legitimate timeouts of a genuinely-timed-out H1) is reported
// informationally.
func TestAppendSession_StaleHeadTimeoutRace_StressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	const iterations = 2000
	const timeout = 50 * time.Millisecond

	retryCfg := &RetryConfig{
		MaxAttempts:       100,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}
	stream := newTestStreamClientForAppend(retryCfg)
	stream.basinClient.requestTimeout = timeout
	stream.logger = slog.New(slog.NewTextHandler(&slowWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))

	var staleSig atomic.Int64
	var totalH2Failed atomic.Int64
	var totalH1Acked atomic.Int64
	for range iterations {
		transport := &transportAppendSession{
			streamClient:  stream,
			acksCh:        make(chan *AppendAck, appendAckChannelBuffer),
			errorsCh:      make(chan error, 1),
			closed:        make(chan struct{}),
			requestWriter: &signalWriteCloser{},
		}
		ctx, cancel := context.WithCancel(context.Background())
		session := &AppendSession{
			streamClient:   stream,
			options:        &AppendSessionOptions{RetryConfig: retryCfg},
			capacity:       newCapacityTracker(1024, 2),
			sessionRefs:    make(map[*transportAppendSession]int),
			currentSession: transport,
			pumpCtx:        ctx, pumpCancel: cancel,
			closeDone: make(chan struct{}),
			wakeup:    make(chan struct{}, 1),
		}
		h1 := &inflightEntry{
			input:          &AppendInput{Records: []AppendRecord{{Body: []byte("h1")}}},
			expectedCount:  1,
			meteredBytes:   1,
			requestTimeout: timeout,
			attemptStart:   time.Now().Add(-100 * time.Millisecond),
			resultCh:       make(chan *inflightResult, 1),
		}
		h2 := &inflightEntry{
			input:          &AppendInput{Records: []AppendRecord{{Body: []byte("h2")}}},
			expectedCount:  1,
			meteredBytes:   1,
			requestTimeout: timeout,
			attemptStart:   time.Now(),
			resultCh:       make(chan *inflightResult, 1),
		}
		session.inflightQueue = []*inflightEntry{h1, h2}
		h1.sentOnSessions = []*transportAppendSession{transport}
		h2.sentOnSessions = []*transportAppendSession{transport}
		session.sessionRefs[transport] = 2
		transport.markWriteSignalled()
		transport.markWriteSignalled()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); session.checkTimeouts() }()
		go func() {
			defer wg.Done()
			session.handleAck(transport, &AppendAck{
				Start: StreamPosition{SeqNum: 0},
				End:   StreamPosition{SeqNum: 1},
				Tail:  StreamPosition{SeqNum: 2},
			})
		}()
		wg.Wait()

		h1Acked := false
		select {
		case result := <-h1.resultCh:
			if result != nil && result.err == nil {
				h1Acked = true
				totalH1Acked.Add(1)
			}
		default:
		}

		h2Failed := false
		select {
		case result := <-h2.resultCh:
			if result != nil && result.err != nil {
				h2Failed = true
				totalH2Failed.Add(1)
			}
		default:
		}

		if h1Acked && h2Failed {
			staleSig.Add(1)
		}
		cancel()
	}
	t.Logf("stress: stale signature (H1 acked + H2 failed) = %d / %d (%.2f%%); "+
		"total H2 failed = %d (%.2f%%); total H1 acked = %d (%.2f%%)",
		staleSig.Load(), iterations, float64(staleSig.Load())/float64(iterations)*100,
		totalH2Failed.Load(), float64(totalH2Failed.Load())/float64(iterations)*100,
		totalH1Acked.Load(), float64(totalH1Acked.Load())/float64(iterations)*100)
	if got := staleSig.Load(); got != 0 {
		t.Fatalf("BUG: stale timeout fired %d time(s) — H1 was acknowledged yet H2 was failed "+
			"by a stale head timeout", got)
	}
}

// TestAppendSession_TimeoutAfterHeadReplacedBySend covers the head-pointer
// re-validation axis directly: the timed-out head H1 is popped (replaced by
// H2 as the new head) while checkTimeouts is in the TOCTOU window, and H2 is
// NOT timed out. The stale timeout must bail without tearing the session
// down. (G1, head-replacement axis)
func TestAppendSession_TimeoutAfterHeadReplacedBySend(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	triggered := make(chan struct{})
	release := make(chan struct{})
	logger := slog.New(&blockingLogHandler{triggered: triggered, release: release})

	session, transport, h1, h2 := newStaleHeadTimeoutSession(t, retryCfg, logger)

	done := make(chan struct{})
	go func() {
		session.checkTimeouts()
		close(done)
	}()

	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not reach logError in time")
	}

	// H1 is acknowledged and popped; H2 (fresh, not timed out) becomes head.
	session.handleAck(transport, &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 2},
	})

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkTimeouts did not complete")
	}

	// H1 succeeded.
	select {
	case result := <-h1.resultCh:
		if result == nil || result.err != nil {
			t.Fatalf("expected H1 to succeed, got %+v", result)
		}
	default:
		t.Fatal("expected H1 to have an acknowledgement result")
	}

	// H2 must still be in flight (its own budget hasn't elapsed; the stale
	// H1 timeout must not have torn the session down).
	select {
	case result := <-h2.resultCh:
		t.Fatalf("BUG: H2 was failed by a stale H1 timeout after H1 was popped: %+v", result)
	default:
	}

	session.sessionMu.RLock()
	current := session.currentSession
	session.sessionMu.RUnlock()
	if current != transport {
		t.Fatalf("BUG: session torn down after head replaced; currentSession = %p, want %p",
			current, transport)
	}
}
