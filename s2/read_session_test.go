package s2

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"google.golang.org/protobuf/proto"
)

func buildReadBatchFrame(t *testing.T, records []*pb.SequencedRecord) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{Records: records})
	if err != nil {
		t.Fatalf("marshal read batch: %v", err)
	}
	return internalframing.CreateFrame(data, false, internalframing.CompressionNone)
}

func newFrameServedStreamClient(rt http.RoundTripper) *StreamClient {
	basinClient := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		retryConfig: &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	}
	basinClient.client = &Client{
		streamingClient: &http.Client{Transport: rt},
	}
	return &StreamClient{
		name:        StreamName("test"),
		basinClient: basinClient,
	}
}

func TestReadSessionDeliversRecordsInOrderAcrossBatches(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("b")},
		{SeqNum: 2, Body: []byte("c")},
	}))
	body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 3, Body: []byte("d")},
		{SeqNum: 4, Body: []byte("e")},
	}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
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

	want := []uint64{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("expected %d records, got %d (%v)", len(want), len(got), got)
	}
	for i, seq := range want {
		if got[i] != seq {
			t.Errorf("record %d: expected seq_num %d, got %d", i, seq, got[i])
		}
	}
}

func TestReadSessionIgnoreCommandRecordsSkipsDelivery(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("token"), Headers: []*pb.Header{{Name: nil, Value: []byte("fence")}}},
		{SeqNum: 2, Body: []byte("b")},
	}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{IgnoreCommandRecords: true})
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

	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("expected seq_nums [0 2], got %v", got)
	}
}

func TestReadSessionNextReadPositionCountsFilteredRecords(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("token"), Headers: []*pb.Header{{Name: nil, Value: []byte("fence")}}},
	}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{IgnoreCommandRecords: true})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	for session.Next() {
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	pos := session.NextReadPosition()
	if pos == nil {
		t.Fatalf("expected next read position, got nil")
	}
	if pos.SeqNum != 2 {
		t.Errorf("expected next read position seq_num 2, got %d", pos.SeqNum)
	}
}
