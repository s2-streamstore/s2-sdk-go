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

// reconnectAdvisedFrame builds a regular (non-terminal) s2s/proto frame carrying
// an empty ReadBatch with the ReconnectAdvised flag set. The flag is applied by
// OR-ing flagReconnectAdvised (0x10, per internal/framing/framing.go:52) into
// the frame's flag byte, because internalframing.CreateFrame does not expose
// the advice flag.
func reconnectAdvisedFrame(t *testing.T) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{})
	if err != nil {
		t.Fatalf("marshal empty read batch: %v", err)
	}
	frame := internalframing.CreateFrame(data, false, internalframing.CompressionNone)
	frame[3] |= 0x10 // flagReconnectAdvised
	return frame
}

func terminalOKFrame() []byte {
	return internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK)
}

// blockingFrameBody delivers a fixed sequence of frame bytes, then blocks until
// Close is called — modeling a streaming response body that stays open while the
// server is still attached (the poison-before-body-close scenario).
type blockingFrameBody struct {
	data      []byte
	delivered int
	closeOnce sync.Once
	closed    chan struct{}
}

func newBlockingFrameBody(data []byte) *blockingFrameBody {
	return &blockingFrameBody{data: data, closed: make(chan struct{})}
}

func (b *blockingFrameBody) Read(p []byte) (int, error) {
	if b.delivered < len(b.data) {
		n := copy(p, b.data[b.delivered:])
		b.delivered += n
		return n, nil
	}
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingFrameBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

// reconnectCountingRT serves a ReconnectAdvised streaming body on the first
// RoundTrip and a terminal OK frame on subsequent calls. It counts
// CloseIdleConnections invocations so the in-package reclaim path through the
// real read/append session run loops can be observed.
type reconnectCountingRT struct {
	adviceFrame   []byte
	terminalFrame []byte

	mu             sync.Mutex
	calls          int
	closeIdleCalls int
}

func (r *reconnectCountingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()

	if call == 1 {
		body := newBlockingFrameBody(r.adviceFrame)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(r.terminalFrame)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *reconnectCountingRT) CloseIdleConnections() {
	r.mu.Lock()
	r.closeIdleCalls++
	r.mu.Unlock()
}

func (r *reconnectCountingRT) CloseIdleCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeIdleCalls
}

func (r *reconnectCountingRT) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newReconnectCountingRT(t *testing.T) *reconnectCountingRT {
	t.Helper()
	return &reconnectCountingRT{
		adviceFrame:   reconnectAdvisedFrame(t),
		terminalFrame: terminalOKFrame(),
	}
}

// newSpreadStreamClient wires a real spreadTransport (the production pool
// type) as the streaming transport, so the session exercises the actual
// capturePoisonHandle -> poison -> releaseOnClose reclaim path instead of
// bypassing the pool the way newFrameServedStreamClient does.
func newSpreadStreamClient(t *testing.T, rt http.RoundTripper) *StreamClient {
	t.Helper()
	spread := newSpreadTransport(func() http.RoundTripper { return rt })
	basinClient := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: &RetryConfig{MaxAttempts: 1, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	}
	basinClient.client = &Client{
		streamingClient: &http.Client{Transport: spread, Timeout: 0},
	}
	return &StreamClient{
		name:        StreamName("test"),
		basinClient: basinClient,
	}
}

func spreadTransportOf(t *testing.T, sc *StreamClient) *spreadTransport {
	t.Helper()
	spread, ok := sc.basinClient.client.streamingClient.Transport.(*spreadTransport)
	if !ok {
		t.Fatalf("expected *spreadTransport, got %T", sc.basinClient.client.streamingClient.Transport)
	}
	return spread
}

// TestReadSession_PoisonBeforeBodyCloseReclaimsOrphan drives the real
// streamReader.runOnce path (s2/read.go: capturePoisonHandle at :456,
// capture.poison() at :631, defer closeRespBody at :496) against a streaming
// body that delivers a ReconnectAdvised frame. It asserts that:
//   - the serving entry is poisoned and removed from the pool (G-A1),
//   - the orphan is reclaimed when the body closes (G-A3): the counting
//     transport's CloseIdleConnections is called by releaseOnClose.Close
//     (poison's no-op sweep + releaseOnClose's reclaim = 2 calls with the fix;
//     only 1 without it).
func TestReadSession_PoisonBeforeBodyCloseReclaimsOrphan(t *testing.T) {
	rt := newReconnectCountingRT(t)
	streamClient := newSpreadStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := streamClient.ReadSession(ctx, nil)
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer session.Close()

	for session.Next() {
		// no records expected on the advice frame
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	if got := rt.Calls(); got != 2 {
		t.Fatalf("expected 2 round-trips (advice + terminal), got %d", got)
	}

	spread := spreadTransportOf(t, streamClient)
	spread.mu.Lock()
	hostEntries := spread.hosts["example.com"]
	spread.mu.Unlock()
	if len(hostEntries) != 1 {
		t.Fatalf("expected 1 in-pool entry remaining from the terminal call, got %d", len(hostEntries))
	}
	if hostEntries[0].poisoned.Load() {
		t.Fatal("the second (terminal) entry must not be poisoned")
	}

	// poison's no-op sweep (1) + releaseOnClose's reclaim on body close (1) = 2.
	// Pre-fix: only poison's sweep (1); the orphan leaked until server TCP close.
	if got := rt.CloseIdleCalls(); got != 2 {
		t.Fatalf("expected 2 CloseIdleConnections calls (poison sweep + releaseOnClose reclaim), got %d — "+
			"orphan reclaim on body close is missing", got)
	}
}

// appendAckAdviceFrame builds a regular s2s/proto frame carrying a VALID
// AppendAck {Start:0, End:1} (matching one submitted record) with the
// ReconnectAdvised flag set. In the append path, handleFrame (s2/append.go)
// poisons on the advice flag and then unmarshals the body as an AppendAck, so
// the advice frame must ALSO be a valid ack to satisfy the pump's ack
// validation (validateAckLocked: ackCount == entry.expectedCount).
func appendAckAdviceFrame(t *testing.T) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.AppendAck{
		Start: &pb.StreamPosition{SeqNum: 0},
		End:   &pb.StreamPosition{SeqNum: 1},
	})
	if err != nil {
		t.Fatalf("marshal append ack: %v", err)
	}
	frame := internalframing.CreateFrame(data, false, internalframing.CompressionNone)
	frame[3] |= 0x10 // flagReconnectAdvised
	return frame
}

