package s2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"
)

// Sentinel errors for SDK-originated errors.
// Use errors.Is() to check for these conditions.
var (
	// ErrSessionClosed is returned when an operation is attempted on a closed session.
	ErrSessionClosed = errors.New("session closed")

	// ErrTimeout is returned when an operation times out.
	ErrTimeout = errors.New("operation timed out")

	// ErrMaxAttemptsExhausted is returned when all retry attempts have been exhausted.
	ErrMaxAttemptsExhausted = errors.New("max attempts exhausted")
)

type S2Error struct {
	Message string
	Code    string
	Status  int
	Origin  string // "server", "sdk", "network"
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorCodeInfo struct {
	status        int
	retryable     bool
	noSideEffects bool
}

// Canonical server error codes; mirrors s2-specs/s2/v1/error-codes.json.
var errorCodes = map[string]errorCodeInfo{
	"access_token_not_found":   {404, false, true},
	"authn":                    {401, false, true},
	"bad_frame":                {400, false, true},
	"bad_header":               {400, false, true},
	"bad_json":                 {400, false, true},
	"bad_path":                 {400, false, true},
	"bad_proto":                {400, false, true},
	"bad_query":                {400, false, true},
	"basin_deletion_pending":   {409, false, true},
	"basin_not_found":          {404, false, true},
	"client_hangup":            {499, false, false},
	"decryption_failed":        {400, false, true},
	"hot_server":               {502, true, true},
	"invalid":                  {422, false, true},
	"network_policy_denied":    {403, false, true},
	"network_policy_in_use":    {409, false, true},
	"network_policy_not_found": {404, false, true},
	"not_implemented":          {501, false, true},
	"other":                    {500, true, false},
	"permission_denied":        {403, false, true},
	"quota_exhausted":          {403, false, true},
	"rate_limited":             {429, true, true},
	"request_timeout":          {408, true, false},
	"resource_already_exists":  {409, false, true},
	"server_draining":          {503, true, true},
	"storage":                  {500, true, false},
	"stream_deletion_pending":  {409, false, true},
	"stream_not_found":         {404, false, true},
	"transaction_conflict":     {409, true, true},
	"unavailable":              {503, true, false},
	"upstream_timeout":         {504, true, false},
}

func isRetryableStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

const (
	validationErrorCode = "VALIDATION"
	protocolErrorCode   = "PROTOCOL"
	errorOriginSDK      = "sdk"
)

func newValidationError(message string) *S2Error {
	return &S2Error{
		Message: message,
		Code:    validationErrorCode,
		Origin:  errorOriginSDK,
	}
}

func (e *S2Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("S2 API error %s: %s (HTTP %d)", e.Code, e.Message, e.Status)
	}
	return fmt.Sprintf("S2 API error: %s (HTTP %d)", e.Message, e.Status)
}

func (e *S2Error) IsRetryable() bool {
	if e == nil {
		return false
	}

	if e.Status == 0 {
		return false
	}

	if e.Origin == "server" {
		if info, ok := errorCodes[e.Code]; ok && info.status == e.Status {
			return info.retryable
		}
	}

	return isRetryableStatus(e.Status)
}

// HasNoSideEffects reports whether this error guarantees no mutation occurred.
func (e *S2Error) HasNoSideEffects() bool {
	if e == nil {
		return false
	}

	// SDK validation errors are raised before any request is sent (spec E3).
	if e.Origin == errorOriginSDK && e.Code == validationErrorCode && e.Status == 0 {
		return true
	}

	if e.Origin != "server" {
		return false
	}

	// Every 412 (append precondition failed) and 416 (read unwritten) response
	// guarantees no mutation occurred (spec E4e, E2).
	if e.Status == http.StatusPreconditionFailed || e.Status == http.StatusRequestedRangeNotSatisfiable {
		return true
	}

	info, ok := errorCodes[e.Code]
	return ok && info.status == e.Status && info.noSideEffects
}

func (e *S2Error) IsNetworkError() bool {
	return e != nil && e.Origin == "network"
}

// AppendIndefiniteFailureError is returned when the final append attempt failed
// definitively, but an earlier attempt may have taken effect, so the entire
// append operation is indefinite.
//
// FinalAttemptError describes why retries stopped. It is exposed via Unwrap,
// so errors.As can still reach the underlying *S2Error for diagnostics; use
// HasNoSideEffects on the outer error to classify the operation as a whole.
type AppendIndefiniteFailureError struct {
	FinalAttemptError error
}

func (e *AppendIndefiniteFailureError) Error() string {
	return fmt.Sprintf("append may have taken effect in an earlier attempt; final attempt failed: %v", e.FinalAttemptError)
}

func (e *AppendIndefiniteFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.FinalAttemptError
}

// HasNoSideEffects reports whether err guarantees that no append took effect
// across every attempt of the operation, not just the final one.
func HasNoSideEffects(err error) bool {
	var indefinite *AppendIndefiniteFailureError
	if errors.As(err, &indefinite) {
		return false
	}
	if errors.Is(err, ErrSessionClosed) || isConnectError(err) {
		return true
	}
	var s2Err *S2Error
	return errors.As(err, &s2Err) && s2Err.HasNoSideEffects()
}

// withPriorUncertainty wraps a definite final error when an earlier attempt may
// have taken effect. Already indefinite errors are returned unchanged.
func withPriorUncertainty(err error, priorUncertainty bool) error {
	if err == nil || !priorUncertainty || !HasNoSideEffects(err) {
		return err
	}
	return &AppendIndefiniteFailureError{FinalAttemptError: err}
}

type SeqNumMismatchError struct {
	*S2Error
	ExpectedSeqNum uint64
}

