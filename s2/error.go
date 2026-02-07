package s2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func newValidationError(message string) *S2Error {
	return &S2Error{
		Message: message,
		Code:    "VALIDATION",
		Origin:  "sdk",
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

	// Retryable HTTP status codes
	switch e.Status {
	case 408: // Request Timeout
		return true
	case 429: // Too Many Requests
		return true
	case 500, 502, 503: // Server Errors
		return true
	case 504: // Gateway Timeout
		return true
	default:
		return false
	}
}

func (e *S2Error) IsNetworkError() bool {
	return e != nil && e.Origin == "network"
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
}

func newRangeNotSatisfiableError(status int, message string) *RangeNotSatisfiableError {
	return &RangeNotSatisfiableError{
		S2Error: &S2Error{
			Message: message,
			Status:  status,
			Code:    "RANGE_NOT_SATISFIABLE",
			Origin:  "server",
		},
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

	if status == http.StatusPreconditionFailed {
		if len(trimmed) > 0 {
			var jsonMap map[string]interface{}
			if json.Unmarshal(trimmed, &jsonMap) == nil {
				return makeAppendPreconditionError(status, jsonMap)
			}
		}
		return &S2Error{
			Message: fmt.Sprintf("HTTP %d", status),
			Status:  status,
			Origin:  "server",
		}
	}

	if status == http.StatusRequestedRangeNotSatisfiable {
		message := http.StatusText(status)
		if len(trimmed) > 0 {
			message = string(trimmed)
		}
		return newRangeNotSatisfiableError(status, message)
	}

	if len(trimmed) > 0 {
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
		Message: http.StatusText(status),
		Status:  status,
		Origin:  "server",
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

func makeAppendPreconditionError(status int, jsonMap map[string]interface{}) error {
	if seqNumMismatch, exists := jsonMap["seq_num_mismatch"]; exists {
		if expectedSeqNum, ok := seqNumMismatch.(float64); ok {
			return newSeqNumMismatchError(status, uint64(expectedSeqNum))
		}
	}

	if fencingMismatch, exists := jsonMap["fencing_token_mismatch"]; exists {
		if expectedToken, ok := fencingMismatch.(string); ok {
			return newFencingTokenMismatchError(status, expectedToken)
		}
	}
	message := "Append condition failed"
	if msg, exists := jsonMap["message"]; exists {
		if msgStr, ok := msg.(string); ok {
			message = msgStr
		}
	}

	return &S2Error{
		Message: message,
		Status:  status,
		Origin:  "server",
	}
}
