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
