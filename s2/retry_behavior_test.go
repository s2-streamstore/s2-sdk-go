package s2

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

type fakeNetErr struct{}

func (fakeNetErr) Error() string   { return "net failure" }
func (fakeNetErr) Timeout() bool   { return false }
func (fakeNetErr) Temporary() bool { return true }

func TestWithRetries_RetryableStatusRetries(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}

	attempts := 0
	result, err := withRetries(ctx, cfg, nil, func() (int, error) {
		attempts++
		if attempts < 3 {
			return 0, &S2Error{Status: 503}
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("expected result 42, got %d", result)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithRetries_NonRetryableStatusStops(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}

	attempts := 0
	_, err := withRetries(ctx, cfg, nil, func() (int, error) {
		attempts++
		return 0, &S2Error{Status: 400}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithRetries_MaxAttemptsRespected(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}

	attempts := 0
	_, err := withRetries(ctx, cfg, nil, func() (int, error) {
		attempts++
		return 0, &S2Error{Status: 503}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithRetries_NetworkErrorRetries(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}

	attempts := 0
	_, err := withRetries(ctx, cfg, nil, func() (int, error) {
		attempts++
		if attempts < 2 {
			return 0, fakeNetErr{}
		}
		return 7, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithAppendRetries_PreservesUncertaintyAcrossAttempts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		responses    []error
		wantWrapped  bool
		wantDefinite bool
		wantAttempts int
	}{
		{
			"indefinite then definite",
			[]error{serverError(503, "unavailable"), serverError(403, "permission_denied")},
			true, false, 2,
		},
		{
			"both indefinite",
			[]error{serverError(503, "unavailable"), serverError(500, "other"), serverError(503, "unavailable")},
			false, false, 3,
		},
		{
			"both definite",
			[]error{serverError(429, "rate_limited"), serverError(403, "permission_denied")},
			false, true, 2,
		},
		{
			"indefinite then retryable definite exhausts",
			[]error{serverError(503, "unavailable"), serverError(429, "rate_limited"), serverError(429, "rate_limited")},
			true, false, 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyAll}
			attempts := 0
			_, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
				resp := tc.responses[attempts]
				attempts++
				return nil, resp
			})
			if attempts != tc.wantAttempts {
				t.Fatalf("expected %d attempts, got %d", tc.wantAttempts, attempts)
			}
			final := tc.responses[tc.wantAttempts-1]
			var indefinite *AppendIndefiniteFailureError
			if got := errors.As(err, &indefinite); got != tc.wantWrapped {
				t.Fatalf("wrapped = %v, want %v (err: %v)", got, tc.wantWrapped, err)
			}
			if got := HasNoSideEffects(err); got != tc.wantDefinite {
				t.Fatalf("HasNoSideEffects = %v, want %v: %v", got, tc.wantDefinite, err)
			}
			var s2Err *S2Error
			if !errors.As(err, &s2Err) || s2Err != final {
				t.Fatalf("final attempt error not reachable, got %v", err)
			}
			if tc.wantWrapped && indefinite.FinalAttemptError != final {
				t.Fatalf("FinalAttemptError = %v, want %v", indefinite.FinalAttemptError, final)
			}
		})
	}
}

func TestWithAppendRetries_SuccessAfterIndefiniteFailure(t *testing.T) {
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyAll}
	attempts := 0
	ack, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		if attempts == 1 {
			return nil, serverError(503, "unavailable")
		}
		return &AppendAck{}, nil
	})
	if err != nil || ack == nil || attempts != 2 {
		t.Fatalf("expected success on second attempt, got ack=%v err=%v attempts=%d", ack, err, attempts)
	}
}

// TestWithAppendRetries_412AfterUncertainAttempt_Wrapped guards the unary
// Append retry path: an indefinite first attempt (503 unavailable, which has
// noSideEffects=false) sets priorUncertainty, then a terminal 412
// AppendConditionFailed on retry must be wrapped in
// *AppendIndefiniteFailureError because a 412 guarantees the retry wrote
// nothing while the earlier attempt may have committed.
func TestWithAppendRetries_412AfterUncertainAttempt_Wrapped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final error
	}{
		{"seq_num_mismatch", newSeqNumMismatchError(412, 7)},
		{"fencing_token_mismatch", newFencingTokenMismatchError(412, "fence")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyAll}
			attempts := 0
			responses := []error{serverError(503, "unavailable"), tc.final}
			_, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{MatchSeqNum: Uint64(0)}, func() (*AppendAck, error) {
				resp := responses[attempts]
				attempts++
				return nil, resp
			})
			if attempts != 2 {
				t.Fatalf("expected 2 attempts (retry then terminal 412), got %d", attempts)
			}
			// 412 is non-retryable: it must terminate at attempt 2, not attempt 3.
			var indef *AppendIndefiniteFailureError
			if !errors.As(err, &indef) {
				t.Fatalf("expected *AppendIndefiniteFailureError wrap, got %T: %v", err, err)
			}
			if indef.FinalAttemptError != tc.final {
				t.Fatalf("FinalAttemptError = %v, want %v", indef.FinalAttemptError, tc.final)
			}
			if HasNoSideEffects(err) {
				t.Fatalf("wrapped indefinite failure must not report HasNoSideEffects")
			}
			// The underlying typed 412 error remains reachable for diagnostics.
			var s2Err *S2Error
			if !errors.As(err, &s2Err) || s2Err.Code != "APPEND_CONDITION_FAILED" || s2Err.Status != 412 {
				t.Fatalf("underlying 412 *S2Error not reachable, got %v", err)
			}
		})
	}
}

