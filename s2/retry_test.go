package s2

import (
	"testing"
	"time"
)

func TestCalculateRetryBackoff_ZeroAttempt(t *testing.T) {
	// This test verifies that calculateRetryBackoff does not panic
	// when called with attempt=0 (which would cause a negative bit shift).
	//
	// The guard returns 0 for invalid attempt values as a defensive measure.

	cfg := &RetryConfig{
		MinBaseDelay: 100 * time.Millisecond,
		MaxBaseDelay: 1 * time.Second,
	}

	// This should not panic and should return 0
	delay := calculateRetryBackoff(cfg, 0)

	if delay != 0 {
		t.Errorf("expected delay=0 for attempt=0, got %v", delay)
	}
}

func TestCalculateRetryBackoff_NegativeAttempt(t *testing.T) {
	// Edge case: negative attempt should not panic and should return 0
	cfg := &RetryConfig{
		MinBaseDelay: 100 * time.Millisecond,
		MaxBaseDelay: 1 * time.Second,
	}

	// This should not panic and should return 0
	delay := calculateRetryBackoff(cfg, -5)

	if delay != 0 {
		t.Errorf("expected delay=0 for negative attempt, got %v", delay)
	}
}

func TestCalculateRetryBackoff_ExponentialGrowth(t *testing.T) {
	cfg := &RetryConfig{
		MinBaseDelay: 100 * time.Millisecond,
		MaxBaseDelay: 10 * time.Second,
	}

	// Test exponential backoff behavior
	// attempt=1: base = 100ms * 2^0 = 100ms, delay in [100ms, 200ms]
	// attempt=2: base = 100ms * 2^1 = 200ms, delay in [200ms, 400ms]
	// attempt=3: base = 100ms * 2^2 = 400ms, delay in [400ms, 800ms]

	testCases := []struct {
		attempt     int
		minExpected time.Duration
		maxExpected time.Duration
	}{
		{1, 100 * time.Millisecond, 200 * time.Millisecond},
		{2, 200 * time.Millisecond, 400 * time.Millisecond},
		{3, 400 * time.Millisecond, 800 * time.Millisecond},
		{4, 800 * time.Millisecond, 1600 * time.Millisecond},
	}

	for _, tc := range testCases {
		delay := calculateRetryBackoff(cfg, tc.attempt)
		if delay < tc.minExpected || delay > tc.maxExpected {
			t.Errorf("attempt=%d: expected delay in [%v, %v], got %v",
				tc.attempt, tc.minExpected, tc.maxExpected, delay)
		}
	}
}

func TestCalculateRetryBackoff_CapsAtMaxDelay(t *testing.T) {
	cfg := &RetryConfig{
		MinBaseDelay: 100 * time.Millisecond,
		MaxBaseDelay: 500 * time.Millisecond,
	}

	// With a high attempt number, the delay should be capped at maxBaseDelay
	// attempt=10: base would be 100ms * 2^9 = 51.2s, but capped to 500ms
	delay := calculateRetryBackoff(cfg, 10)

	// delay = baseDelay + jitter, where jitter is in [0, baseDelay]
	// so max delay is 2 * maxBaseDelay
	if delay < cfg.MaxBaseDelay || delay > 2*cfg.MaxBaseDelay {
		t.Errorf("expected delay in [%v, %v], got %v",
			cfg.MaxBaseDelay, 2*cfg.MaxBaseDelay, delay)
	}
}

func TestCalculateRetryBackoff_DefaultsForZeroConfig(t *testing.T) {
	cfg := &RetryConfig{
		MinBaseDelay: 0, // Should default to 100ms
		MaxBaseDelay: 0, // Should default to 1s
	}

	delay := calculateRetryBackoff(cfg, 1)

	// Should use defaults: minBaseDelay=100ms, so delay in [100ms, 200ms]
	if delay < defaultMinBaseDelay || delay > 2*defaultMinBaseDelay {
		t.Errorf("expected delay in [%v, %v], got %v",
			defaultMinBaseDelay, 2*defaultMinBaseDelay, delay)
	}
}
