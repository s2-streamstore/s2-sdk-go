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

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
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

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Wait: Int32(0), IgnoreCommandRecords: true})
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
	r.session, err = newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
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
	// Only a stop condition ends iteration cleanly (spec R9b).
	if err := session.Err(); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed after close, got %v", err)
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

func TestReadSessionCaughtUpAfterFinalRecord(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("b")},
	}, &pb.StreamPosition{SeqNum: 2, Timestamp: 42}))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	future := session.CaughtUp()
	deadline := time.Now().Add(time.Second)
	for session.LastObservedTail() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if session.LastObservedTail() == nil {
		t.Fatal("timed out waiting for the batch")
	}
	// Like caught-up state, the resume position follows delivery.
	if position := session.NextReadPosition(); position != nil {
		t.Fatalf("expected no read position before Next, got %+v", position)
	}
	select {
	case <-future.result.done:
		t.Fatal("caught up before Next returned any records")
	default:
	}
	if session.IsCaughtUp() {
		t.Fatal("caught up before Next returned any records")
	}

	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatal("expected record 0")
	}
	if position := session.NextReadPosition(); position == nil || position.SeqNum != 1 {
		t.Fatalf("expected read position 1 after record 0, got %v", position)
	}
	select {
	case <-future.result.done:
		t.Fatal("caught up before Next returned the final record")
	default:
	}
	if session.IsCaughtUp() {
		t.Fatal("caught up before Next returned the final record")
	}

	if !session.Next() || session.Record().SeqNum != 1 {
		t.Fatal("expected record 1")
	}
	want := StreamPosition{SeqNum: 2, Timestamp: 42}
	if tail := waitCaughtUp(t, future); tail != want {
		t.Fatalf("expected tail %+v, got %+v", want, tail)
	}
	if position := session.NextReadPosition(); position == nil || position.SeqNum != 2 {
		t.Fatalf("expected read position 2 after record 1, got %v", position)
	}
	if !session.IsCaughtUp() {
		t.Fatal("expected caught up after the final record")
	}

	if session.Next() {
		t.Fatal("expected the session to end")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}
}

func TestReadSessionCleanEndClosesCaughtUpFutureWithoutNext(t *testing.T) {
	rt := &staticStatusRoundTripper{status: http.StatusOK}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
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

	reader := &streamReader{
		streamClient: newFrameServedStreamClient(&staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}),
		recordsCh:    make(chan readDelivery, 1),
	}
	reader.caughtUp.setCaughtUp(StreamPosition{SeqNum: 1})

	if err := reader.runOnce(context.Background(), &ReadOptions{Wait: Int32(0)}); err != nil {
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

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	future := session.CaughtUp()
	if session.Next() {
		t.Fatalf("expected no records, got seq_num %d", session.Record().SeqNum)
	}
	tail := waitCaughtUp(t, future)
	if tail.SeqNum != 5 || tail.Timestamp != 99 {
		t.Errorf("expected tail seq_num 5 timestamp 99, got %+v", tail)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	// Once iteration ends, the session is no longer caught up (spec R7c).
	if session.IsCaughtUp() {
		t.Error("expected session not to be caught up once iteration ended")
	}
	if _, err := session.CaughtUp().Wait(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("expected a new CaughtUp after the end to fail with ErrSessionClosed, got %v", err)
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
	}, nil))
	body.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 1, Body: []byte("token"), Headers: []*pb.Header{{Name: nil, Value: []byte("fence")}}},
	}, &pb.StreamPosition{SeqNum: 2, Timestamp: 20}))

	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), &ReadOptions{Wait: Int32(0), IgnoreCommandRecords: true})
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	future := session.CaughtUp()
	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatal("expected record 0")
	}
	select {
	case <-future.result.done:
		t.Fatal("caught up while a later filtered batch was still pending")
	default:
	}
	if session.Next() {
		t.Fatalf("expected no more records, got %d", session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected session error: %v", err)
	}

	want := StreamPosition{SeqNum: 2, Timestamp: 20}
	tail, err := future.Wait(context.Background())
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

	read := newRetryRead(t, first, second.Bytes())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstFuture := read.session.CaughtUp()
	if !read.session.Next() || read.session.Record().SeqNum != 0 {
		t.Fatal("expected record 0")
	}
	firstTail := waitCaughtUp(t, firstFuture)
	if firstTail.SeqNum != 1 {
		t.Fatalf("expected first tail 1, got %+v", firstTail)
	}

	read.startRetry(t, ctx)
	if read.session.IsCaughtUp() {
		t.Fatal("expected retry to mark the session behind without calling Next")
	}

	future := read.session.CaughtUp()
	close(read.releaseSecond)
	if read.session.Next() {
		t.Fatalf("expected no more records, got %d", read.session.Record().SeqNum)
	}
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
	if !read.session.Next() || read.session.Record().SeqNum != 0 {
		t.Fatal("expected record 0")
	}
	select {
	case <-future.result.done:
		t.Fatal("expected future to stay pending after the gap batch")
	default:
	}

	close(read.releaseSecond)
	if read.session.Next() {
		t.Fatalf("expected no more records, got %d", read.session.Record().SeqNum)
	}
	tail, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("failed to catch up after retry: %v", err)
	}
	want := StreamPosition{SeqNum: 1, Timestamp: 20}
	if tail != want {
		t.Fatalf("expected tail %+v, got %+v", want, tail)
	}
}

