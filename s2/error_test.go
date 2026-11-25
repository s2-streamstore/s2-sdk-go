package s2

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func TestDecodeAPIError_RangeNotSatisfiableWithoutTailIsPlain(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantCode    string
		wantMessage string
	}{
		{name: "plain body", body: "custom range error", wantMessage: "custom range error"},
		{name: "empty body", body: "", wantMessage: "416"},
		{name: "partial tail", body: `{"tail":{"seq_num":1}}`, wantMessage: `{"tail":{"seq_num":1}}`},
		{name: "api error", body: `{"code":"x","message":"y"}`, wantCode: "x", wantMessage: "y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := decodeAPIError(http.StatusRequestedRangeNotSatisfiable, []byte(tt.body))

			var rangeErr *RangeNotSatisfiableError
			if errors.As(err, &rangeErr) {
				t.Fatalf("expected plain S2Error, got RangeNotSatisfiableError with tail %v", rangeErr.Tail)
			}
			var s2Err *S2Error
			if !errors.As(err, &s2Err) {
				t.Fatalf("expected S2Error, got %T", err)
			}
			if s2Err.Status != http.StatusRequestedRangeNotSatisfiable || s2Err.Code != tt.wantCode || s2Err.Message != tt.wantMessage {
				t.Fatalf("expected status 416, code %q, message %q; got %d, %q, %q",
					tt.wantCode, tt.wantMessage, s2Err.Status, s2Err.Code, s2Err.Message)
			}
		})
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
			name: "server invalid status mismatch falls back to status retryability",
			err:  &S2Error{Status: 500, Code: "invalid", Origin: "server"},
			want: true,
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

