package s2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

type signalWriteCloser struct {
	err    error
	signal chan struct{}
}

func (w *signalWriteCloser) Write(p []byte) (int, error) {
	if w.signal != nil {
		select {
		case w.signal <- struct{}{}:
		default:
		}
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

func (w *signalWriteCloser) Close() error {
	return nil
}

type blockingClosedPipeWriter struct {
	started chan struct{}
	closed  chan struct{}
}

func (w *blockingClosedPipeWriter) Write(p []byte) (int, error) {
	if w.started != nil {
		select {
		case <-w.started:
		default:
			close(w.started)
		}
	}
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockingClosedPipeWriter) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

func newTestStreamClientForAppend(retryCfg *RetryConfig) *StreamClient {
	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: retryCfg,
	}
	return &StreamClient{
		name:        StreamName("test"),
		basinClient: basin,
	}
}

func newTransportSession(stream *StreamClient, writer io.WriteCloser) *transportAppendSession {
	return &transportAppendSession{
		streamClient:  stream,
		acksCh:        make(chan *AppendAck, 1),
		errorsCh:      make(chan error, 1),
		closed:        make(chan struct{}),
		requestWriter: writer,
	}
}

func TestTransportAppendSession_AppendInputReturnsErrSessionClosedOnConcurrentClose(t *testing.T) {
	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	writer := &blockingClosedPipeWriter{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	session := newTransportSession(stream, writer)

	done := make(chan error, 1)
	go func() {
		done <- session.appendInput(&AppendInput{
			Records: []AppendRecord{{Body: []byte("x")}},
		})
	}()

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for append write to start")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("expected ErrSessionClosed, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for append to exit after close")
	}
}

func TestTransportAppendSession_AppendInputPreservesWriteErrorsWhenOpen(t *testing.T) {
	stream := newTestStreamClientForAppend(DefaultRetryConfig)
	writeErr := errors.New("boom")
	session := newTransportSession(stream, &signalWriteCloser{err: writeErr})

	err := session.appendInput(&AppendInput{
		Records: []AppendRecord{{Body: []byte("x")}},
	})
	if err == nil {
		t.Fatal("expected append error")
	}
	if errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected non-close error, got %v", err)
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error %v, got %v", writeErr, err)
	}
}

func TestAppendSession_RetryAfterSendError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	writeSignal := make(chan struct{}, 1)
	var secondSession *transportAppendSession
	factoryCalls := 0

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
		var writer io.WriteCloser
		if factoryCalls == 1 {
			writer = &signalWriteCloser{err: errors.New("boom")}
		} else {
			writer = &signalWriteCloser{signal: writeSignal}
		}
		session := newTransportSession(stream, writer)
		if factoryCalls == 2 {
			secondSession = session
		}
		return session, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}
	defer session.Close()

	input := &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}}
	future, err := session.Submit(input)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	select {
	case <-writeSignal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for retry write")
	}
	if secondSession == nil {
		t.Fatalf("expected second session to be created")
	}

	secondSession.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if ack.End.SeqNum-ack.Start.SeqNum != 1 {
		t.Fatalf("expected 1 record acked, got %d", ack.End.SeqNum-ack.Start.SeqNum)
	}
	if factoryCalls != 2 {
		t.Fatalf("expected 2 sessions, got %d", factoryCalls)
	}
}

func TestAppendSession_MaxAttemptsExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       1,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{err: errors.New("boom")}), nil
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
	_, err = ticket.Ack(ctx)
	if err == nil {
		t.Fatalf("expected ack error")
	}
	if !errors.Is(err, ErrMaxAttemptsExhausted) {
		t.Fatalf("expected ErrMaxAttemptsExhausted, got %v", err)
	}
}

