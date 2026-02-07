package s2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestReadSessionBuildAttemptOptions_AdjustsLimits(t *testing.T) {
	baseCount := uint64(10)
	baseBytes := uint64(100)
	baseWait := int32(5)
	baseUntil := uint64(1234)

	now := time.Now()
	r := &streamReader{
		baseOpts: &ReadOptions{
			Count: Uint64(baseCount),
			Bytes: Uint64(baseBytes),
			Wait:  Int32(baseWait),
			Until: Uint64(baseUntil),
		},
		recordsRead:    4,
		bytesRead:      40,
		lastRecordTime: now.Add(-2 * time.Second),
		lastTail:       &StreamPosition{SeqNum: 100},
		lastTailAt:     now.Add(-2 * time.Second),
		hasNextSeq:     true,
		nextSeq:        7,
	}

	opts := r.buildAttemptOptions(1 * time.Second)
	if opts == nil {
		t.Fatalf("expected options, got nil")
	}
	if opts.Count == nil || *opts.Count != 6 {
		t.Fatalf("expected remaining count 6, got %v", opts.Count)
	}
	if opts.Bytes == nil || *opts.Bytes != 60 {
		t.Fatalf("expected remaining bytes 60, got %v", opts.Bytes)
	}
	if opts.Wait == nil || *opts.Wait != 2 {
		t.Fatalf("expected remaining wait 2, got %v", opts.Wait)
	}
	if opts.Until == nil || *opts.Until != baseUntil {
		t.Fatalf("expected until to remain %d, got %v", baseUntil, opts.Until)
	}
	if opts.SeqNum == nil || *opts.SeqNum != 7 {
		t.Fatalf("expected seq_num 7, got %v", opts.SeqNum)
	}
	if opts.Timestamp != nil || opts.TailOffset != nil {
		t.Fatalf("expected timestamp/tail_offset to be cleared when seq_num is set")
	}
}

func TestReadSessionBuildAttemptOptions_DoesNotOverSubtract(t *testing.T) {
	baseCount := uint64(5)
	r := &streamReader{
		baseOpts: &ReadOptions{
			Count: Uint64(baseCount),
		},
		recordsRead: 3,
	}

	opts := r.buildAttemptOptions(0)
	if opts == nil || opts.Count == nil || *opts.Count != 2 {
		t.Fatalf("expected remaining count 2, got %v", opts)
	}

	r.recordsRead = 5
	opts = r.buildAttemptOptions(0)
	if opts == nil || opts.Count == nil || *opts.Count != 0 {
		t.Fatalf("expected remaining count 0, got %v", opts)
	}
}

type staticStatusRoundTripper struct {
	status int
	body   []byte
	calls  int
}

func (r *staticStatusRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(bytes.NewReader(r.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestIsCommandRecord(t *testing.T) {
	// Fence command record: single header with empty name
	fence := SequencedRecord{
		SeqNum:  0,
		Body:    []byte("token"),
		Headers: []Header{{Name: nil, Value: []byte("fence")}},
	}
	if !fence.IsCommandRecord() {
		t.Fatal("expected fence to be a command record")
	}

	// Trim command record: single header with empty name
	trim := SequencedRecord{
		SeqNum:  1,
		Body:    []byte{0, 0, 0, 0, 0, 0, 0, 2},
		Headers: []Header{{Name: nil, Value: []byte("trim")}},
	}
	if !trim.IsCommandRecord() {
		t.Fatal("expected trim to be a command record")
	}

	// Normal record: not a command record
	normal := SequencedRecord{
		SeqNum:  2,
		Body:    []byte("hello"),
		Headers: []Header{{Name: []byte("key"), Value: []byte("val")}},
	}
	if normal.IsCommandRecord() {
		t.Fatal("expected normal record not to be a command record")
	}

	// No headers: not a command record
	noHeaders := SequencedRecord{SeqNum: 3, Body: []byte("hello")}
	if noHeaders.IsCommandRecord() {
		t.Fatal("expected record with no headers not to be a command record")
	}

	// Multiple headers: not a command record
	multiHeaders := SequencedRecord{
		SeqNum:  4,
		Headers: []Header{{Name: nil, Value: []byte("fence")}, {Name: []byte("x"), Value: []byte("y")}},
	}
	if multiHeaders.IsCommandRecord() {
		t.Fatal("expected record with multiple headers not to be a command record")
	}
}

func TestFilterCommandRecords(t *testing.T) {
	batch := &ReadBatch{
		Records: []SequencedRecord{
			{SeqNum: 0, Body: []byte("a")},
			{SeqNum: 1, Body: []byte("token"), Headers: []Header{{Name: nil, Value: []byte("fence")}}},
			{SeqNum: 2, Body: []byte("b")},
		},
	}

	batch.filterCommandRecords()

	if len(batch.Records) != 2 {
		t.Fatalf("expected 2 records after filter, got %d", len(batch.Records))
	}
	if batch.Records[0].SeqNum != 0 {
		t.Fatalf("expected first record seq_num 0, got %d", batch.Records[0].SeqNum)
	}
	if batch.Records[1].SeqNum != 2 {
		t.Fatalf("expected second record seq_num 2, got %d", batch.Records[1].SeqNum)
	}
}

func TestFilterCommandRecords_NoCommandRecords(t *testing.T) {
	batch := &ReadBatch{
		Records: []SequencedRecord{
			{SeqNum: 0, Body: []byte("a")},
			{SeqNum: 1, Body: []byte("b")},
		},
	}

	batch.filterCommandRecords()

	if len(batch.Records) != 2 {
		t.Fatalf("expected 2 records unchanged, got %d", len(batch.Records))
	}
}

func TestReadSessionMaxAttempts(t *testing.T) {
	rt := &staticStatusRoundTripper{
		status: http.StatusInternalServerError,
		body:   []byte(`{"message":"boom","code":"internal"}`),
	}

	basinClient := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	}
	basinClient.client = &Client{
		streamingClient: &http.Client{Transport: rt},
	}
	streamClient := &StreamClient{
		name:        StreamName("test"),
		basinClient: basinClient,
	}

	reader, err := streamClient.newStreamReader(context.Background(), &ReadOptions{Count: Uint64(1)})
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}
	defer reader.Close()

	select {
	case err := <-reader.Errors():
		if err == nil {
			t.Fatalf("expected error after max attempts")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for read session error")
	}

	if rt.calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", rt.calls)
	}
}
