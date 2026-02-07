package s2

import (
	"errors"
	"net/http"
	"testing"
)

func TestDecodeAPIError_WithMessageAndCode(t *testing.T) {
	err := decodeAPIError(400, []byte(`{"code":"bad_request","message":"nope"}`))

	var s2Err *S2Error
	if !errors.As(err, &s2Err) {
		t.Fatalf("expected S2Error, got %T", err)
	}
	if s2Err.Status != 400 {
		t.Fatalf("expected status 400, got %d", s2Err.Status)
	}
	if s2Err.Code != "bad_request" {
		t.Fatalf("expected code bad_request, got %q", s2Err.Code)
	}
	if s2Err.Message != "nope" {
		t.Fatalf("expected message nope, got %q", s2Err.Message)
	}
}

func TestDecodeAPIError_FallbackStatusText(t *testing.T) {
	err := decodeAPIError(http.StatusInternalServerError, nil)

	var s2Err *S2Error
	if !errors.As(err, &s2Err) {
		t.Fatalf("expected S2Error, got %T", err)
	}
	if s2Err.Message != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("expected fallback message %q, got %q", http.StatusText(http.StatusInternalServerError), s2Err.Message)
	}
}

func TestDecodeAPIError_RangeNotSatisfiable_WithTail(t *testing.T) {
	body := []byte(`{"tail":{"seq_num":42,"timestamp":1234}}`)
	err := decodeAPIError(http.StatusRequestedRangeNotSatisfiable, body)

	var rangeErr *RangeNotSatisfiableError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("expected RangeNotSatisfiableError, got %T", err)
	}
	if rangeErr.Status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected status 416, got %d", rangeErr.Status)
	}
	if rangeErr.Tail == nil {
		t.Fatal("expected Tail to be set")
	}
	if rangeErr.Tail.SeqNum != 42 {
		t.Fatalf("expected tail seq_num 42, got %d", rangeErr.Tail.SeqNum)
	}
	if rangeErr.Tail.Timestamp != 1234 {
		t.Fatalf("expected tail timestamp 1234, got %d", rangeErr.Tail.Timestamp)
	}
}

func TestDecodeAPIError_RangeNotSatisfiable_PlainBody(t *testing.T) {
	err := decodeAPIError(http.StatusRequestedRangeNotSatisfiable, []byte("custom range error"))

	var rangeErr *RangeNotSatisfiableError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("expected RangeNotSatisfiableError, got %T", err)
	}
	if rangeErr.Status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected status 416, got %d", rangeErr.Status)
	}
	if rangeErr.Message != "custom range error" {
		t.Fatalf("expected message %q, got %q", "custom range error", rangeErr.Message)
	}
	if rangeErr.Tail != nil {
		t.Fatal("expected Tail to be nil for plain body")
	}
}

func TestDecodeAPIError_RangeNotSatisfiable_EmptyBody(t *testing.T) {
	err := decodeAPIError(http.StatusRequestedRangeNotSatisfiable, nil)

	var rangeErr *RangeNotSatisfiableError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("expected RangeNotSatisfiableError, got %T", err)
	}
	if rangeErr.Tail != nil {
		t.Fatal("expected Tail to be nil for empty body")
	}
}
