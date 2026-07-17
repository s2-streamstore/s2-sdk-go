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

type gatedBody struct {
	first     *bytes.Reader
	second    *bytes.Reader
	release   <-chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	released  bool
}

func newGatedBody(first, second []byte, release <-chan struct{}) *gatedBody {
	return &gatedBody{
		first:   bytes.NewReader(first),
		second:  bytes.NewReader(second),
		release: release,
		closed:  make(chan struct{}),
	}
}

func (b *gatedBody) Read(p []byte) (int, error) {
	if b.first.Len() > 0 {
		return b.first.Read(p)
	}
	if !b.released {
		select {
		case <-b.release:
			b.released = true
		case <-b.closed:
			return 0, io.ErrClosedPipe
		}
	}
	return b.second.Read(p)
}

func (b *gatedBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type failAfterGateBody struct {
	data      *bytes.Reader
	fail      <-chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newFailAfterGateBody(data []byte, fail <-chan struct{}) *failAfterGateBody {
	return &failAfterGateBody{
		data:   bytes.NewReader(data),
		fail:   fail,
		closed: make(chan struct{}),
	}
}

func (b *failAfterGateBody) Read(p []byte) (int, error) {
	if b.data.Len() > 0 {
		return b.data.Read(p)
	}
	select {
	case <-b.fail:
		return 0, io.ErrUnexpectedEOF
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
}

func (b *failAfterGateBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type bodyRoundTripper struct {
	body io.ReadCloser
}

func (r *bodyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       r.body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type retryCaughtUpRoundTripper struct {
	mu            sync.Mutex
	firstBody     io.ReadCloser
	secondBody    []byte
	secondStarted chan struct{}
	releaseSecond <-chan struct{}
	calls         int
}

func (r *retryCaughtUpRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()

	if call == 1 {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       r.firstBody,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	if call != 2 {
		return nil, errors.New("unexpected extra read attempt")
	}

	close(r.secondStarted)
	select {
	case <-r.releaseSecond:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(r.secondBody)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestCaughtUpFutureCapturesTransitionBeforeWait(t *testing.T) {
	state := &caughtUpState{}
	session := &ReadSession{reader: &streamReader{caughtUp: state}}
	future := session.CaughtUp()
	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	state.setCaughtUp(want)
	state.setBehind()

	got, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
	if session.IsCaughtUp() {
		t.Fatal("expected current state to be behind")
	}
}

func TestCaughtUpFutureKeepsTailFromCreation(t *testing.T) {
	var state caughtUpState
	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	state.setCaughtUp(want)
	future := &CaughtUpFuture{result: state.newResult()}
	state.setBehind()

	got, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
}

func TestCaughtUpStateCleanEndKeepsTail(t *testing.T) {
	var state caughtUpState
	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	state.setCaughtUp(want)
	state.end(nil)

	state.setBehind()
	state.setCaughtUp(StreamPosition{SeqNum: 11, Timestamp: 21})

	if !state.isCaughtUp() {
		t.Fatal("expected clean end to keep the caught-up state")
	}
	got, err := (&CaughtUpFuture{result: state.newResult()}).Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
}

func TestCaughtUpFutureReturnsFatalError(t *testing.T) {
	var state caughtUpState
	future := &CaughtUpFuture{result: state.newResult()}
	wantErr := errors.New("fatal read")
	state.end(wantErr)

	if _, err := future.Wait(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected fatal read error, got %v", err)
	}
}

func TestCaughtUpFutureReturnsSessionClosedOnClose(t *testing.T) {
	body := newFailAfterGateBody(nil, make(chan struct{}))
	session, err := newFrameServedStreamClient(&bodyRoundTripper{body: body}).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	future := session.CaughtUp()
	if err := session.Close(); err != nil {
		t.Fatalf("failed to close session: %v", err)
	}
	session.reader.caughtUp.setCaughtUp(StreamPosition{SeqNum: 1})
	if session.IsCaughtUp() {
		t.Fatal("closed session accepted a late caught-up update")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := future.Wait(ctx); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func TestCaughtUpFutureSurvivesCanceledWait(t *testing.T) {
	var state caughtUpState
	future := &CaughtUpFuture{result: state.newResult()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := future.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	state.setCaughtUp(want)
	got, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected second wait error: %v", err)
	}
	if got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
}

func TestCaughtUpFutureStaysPendingWhileBehind(t *testing.T) {
	var state caughtUpState
	result := state.newResult()
	state.setBehind()
	select {
	case <-result.done:
		t.Fatal("expected future to stay pending while behind")
	default:
	}

	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	state.setCaughtUp(want)
	got, err := (&CaughtUpFuture{result: result}).Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
}

func TestReadSessionCaughtUpWithoutCallingNext(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tail, err := session.CaughtUp().Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected CaughtUp Wait error: %v", err)
	}
	if tail.SeqNum != 2 || tail.Timestamp != 42 {
		t.Errorf("expected tail seq_num 2 timestamp 42, got %+v", tail)
	}
	position := session.NextReadPosition()
	if position == nil || position.SeqNum != 2 {
		t.Fatalf("expected next read position 2 before catch-up resolved, got %v", position)
	}

	var records []uint64
	for session.Next() {
		records = append(records, session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}
	if len(records) != 2 || records[0] != 0 || records[1] != 1 {
		t.Fatalf("expected records [0 1], got %v", records)
	}
}

func TestReadSessionDoesNotCatchUpBeforeRecordsAreQueued(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1, Timestamp: 10}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	ctx, cancel := context.WithCancel(context.Background())
	state := &caughtUpState{}
	reader := &streamReader{
		streamClient: newFrameServedStreamClient(rt),
		recordsCh:    make(chan []SequencedRecord, 1),
		closed:       make(chan struct{}),
		caughtUp:     state,
		ctx:          ctx,
	}
	reader.recordsCh <- []SequencedRecord{{SeqNum: 99}}
	future := &CaughtUpFuture{result: state.newResult()}
	runErr := make(chan error, 1)
	go func() { runErr <- reader.runOnce(ctx, nil) }()

	deadline := time.Now().Add(time.Second)
	for reader.NextReadPosition() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	position := reader.NextReadPosition()
	if position == nil || position.SeqNum != 1 {
		cancel()
		t.Fatalf("reader did not process the tail batch: %v", position)
	}
	select {
	case <-future.result.done:
		cancel()
		t.Fatal("caught-up future resolved before the record batch entered the session queue")
	default:
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled handoff, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out canceling blocked record handoff")
	}
	state.end(nil)
	if _, err := future.Wait(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func TestReadSessionCleanEndClosesCaughtUpFutureWithoutNext(t *testing.T) {
	body := internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK)
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := session.CaughtUp().Wait(ctx); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func TestReadSessionFatalErrorRejectsCaughtUpFutureWithoutNext(t *testing.T) {
	body := internalframing.CreateFrameWithStatus(
		[]byte(`{"message":"bad read","code":"BAD_READ"}`),
		true,
		internalframing.CompressionNone,
		http.StatusBadRequest,
	)
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body}
	sessionCtx, stopSession := context.WithCancel(context.Background())
	session, err := newFrameServedStreamClient(rt).ReadSession(sessionCtx, nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer stopSession()
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, waitErr := session.CaughtUp().Wait(ctx)
	var s2Err *S2Error
	if !errors.As(waitErr, &s2Err) {
		t.Fatalf("expected S2Error, got %v", waitErr)
	}
	if s2Err.Code != "BAD_READ" || s2Err.Message != "bad read" {
		t.Fatalf("unexpected fatal error: %+v", s2Err)
	}

	stopSession()
	for session.Next() {
	}
	var sessionErr *S2Error
	if !errors.As(session.Err(), &sessionErr) || sessionErr.Code != "BAD_READ" {
		t.Fatalf("expected Next to preserve the fatal error, got %v", session.Err())
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
	last := session.LastObservedTail()
	if last == nil || last.SeqNum != 5 || last.Timestamp != 99 {
		t.Errorf("expected last observed tail 5/99, got %v", last)
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
	if _, err := session.CaughtUp().Wait(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
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

func TestReadSessionCaughtUpTailAdvancesAfterFilteredBatch(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1, Timestamp: 10}))
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 1, Body: []byte("token"), Headers: []*pb.Header{{Name: nil, Value: []byte("fence")}}},
	}, &pb.StreamPosition{SeqNum: 2, Timestamp: 20}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{IgnoreCommandRecords: true})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	var records []uint64
	for session.Next() {
		records = append(records, session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}
	if len(records) != 1 || records[0] != 0 {
		t.Fatalf("expected only record 0, got %v", records)
	}

	want := StreamPosition{SeqNum: 2, Timestamp: 20}
	tail, err := session.CaughtUp().Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if tail != want {
		t.Fatalf("expected caught-up tail %+v, got %+v", want, tail)
	}
	last := session.LastObservedTail()
	if last == nil || *last != want {
		t.Fatalf("expected last observed tail %+v, got %v", want, last)
	}
}

func TestReadSessionFallsBehindAfterCaughtUp(t *testing.T) {
	first := buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1})
	tests := []struct {
		name  string
		frame []byte
	}{
		{
			name: "tail does not abut records",
			frame: buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
				{SeqNum: 1, Body: []byte("b")},
				{SeqNum: 2, Body: []byte("c")},
			}, &pb.StreamPosition{SeqNum: 5}),
		},
		{
			name: "tail is absent",
			frame: buildReadBatchFrame(t, []*pb.SequencedRecord{
				{SeqNum: 1, Body: []byte("b")},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			second := append([]byte(nil), tt.frame...)
			second = append(second, internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK)...)
			release := make(chan struct{})
			rt := &bodyRoundTripper{body: newGatedBody(first, second, release)}
			session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
			if err != nil {
				t.Fatalf("failed to open read session: %v", err)
			}
			defer session.Close()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := session.CaughtUp().Wait(ctx); err != nil {
				t.Fatalf("failed to reach first tail: %v", err)
			}
			if !session.IsCaughtUp() {
				t.Fatal("expected session to be caught up after the first batch")
			}
			if !session.Next() || session.Record().SeqNum != 0 {
				t.Fatalf("expected first record, got error %v", session.Err())
			}

			close(release)
			if !session.Next() || session.Record().SeqNum != 1 {
				t.Fatalf("expected second-batch record, got error %v", session.Err())
			}
			if session.IsCaughtUp() {
				t.Fatal("expected session to fall behind after the second batch")
			}
		})
	}
}

func TestReadSessionRetryMarksBehindBeforeNextAttempt(t *testing.T) {
	first := buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1, Timestamp: 10})
	var second bytes.Buffer
	second.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 2, Timestamp: 20}))
	second.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	failFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	rt := &retryCaughtUpRoundTripper{
		firstBody:     newFailAfterGateBody(first, failFirst),
		secondBody:    second.Bytes(),
		secondStarted: secondStarted,
		releaseSecond: releaseSecond,
	}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstTail, err := session.CaughtUp().Wait(ctx)
	if err != nil {
		t.Fatalf("failed to reach first tail: %v", err)
	}
	if firstTail.SeqNum != 1 {
		t.Fatalf("expected first tail 1, got %+v", firstTail)
	}

	close(failFirst)
	select {
	case <-secondStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for retry")
	}
	if session.IsCaughtUp() {
		t.Fatal("expected retry to mark the session behind without calling Next")
	}

	future := session.CaughtUp()
	close(releaseSecond)
	secondTail, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("failed to reach second tail: %v", err)
	}
	want := StreamPosition{SeqNum: 2, Timestamp: 20}
	if secondTail != want {
		t.Fatalf("expected second tail %+v, got %+v", want, secondTail)
	}
}

func TestReadSessionCaughtUpFutureStaysPendingAcrossRetry(t *testing.T) {
	first := buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 5, Timestamp: 10})
	var second bytes.Buffer
	second.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 1, Timestamp: 20}))
	second.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	failFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	rt := &retryCaughtUpRoundTripper{
		firstBody:     newFailAfterGateBody(first, failFirst),
		secondBody:    second.Bytes(),
		secondStarted: secondStarted,
		releaseSecond: releaseSecond,
	}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	future := session.CaughtUp()
	close(failFirst)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-secondStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for retry")
	}
	select {
	case <-future.result.done:
		t.Fatal("expected future to stay pending across retry")
	default:
	}

	close(releaseSecond)
	tail, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("failed to catch up after retry: %v", err)
	}
	want := StreamPosition{SeqNum: 1, Timestamp: 20}
	if tail != want {
		t.Fatalf("expected tail %+v, got %+v", want, tail)
	}
}
