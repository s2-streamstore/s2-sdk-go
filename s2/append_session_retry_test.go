package s2

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
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
	var transport *transportAppendSession

	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		transport = newTransportSession(stream, &signalWriteCloser{})
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

func TestAppendSession_NoSideEffectsNonIdempotentFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       3,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyNoSideEffects,
	}

	stream := newTestStreamClientForAppend(retryCfg)
	factoryCalls := 0
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		factoryCalls++
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
	if _, err := ticket.Ack(ctx); err == nil {
		t.Fatalf("expected ack error")
	}
	if factoryCalls != 1 {
		t.Fatalf("expected 1 session attempt, got %d", factoryCalls)
	}
}