func TestReadSessionDeliversBufferedRecordsBeforeFatalError(t *testing.T) {
	const batches = readSessionBatchesBuffer
	var body bytes.Buffer
	for i := range batches {
		body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
			{SeqNum: uint64(i), Body: []byte("r")},
		}))
	}
	body.Write(internalframing.CreateFrameWithStatus(
		[]byte(`{"message":"bad read","code":"BAD_READ"}`),
		true,
		internalframing.CompressionNone,
		http.StatusBadRequest,
	))
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	// Wait until the reader has buffered every batch and reported the error.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := session.CaughtUp().Wait(ctx); err == nil {
		t.Fatal("expected fatal error")
	}

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if len(got) != batches {
		t.Fatalf("expected %d buffered records before the fatal error, got %v", batches, got)
	}
	var s2Err *S2Error
	if !errors.As(session.Err(), &s2Err) || s2Err.Status != http.StatusBadRequest {
		t.Fatalf("expected the fatal error after buffered records, got %v", session.Err())
	}
	if pos := session.NextReadPosition(); pos == nil || pos.SeqNum != batches {
		t.Fatalf("expected next read position %d, got %+v", batches, pos)
	}
}

func TestReadSessionCleanEndRetriesWithoutStopOption(t *testing.T) {
	tests := []struct {
		name      string
		opts      *ReadOptions
		wantCalls int
		wantErr   bool
	}{
		{name: "no stop option", opts: nil, wantCalls: 2, wantErr: true},
		{name: "count", opts: &ReadOptions{Count: Uint64(10)}, wantCalls: 1},
		{name: "wait", opts: &ReadOptions{Wait: Int32(5)}, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &staticStatusRoundTripper{status: http.StatusOK}
			session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), tt.opts)
			if err != nil {
				t.Fatalf("open read session: %v", err)
			}
			defer session.Close()

			if session.Next() {
				t.Fatal("unexpected record")
			}
			err = session.Err()
			var s2Err *S2Error
			if tt.wantErr && (!errors.As(err, &s2Err) || !s2Err.IsRetryable()) {
				t.Fatalf("expected retryable connection error, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected session error: %v", err)
			}
			if rt.calls != tt.wantCalls {
				t.Fatalf("expected %d requests, got %d", tt.wantCalls, rt.calls)
			}
		})
	}
}

func TestReadSessionDecodesCompressedErrorResponse(t *testing.T) {
	body, err := internalframing.Compress([]byte(`{"code":"invalid","message":"bad stream config"}`), internalframing.CompressionGzip)
	if err != nil {
		t.Fatal(err)
	}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Encoding", "gzip")
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     header,
			Request:    req,
		}, nil
	})
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer session.Close()

	if session.Next() {
		t.Fatal("unexpected record")
	}
	var s2Err *S2Error
	if !errors.As(session.Err(), &s2Err) || s2Err.Code != "invalid" || s2Err.Message != "bad stream config" {
		t.Fatalf("expected decoded server error, got %v", session.Err())
	}
}