// TestAppendSession_PoisonBeforeBodyCloseReclaimsOrphan drives the real
// transportAppendSession path (s2/append.go: capturePoisonHandle at :125,
// poisonCapture.poison() at :400, readAcksLoop body close at :358) against a
// streaming acks body that delivers a ReconnectAdvised ack frame for one
// submitted record. It asserts the orphan is reclaimed when the acks body
// closes — which happens via the pump's reconnect path
// (handleReconnectAdvice -> closeSessionIfUnused -> session.Close ->
// conn.Body.Close -> releaseOnClose.Close) and/or via the outer session.Close.
func TestAppendSession_PoisonBeforeBodyCloseReclaimsOrphan(t *testing.T) {
	rt := &reconnectCountingRT{
		adviceFrame:   appendAckAdviceFrame(t),
		terminalFrame: terminalOKFrame(),
	}
	streamClient := newSpreadStreamClient(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	session, err := streamClient.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("open append session: %v", err)
	}

	// Submit one record so the pump starts a transport session (RoundTrip).
	if _, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Wait for poison (CloseIdleConnections #1, runs in readAcksLoop when the
	// advice frame is decoded) and the body-close reclaim (#2, fires when the
	// pump closes the old session on reconnect advice).
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && rt.CloseIdleCalls() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := rt.CloseIdleCalls(); got < 2 {
		t.Fatalf("expected >= 2 CloseIdleConnections calls (poison sweep + releaseOnClose reclaim), got %d — "+
			"orphan reclaim on body close is missing (calls=%d)", got, rt.Calls())
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close append session: %v", err)
	}
}