// TestWithAppendRetries_Bare412NotWrapped asserts a single-attempt 412 (no
// prior uncertainty) classifies as HasNoSideEffects but is returned bare
// (not wrapped), matching Rust's AppendError::ConditionFailed for the
// single-attempt case.
func TestWithAppendRetries_Bare412NotWrapped(t *testing.T) {
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyAll}
	attempts := 0
	seqErr := newSeqNumMismatchError(412, 7)
	_, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{MatchSeqNum: Uint64(0)}, func() (*AppendAck, error) {
		attempts++
		return nil, seqErr
	})
	if attempts != 1 {
		t.Fatalf("expected a single attempt (412 non-retryable), got %d", attempts)
	}
	var indef *AppendIndefiniteFailureError
	if errors.As(err, &indef) {
		t.Fatalf("bare 412 with no prior uncertainty must not be wrapped, got %v", err)
	}
	if err != seqErr {
		t.Fatalf("expected bare typed error, got %v", err)
	}
	if !HasNoSideEffects(err) {
		t.Fatalf("bare 412 must be HasNoSideEffects")
	}
}

// TestWithAppendRetries_412NotRetriedUnderNoSideEffectsPolicy asserts a 412
// under AppendRetryPolicyNoSideEffects is terminal (not retried) and reaches
// the caller as a typed error. 412 is non-retryable, so it never reaches the
// no-side-effects retry gating.
func TestWithAppendRetries_412NotRetriedUnderNoSideEffectsPolicy(t *testing.T) {
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}
	attempts := 0
	seqErr := newSeqNumMismatchError(412, 7)
	_, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{MatchSeqNum: Uint64(0)}, func() (*AppendAck, error) {
		attempts++
		return nil, seqErr
	})
	if attempts != 1 {
		t.Fatalf("expected 1 attempt (412 non-retryable under NoSideEffects), got %d", attempts)
	}
	if err != seqErr {
		t.Fatalf("expected bare typed 412 (no prior uncertainty), got %v", err)
	}
	var indef *AppendIndefiniteFailureError
	if errors.As(err, &indef) {
		t.Fatalf("single-attempt 412 must not be wrapped, got %v", err)
	}
}

func serverError(status int, code string) *S2Error {
	return &S2Error{Message: code, Code: code, Status: status, Origin: "server"}
}

func TestWithAppendRetries_NoSideEffectsWithoutMatchSeqNum(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		return nil, &S2Error{Status: 503}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithAppendRetries_NoSideEffectsWithMatchSeqNumDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{MatchSeqNum: Uint64(0)}, func() (*AppendAck, error) {
		attempts++
		return nil, &S2Error{Status: 503, Origin: "server"}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithAppendRetries_NoSideEffectsRetriesNoSideEffectServerError(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		if attempts < 2 {
			return nil, &S2Error{Status: 429, Code: "rate_limited", Origin: "server"}
		}
		return &AppendAck{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithAppendRetries_NoSideEffectsRetriesTransactionConflict(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		if attempts < 2 {
			return nil, &S2Error{Status: 409, Code: "transaction_conflict", Origin: "server"}
		}
		return &AppendAck{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithAppendRetries_NoSideEffectsDoesNotRetryUnavailable(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		return nil, &S2Error{Status: 503, Code: "unavailable", Origin: "server"}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithAppendRetries_NoSideEffectsNetworkError(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		return nil, fakeNetErr{}
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithRetries_ContextCancellationStopsRetryLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(50*time.Millisecond, cancel)

	cfg := &RetryConfig{MaxAttempts: 5, MinBaseDelay: 200 * time.Millisecond, MaxBaseDelay: 200 * time.Millisecond}

	attempts := 0
	start := time.Now()
	_, err := withRetries(ctx, cfg, nil, func() (int, error) {
		attempts++
		return 0, &S2Error{Status: 503, Message: "Service Unavailable"}
	})
	duration := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
	if duration > 350*time.Millisecond {
		t.Fatalf("expected retry loop to stop quickly after cancellation, took %v", duration)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt before cancellation, got %d", attempts)
	}
}

func TestWithAppendRetries_ContextCancellationStopsRetryLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(50*time.Millisecond, cancel)

	cfg := &RetryConfig{
		MaxAttempts:       5,
		MinBaseDelay:      200 * time.Millisecond,
		MaxBaseDelay:      200 * time.Millisecond,
		AppendRetryPolicy: AppendRetryPolicyAll,
	}

	attempts := 0
	start := time.Now()
	_, err := withAppendRetries(ctx, cfg, nil, &AppendInput{MatchSeqNum: Uint64(0)}, func() (*AppendAck, error) {
		attempts++
		return nil, &S2Error{Status: 503, Message: "Service Unavailable"}
	})
	duration := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
	if duration > 350*time.Millisecond {
		t.Fatalf("expected append retry loop to stop quickly after cancellation, took %v", duration)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 append attempt before cancellation, got %d", attempts)
	}
}

func TestWithRetries_NonS2ErrorPropagates(t *testing.T) {
	ctx := context.Background()
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}

	sentinel := errors.New("boom")
	_, err := withRetries(ctx, cfg, nil, func() (int, error) {
		return 0, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestIsNetworkError_NetError(t *testing.T) {
	if !isNetworkError(context.Background(), fakeNetErr{}) {
		t.Fatalf("expected fakeNetErr to be treated as network error")
	}
	opErr := &net.OpError{Err: fakeNetErr{}}
	if !isNetworkError(context.Background(), opErr) {
		t.Fatalf("expected opErr to be treated as network error")
	}
	urlErr := &url.Error{Err: fakeNetErr{}}
	if !isNetworkError(context.Background(), urlErr) {
		t.Fatalf("expected urlErr to be treated as network error")
	}
}
