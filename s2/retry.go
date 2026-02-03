package s2

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"net/url"
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
		result, err := operation()
		if err == nil {
			return result, nil
		}

		lastError = err

		if isNetworkError(ctx, err) {
			if attemptNo < maxAttempts {
				delay := calculateRetryBackoff(config, attemptNo)
				logInfo(logger, "s2 retrying after network error",
					"attempt", attemptNo,
					"max_attempts", maxAttempts,
					"delay", delay,
					"error", err)
				time.Sleep(delay)

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
		time.Sleep(delay)
	}

	return zero, lastError
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

	// baseDelay = min((minBaseDelay * 2^(n-1)), maxBaseDelay)
	baseDelay := minBaseDelay << (attempt - 1)                // minBaseDelay * 2^(attempt-1)
	if baseDelay > maxBaseDelay || baseDelay < minBaseDelay { // overflow check
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

func withAppendRetries(ctx context.Context, config *RetryConfig, logger *slog.Logger, input *AppendInput, operation func() (*AppendAck, error)) (*AppendAck, error) {
	if config == nil {
		config = DefaultRetryConfig
	}

	maxAttempts := config.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastError error

	for attemptNo := 1; attemptNo <= maxAttempts; attemptNo++ {
		result, err := operation()
		if err == nil {
			return result, nil
		}

		lastError = err

		if isNetworkError(ctx, err) {
			if attemptNo == maxAttempts || !shouldRetryNetworkError(config, input) {
				logError(logger, "s2 append network error, max attempts exhausted or not retryable",
					"attempts", maxAttempts,
					"error", err)

				break
			}
			delay := calculateRetryBackoff(config, attemptNo)
			logInfo(logger, "s2 append retrying after network error",
				"attempt", attemptNo,
				"max_attempts", maxAttempts,
				"delay", delay,
				"error", err)
			time.Sleep(delay)

			continue
		}

		if !shouldRetryError(err, config, input) || attemptNo == maxAttempts {
			return nil, err
		}

		delay := calculateRetryBackoff(config, attemptNo)
		logInfo(logger, "s2 append retrying after error",
			"attempt", attemptNo,
			"max_attempts", maxAttempts,
			"delay", delay,
			"error", err)
		time.Sleep(delay)
	}

	return nil, lastError
}

func shouldRetryNetworkError(config *RetryConfig, input *AppendInput) bool {
	if config.AppendRetryPolicy != AppendRetryPolicyNoSideEffects {
		return true
	}

	return input != nil && input.MatchSeqNum != nil
}