func TestReadSession_ProtocolErrorsDoNotRetry(t *testing.T) {
	oversizedPayload := make([]byte, internalframing.MaxDecompressedSize+1)
	oversizedGzip, err := internalframing.Compress(oversizedPayload, internalframing.CompressionGzip)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		status int
		header http.Header
		body   []byte
	}{
		{
			name:   "oversized s2s frame header",
			status: http.StatusOK,
			body:   []byte{0xFF, 0xFF, 0xFF, 0x00},
		},
		{
			name:   "terminal frame with 2xx status",
			status: http.StatusOK,
			body:   internalframing.CreateFrameWithStatus(nil, true, internalframing.CompressionNone, http.StatusOK),
		},
		{
			name:   "invalid protobuf ReadBatch",
			status: http.StatusOK,
			body:   internalframing.CreateFrame([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, false, internalframing.CompressionNone),
		},
		{
			name:   "oversized compressed frame",
			status: http.StatusOK,
			body:   internalframing.CreateFrame(oversizedPayload, false, internalframing.CompressionGzip),
		},
		{
			name:   "oversized compressed 500 error response",
			status: http.StatusInternalServerError,
			header: http.Header{"Content-Encoding": {"gzip"}},
			body:   oversizedGzip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				hdr := make(http.Header)
				if tt.header != nil {
					hdr = tt.header.Clone()
				}
				return &http.Response{
					StatusCode: tt.status,
					Header:     hdr,
					Body:       io.NopCloser(bytes.NewReader(tt.body)),
					Request:    req,
				}, nil
			})
			stream := newFrameServedStreamClient(rt)
			stream.basinClient.retryConfig.MaxAttempts = 3

			session, err := stream.ReadSession(context.Background(), nil)
			if err != nil {
				t.Fatalf("open read session: %v", err)
			}
			defer session.Close()

			if session.Next() {
				t.Fatal("unexpected record")
			}
			var s2Err *S2Error
			if !errors.As(session.Err(), &s2Err) || s2Err.Code != protocolErrorCode || s2Err.IsRetryable() {
				t.Fatalf("expected non-retryable PROTOCOL S2Error, got %v", session.Err())
			}
			if calls != 1 {
				t.Fatalf("expected protocol error not to retry, got %d requests", calls)
			}
		})
	}
}

