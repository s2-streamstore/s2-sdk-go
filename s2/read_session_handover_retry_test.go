package s2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

// buildReadBatchFrameWithReconnectAdvice builds a regular S2S frame carrying a
// ReadBatch payload with the ReconnectAdvised flag (bit 4) set. Regular frames
// only, no compression.
func buildReadBatchFrameWithReconnectAdvice(t *testing.T, records []*pb.SequencedRecord) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{Records: records})
	if err != nil {
		t.Fatalf("marshal read batch: %v", err)
	}
	payloadLen := 1 + len(data) // 1 flag byte + payload
	frame := make([]byte, 4+len(data))
	frame[0] = byte(payloadLen >> 16)
	frame[1] = byte(payloadLen >> 8)
	frame[2] = byte(payloadLen)
	frame[3] = 0x10 // flagReconnectAdvised (bit 4): regular frame, no compression, advice set
	copy(frame[4:], data)
	return frame
}

// buildServerDrainingTerminalFrame builds a terminal S2S frame carrying a 503
// status code with a server_draining JSON body, mirroring a server that ends a
// response with a draining handover after earlier non-terminal frames.
func buildServerDrainingTerminalFrame(t *testing.T) []byte {
	t.Helper()
	return internalframing.CreateFrameWithStatus(
		[]byte(`{"message":"server draining","code":"server_draining"}`),
		true,
		internalframing.CompressionNone,
		http.StatusServiceUnavailable,
	)
}

type scriptEntry struct {
	status int
	body   []byte
}

// scriptedRoundTripper serves a fixed sequence of HTTP responses, one per
// RoundTrip call. Calls beyond the scripted sequence return HTTP 500 with no
// body (a plain retryable server error, never server_draining).
type scriptedRoundTripper struct {
	mu     sync.Mutex
	script []scriptEntry
	calls  int
}

func (r *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	idx := r.calls - 1
	status := http.StatusInternalServerError
	body := []byte(nil)
	if idx < len(r.script) {
		status = r.script[idx].status
		body = r.script[idx].body
	}
	r.mu.Unlock()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *scriptedRoundTripper) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// runReadSessionScript drives a streaming read session against the given
// response script with the provided MaxAttempts, draining the records channel
// in the background, and returns the number of HTTP calls made, the count of
// records delivered when the session terminated, and the terminal error.
func runReadSessionScript(t *testing.T, maxAttempts int, script []scriptEntry) (int, uint64, error) {
	t.Helper()

	rt := &scriptedRoundTripper{script: script}
	basinClient := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: &RetryConfig{
			MaxAttempts:  maxAttempts,
			MinBaseDelay: time.Millisecond,
			MaxBaseDelay: time.Millisecond,
		},
	}
	basinClient.client = &Client{streamingClient: &http.Client{Transport: rt}}
	streamClient := &StreamClient{name: StreamName("test"), basinClient: basinClient}

	reader, err := streamClient.newStreamReader(context.Background(), &ReadOptions{Count: Uint64(1000)})
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		for range reader.recordsCh {
		}
		close(drained)
	}()

	select {
	case terminalErr := <-reader.errorCh:
		reader.Close()
		<-drained
		return rt.callCount(), reader.recordsRead, terminalErr
	case <-time.After(5 * time.Second):
		reader.Close()
		t.Fatal("timed out waiting for read session terminal error")
		return 0, 0, nil
	}
}

// oneRecordAdviceBody builds a 200 body carrying a single record in a frame
// with the ReconnectAdvised flag set.
func oneRecordAdviceBody(t *testing.T) []byte {
	t.Helper()
	return buildReadBatchFrameWithReconnectAdvice(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	})
}

// oneRecordThenDrainBody builds a 200 body carrying a single record in a
// regular frame followed by a terminal 503 server_draining frame, so runOnce
// delivers the record (advancing recordsRead) before returning the drain error.
func oneRecordThenDrainBody(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}))
	body.Write(buildServerDrainingTerminalFrame(t))
	return body.Bytes()
}

func server500Body() []byte {
	return []byte(`{"message":"boom","code":"internal"}`)
}

func serverDrainingHTTPStatusBody() []byte {
	return []byte(`{"message":"server draining","code":"server_draining"}`)
}

func assertTerminal500(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a terminal error, got nil")
	}
	var s2Err *S2Error
	if !errors.As(err, &s2Err) {
		t.Fatalf("expected *S2Error terminal error, got %T: %v", err, err)
	}
	if s2Err.Status != http.StatusInternalServerError {
		t.Fatalf("expected terminal HTTP 500, got HTTP %d (%v)", s2Err.Status, s2Err)
	}
}

// TestReadSessionReconnectAdviceResetsFailureStreakOnProgress verifies that a
// reconnect-advised handover which delivered records forgives the prior
// consecutive-failure streak, matching the non-handover progress rule. At the
// default MaxAttempts=3 the session must survive one extra transient failure.
func TestReadSessionReconnectAdviceResetsFailureStreakOnProgress(t *testing.T) {
	script := []scriptEntry{
		{status: http.StatusInternalServerError, body: server500Body()}, // 1
		{status: http.StatusOK, body: oneRecordAdviceBody(t)},           // 2: record + advice (progress)
		{status: http.StatusInternalServerError, body: server500Body()}, // 3
		{status: http.StatusInternalServerError, body: server500Body()}, // 4
		{status: http.StatusInternalServerError, body: server500Body()}, // 5: terminal
	}

	calls, recordsRead, terminalErr := runReadSessionScript(t, 3, script)
	assertTerminal500(t, terminalErr)
	if recordsRead != 1 {
		t.Fatalf("expected 1 record delivered, got %d", recordsRead)
	}
	// With the fix the streak resets on the progress-making handover, so the
	// session tolerates three post-handover failures (attempts 3, 4, 5) before
	// terminating at calls=5. Pre-fix this was calls=4.
	if calls != 5 {
		t.Fatalf("expected 5 total attempts after streak reset, got %d", calls)
	}
}