func TestAppendSession_CloseDrainsInflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	writes := make(chan struct{}, 3)
	transport := newTransportSession(stream, &signalWriteCloser{signal: writes})

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	// Submit 3 batches
	var tickets [3]*BatchSubmitTicket
	for i := range 3 {
		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
		if err != nil {
			t.Fatalf("submit %d failed: %v", i, err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatalf("wait %d failed: %v", i, err)
		}
		tickets[i] = ticket
	}
	for range tickets {
		select {
		case <-writes:
		case <-ctx.Done():
			t.Fatal("timed out waiting for all batches to be sent")
		}
	}

	// Start closing in background — should wait for drain
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- session.Close()
	}()

	// Close should NOT complete yet — inflight entries still pending
	select {
	case <-closeDone:
		t.Fatal("Close returned before inflight entries were drained")
	case <-time.After(50 * time.Millisecond):
	}

	// Send acks for all 3 batches
	for i := range 3 {
		transport.acksCh <- &AppendAck{
			Start: StreamPosition{SeqNum: uint64(i)},
			End:   StreamPosition{SeqNum: uint64(i + 1)},
			Tail:  StreamPosition{SeqNum: uint64(i + 1)},
		}
	}

	// All tickets should resolve
	for i := range 3 {
		ack, err := tickets[i].Ack(ctx)
		if err != nil {
			t.Fatalf("ack %d failed: %v", i, err)
		}
		if ack.Start.SeqNum != uint64(i) {
			t.Fatalf("ack %d: expected start seq %d, got %d", i, i, ack.Start.SeqNum)
		}
	}

	// Close should now complete
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after drain")
	}
}

func TestAppendSession_CloseRejectsNewSubmits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{}), nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	// Close with nothing inflight — should return immediately
	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// Submit after close should fail
	_, err = session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed after close, got %v", err)
	}
}

func TestAppendSession_CloseRejectsLateReservedEnqueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{}), nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	prepared, size, err := prepareAppendInput(&AppendInput{
		Records: []AppendRecord{{Body: []byte("x")}},
	})
	if err != nil {
		t.Fatalf("prepare append input failed: %v", err)
	}

	if err := session.capacity.reserve(ctx, size); err != nil {
		t.Fatalf("reserve failed: %v", err)
	}
	defer session.capacity.release(size)

	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	entry, err := session.enqueueReservedEntry(prepared, size)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
	if entry != nil {
		t.Fatal("expected no entry to be returned after close")
	}

	session.inflightMu.RLock()
	queueLen := len(session.inflightQueue)
	session.inflightMu.RUnlock()
	if queueLen != 0 {
		t.Fatalf("expected empty inflight queue after rejected enqueue, got %d entries", queueLen)
	}
}

func TestAppendSession_CreateSubmitFutureReleasesCapacityWhenEnqueueRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	prepared, size, err := prepareAppendInput(&AppendInput{
		Records: []AppendRecord{{Body: []byte("x")}},
	})
	if err != nil {
		t.Fatalf("prepare append input failed: %v", err)
	}

	session := &AppendSession{
		streamClient: newTestStreamClientForAppend(DefaultRetryConfig),
		capacity:     newCapacityTracker(1024, 1),
		wakeup:       make(chan struct{}, 1),
	}
	session.closing.Store(true)

	future := session.createSubmitFuture(prepared, size)

	_, err = future.Wait(ctx)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}

	session.capacity.mu.Lock()
	curBytes := session.capacity.curBytes
	curItems := session.capacity.curItems
	session.capacity.mu.Unlock()
	if curBytes != 0 || curItems != 0 {
		t.Fatalf("expected reserved capacity to be released, got bytes=%d items=%d", curBytes, curItems)
	}
}

func TestAppendSession_CloseWithEmptyQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{}), nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}

	// Close immediately with nothing submitted
	done := make(chan error, 1)
	go func() { done <- session.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close hung on empty queue")
	}
}

func TestAppendSession_CloseDuringRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      10 * time.Millisecond,
		MaxBaseDelay:      10 * time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	factoryCalls := 0
	writeSignal := make(chan struct{}, 1)

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
		if factoryCalls == 1 {
			// First session: write fails, triggers retry
			return newTransportSession(stream, &signalWriteCloser{err: errors.New("boom")}), nil
		}
		// Second session: write succeeds
		transport := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		// Send ack asynchronously after write
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

	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	// Close while retry is in progress — should wait for drain through retry
	done := make(chan error, 1)
	go func() { done <- session.Close() }()

	// Ack should resolve (retry succeeded, ack delivered)
	ack, err := ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if ack.End.SeqNum != 1 {
		t.Fatalf("expected end seq 1, got %d", ack.End.SeqNum)
	}

	// Close should complete
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after retry + drain")
	}
}

