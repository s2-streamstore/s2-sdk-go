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

type RetryConfig struct {
	// Total number of attempts, including the initial try. Must be >= 1. A value of 1 means no retries.
	MaxAttempts int
	// Minimum delay for exponential backoff. Defaults to 100ms.
	MinDelay time.Duration
	// Maximum delay for exponential backoff. Defaults to 1s.
	MaxDelay time.Duration
	// Policy for retrying append operations.
	AppendRetryPolicy AppendRetryPolicy
}

const (
	defaultMaxAttempts = 3
	defaultMinDelay    = 100 * time.Millisecond
	defaultMaxDelay    = 1 * time.Second
)

var DefaultRetryConfig = &RetryConfig{
	MaxAttempts:       defaultMaxAttempts,
	MinDelay:          defaultMinDelay,
	MaxDelay:          defaultMaxDelay,
	AppendRetryPolicy: AppendRetryPolicyAll,
}

func withRetries[T any](config *RetryConfig, logger *slog.Logger, operation func() (T, error)) (T, error) {
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

		if isNetworkError(err) {
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
	minDelay := config.MinDelay
	if minDelay <= 0 {
		minDelay = defaultMinDelay
	}
	maxDelay := config.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}

	// baseDelay = min((minDelay * 2^(n-1)), maxDelay)
	baseDelay := minDelay << (attempt - 1) // minDelay * 2^(attempt-1)
	if baseDelay > maxDelay || baseDelay < minDelay { // overflow check
		baseDelay = maxDelay
	}

	// jitter = random(0, baseDelay)
	jitter := time.Duration(rand.Int63n(int64(baseDelay)))

	// delay = baseDelay + jitter
	return baseDelay + jitter
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
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
		return isNetworkError(urlErr.Err)
	}

	return false
}

func withAppendRetries(config *RetryConfig, logger *slog.Logger, input *AppendInput, operation func() (*AppendAck, error)) (*AppendAck, error) {
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

		if isNetworkError(err) {
			if attemptNo == maxAttempts {
				logError(logger, "s2 append network error, max attempts exhausted",
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
