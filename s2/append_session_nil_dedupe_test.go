package s2

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// newAppendSessionForNilDedupeTest constructs an AppendSession in the state
// checkTimeouts observes in the race window of a concurrent non-nil
// handleSessionError: a single inflight entry was sent on `failed` and has a
// stale, already-timed-out attemptStart; currentSession still points at
// `failed`; currentAttempt is 0. Each test then drives the two racing callers
// sequentially:
//   - the transport-error handler (readAcks/submitInflightBatches) reports
//     `failed` (non-nil), which claim-nils currentSession and bumps
//     currentAttempt;
//   - the request-timeout handler (checkTimeouts) observed currentSession
//     already nil and reports handleSessionError(nil, &S2Error{REQUEST_TIMEOUT}).
func newAppendSessionForNilDedupeTest(t *testing.T, retryCfg *RetryConfig) (*AppendSession, *transportAppendSession, *inflightEntry) {
	t.Helper()
	stream := newTestStreamClientForAppend(retryCfg)
	failed := newTransportSession(stream, &signalWriteCloser{})

	ctx, cancel := context.WithCancel(context.Background())
	session := &AppendSession{
		streamClient:   stream,
		options:        &AppendSessionOptions{RetryConfig: retryCfg},
		capacity:       newCapacityTracker(1024, 1),
		sessionRefs:    map[*transportAppendSession]int{failed: 1},
		currentSession: failed,
		pumpCtx:        ctx,
		pumpCancel:     cancel,
		closeDone:      make(chan struct{}),
		wakeup:         make(chan struct{}, 1),
	}

	// The head inflight entry models what checkTimeouts snapshotted: sent on
	// `failed` and already past its requestTimeout deadline.
	entry := &inflightEntry{
		input:          &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}},
		expectedCount:  1,
		meteredBytes:   1,
		requestTimeout: 10 * time.Millisecond,
		attemptStart:   time.Now().Add(-100 * time.Millisecond),
		resultCh:       make(chan *inflightResult, 1),
		sentOnSessions: []*transportAppendSession{failed},
	}
	session.inflightQueue = []*inflightEntry{entry}
	return session, failed, entry
}

// requestTimeoutError mirrors the &S2Error checkTimeouts constructs when it
// fires the request-timeout path (s2/append_session.go:905).
func requestTimeoutError(attempt int) *S2Error {
	return &S2Error{
		Message: "append request timed out after 10ms (attempt " + strconv.Itoa(attempt) + ")",
		Code:    "REQUEST_TIMEOUT",
		Status:  408,
		Origin:  "sdk",
	}
}

// TestAppendSession_NilSessionTimeoutDoubleIncrementsAttempt asserts that a
// request timeout racing with a single transport failure consumes exactly one
// retry attempt under the default policy. Without the nil-arm bail, the
// timeout report (handleSessionError(nil, ...)) runs concurrently with the
// non-nil transport-error handler and bumps currentAttempt a second time for
// the same logical failure.
func TestAppendSession_NilSessionTimeoutDoubleIncrementsAttempt(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       5,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	session, failed, _ := newAppendSessionForNilDedupeTest(t, retryCfg)
	defer session.pumpCancel()

	// Call 1: the transport-error handler claims the failure, nilifies
	// currentSession, and bumps currentAttempt 0 -> 1.
	session.handleSessionError(failed, errors.New("transport boom"))

	// Call 2: the request-timeout handler observed currentSession already nil
	// and fires handleSessionError(nil, ...). Without the nil-arm bail this
	// bumps currentAttempt again (1 -> 2); with the bail it consumes no
	// attempt.
	session.handleSessionError(nil, requestTimeoutError(0))

	session.stateMu.RLock()
	got := session.currentAttempt
	session.stateMu.RUnlock()
	if got != 1 {
		t.Fatalf("a request timeout racing a single transport failure should consume one retry attempt, got currentAttempt=%d (expected 1)", got)
	}
}

// TestAppendSession_NilPathTimeoutTearsDownUnderNoSideEffects asserts that
// under AppendRetryPolicyNoSideEffects a request timeout racing a retryable
// transport error does NOT tear down the session on the first raced failure.
// Without the nil-arm bail, the nil-path call hits
// canRetryUnderNoSideEffects(nil, 408) -> false (failedSession == nil is an
// automatic false) and failAllInflight's the session even though the transport
// failure path would have retried.
func TestAppendSession_NilPathTimeoutTearsDownUnderNoSideEffects(t *testing.T) {
	retryCfg := &RetryConfig{
		MaxAttempts:       5,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}
	session, failed, entry := newAppendSessionForNilDedupeTest(t, retryCfg)
	defer session.pumpCancel()

	// Call 1: a retryable transport error under NoSideEffects. `failed` has
	// not effect-signalled, so canRetryUnderNoSideEffects returns true and the
	// session is preserved for the retry (currentAttempt 0 -> 1).
	session.handleSessionError(failed, errors.New("transport boom"))

	// Call 2: the racing request timeout is reported with failedSession ==
	// nil (checkTimeouts observed currentSession already niled). Without the
	// nil-arm bail this tears the session down; with the bail the inflight
	// entry is preserved for the retry.
	session.handleSessionError(nil, requestTimeoutError(0))

	select {
	case result := <-entry.resultCh:
		t.Fatalf("nil-path timeout tore down the inflight entry under NoSideEffects on the first raced failure, got result=%#v", result)
	default:
	}

	session.closedMu.RLock()
	closed := session.closed
	session.closedMu.RUnlock()
	if closed {
		t.Fatalf("nil-path timeout closed the session under NoSideEffects on the first raced failure")
	}
}
