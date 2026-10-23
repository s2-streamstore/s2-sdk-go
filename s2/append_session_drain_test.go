package s2

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func drainS2Error() *S2Error {
	return &S2Error{Status: 503, Code: "server_draining", Origin: "server", Message: "server_draining"}
}

// TestAppendSession_PersistentServerDrainingIsBounded reproduces the hot-loop
// bug: a persistently draining server must not cause unbounded reconnects, and
// the inflight append must eventually fail so Submit resolves and Close returns.
func TestAppendSession_PersistentServerDrainingIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	var factoryCalls atomic.Int32
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls.Add(1)
		transport := newTransportSession(stream, &signalWriteCloser{})
		transport.reportError(drainS2Error())
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	ack, err := ticket.Ack(ctx)
	if err == nil {
		t.Fatalf("expected ack to fail during persistent draining, got ack=%+v", ack)
	}
	if !errors.Is(err, ErrMaxAttemptsExhausted) {
		t.Fatalf("expected ErrMaxAttemptsExhausted, got %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// The first drain is a free, immediate handover; subsequent drains consume
	// the retry budget. With MaxAttempts=3 that yields at most 1+3 = 4 transport
	// sessions before ErrMaxAttemptsExhausted fails the inflight batch. Before
	// the fix this hot-looped thousands of times per second.
	if got := factoryCalls.Load(); got > 4 {
		t.Fatalf("persistent draining hot-looped: created %d sessions, want <= 4", got)
	}
	if got := factoryCalls.Load(); got < 1 {
		t.Fatalf("expected at least one session attempt, got %d", got)
	}
}

// TestAppendSession_PersistentServerDrainingNoSideEffectsPolicy confirms the
// bound also holds under the noSideEffects retry policy with a single
// (non-pipelined) append.
func TestAppendSession_PersistentServerDrainingNoSideEffectsPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	var factoryCalls atomic.Int32
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls.Add(1)
		transport := newTransportSession(stream, &signalWriteCloser{})
		transport.reportError(drainS2Error())
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	if _, err := ticket.Ack(ctx); err == nil {
		t.Fatal("expected ack to fail during persistent draining")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if got := factoryCalls.Load(); got > 3 {
		t.Fatalf("persistent draining hot-looped under noSideEffects: %d sessions, want <= 3", got)
	}
}

// TestAppendSession_ServerDrainingReconnectsOnceThenSucceeds verifies a
// transient drain triggers the intended fast reconnect without consuming the
// retry budget, and the next session acknowledges the append.
func TestAppendSession_ServerDrainingReconnectsOnceThenSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	var factoryCalls atomic.Int32
	writeSignal := make(chan struct{}, 1)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		n := factoryCalls.Add(1)
		if n == 1 {
			transport := newTransportSession(stream, &signalWriteCloser{})
			transport.reportError(drainS2Error())
			return transport, nil
		}
		transport := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		go func() {
			<-writeSignal
			transport.acksCh <- &AppendAck{
				Start: StreamPosition{SeqNum: 0},
				End:   StreamPosition{SeqNum: 1},
				Tail:  StreamPosition{SeqNum: 1},
			}
		}()
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}
	defer session.Close()

	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if ack.End.SeqNum-ack.Start.SeqNum != 1 {
		t.Fatalf("expected 1 record acked, got %d", ack.End.SeqNum-ack.Start.SeqNum)
	}

	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("expected exactly 2 sessions (drain + reconnect), got %d", got)
	}

	// The drain handover must not consume the retry budget.
	session.stateMu.RLock()
	attempt := session.currentAttempt
	session.stateMu.RUnlock()
	if attempt != 0 {
		t.Fatalf("transient drain should not consume retry budget, got currentAttempt=%d", attempt)
	}
}
