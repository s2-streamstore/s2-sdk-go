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
)

const badReadCode = "BAD_READ"

// buildBufferedThenTerminalBody constructs a response body that emits `buffered`
// single-record, non-caught-up read-batch frames (no tail) followed by a
// terminal BAD_READ error frame with the given status code.
func buildBufferedThenTerminalBody(t *testing.T, buffered int, terminalStatus int) []byte {
	t.Helper()
	var body bytes.Buffer
	for i := range buffered {
		body.Write(buildReadBatchFrame(t, []*pb.SequencedRecord{
			{SeqNum: uint64(i), Body: []byte("r")},
		}))
	}
	body.Write(internalframing.CreateFrameWithStatus(
		[]byte(`{"message":"bad read","code":"BAD_READ"}`),
		true, internalframing.CompressionNone, terminalStatus,
	))
	return body.Bytes()
}

func wantFatalBADREAD(t *testing.T, waitErr error) {
	t.Helper()
	var s2Err *S2Error
	if !errors.As(waitErr, &s2Err) || s2Err.Code != badReadCode {
		t.Fatalf("expected BAD_READ fatal error, got %v", waitErr)
	}
}

// TestReadSessionFatalErrorDropsBufferedRecordsE2E drives a real ReadSession
// whose reader emits 10 non-caught-up batches and then a fatal terminal error.
// All buffered records must be delivered before Next returns false, and the
// fatal error must still be reported by Err().
func TestReadSessionFatalErrorDropsBufferedRecordsE2E(t *testing.T) {
	const batches = 10
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: buildBufferedThenTerminalBody(t, batches, http.StatusBadRequest)}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, waitErr := session.CaughtUp().Wait(ctx)
	wantFatalBADREAD(t, waitErr)

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if len(got) != batches {
		t.Fatalf("expected all %d buffered records delivered before the fatal error, got %d: %v (Err=%v)",
			batches, len(got), got, session.Err())
	}
	for i, seq := range got {
		if seq != uint64(i) {
			t.Fatalf("record %d: expected seq_num %d, got %d", i, i, seq)
		}
	}
	var sessionErr *S2Error
	if !errors.As(session.Err(), &sessionErr) || sessionErr.Code != badReadCode {
		t.Fatalf("expected Next to surface the deferred fatal error, got %v", session.Err())
	}
}

// TestReadSessionFatalErrorDrainsMultiRecordBatchesE2E verifies that when
// batches contain multiple records, every record already pulled from the wire
// is yielded (across multiple Next calls that drain s.pending while pendingErr
// is set) before the fatal error surfaces.
func TestReadSessionFatalErrorDrainsMultiRecordBatchesE2E(t *testing.T) {
	const batches = 5
	const perBatch = 3
	var body bytes.Buffer
	for i := range batches {
		records := []*pb.SequencedRecord{
			{SeqNum: uint64(i*perBatch + 0), Body: []byte("a")},
			{SeqNum: uint64(i*perBatch + 1), Body: []byte("b")},
			{SeqNum: uint64(i*perBatch + 2), Body: []byte("c")},
		}
		body.Write(buildReadBatchFrame(t, records))
	}
	body.Write(internalframing.CreateFrameWithStatus(
		[]byte(`{"message":"bad read","code":"BAD_READ"}`),
		true, internalframing.CompressionNone, http.StatusBadRequest,
	))
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body.Bytes()}
	session, err := newFrameServedStreamClient(rt).ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, waitErr := session.CaughtUp().Wait(ctx)
	wantFatalBADREAD(t, waitErr)

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	const want = batches * perBatch
	if len(got) != want {
		t.Fatalf("expected all %d buffered records delivered before the fatal error, got %d: %v (Err=%v)",
			want, len(got), got, session.Err())
	}
	for i, seq := range got {
		if seq != uint64(i) {
			t.Fatalf("record %d: expected seq_num %d, got %d", i, i, seq)
		}
	}
	var sessionErr *S2Error
	if !errors.As(session.Err(), &sessionErr) || sessionErr.Code != badReadCode {
		t.Fatalf("expected Next to surface the deferred fatal error, got %v", session.Err())
	}
}

// TestReadSessionFatalErrorDropsBufferedRecords isolates the Next() branch by
// constructing a streamReader in the exact post-defer state: deliveries are
// buffered in recordsCh, the fatal error is buffered in errorCh, and both
// channels are closed. Iterating via Next() must deliver every buffered record
// and then surface the fatal error, regardless of which channel Go's
// randomized select picks first.
func TestReadSessionFatalErrorDropsBufferedRecords(t *testing.T) {
	const batches = 5
	reader := &streamReader{
		recordsCh: make(chan readDelivery, batches),
		errorCh:   make(chan error, 1),
	}
	for i := range batches {
		reader.recordsCh <- readDelivery{records: []SequencedRecord{
			{SeqNum: uint64(i), Body: []byte("r")},
		}}
	}
	fatalErr := errors.New("fatal reader error")
	reader.errorCh <- fatalErr
	close(reader.recordsCh)
	close(reader.errorCh)

	session := &ReadSession{reader: reader}
	defer session.Close()

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if len(got) != batches {
		t.Fatalf("expected all %d buffered records delivered, got %d: %v (Err=%v)",
			batches, len(got), got, session.Err())
	}
	if !errors.Is(session.Err(), fatalErr) {
		t.Fatalf("expected the deferred fatal error to be surfaced, got %v", session.Err())
	}
}