func TestAppendSession_NoSideEffectsWithMatchSeqNumDoesNotRetryWhenEffectSignalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	factoryCalls := 0
	writeSignal := make(chan struct{}, 1)
	var firstSession *transportAppendSession
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
		transport := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		if factoryCalls == 1 {
			firstSession = transport
		}
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}
	defer session.Close()

	future, err := session.Submit(&AppendInput{
		Records:     []AppendRecord{{Body: []byte("x")}},
		MatchSeqNum: Uint64(0),
	})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	select {
	case <-writeSignal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first write")
	}

	firstSession.errorsCh <- &S2Error{
		Message: "unavailable",
		Status:  503,
		Origin:  "server",
	}

	if _, err := ticket.Ack(ctx); err == nil {
		t.Fatalf("expected ack error")
	}
	if factoryCalls != 1 {
		t.Fatalf("expected 1 session attempt, got %d", factoryCalls)
	}
}

func TestAppendSession_NoSideEffectsRetriesWhenEffectNotSignalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	factoryCalls := 0
	writeSignal := make(chan struct{}, 1)
	var secondSession *transportAppendSession

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
		if factoryCalls == 1 {
			// Fail before any session/write exists: no effect signalled, retry is safe.
			return nil, &S2Error{Message: "connect failed", Status: 503, Origin: "server"}
		}
		transport := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		secondSession = transport
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}
	defer session.Close()

	future, err := session.Submit(&AppendInput{
		Records:     []AppendRecord{{Body: []byte("x")}},
		MatchSeqNum: Uint64(0),
	})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	select {
	case <-writeSignal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry write")
	}
	if secondSession == nil {
		t.Fatalf("expected second session to be created")
	}

	secondSession.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}

	ack, err := ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("ack failed: %v", err)
	}
	if ack.End.SeqNum-ack.Start.SeqNum != 1 {
		t.Fatalf("expected 1 record acked, got %d", ack.End.SeqNum-ack.Start.SeqNum)
	}
	if factoryCalls != 2 {
		t.Fatalf("expected 2 session attempts, got %d", factoryCalls)
	}
}

// Canceling the session context aborts it. A batch already written to the
// server may have been applied, so its error must not claim no side effects.
func TestAppendSession_AbortReportsSentBatchAsIndefinite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}
	stream := newTestStreamClientForAppend(retryCfg)
	writeSignal := make(chan struct{}, 1)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{signal: writeSignal}), nil
	}

	sessionCtx, abort := context.WithCancel(ctx)
	session, err := stream.AppendSession(sessionCtx, &AppendSessionOptions{RetryConfig: retryCfg})
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
	select {
	case <-writeSignal:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the batch to be written")
	}

	abort()

	_, err = ticket.Ack(ctx)
	if err == nil {
		t.Fatal("expected ack error after abort")
	}
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected error to match ErrSessionClosed, got %v", err)
	}
	if HasNoSideEffects(err) {
		t.Fatalf("sent batch reported as side-effect free after abort: %v", err)
	}
}

func TestAppendSession_NoSideEffectsNoRetryWithNonIdempotentSentPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	factoryCalls := 0
	writeSignal := make(chan struct{}, 4)
	var firstSession *transportAppendSession

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
		transport := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		if factoryCalls == 1 {
			firstSession = transport
		}
		return transport, nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("append session failed: %v", err)
	}
	defer session.Close()

	future1, err := session.Submit(&AppendInput{
		Records:     []AppendRecord{{Body: []byte("head")}},
		MatchSeqNum: Uint64(0),
	})
	if err != nil {
		t.Fatalf("submit head failed: %v", err)
	}
	ticket1, err := future1.Wait(ctx)
	if err != nil {
		t.Fatalf("wait head failed: %v", err)
	}

	future2, err := session.Submit(&AppendInput{
		Records: []AppendRecord{{Body: []byte("tail")}},
	})
	if err != nil {
		t.Fatalf("submit tail failed: %v", err)
	}
	ticket2, err := future2.Wait(ctx)
	if err != nil {
		t.Fatalf("wait tail failed: %v", err)
	}

	for i := range 2 {
		select {
		case <-writeSignal:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for write %d", i+1)
		}
	}

	firstSession.errorsCh <- &S2Error{
		Message: "rate limited",
		Status:  429,
		Code:    "rate_limited",
		Origin:  "server",
	}

	if _, err := ticket1.Ack(ctx); err == nil {
		t.Fatalf("expected head ack error")
	}
	if _, err := ticket2.Ack(ctx); err == nil {
		t.Fatalf("expected tail ack error")
	}
	if factoryCalls != 1 {
		t.Fatalf("expected 1 session attempt, got %d", factoryCalls)
	}
}