func (e *SeqNumMismatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.S2Error
}

func newSeqNumMismatchError(status int, expectedSeqNum uint64) *SeqNumMismatchError {
	return &SeqNumMismatchError{
		S2Error: &S2Error{
			Message: "Append condition failed: sequence number mismatch",
			Code:    "APPEND_CONDITION_FAILED",
			Status:  status,
			Origin:  "server",
		},
		ExpectedSeqNum: expectedSeqNum,
	}
}

type FencingTokenMismatchError struct {
	*S2Error
	ExpectedFencingToken string
}

func newFencingTokenMismatchError(status int, expectedToken string) *FencingTokenMismatchError {
	return &FencingTokenMismatchError{
		S2Error: &S2Error{
			Message: "Append condition failed: fencing token mismatch",
			Code:    "APPEND_CONDITION_FAILED",
			Status:  status,
			Origin:  "server",
		},
		ExpectedFencingToken: expectedToken,
	}
}

func (e *FencingTokenMismatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.S2Error
}

type RangeNotSatisfiableError struct {
	*S2Error
	// The current tail position of the stream, if available.
	Tail *StreamPosition
}

func newRangeNotSatisfiableError(status int, tail *StreamPosition) *RangeNotSatisfiableError {
	return &RangeNotSatisfiableError{
		S2Error: &S2Error{
			Message: http.StatusText(status),
			Status:  status,
			Code:    "RANGE_NOT_SATISFIABLE",
			Origin:  "server",
		},
		Tail: tail,
	}
}

func (e *RangeNotSatisfiableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.S2Error
}

func decodeAPIError(status int, body []byte) error {
	trimmed := bytes.TrimSpace(body)

	if status == http.StatusPreconditionFailed && len(trimmed) > 0 {
		var payload struct {
			SeqNumMismatch       *uint64 `json:"seq_num_mismatch"`
			FencingTokenMismatch *string `json:"fencing_token_mismatch"`
		}
		if json.Unmarshal(trimmed, &payload) == nil {
			if payload.SeqNumMismatch != nil {
				return newSeqNumMismatchError(status, *payload.SeqNumMismatch)
			}
			if payload.FencingTokenMismatch != nil {
				return newFencingTokenMismatchError(status, *payload.FencingTokenMismatch)
			}
		}
	}

	// A 416 without a tail is a plain server error: callers rely on Tail
	// to reposition, so it must never be fabricated.
	if status == http.StatusRequestedRangeNotSatisfiable && len(trimmed) > 0 {
		var payload struct {
			Tail *struct {
				SeqNum    *uint64 `json:"seq_num"`
				Timestamp *uint64 `json:"timestamp"`
			} `json:"tail"`
		}
		if json.Unmarshal(trimmed, &payload) == nil && payload.Tail != nil && payload.Tail.SeqNum != nil && payload.Tail.Timestamp != nil {
			return newRangeNotSatisfiableError(status, &StreamPosition{
				SeqNum:    *payload.Tail.SeqNum,
				Timestamp: *payload.Tail.Timestamp,
			})
		}
	}

	if len(trimmed) > 0 && utf8.Valid(trimmed) {
		var apiErr ErrorInfo
		if json.Unmarshal(trimmed, &apiErr) == nil && apiErr.Message != "" {
			return &S2Error{
				Message: apiErr.Message,
				Code:    apiErr.Code,
				Status:  status,
				Origin:  "server",
			}
		}
		return &S2Error{
			Message: string(trimmed),
			Status:  status,
			Origin:  "server",
		}
	}

	return &S2Error{
		Message: statusFallbackMessage(status),
		Status:  status,
		Origin:  "server",
	}
}

func statusFallbackMessage(status int) string {
	if text := http.StatusText(status); text != "" {
		return text
	}
	return strconv.Itoa(status)
}

// newBodyReadError reports a failure to read an HTTP error-response body. The
// HTTP status is preserved, but Origin is "network" because the body read (not
// the server) is what failed; any partial body is included for diagnostics.
// Without this, a body read that fails mid-stream would feed partial bytes to
// decodeAPIError and surface as a misleading server error.
func newBodyReadError(status int, partial []byte, err error) *S2Error {
	message := fmt.Sprintf("failed to read error response body: %s", err)
	if trimmed := bytes.TrimSpace(partial); len(trimmed) > 0 {
		message = fmt.Sprintf("%s (partial body: %q)", message, trimmed)
	}
	return &S2Error{
		Message: message,
		Status:  status,
		Origin:  "network",
	}
}

func makeStreamResetError(err error, context string) error {
	var message string
	if err != nil {
		message = fmt.Sprintf("Stream reset during %s: %s", context, err.Error())
	} else {
		message = fmt.Sprintf("Stream reset during %s", context)
	}

	return &S2Error{
		Message: message,
		Code:    "STREAM_RESET",
		Status:  502,
		Origin:  "network",
	}
}

// makeConnectionLostError reports a response that broke or ended before it was
// complete; it is retryable like any other network failure.
func makeConnectionLostError(err error, context string) *S2Error {
	message := fmt.Sprintf("Connection lost during %s", context)
	if err != nil {
		message = fmt.Sprintf("%s: %s", message, err.Error())
	}

	return &S2Error{
		Message: message,
		Code:    "CONNECTION_LOST",
		Status:  502,
		Origin:  "network",
	}
}

// makeProtocolError reports a response or session message that violates the
// protocol. Retrying would not help, and the request may have taken effect.
func makeProtocolError(err error, context string) *S2Error {
	return &S2Error{
		Message: fmt.Sprintf("Protocol error during %s: %s", context, err.Error()),
		Code:    protocolErrorCode,
		Origin:  errorOriginSDK,
	}
}