func TestReadSession_ServerDrainingRespectsProgress(t *testing.T) {
	t.Run("persistent drain without progress exhausts retries", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		calls := 0
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"code":"server_draining","message":"draining"}`))),
				Request:    req,
			}, nil
		})
		stream := newFrameServedStreamClient(rt)
		stream.basinClient.retryConfig.MaxAttempts = 3

		session, err := stream.ReadSession(ctx, nil)
		if err != nil {
			t.Fatalf("open read session: %v", err)
		}
		defer session.Close()

		if session.Next() {
			t.Fatal("unexpected record")
		}
		if !isServerDraining(session.Err()) {
			t.Fatalf("expected server_draining error, got %v", session.Err())
		}
		if calls != 3 {
			t.Fatalf("expected 3 attempts, got %d", calls)
		}
	})

	t.Run("drain after heartbeat hands over without spending retry budget", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		var firstBody bytes.Buffer
		firstBody.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 0, Timestamp: 10}))
		firstBody.Write(internalframing.CreateFrameWithStatus(
			[]byte(`{"code":"server_draining","message":"draining"}`),
			true,
			internalframing.CompressionNone,
			http.StatusServiceUnavailable,
		))

		var secondBody bytes.Buffer
		secondBody.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
			{SeqNum: 0, Body: []byte("after-drain")},
		}, &pb.StreamPosition{SeqNum: 1, Timestamp: 20}))

		calls := 0
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := secondBody.Bytes()
			if calls == 1 {
				body = firstBody.Bytes()
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    req,
			}, nil
		})
		stream := newFrameServedStreamClient(rt)
		stream.basinClient.retryConfig.MaxAttempts = 1

		session, err := stream.ReadSession(ctx, &ReadOptions{Wait: Int32(0)})
		if err != nil {
			t.Fatalf("open read session: %v", err)
		}
		defer session.Close()

		if !session.Next() {
			t.Fatalf("expected record after drain handover, err=%v", session.Err())
		}
		if got := string(session.Record().Body); got != "after-drain" {
			t.Fatalf("expected record body %q, got %q", "after-drain", got)
		}
		if session.Next() {
			t.Fatal("unexpected second record")
		}
		if err := session.Err(); err != nil {
			t.Fatalf("unexpected error after handover: %v", err)
		}
		if calls != 2 {
			t.Fatalf("expected 2 requests, got %d", calls)
		}
	})
}

func TestReadSessionResumesBySeqNumAfterResolvingStart(t *testing.T) {
	var firstBody bytes.Buffer
	firstBody.Write(buildReadBatchFrameWithTail(t, nil, &pb.StreamPosition{SeqNum: 10, Timestamp: 100}))
	firstBody.Write(internalframing.CreateFrameWithStatus(
		[]byte(`{"code":"server_draining","message":"draining"}`),
		true, internalframing.CompressionNone, http.StatusServiceUnavailable,
	))
	var secondBody bytes.Buffer
	secondBody.Write(buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 10, Body: []byte("r10")},
	}, &pb.StreamPosition{SeqNum: 11, Timestamp: 110}))

	var queries []string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		queries = append(queries, req.URL.RawQuery)
		b := secondBody.Bytes()
		if len(queries) == 1 {
			b = firstBody.Bytes()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(b)),
			Request:    req,
		}, nil
	})
	stream := newFrameServedStreamClient(rt)
	session, err := stream.ReadSession(context.Background(), &ReadOptions{
		Timestamp: Uint64(99),
		Clamp:     Bool(true),
		Wait:      Int32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if !session.Next() || session.Record().SeqNum != 10 {
		t.Fatalf("expected record 10, err=%v", session.Err())
	}
	if session.Next() {
		t.Fatal("expected clean end")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(queries))
	}
	if got := queries[1]; got != "seq_num=10&wait=0" {
		t.Fatalf("expected resumed query %q (no timestamp/clamp), got %q", "seq_num=10&wait=0", got)
	}
}

func TestReadSessionMaxAttemptsOneDoesNotRetryAfterProgress(t *testing.T) {
	var firstBody bytes.Buffer
	firstBody.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("a")}}))
	firstBody.Write(internalframing.CreateFrameWithStatus(
		[]byte(`{"code":"unavailable","message":"boom"}`),
		true, internalframing.CompressionNone, http.StatusServiceUnavailable,
	))
	calls := 0
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(firstBody.Bytes())),
			Request:    req,
		}, nil
	})
	stream := newFrameServedStreamClient(rt)
	stream.basinClient.retryConfig.MaxAttempts = 1

	session, err := stream.ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatalf("expected record 0, err=%v", session.Err())
	}
	if session.Next() {
		t.Fatal("unexpected second record")
	}
	if session.Err() == nil {
		t.Fatal("expected 503 error when MaxAttempts=1")
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt with MaxAttempts=1 even after progress, got %d", calls)
	}
}

func TestReadSessionStopCondition416ClosesCleanly(t *testing.T) {
	firstBody := buildReadBatchFrameWithTail(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	}, &pb.StreamPosition{SeqNum: 5, Timestamp: 10})
	failFirst := make(chan struct{})
	calls := 0
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       newFailingReadBody(firstBody, failFirst),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"tail":{"seq_num":5,"timestamp":50}}`))),
			Request:    req,
		}, nil
	})
	stream := newFrameServedStreamClient(rt)
	session, err := stream.ReadSession(context.Background(), &ReadOptions{Count: Uint64(10)})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	future := session.CaughtUp()

	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatalf("expected record 0, err=%v", session.Err())
	}
	close(failFirst)
	if session.Next() {
		t.Fatal("expected clean stop on resumed 416")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("expected nil error on stop-condition 416, got %v", err)
	}
	tail := waitCaughtUp(t, future)
	if tail.SeqNum != 5 || tail.Timestamp != 50 {
		t.Fatalf("expected caught_up tail 5/50, got %+v", tail)
	}
	if pos := session.NextReadPosition(); pos == nil || pos.SeqNum != 5 {
		t.Fatalf("expected NextReadPosition 5, got %+v", pos)
	}
}

func TestReadSessionCountExhaustedClosesWithoutNewRequest(t *testing.T) {
	firstBody := buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("a")}})
	failFirst := make(chan struct{})
	calls := 0
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       newFailingReadBody(firstBody, failFirst),
			Request:    req,
		}, nil
	})
	stream := newFrameServedStreamClient(rt)
	// With no retry budget left, only the stop condition can end this cleanly.
	stream.basinClient.retryConfig.MaxAttempts = 1
	session, err := stream.ReadSession(context.Background(), &ReadOptions{Count: Uint64(1)})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatalf("expected record 0, err=%v", session.Err())
	}
	close(failFirst)
	if session.Next() {
		t.Fatal("expected clean end after count reached")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("expected nil error after count reached, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected no second request after count reached, got %d", calls)
	}
}

