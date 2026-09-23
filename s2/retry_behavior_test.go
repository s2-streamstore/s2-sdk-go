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
