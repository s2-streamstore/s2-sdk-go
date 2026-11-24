package s2

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"net/url"
	"strings"
	"time"
)

// Retry configuration.
type RetryConfig struct {
	// Total number of attempts, including the initial try. Must be >= 1. A value of 1 means no retries.
	MaxAttempts int
	// Minimum base delay for exponential backoff. Defaults to 100ms.
	MinBaseDelay time.Duration
	// Maximum base delay for exponential backoff. Defaults to 1s.
	// Note: actual delay with jitter can be up to 2x this value.
	MaxBaseDelay time.Duration
	// Policy for retrying append operations.
	AppendRetryPolicy AppendRetryPolicy
}

const (
	defaultMaxAttempts  = 3
	defaultMinBaseDelay = 100 * time.Millisecond
	defaultMaxBaseDelay = 1 * time.Second
)

// Default retry configuration.
// It retries up to 3 times with exponential backoff between 100ms and 1s.
var DefaultRetryConfig = &RetryConfig{
	MaxAttempts:       defaultMaxAttempts,
	MinBaseDelay:      defaultMinBaseDelay,
	MaxBaseDelay:      defaultMaxBaseDelay,
	AppendRetryPolicy: AppendRetryPolicyAll,
}

func withRetries[T any](ctx context.Context, config *RetryConfig, logger *slog.Logger, operation func() (T, error)) (T, error) {
	return retryOperation(ctx, config, logger, nil, operation)
}

// withDefiniteRetries is for non-idempotent calls without a request token,
// such as issuing an access token: an attempt that may have taken effect is
// never repeated, or a lost success would come back as a conflict (spec T2d).
func withDefiniteRetries[T any](ctx context.Context, config *RetryConfig, logger *slog.Logger, operation func() (T, error)) (T, error) {
	return retryOperation(ctx, config, logger, HasNoSideEffects, operation)
}

// retryOperation retries operation per config; a non-nil safeToRepeat must
// also approve each error before it is retried.
func retryOperation[T any](ctx context.Context, config *RetryConfig, logger *slog.Logger, safeToRepeat func(error) bool, operation func() (T, error)) (T, error) {
	var zero T

	if config == nil {
		config = DefaultRetryConfig
	}

	maxAttempts := config.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastError error

	for attemptNo := 1; attemptNo <= maxAttempts; attemptNo++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}

		result, err := operation()
		if err == nil {
			return result, nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}

		lastError = err

		if safeToRepeat != nil && !safeToRepeat(err) {
			return zero, err
		}

		if isNetworkError(ctx, err) {
			if attemptNo < maxAttempts {
				delay := calculateRetryBackoff(config, attemptNo)
				logInfo(logger, "s2 retrying after network error",
					"attempt", attemptNo,
					"max_attempts", maxAttempts,
					"delay", delay,
					"error", err)
				if err := waitForRetryBackoff(ctx, delay); err != nil {
					return zero, err
				}

				continue
			}
			logError(logger, "s2 network error, max attempts exhausted",
				"attempts", maxAttempts,
				"error", err)
			return zero, err
		}

		var s2Err *S2Error
		if !errors.As(err, &s2Err) {
			return zero, err
		}

		if attemptNo == maxAttempts {
			break
		}

		if !s2Err.IsRetryable() {
			return zero, s2Err
		}

		delay := calculateRetryBackoff(config, attemptNo)
		logInfo(logger, "s2 retrying after retryable error",
			"attempt", attemptNo,
			"max_attempts", maxAttempts,
			"delay", delay,
			"status", s2Err.Status,
			"code", s2Err.Code,
			"error", s2Err.Message)
		if err := waitForRetryBackoff(ctx, delay); err != nil {
			return zero, err
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return zero, ctxErr
	}

	return zero, lastError
}

func waitForRetryBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func calculateRetryBackoff(config *RetryConfig, attempt int) time.Duration {
	// guard against non-positive attempt to prevent negative bit shift panic.
	if attempt < 1 {
		return 0
	}

	minBaseDelay := config.MinBaseDelay
	if minBaseDelay <= 0 {
		minBaseDelay = defaultMinBaseDelay
	}
	maxBaseDelay := config.MaxBaseDelay
	if maxBaseDelay <= 0 {
		maxBaseDelay = defaultMaxBaseDelay
	}

	// baseDelay = min((minBaseDelay * 2^(n-1)), maxBaseDelay) without int64 shift overflow.
	baseDelay := minBaseDelay
	for range attempt - 1 {
		if baseDelay >= maxBaseDelay || baseDelay > maxBaseDelay/2 {
			baseDelay = maxBaseDelay
			break
		}
		baseDelay *= 2
	}
	if baseDelay > maxBaseDelay {
		baseDelay = maxBaseDelay
	}

	// jitter = random(0, baseDelay)
	jitter := time.Duration(rand.Int63n(int64(baseDelay)))

	// delay = baseDelay + jitter
	return baseDelay + jitter
}

func isNetworkError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}

	if ctx.Err() != nil {
		return false
	}

	// A response error raised inside RoundTrip (see userAgentRoundTripper)
	// arrives wrapped in *url.Error, which is itself a net.Error.
	var s2Err *S2Error
	if errors.As(err, &s2Err) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isNetworkError(ctx, urlErr.Err)
	}

	return false
}

func withAppendRetries(ctx context.Context, config *RetryConfig, logger *slog.Logger, _ *AppendInput, operation func() (*AppendAck, error)) (*AppendAck, error) {
	if config == nil {
		config = DefaultRetryConfig
	}

	maxAttempts := config.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastError error
	priorUncertainty := false

	for attemptNo := 1; attemptNo <= maxAttempts; attemptNo++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		result, err := operation()
		if err == nil {
			return result, nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		lastError = err

		if isNetworkError(ctx, err) {
			sentNothing := isConnectError(err)
			if attemptNo == maxAttempts || (!sentNothing && !shouldRetryNetworkError(config)) {
				logError(logger, "s2 append network error, max attempts exhausted or not retryable",
					"attempts", maxAttempts,
					"error", err)

				break
			}
			if !sentNothing {
				priorUncertainty = true
			}
			delay := calculateRetryBackoff(config, attemptNo)
			logInfo(logger, "s2 append retrying after network error",
				"attempt", attemptNo,
				"max_attempts", maxAttempts,
				"delay", delay,
				"error", err)
			if err := waitForRetryBackoff(ctx, delay); err != nil {
				return nil, err
			}

			continue
		}

		if !shouldRetryError(err, config) || attemptNo == maxAttempts {
			return nil, withPriorUncertainty(err, priorUncertainty)
		}

		if !HasNoSideEffects(err) {
			priorUncertainty = true
		}
		delay := calculateRetryBackoff(config, attemptNo)
		logInfo(logger, "s2 append retrying after error",
			"attempt", attemptNo,
			"max_attempts", maxAttempts,
			"delay", delay,
			"error", err)
		if err := waitForRetryBackoff(ctx, delay); err != nil {
			return nil, err
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	return nil, withPriorUncertainty(lastError, priorUncertainty)
}

func shouldRetryNetworkError(config *RetryConfig) bool {
	return !config.AppendRetryPolicy.isNoSideEffects()
}

// connectError marks an error from connection establishment (TCP dial, proxy
// CONNECT, or TLS handshake) before any request bytes were sent (spec E3, T7a).
type connectError struct {
	err error
}

func (e *connectError) Error() string {
	return e.err.Error()
}

func (e *connectError) Unwrap() error {
	return e.err
}

// isConnectError reports whether err came from establishing a connection,
// so no request bytes were sent and the attempt had no side effects.
func isConnectError(err error) bool {
	var connErr *connectError
	if errors.As(err, &connErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	// net/http reports an expired TLSHandshakeTimeout only by this message
	// (its type is unexported). The handshake precedes the request.
	return strings.Contains(err.Error(), "net/http: TLS handshake timeout")
}