func TestAppendSession_HandleSessionErrorIsIdempotentAcrossConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       5,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		return newTransportSession(stream, &signalWriteCloser{}), nil
	}

	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatalf("AppendSession: %v", err)
	}
	defer session.Close()
	defer session.pumpCancel()

	failed := newTransportSession(stream, &signalWriteCloser{})
	session.sessionMu.Lock()
	session.currentSession = failed
	session.sessionMu.Unlock()
	// An idle session's failure uses no budget, so give it a batch.
	session.inflightMu.Lock()
	session.inflightQueue = append(session.inflightQueue, &inflightEntry{
		input:         &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}},
		expectedCount: 1,
		resultCh:      make(chan *inflightResult, 1),
	})
	session.inflightMu.Unlock()

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	transportErr := errors.New("transport boom")

	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session.handleSessionError(failed, transportErr)
		}()
	}
	close(start)
	wg.Wait()

	session.stateMu.RLock()
	got := session.currentAttempt
	session.stateMu.RUnlock()
	if got != 1 {
		t.Fatalf("one logical failure should bump currentAttempt by 1, got %d after %d concurrent reports", got, callers)
	}

	session.sessionMu.RLock()
	current := session.currentSession
	session.sessionMu.RUnlock()
	if current != nil {
		t.Fatalf("expected currentSession cleared after claim, still %p", current)
	}
}

func TestAppendSession_IgnoresTimeoutAfterTransportFailure(t *testing.T) {
	for _, policy := range []AppendRetryPolicy{AppendRetryPolicyAll, AppendRetryPolicyNoSideEffects} {
		t.Run(string(policy), func(t *testing.T) {
			session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
				MaxAttempts:       3,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: policy,
			}, 1)

			session.handleSessionError(transport, &S2Error{
				Status: 429, Code: "rate_limited", Origin: "server",
			})
			session.handleSessionError(nil, &S2Error{
				Status: 408, Code: "REQUEST_TIMEOUT", Origin: "sdk",
			})

			if session.currentAttempt != 1 {
				t.Errorf("expected one retry attempt, got %d", session.currentAttempt)
			}
			if session.closed || len(session.inflightQueue) != 1 {
				t.Errorf("expected open session with pending append, got closed=%t pending=%d",
					session.closed, len(session.inflightQueue))
			}
			select {
			case result := <-entries[0].resultCh:
				t.Errorf("append completed before retry: %v", result.err)
			default:
			}
		})
	}
}

func TestAppendSession_WriteUnblockedByTerminalErrorUsesServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       5,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	writer := &blockingClosedPipeWriter{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	failed := newTransportSession(stream, writer)
	session := &AppendSession{
		streamClient:   stream,
		options:        &AppendSessionOptions{RetryConfig: retryCfg},
		capacity:       newCapacityTracker(1024, 1),
		sessionRefs:    make(map[*transportAppendSession]int),
		currentSession: failed,
		pumpCtx:        ctx,
		pumpCancel:     cancel,
		closeDone:      make(chan struct{}),
		wakeup:         make(chan struct{}, 1),
	}
	entry := &inflightEntry{
		input:         &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}},
		expectedCount: 1,
		meteredBytes:  1,
		resultCh:      make(chan *inflightResult, 1),
	}
	session.inflightQueue = []*inflightEntry{entry}

	done := make(chan struct{})
	go func() {
		session.submitInflightBatches()
		close(done)
	}()

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for append write to start")
	}

	terminalErr := &S2Error{
		Message: "bad request",
		Status:  400,
		Origin:  "server",
	}
	failed.reportError(terminalErr)
	if err := failed.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submitInflightBatches to return")
	}

	select {
	case result := <-entry.resultCh:
		if result == nil || result.err == nil {
			t.Fatalf("expected terminal error result, got %#v", result)
		}
		var s2Err *S2Error
		if !errors.As(result.err, &s2Err) || s2Err.Status != 400 {
			t.Fatalf("expected terminal S2Error status 400, got %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inflight entry to fail")
	}

	session.stateMu.RLock()
	attempt := session.currentAttempt
	session.stateMu.RUnlock()
	if attempt != 0 {
		t.Fatalf("non-retryable terminal error should not consume a retry attempt, got %d", attempt)
	}
}