// TestReadSessionServerDrainingTerminalResetsFailureStreakOnProgress verifies
// the terminal-frame server_draining handover path. When runOnce delivers a
// record batch and then returns a 503 server_draining terminal error, the
// handover must forgive the prior streak just like reconnect-advised.
func TestReadSessionServerDrainingTerminalResetsFailureStreakOnProgress(t *testing.T) {
	script := []scriptEntry{
		{status: http.StatusInternalServerError, body: server500Body()}, // 1
		{status: http.StatusOK, body: oneRecordThenDrainBody(t)},        // 2: record + draining terminal
		{status: http.StatusInternalServerError, body: server500Body()}, // 3
		{status: http.StatusInternalServerError, body: server500Body()}, // 4
		{status: http.StatusInternalServerError, body: server500Body()}, // 5: terminal
	}

	calls, recordsRead, terminalErr := runReadSessionScript(t, 3, script)
	assertTerminal500(t, terminalErr)
	if recordsRead != 1 {
		t.Fatalf("expected 1 record delivered, got %d", recordsRead)
	}
	if calls != 5 {
		t.Fatalf("expected 5 total attempts after streak reset, got %d", calls)
	}
}

// TestReadSessionReconnectAdviceNoProgressPreservesFailureStreak guards the
// design contract that a no-progress handover neither consumes nor forgives
// the retry budget. With no records delivered, the streak must carry across
// the reconnect unchanged. This behavior is identical pre- and post-fix.
func TestReadSessionReconnectAdviceNoProgressPreservesFailureStreak(t *testing.T) {
	emptyAdviceBody := buildReadBatchFrameWithReconnectAdvice(t, nil)
	script := []scriptEntry{
		{status: http.StatusInternalServerError, body: server500Body()}, // 1: streak=1
		{status: http.StatusOK, body: emptyAdviceBody},                  // 2: advice, no records -> no reset
		{status: http.StatusInternalServerError, body: server500Body()}, // 3: streak=2
		{status: http.StatusInternalServerError, body: server500Body()}, // 4: streak=3 -> terminal
	}

	calls, recordsRead, terminalErr := runReadSessionScript(t, 3, script)
	assertTerminal500(t, terminalErr)
	if recordsRead != 0 {
		t.Fatalf("expected 0 records delivered, got %d", recordsRead)
	}
	if calls != 4 {
		t.Fatalf("expected 4 total attempts (no-progress handover preserves streak), got %d", calls)
	}
}

// TestReadSessionHTTPStatusServerDrainingNoProgressPreservesFailureStreak
// guards the HTTP-status server_draining path: parseHTTPError returns before
// any frame is read, so no records are delivered and the fix must not reset the
// streak. Behavior is identical pre- and post-fix.
func TestReadSessionHTTPStatusServerDrainingNoProgressPreservesFailureStreak(t *testing.T) {
	script := []scriptEntry{
		{status: http.StatusInternalServerError, body: server500Body()},               // 1: streak=1
		{status: http.StatusServiceUnavailable, body: serverDrainingHTTPStatusBody()}, // 2: HTTP 503 drain, no records
		{status: http.StatusInternalServerError, body: server500Body()},               // 3: streak=2
		{status: http.StatusInternalServerError, body: server500Body()},               // 4: streak=3 -> terminal
	}

	calls, recordsRead, terminalErr := runReadSessionScript(t, 3, script)
	assertTerminal500(t, terminalErr)
	if recordsRead != 0 {
		t.Fatalf("expected 0 records delivered, got %d", recordsRead)
	}
	if calls != 4 {
		t.Fatalf("expected 4 total attempts (HTTP-status drain has no progress), got %d", calls)
	}
}

// TestReadSessionReconnectAdviceHandoverResumesAfterRecord verifies the
// end-to-end happy-path interleaving: a record is delivered, the session
// reconnects on advice, and a subsequent attempt delivers a second record at
// the resumed position with no duplicates, then ends cleanly.
func TestReadSessionReconnectAdviceHandoverResumesAfterRecord(t *testing.T) {
	first := buildReadBatchFrameWithReconnectAdvice(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	})
	var second bytes.Buffer
	second.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 1, Body: []byte("b")},
	}))
	second.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	script := []scriptEntry{
		{status: http.StatusOK, body: first},          // 1: record 0 + advice -> handover
		{status: http.StatusOK, body: second.Bytes()}, // 2: record 1 + clean terminal
	}

	rt := &scriptedRoundTripper{script: script}
	basinClient := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	}
	basinClient.client = &Client{streamingClient: &http.Client{Transport: rt}}
	streamClient := &StreamClient{name: StreamName("test"), basinClient: basinClient}

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Count: Uint64(1000)})
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer session.Close()

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	want := []uint64{0, 1}
	if len(got) != len(want) {
		t.Fatalf("expected %d records, got %d (%v)", len(want), len(got), got)
	}
	for i, seq := range want {
		if got[i] != seq {
			t.Errorf("record %d: expected seq_num %d, got %d", i, seq, got[i])
		}
	}
	if rt.callCount() != 2 {
		t.Fatalf("expected exactly 2 HTTP calls, got %d", rt.callCount())
	}
}