func TestReadSessionHotServerRetiresConnection(t *testing.T) {
	var dialCount int
	var connIDs []int
	pool := newSpreadTransport(func() http.RoundTripper {
		dialCount++
		id := dialCount
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			connIDs = append(connIDs, id)
			if len(connIDs) == 1 {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"code":"hot_server","message":"hot"}`))),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("ok")}}))),
				Request:    req,
			}, nil
		})
	})
	stream := newFrameServedStreamClient(pool)
	session, err := stream.ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if !session.Next() || session.Record().SeqNum != 0 {
		t.Fatalf("expected record 0 after hot_server retry, err=%v", session.Err())
	}
	if len(connIDs) != 2 || connIDs[0] == connIDs[1] {
		t.Fatalf("expected hot_server to retire connection and dial a fresh one, got connIDs=%v", connIDs)
	}
}

// stallRoundTripper stalls until the request context ends, either before
// headers or while sending a 503 body.
type stallRoundTripper struct{ stallBody bool }

func (r stallRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !r.stallBody {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: ctxBlockedBody{req.Context()}, Header: make(http.Header), Request: req}, nil
}

type ctxBlockedBody struct{ ctx context.Context }

func (b ctxBlockedBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (ctxBlockedBody) Close() error { return nil }

func TestReadSessionRequestTimeoutEndsStalledAttempt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stallBody bool
	}{
		{name: "no response headers", stallBody: false},
		{name: "error body never completes", stallBody: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streamClient := newFrameServedStreamClient(stallRoundTripper{stallBody: tc.stallBody})
			streamClient.basinClient.requestTimeout = 50 * time.Millisecond
			streamClient.basinClient.retryConfig.MaxAttempts = 1

			session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
			if err != nil {
				t.Fatalf("open read session: %v", err)
			}
			defer session.Close()

			done := make(chan struct{})
			go func() {
				for session.Next() {
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("read session hung on a stalled response")
			}

			var s2Err *S2Error
			if err := session.Err(); !errors.As(err, &s2Err) || s2Err.Status != 408 || s2Err.Code != "REQUEST_TIMEOUT" || s2Err.Origin != errorOriginSDK {
				t.Fatalf("expected an SDK 408 REQUEST_TIMEOUT error, got %v", err)
			}
		})
	}
}

func TestReadSessionTruncatedFrameIsConnectionLost(t *testing.T) {
	frame := buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("abc")}})
	streamClient := newFrameServedStreamClient(&staticStatusRoundTripper{status: http.StatusOK, body: frame[:len(frame)-1]})
	streamClient.basinClient.retryConfig.MaxAttempts = 1

	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Wait: Int32(0)})
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer session.Close()
	for session.Next() {
	}

	var s2Err *S2Error
	if err := session.Err(); !errors.As(err, &s2Err) || s2Err.Code != "CONNECTION_LOST" {
		t.Fatalf("expected a connection-lost error, got %v", err)
	}
}

func TestReadSessionCloseAfterStopConditionKeepsCleanEnd(t *testing.T) {
	frame := buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("a")}})
	streamClient := newFrameServedStreamClient(&staticStatusRoundTripper{status: http.StatusOK, body: frame})
	session, err := streamClient.ReadSession(context.Background(), &ReadOptions{Count: Uint64(1)})
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	if !session.Next() {
		t.Fatalf("expected the only record, got err %v", session.Err())
	}

	// Once the reader has ended on the stop condition and every fetched
	// record is delivered, the clean end is locked in (spec R9).
	deadline := time.Now().Add(2 * time.Second)
	for !session.reader.stoppedCleanly.Load() {
		if time.Now().After(deadline) {
			t.Fatal("reader did not stop on the stop condition")
		}
		time.Sleep(time.Millisecond)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if session.Next() {
		t.Fatal("expected end of stream")
	}
	if err := session.Err(); err != nil {
		t.Fatalf("expected a clean end after the stop condition, got %v", err)
	}
}