func TestAppendSession_PersistentDrainExhaustsRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	stream := newTestStreamClientForAppend(retryCfg)
	var requests atomic.Int32
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		requests.Add(1)
		transport := newTransportSession(stream, &signalWriteCloser{})
		transport.reportError(&S2Error{Status: 503, Code: "server_draining", Origin: "server"})
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatal(err)
	}
	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Drains that make no progress count as retries.
	if _, err := ticket.Ack(ctx); !isServerDraining(err) {
		t.Fatalf("expected the drain error once retries ran out, got %v", err)
	}
	if got := requests.Load(); got != int32(retryCfg.MaxAttempts) {
		t.Fatalf("expected %d requests, got %d", retryCfg.MaxAttempts, got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendSession_NoSideEffectsRetriesRefusedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}
	stream := newTestStreamClientForAppend(retryCfg)
	writes := make(chan struct{}, 2)
	transports := make(chan *transportAppendSession, 2)
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		transport := newTransportSession(stream, &signalWriteCloser{signal: writes})
		transports <- transport
		return transport, nil
	}
	session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	next := func() *transportAppendSession {
		select {
		case transport := <-transports:
			<-writes
			return transport
		case <-ctx.Done():
			t.Fatal("batch was not retried on a new request")
			return nil
		}
	}
	// The batch was handed to the request body, but the connection was
	// refused, so none of it left the client.
	next().reportError(fmt.Errorf("failed to connect: %w", dialRefusedError()))
	next().acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}
	if _, err := ticket.Ack(ctx); err != nil {
		t.Fatalf("expected the batch to be retried after a refused connection, got %v", err)
	}
}

func TestAppendSession_ProtocolErrorsDoNotRetry(t *testing.T) {
	oversizedPayload := make([]byte, internalframing.MaxDecompressedSize+1)
	oversizedGzip, err := internalframing.Compress(oversizedPayload, internalframing.CompressionGzip)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		status int
		header http.Header
		body   []byte
	}{
		{
			name:   "oversized s2s frame header",
			status: http.StatusOK,
			body:   []byte{0xFF, 0xFF, 0xFF, 0x00},
		},
		{
			name:   "zero-length s2s frame",
			status: http.StatusOK,
			body:   []byte{0x00, 0x00, 0x00, 0x00},
		},
		{
			name:   "invalid protobuf AppendAck",
			status: http.StatusOK,
			body:   internalframing.CreateFrame([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, false, internalframing.CompressionNone),
		},
		{
			name:   "oversized compressed ack frame",
			status: http.StatusOK,
			body:   internalframing.CreateFrame(oversizedPayload, false, internalframing.CompressionGzip),
		},
		{
			name:   "oversized compressed 500 error response",
			status: http.StatusInternalServerError,
			header: http.Header{"Content-Encoding": {"gzip"}},
			body:   oversizedGzip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			var requests atomic.Int32
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				if req.Body != nil {
					go func() {
						_, _ = io.Copy(io.Discard, req.Body)
						_ = req.Body.Close()
					}()
				}
				hdr := make(http.Header)
				if tt.header != nil {
					hdr = tt.header.Clone()
				}
				return &http.Response{
					StatusCode: tt.status,
					Header:     hdr,
					Body:       io.NopCloser(bytes.NewReader(tt.body)),
					Request:    req,
				}, nil
			})

			retryCfg := &RetryConfig{
				MaxAttempts:       3,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: AppendRetryPolicyAll,
			}
			basin := &BasinClient{
				baseURL:     "http://example.com/v1",
				accessToken: "token",
				retryConfig: retryCfg,
			}
			basin.client = &Client{streamingClient: &http.Client{Transport: rt}}
			stream := &StreamClient{name: StreamName("test"), basinClient: basin}

			session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := future.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ticket.Ack(ctx)
			var s2Err *S2Error
			if !errors.As(err, &s2Err) || s2Err.Code != protocolErrorCode || s2Err.IsRetryable() {
				t.Fatalf("expected non-retryable PROTOCOL S2Error, got %v", err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("expected protocol error not to retry, got %d requests", got)
			}
		})
	}
}

