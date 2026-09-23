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

func TestWithPriorUncertainty(t *testing.T) {
	definite := &S2Error{Code: "permission_denied", Status: 403, Origin: "server"}
	indefinite := &S2Error{Code: "unavailable", Status: 503, Origin: "server"}

	if got := withPriorUncertainty(definite, false); got != definite {
		t.Fatalf("no prior uncertainty should return error unchanged, got %v", got)
	}
	if got := withPriorUncertainty(indefinite, true); got != indefinite {
		t.Fatalf("indefinite error should not be wrapped, got %v", got)
	}

	wrapped := withPriorUncertainty(definite, true)
	var indef *AppendIndefiniteFailureError
	if !errors.As(wrapped, &indef) || indef.FinalAttemptError != definite {
		t.Fatalf("expected wrapper around final error, got %v", wrapped)
	}
	if HasNoSideEffects(wrapped) || !HasNoSideEffects(definite) || HasNoSideEffects(indefinite) {
		t.Fatal("HasNoSideEffects classification wrong")
	}
	var s2Err *S2Error
	if !errors.As(wrapped, &s2Err) || s2Err != definite {
		t.Fatalf("final attempt error not reachable through wrapper: %v", wrapped)
	}
	if got := withPriorUncertainty(wrapped, true); got != wrapped {
		t.Fatal("wrapper must not be wrapped twice")
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

func TestS2Error_HasNoSideEffects(t *testing.T) {
	tests := []struct {
		name string
		err  *S2Error
		want bool
	}{
		{
			name: "server rate_limited",
			err:  &S2Error{Status: 429, Code: "rate_limited", Origin: "server"},
			want: true,
		},
		{
			name: "server hot_server",
			err:  &S2Error{Status: 502, Code: "hot_server", Origin: "server"},
			want: true,
		},
		{
			name: "server transaction_conflict",
			err:  &S2Error{Status: 409, Code: "transaction_conflict", Origin: "server"},
			want: true,
		},
		{
			name: "server server_draining",
			err:  &S2Error{Status: 503, Code: "server_draining", Origin: "server"},
			want: true,
		},
		{
			name: "server unavailable",
			err:  &S2Error{Status: 503, Code: "unavailable", Origin: "server"},
			want: false,
		},
		{
			name: "server rate_limited status mismatch",
			err:  &S2Error{Status: 500, Code: "rate_limited", Origin: "server"},
			want: false,
		},
		{
			name: "unknown server code",
			err:  &S2Error{Status: 429, Code: "unknown", Origin: "server"},
			want: false,
		},
		{
			name: "non-server origin",
			err:  &S2Error{Status: 429, Code: "rate_limited", Origin: "network"},
			want: false,
		},
		{
			name: "generic retryable server error",
			err:  &S2Error{Status: 503, Origin: "server"},
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.HasNoSideEffects(); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestS2Error_IsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *S2Error
		want bool
	}{
		{
			name: "server transaction_conflict",
			err:  &S2Error{Status: 409, Code: "transaction_conflict", Origin: "server"},
			want: true,
		},
		{
			name: "server resource_already_exists",
			err:  &S2Error{Status: 409, Code: "resource_already_exists", Origin: "server"},
			want: false,
		},
		{
			name: "server unknown code non-retryable status",
			err:  &S2Error{Status: 409, Code: "unknown", Origin: "server"},
			want: false,
		},
		{
			name: "server unknown code retryable status",
			err:  &S2Error{Status: 503, Code: "unknown", Origin: "server"},
			want: true,
		},
		{
			name: "server empty code retryable status",
			err:  &S2Error{Status: 500, Origin: "server"},
			want: true,
		},
		{
			name: "server invalid status mismatch",
			err:  &S2Error{Status: 500, Code: "invalid", Origin: "server"},
			want: false,
		},
		{
			name: "server other",
			err:  &S2Error{Status: 500, Code: "other", Origin: "server"},
			want: true,
		},
		{
			name: "network stream reset",
			err:  &S2Error{Status: 502, Code: "STREAM_RESET", Origin: "network"},
			want: true,
		},
		{
			name: "status zero",
			err:  &S2Error{Status: 0, Origin: "server"},
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsRetryable(); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}
