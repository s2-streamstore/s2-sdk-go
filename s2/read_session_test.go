package s2

import (
	"bytes"
	"context"
	"errors"
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

func buildReadBatchFrameWithTail(t *testing.T, records []*pb.SequencedRecord, tail *pb.StreamPosition) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{Records: records, Tail: tail})
	if err != nil {
		t.Fatalf("marshal read batch: %v", err)
	}
	return internalframing.CreateFrame(data, false, internalframing.CompressionNone)
}

func TestReadSessionCaughtUpWhenFinalRecordOfTailAbuttingBatchFetched(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("b")},
	}, &pb.StreamPosition{SeqNum: 2, Timestamp: 42}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	if session.IsCaughtUp() {
		t.Fatal("expected session to start behind")
	}
	if !session.Next() {
		t.Fatalf("expected first record, session error: %v", session.Err())
	}
	if session.IsCaughtUp() {
		t.Error("expected session to be behind with a record still pending")
	}
	if !session.Next() {
		t.Fatalf("expected second record, session error: %v", session.Err())
	}
	if !session.IsCaughtUp() {
		t.Error("expected session to be caught up after fetching the final record")
	}

	tail, err := session.CaughtUp().Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected CaughtUp Wait error: %v", err)
	}
	if tail.SeqNum != 2 || tail.Timestamp != 42 {
		t.Errorf("expected tail seq_num 2 timestamp 42, got %+v", tail)
	}
}

func TestReadSessionHeartbeatMarksCaughtUpWithoutRecords(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 5, Timestamp: 99}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	tailCh := make(chan StreamPosition, 1)
	errCh := make(chan error, 1)
	go func() {
		tail, err := session.CaughtUp().Wait(context.Background())
		if err != nil {
			errCh <- err
			return
		}
		tailCh <- tail
	}()

	for session.Next() {
		t.Errorf("expected no records, got seq_num %d", session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	select {
	case tail := <-tailCh:
		if tail.SeqNum != 5 || tail.Timestamp != 99 {
			t.Errorf("expected tail seq_num 5 timestamp 99, got %+v", tail)
		}
	case err := <-errCh:
		t.Fatalf("unexpected CaughtUp Wait error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for caught-up signal")
	}

	if !session.IsCaughtUp() {
		t.Error("expected session to remain caught up after a clean end at the tail")
	}
}

func TestReadSessionCaughtUpWaitErrsWhenSessionEndsBehind(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 5}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	for session.Next() {
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	if session.IsCaughtUp() {
		t.Error("expected session to be behind with a tail its last record does not abut")
	}
	if _, err := session.CaughtUp().Wait(context.Background()); !errors.Is(err, ErrSessionEndedBeforeCaughtUp) {
		t.Fatalf("expected ErrSessionEndedBeforeCaughtUp, got %v", err)
	}
}

func TestReadSessionCaughtUpCountsFilteredCommandRecordAtTail(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("token"), Headers: []*pb.Header{{Name: nil, Value: []byte("fence")}}},
	}, &pb.StreamPosition{SeqNum: 2}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{IgnoreCommandRecords: true})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	if !session.Next() {
		t.Fatalf("expected a record, session error: %v", session.Err())
	}
	if !session.IsCaughtUp() {
		t.Error("expected session to be caught up after the filtered record at the tail")
	}
}

func TestReadSessionFallsBehindAfterCaughtUp(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1}))
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 1, Body: []byte("b")},
		{SeqNum: 2, Body: []byte("c")},
	}, &pb.StreamPosition{SeqNum: 5}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	if !session.Next() {
		t.Fatalf("expected first record, session error: %v", session.Err())
	}
	if !session.IsCaughtUp() {
		t.Error("expected session to be caught up after the first batch")
	}
	if !session.Next() {
		t.Fatalf("expected second record, session error: %v", session.Err())
	}
	if session.IsCaughtUp() {
		t.Error("expected session to fall behind on a batch whose tail its records do not abut")
	}
}
