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
	return buildReadBatchFrameWithTail(t, records, nil)
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

func buildReadBatchFrameWithTail(t *testing.T, records []*pb.SequencedRecord, tail *pb.StreamPosition) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.ReadBatch{Records: records, Tail: tail})
	if err != nil {
		t.Fatalf("marshal read batch: %v", err)
	}
	return internalframing.CreateFrame(data, false, internalframing.CompressionNone)
}

type failingReadBody struct {
	data      *bytes.Reader
	fail      <-chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newFailingReadBody(data []byte, fail <-chan struct{}) *failingReadBody {
	return &failingReadBody{
		data:   bytes.NewReader(data),
		fail:   fail,
		closed: make(chan struct{}),
	}
}

func (b *failingReadBody) Read(p []byte) (int, error) {
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

func (b *failingReadBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type retryReadRoundTripper struct {
	firstBody     io.ReadCloser
	secondBody    []byte
	secondStarted chan struct{}
	releaseSecond <-chan struct{}
	calls         int
}

func (r *retryReadRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	call := r.calls

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

type retryRead struct {
	session       *ReadSession
	failFirst     chan struct{}
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func newRetryRead(t *testing.T, first, second []byte) *retryRead {
	t.Helper()

	r := &retryRead{
		failFirst:     make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	rt := &retryReadRoundTripper{
		firstBody:     newFailingReadBody(first, r.failFirst),
		secondBody:    second,
		secondStarted: r.secondStarted,
		releaseSecond: r.releaseSecond,
	}
	var err error
	r.session, err = newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	t.Cleanup(func() { _ = r.session.Close() })
	return r
}

func (r *retryRead) startRetry(t *testing.T, ctx context.Context) {
	t.Helper()
	close(r.failFirst)
	select {
	case <-r.secondStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for retry")
	}
}

func waitCaughtUp(t *testing.T, future *CaughtUpFuture) StreamPosition {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tail, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for caught up: %v", err)
	}
	return tail
}

func TestCaughtUpFuturePinsTail(t *testing.T) {
	want := StreamPosition{SeqNum: 10, Timestamp: 20}

	t.Run("created before catch-up", func(t *testing.T) {
		session := &ReadSession{reader: &streamReader{}}
		future := *session.CaughtUp()
		session.reader.caughtUp.setCaughtUp(want)
		session.reader.caughtUp.setBehind()

		if got := waitCaughtUp(t, &future); got != want {
			t.Fatalf("expected tail %+v, got %+v", want, got)
		}
		if session.IsCaughtUp() {
			t.Fatal("expected current state to be behind")
		}
	})

	t.Run("created while caught up", func(t *testing.T) {
		session := &ReadSession{reader: &streamReader{}}
		session.reader.caughtUp.setCaughtUp(want)
		future := session.CaughtUp()
		session.reader.caughtUp.setBehind()

		if got := waitCaughtUp(t, future); got != want {
			t.Fatalf("expected tail %+v, got %+v", want, got)
		}
	})
}

func TestCaughtUpFutureReturnsSessionClosedOnClose(t *testing.T) {
	started := make(chan struct{})
	body := newFailingReadBody(nil, make(chan struct{}))
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	future := session.CaughtUp()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close read session: %v", err)
	}
	session.reader.caughtUp.setCaughtUp(StreamPosition{SeqNum: 1})
	if session.IsCaughtUp() {
		t.Fatal("closed session accepted a late update")
	}
	if _, err := future.Wait(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
	if session.Next() {
		t.Fatal("closed session returned a record")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("close returned a read error: %v", err)
	}
	select {
	case _, ok := <-session.reader.recordsCh:
		if ok {
			t.Fatal("reader returned records after close")
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not stop after close")
	}
	if err, ok := <-session.reader.errorCh; ok {
		t.Fatalf("reader returned an error after close: %v", err)
	}
}

func TestCaughtUpFutureSurvivesCanceledWait(t *testing.T) {
	session := &ReadSession{reader: &streamReader{}}
	future := session.CaughtUp()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := future.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	want := StreamPosition{SeqNum: 10, Timestamp: 20}
	session.reader.caughtUp.setCaughtUp(want)
	if got := waitCaughtUp(t, future); got != want {
		t.Fatalf("expected tail %+v, got %+v", want, got)
	}
}

func TestReadBatchCaughtUp(t *testing.T) {
	tests := []struct {
		name  string
		batch ReadBatch
		want  bool
	}{
		{
			name:  "heartbeat",
			batch: ReadBatch{Tail: &StreamPosition{SeqNum: 5}},
			want:  true,
		},
		{
			name: "last record reaches tail",
			batch: ReadBatch{
				Records: []SequencedRecord{{SeqNum: 3}, {SeqNum: 4}},
				Tail:    &StreamPosition{SeqNum: 5},
			},
			want: true,
		},
		{
			name: "gap remains",
			batch: ReadBatch{
				Records: []SequencedRecord{{SeqNum: 1}},
				Tail:    &StreamPosition{SeqNum: 5},
			},
		},
		{
			name:  "tail omitted",
			batch: ReadBatch{Records: []SequencedRecord{{SeqNum: 4}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readBatchCaughtUp(&tt.batch); got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
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

	tail := waitCaughtUp(t, session.CaughtUp())
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
	reader := &streamReader{
		streamClient: newFrameServedStreamClient(rt),
		recordsCh:    make(chan []SequencedRecord, 1),
		ctx:          ctx,
	}
	reader.recordsCh <- []SequencedRecord{{SeqNum: 99}}
	future := reader.caughtUp.newFuture()
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
	reader.caughtUp.end(nil)
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

func TestReadSessionFallsBehindOnGap(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{{SeqNum: 1}}, &pb.StreamPosition{SeqNum: 5}))
	body.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	reader := &streamReader{
		streamClient: newFrameServedStreamClient(&staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}),
		recordsCh:    make(chan []SequencedRecord, 1),
	}
	reader.caughtUp.setCaughtUp(StreamPosition{SeqNum: 1})

	if err := reader.runOnce(context.Background(), nil); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if reader.caughtUp.isCaughtUp() {
		t.Fatal("expected a gap before the reported tail to mark the session behind")
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
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
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

	tail := waitCaughtUp(t, session.CaughtUp())
	if tail.SeqNum != 5 || tail.Timestamp != 99 {
		t.Errorf("expected tail seq_num 5 timestamp 99, got %+v", tail)
	}

	for session.Next() {
		t.Errorf("expected no records, got seq_num %d", session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	if !session.IsCaughtUp() {
		t.Error("expected session to remain caught up after a clean end at the tail")
	}
	last := session.LastObservedTail()
	if last == nil || last.SeqNum != 5 || last.Timestamp != 99 {
		t.Errorf("expected last observed tail 5/99, got %v", last)
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
	position := session.NextReadPosition()
	if position == nil || position.SeqNum != 2 {
		t.Fatalf("expected next read position 2, got %v", position)
	}
}

func TestReadSessionRetryMarksBehindBeforeNextAttempt(t *testing.T) {
	first := buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 1, Timestamp: 10})
	var second bytes.Buffer
	second.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 2, Timestamp: 20}))
	second.Write(internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK))

	read := newRetryRead(t, first, second.Bytes())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstTail := waitCaughtUp(t, read.session.CaughtUp())
	if firstTail.SeqNum != 1 {
		t.Fatalf("expected first tail 1, got %+v", firstTail)
	}

	read.startRetry(t, ctx)
	if read.session.IsCaughtUp() {
		t.Fatal("expected retry to mark the session behind without calling Next")
	}

	future := read.session.CaughtUp()
	close(read.releaseSecond)
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

	read := newRetryRead(t, first, second.Bytes())
	future := read.session.CaughtUp()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	read.startRetry(t, ctx)
	select {
	case <-future.result.done:
		t.Fatal("expected future to stay pending across retry")
	default:
	}

	close(read.releaseSecond)
	tail, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("failed to catch up after retry: %v", err)
	}
	want := StreamPosition{SeqNum: 1, Timestamp: 20}
	if tail != want {
		t.Fatalf("expected tail %+v, got %+v", want, tail)
	}
}