func TestErrorCodesMatchSpec(t *testing.T) {
	specPath := filepath.Join("..", "s2-specs", "s2", "v1", "error-codes.json")
	raw, err := os.ReadFile(specPath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("s2-specs submodule not checked out")
	}
	if err != nil {
		t.Fatalf("read error-codes.json: %v", err)
	}

	var spec struct {
		Codes []struct {
			Code        string `json:"code"`
			Status      int    `json:"status"`
			Retryable   bool   `json:"retryable"`
			SideEffects string `json:"side_effects"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unmarshal error-codes.json: %v", err)
	}

	seen := make(map[string]bool, len(spec.Codes))
	for _, want := range spec.Codes {
		seen[want.Code] = true
		wantNoSideEffects := want.SideEffects == "none"
		got, ok := errorCodes[want.Code]
		if !ok {
			t.Errorf("missing error code %q from s2-specs/s2/v1/error-codes.json", want.Code)
			continue
		}
		if got.status != want.Status || got.retryable != want.Retryable || got.noSideEffects != wantNoSideEffects {
			t.Errorf("code %q: got %+v, want status=%d retryable=%v noSideEffects=%v",
				want.Code, got, want.Status, want.Retryable, wantNoSideEffects)
		}
	}
	for code := range errorCodes {
		if !seen[code] {
			t.Errorf("extra error code %q not present in s2-specs/s2/v1/error-codes.json", code)
		}
	}
}

func TestErrorVectors(t *testing.T) {
	vectorPath := filepath.Join("..", "s2-specs", "sdk", "vectors", "errors.json")
	raw, err := os.ReadFile(vectorPath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("s2-specs submodule not checked out")
	}
	if err != nil {
		t.Fatalf("read error vectors: %v", err)
	}

	type errorExpect struct {
		Type                string `json:"type"`
		Status              int    `json:"status"`
		Code                string `json:"code"`
		Message             string `json:"message"`
		IsRetryable         bool   `json:"is_retryable"`
		HasNoSideEffects    bool   `json:"has_no_side_effects"`
		AssignableSeqNum    string `json:"assignable_seq_num"`
		CurrentFencingToken string `json:"current_fencing_token"`
		Tail                *struct {
			SeqNum    string `json:"seq_num"`
			Timestamp string `json:"timestamp"`
		} `json:"tail"`
		IfRepresentable *errorExpect `json:"if_representable"`
	}
	var doc struct {
		Cases []struct {
			Rule   string      `json:"rule"`
			Name   string      `json:"name"`
			Status int         `json:"status"`
			Body   string      `json:"body"`
			Expect errorExpect `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal error vectors: %v", err)
	}

	for _, tc := range doc.Cases {
		t.Run(tc.Rule+"/"+tc.Name, func(t *testing.T) {
			want := tc.Expect
			if want.IfRepresentable != nil {
				want = *want.IfRepresentable
			}
			gotErr := decodeAPIError(tc.Status, []byte(tc.Body))
			var s2Err *S2Error
			if !errors.As(gotErr, &s2Err) {
				t.Fatalf("expected *S2Error, got %T", gotErr)
			}
			if s2Err.Status != want.Status {
				t.Errorf("status=%d, want %d", s2Err.Status, want.Status)
			}
			if s2Err.IsRetryable() != want.IsRetryable {
				t.Errorf("IsRetryable()=%v, want %v", s2Err.IsRetryable(), want.IsRetryable)
			}
			if HasNoSideEffects(gotErr) != want.HasNoSideEffects {
				t.Errorf("HasNoSideEffects()=%v, want %v", HasNoSideEffects(gotErr), want.HasNoSideEffects)
			}
			switch want.Type {
			case "SeqNumMismatchError":
				var seqErr *SeqNumMismatchError
				if !errors.As(gotErr, &seqErr) {
					t.Fatalf("expected *SeqNumMismatchError, got %T", gotErr)
				}
				wantSeq, _ := strconv.ParseUint(want.AssignableSeqNum, 10, 64)
				if seqErr.ExpectedSeqNum != wantSeq {
					t.Errorf("ExpectedSeqNum=%d, want %d", seqErr.ExpectedSeqNum, wantSeq)
				}
			case "FencingTokenMismatchError":
				var fenceErr *FencingTokenMismatchError
				if !errors.As(gotErr, &fenceErr) {
					t.Fatalf("expected *FencingTokenMismatchError, got %T", gotErr)
				}
				if fenceErr.ExpectedFencingToken != want.CurrentFencingToken {
					t.Errorf("ExpectedFencingToken=%q, want %q", fenceErr.ExpectedFencingToken, want.CurrentFencingToken)
				}
			case "ReadUnwrittenError":
				var rangeErr *RangeNotSatisfiableError
				if !errors.As(gotErr, &rangeErr) || rangeErr.Tail == nil {
					t.Fatalf("expected *RangeNotSatisfiableError with Tail, got %T", gotErr)
				}
				wantSeq, _ := strconv.ParseUint(want.Tail.SeqNum, 10, 64)
				wantTS, _ := strconv.ParseUint(want.Tail.Timestamp, 10, 64)
				if rangeErr.Tail.SeqNum != wantSeq || rangeErr.Tail.Timestamp != wantTS {
					t.Errorf("Tail=%+v, want seq=%d ts=%d", rangeErr.Tail, wantSeq, wantTS)
				}
			default:
				if s2Err.Code != want.Code {
					t.Errorf("code=%q, want %q", s2Err.Code, want.Code)
				}
				if s2Err.Message != want.Message {
					t.Errorf("message=%q, want %q", s2Err.Message, want.Message)
				}
			}
		})
	}
}

func TestDecodeAPIError_InvalidUTF8BodyIsPlain(t *testing.T) {
	err := decodeAPIError(http.StatusPreconditionFailed, []byte("{\"fencing_token_mismatch\":\"\xff\"}"))

	var fenceErr *FencingTokenMismatchError
	if errors.As(err, &fenceErr) {
		t.Fatalf("expected a plain error, got FencingTokenMismatchError with token %q", fenceErr.ExpectedFencingToken)
	}
	var s2Err *S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 412 || s2Err.Message != "412" {
		t.Fatalf("expected a plain 412 with message \"412\", got %v", err)
	}
}
