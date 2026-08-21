package s2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

// Serves a different response body per connection attempt.
type scriptedRoundTripper struct {
	mu       sync.Mutex
	bodies   [][]byte
	attempts int
}

func (r *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	i := r.attempts
	r.attempts++
	r.mu.Unlock()
	if i >= len(r.bodies) {
		i = len(r.bodies) - 1
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(r.bodies[i])),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func buildAdvisedReadBatchFrame(t *testing.T, records []*pb.SequencedRecord) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{Records: records})
	if err != nil {
		t.Fatalf("marshal read batch: %v", err)
	}
	return internalframing.CreateRegularFrameReconnectAdvised(data, internalframing.CompressionNone)
}

func TestReadSessionResumesOnReconnectAdvice(t *testing.T) {
	var advised bytes.Buffer
	advised.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("a")}}))
	advised.Write(buildAdvisedReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 1, Body: []byte("b")}}))

	var replacement bytes.Buffer
	replacement.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 2, Body: []byte("c")}}))
	replacement.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &scriptedRoundTripper{bodies: [][]byte{advised.Bytes(), replacement.Bytes()}}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	if rt.attempts != 2 {
		t.Fatalf("expected a second connection, got %d attempts", rt.attempts)
	}
	// The advised batch is delivered before the handover, so the replacement
	// resumes after it with no gap and no repeat.
	want := []uint64{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("expected seq_nums %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected seq_nums %v, got %v", want, got)
		}
	}
}

func TestAppendSessionReconnectsOnAdvice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	retryCfg := &RetryConfig{
		MaxAttempts:       2,
		MinBaseDelay:      time.Millisecond,
		MaxBaseDelay:      time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}
	stream := newTestStreamClientForAppend(retryCfg)

	writeSignal := make(chan struct{}, 4)
	var sessions []*transportAppendSession
	stream.appendSessionFactory = func(context.Context) (*transportAppendSession, error) {
		session := newTransportSession(stream, &signalWriteCloser{signal: writeSignal})
		sessions = append(sessions, session)
		return session, nil
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
	select {
	case <-writeSignal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for the first write")
	}

	// The server flags the ack frame, then ends the response cleanly.
	first := sessions[0]
	first.reconnectAdvised.Store(true)
	first.acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 0},
		End:   StreamPosition{SeqNum: 1},
		Tail:  StreamPosition{SeqNum: 1},
	}
	if _, err := ticket.Ack(ctx); err != nil {
		t.Fatalf("first append was not acked: %v", err)
	}
	close(first.errorsCh)
	close(first.acksCh)

	future, err = session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("y")}}})
	if err != nil {
		t.Fatalf("submit after advice failed: %v", err)
	}
	if _, err := future.Wait(ctx); err != nil {
		t.Fatalf("wait after advice failed: %v", err)
	}
	select {
	case <-writeSignal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for the replacement session")
	}

	if len(sessions) != 2 {
		t.Fatalf("expected a replacement session, got %d", len(sessions))
	}
	// Advice is a planned handover, so it must not spend the retry budget.
	session.stateMu.RLock()
	attempts := session.currentAttempt
	session.stateMu.RUnlock()
	if attempts != 0 {
		t.Errorf("advice consumed %d retry attempts", attempts)
	}

	sessions[1].acksCh <- &AppendAck{
		Start: StreamPosition{SeqNum: 1},
		End:   StreamPosition{SeqNum: 2},
		Tail:  StreamPosition{SeqNum: 2},
	}
}

func TestAdviceStreakPacesOnlyRapidRepeats(t *testing.T) {
	var streak adviceStreak
	now := time.Now()

	for i := range maxImmediateAdvisedReconnects {
		if delay := streak.record(now); delay != 0 {
			t.Fatalf("reconnect %d should not be delayed, got %s", i+1, delay)
		}
	}
	if delay := streak.record(now); delay != advisedReconnectDelay {
		t.Fatalf("expected pacing past %d reconnects, got %s", maxImmediateAdvisedReconnects, delay)
	}

	// Advice that arrives well after the previous one starts over.
	if delay := streak.record(now.Add(adviceStreakWindow * 2)); delay != 0 {
		t.Fatalf("expected a fresh streak after the window, got %s", delay)
	}
}
