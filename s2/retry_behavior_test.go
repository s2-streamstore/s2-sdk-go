package s2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"syscall"
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

func dialRefusedError() error {
	return &url.Error{Op: "Post", URL: "http://example.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
}

func TestWithAppendRetries_NoSideEffectsRetriesDialError(t *testing.T) {
	cfg := &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond, AppendRetryPolicy: AppendRetryPolicyNoSideEffects}

	attempts := 0
	_, err := withAppendRetries(context.Background(), cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		attempts++
		if attempts == 1 {
			return nil, dialRefusedError()
		}
		return nil, serverError(429, "rate_limited")
	})
	if attempts != 2 {
		t.Fatalf("expected the refused dial to be retried, got %d attempts", attempts)
	}
	// The refused dial sent nothing, so the final error stays definite.
	if !HasNoSideEffects(err) {
		t.Fatalf("expected a definite final error, got %v", err)
	}

	_, err = withAppendRetries(context.Background(), cfg, nil, &AppendInput{}, func() (*AppendAck, error) {
		return nil, dialRefusedError()
	})
	if !HasNoSideEffects(err) {
		t.Fatalf("expected a definite error when every dial was refused, got %v", err)
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

func TestNonIdempotentCallsRetryOnlyDefiniteErrors(t *testing.T) {
	calls := []struct {
		name string
		call func(*Client) error
	}{
		{"issue access token", func(c *Client) error {
			_, err := c.AccessTokens.Issue(context.Background(), IssueAccessTokenArgs{ID: "token-1"})
			return err
		}},
		{"create network policy", func(c *Client) error {
			_, err := c.NetworkPolicies.Create(context.Background(), CreateNetworkPolicyArgs{Slug: "policy"})
			return err
		}},
	}
	failures := []struct {
		name         string
		fail         func() (*http.Response, error)
		wantAttempts int
	}{
		{"definite rate limit is retried", func() (*http.Response, error) {
			return jsonResponse(http.StatusTooManyRequests, "", `{"code":"rate_limited","message":"slow down"}`), nil
		}, 2},
		{"indefinite unavailable is not retried", func() (*http.Response, error) {
			return jsonResponse(http.StatusServiceUnavailable, "", `{"code":"unavailable","message":"unavailable"}`), nil
		}, 1},
		{"connection reset after sending is not retried", func() (*http.Response, error) {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}, 1},
		{"refused connection is retried", func() (*http.Response, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}, 2},
	}
	for _, call := range calls {
		for _, failure := range failures {
			t.Run(call.name+"/"+failure.name, func(t *testing.T) {
				attempts := 0
				client := New("token", &ClientOptions{
					HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						attempts++
						if attempts == 1 {
							return failure.fail()
						}
						return jsonResponse(http.StatusCreated, "", `{}`), nil
					})},
					RetryConfig: &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
				})
				err := call.call(client)
				if attempts != failure.wantAttempts {
					t.Fatalf("expected %d attempts, got %d (err %v)", failure.wantAttempts, attempts, err)
				}
				if (failure.wantAttempts == 2) != (err == nil) {
					t.Fatalf("unexpected result after %d attempts: %v", attempts, err)
				}
			})
		}
	}
}

func TestIssueRetriesTLSHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			defer conn.Close() // hold the connection without ever answering the handshake
		}
	}()

	endpoint := "https://" + ln.Addr().String()
	client := New("token", &ClientOptions{
		BaseURL:           endpoint,
		MakeBasinBaseURL:  func(string) string { return endpoint },
		ConnectionTimeout: 100 * time.Millisecond,
		RetryConfig:       &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	})
	_, err = client.AccessTokens.Issue(context.Background(), IssueAccessTokenArgs{ID: "token-1"})
	if err == nil {
		t.Fatal("expected the handshake to time out")
	}
	if got := accepted.Load(); got != 2 {
		t.Fatalf("expected the handshake timeout to be retried (2 connections), got %d: %v", got, err)
	}
}