func TestAppendSession_SpecConformance(t *testing.T) {
	t.Run("terminal status outside 300-599 fails with PROTOCOL error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var requests atomic.Int32
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			if req.Body != nil {
				go func() {
					_, _ = io.Copy(io.Discard, req.Body)
					_ = req.Body.Close()
				}()
			}
			body := internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    req,
			}, nil
		})
		retryCfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}
		basin := &BasinClient{baseURL: "http://example.com/v1", accessToken: "token", retryConfig: retryCfg}
		basin.client = &Client{streamingClient: &http.Client{Transport: rt}}
		stream := &StreamClient{name: StreamName("test"), basinClient: basin}

		session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ticket.Ack(ctx)
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Code != protocolErrorCode || s2Err.IsRetryable() {
			t.Fatalf("expected non-retryable PROTOCOL error, got %v", err)
		}
		if got := requests.Load(); got != 1 {
			t.Fatalf("expected no retry on invalid terminal status, got %d requests", got)
		}
	})

	t.Run("overlapping ack fails with PROTOCOL error and LastAckedPosition returns copy", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		retryCfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}
		stream := newTestStreamClientForAppend(retryCfg)
		writes := make(chan struct{}, 4)
		transports := make(chan *transportAppendSession, 1)
		stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
			tr := newTransportSession(stream, &signalWriteCloser{signal: writes})
			transports <- tr
			return tr, nil
		}
		session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		f1, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("a")}, {Body: []byte("b")}, {Body: []byte("c")}}})
		if err != nil {
			t.Fatal(err)
		}
		t1, err := f1.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tr := <-transports
		<-writes
		tr.acksCh <- &AppendAck{Start: StreamPosition{SeqNum: 0}, End: StreamPosition{SeqNum: 3}}
		if _, err := t1.Ack(ctx); err != nil {
			t.Fatalf("first ack failed: %v", err)
		}

		// Mutating LastAckedPosition() must not affect internal session state (spec C10).
		pos := session.LastAckedPosition()
		pos.End.SeqNum = 999
		if got := session.LastAckedPosition(); got.End.SeqNum != 3 {
			t.Fatalf("LastAckedPosition aliased internal state: got %d, want 3", got.End.SeqNum)
		}

		// Submit 3 more records; server responds with overlapping [2, 5) instead of [3, 6).
		f2, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("d")}, {Body: []byte("e")}, {Body: []byte("f")}}})
		if err != nil {
			t.Fatal(err)
		}
		t2, err := f2.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		<-writes
		tr.acksCh <- &AppendAck{Start: StreamPosition{SeqNum: 2}, End: StreamPosition{SeqNum: 5}}
		_, err = t2.Ack(ctx)
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Code != protocolErrorCode {
			t.Fatalf("expected PROTOCOL error on overlapping ack [2,5) after [0,3), got %v", err)
		}
	})

	t.Run("connectError retries under noSideEffects policy", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		retryCfg := &RetryConfig{
			MaxAttempts:       2,
			MinBaseDelay:      time.Millisecond,
			MaxBaseDelay:      time.Millisecond,
			AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
		}
		stream := newTestStreamClientForAppend(retryCfg)
		writes := make(chan struct{}, 2)
		transports := make(chan *transportAppendSession, 2)
		stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
			tr := newTransportSession(stream, &signalWriteCloser{signal: writes})
			transports <- tr
			return tr, nil
		}
		session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		f, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := f.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tr1 := <-transports
		<-writes
		tr1.reportError(&connectError{err: errors.New("tls handshake failed")})

		tr2 := <-transports
		<-writes
		tr2.acksCh <- &AppendAck{Start: StreamPosition{SeqNum: 0}, End: StreamPosition{SeqNum: 1}}
		if _, err := ticket.Ack(ctx); err != nil {
			t.Fatalf("expected connectError to retry under noSideEffects policy, got %v", err)
		}
	})

	t.Run("batcher match_seq_num uint64 overflow returns ValidationError", func(t *testing.T) {
		start := uint64(math.MaxUint64 - 1)
		b := NewBatcher(context.Background(), &BatchingOptions{
			Linger:      time.Hour,
			MaxRecords:  10,
			MatchSeqNum: &start,
		})
		defer b.Close()

		if err := b.Add(AppendRecord{Body: []byte("ok")}, nil); err != nil {
			t.Fatalf("first record at MaxUint64-1 should succeed, got %v", err)
		}
		err := b.Add(AppendRecord{Body: []byte("overflow")}, nil)
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Code != validationErrorCode {
			t.Fatalf("expected ValidationError on uint64 overflow, got %v", err)
		}
	})
}
