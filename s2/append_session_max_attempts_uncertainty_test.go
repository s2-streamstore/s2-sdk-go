package s2

import (
	"errors"
	"strings"
	"testing"
)

// TestAppendSession_MaxAttemptsExhaustedUncertainty verifies that when the
// append session exhausts its retry budget via the max-attempts branch, the
// terminal error preserves reachability of the final attempt's *S2Error and
// honors the HasNoSideEffects contract documented at error.go:155-156.
//
// The session's max-attempts branch constructs the terminal error with
// fmt.Errorf("max attempts (%d) exhausted, last error: %v: %w", ...). When
// the final *S2Error is formatted with %v it is dropped from the unwrap
// chain, so errors.As(&s2Err) and HasNoSideEffects both misbehave.
func TestAppendSession_MaxAttemptsExhaustedUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []error
		// all-definite: every attempt is provably side-effect free, so the
		// whole operation is side-effect free (HasNoSideEffects must be true).
		// indefinite-then-definite: an earlier attempt may have taken effect,
		// so the result must be wrapped in *AppendIndefiniteFailureError and
		// HasNoSideEffects must be false.
		wantHasNoSideEffects  bool
		wantIndefiniteWrapped bool
	}{
		{
			name: "indefinite_then_definite_exhausts",
			responses: []error{
				serverError(503, "unavailable"),  // noSideEffects=false -> sets priorUncertainty
				serverError(429, "rate_limited"), // noSideEffects=true  -> leaves priorUncertainty
				serverError(429, "rate_limited"), // final; exhausts budget
			},
			wantHasNoSideEffects:  false,
			wantIndefiniteWrapped: true,
		},
		{
			name: "all_definite_exhausts",
			responses: []error{
				serverError(429, "rate_limited"), // noSideEffects=true
				serverError(429, "rate_limited"), // noSideEffects=true
				serverError(429, "rate_limited"), // final; exhausts budget
			},
			wantHasNoSideEffects:  true,
			wantIndefiniteWrapped: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, transport, entries := newAppendAckDrainSession(t, &RetryConfig{
				AppendRetryPolicy: AppendRetryPolicyAll,
				MaxAttempts:       3,
			}, 2)

			for _, resp := range tc.responses {
				// Reattach the transport as the current session before each
				// retry (handleSessionError clears currentSession on failure).
				session.sessionMu.Lock()
				session.currentSession = transport
				session.sessionMu.Unlock()
				session.handleSessionError(transport, resp)
			}

			for i, entry := range entries {
				result := <-entry.resultCh
				if result == nil || result.err == nil {
					t.Fatalf("batch %d: expected error, got %+v", i, result)
				}
				if !errors.Is(result.err, ErrMaxAttemptsExhausted) {
					t.Fatalf("batch %d: expected ErrMaxAttemptsExhausted in chain, got %v", i, result.err)
				}
				if got := HasNoSideEffects(result.err); got != tc.wantHasNoSideEffects {
					t.Fatalf("batch %d: HasNoSideEffects = %v, want %v (err: %v)", i, got, tc.wantHasNoSideEffects, result.err)
				}
				var indefinite *AppendIndefiniteFailureError
				if got := errors.As(result.err, &indefinite); got != tc.wantIndefiniteWrapped {
					t.Fatalf("batch %d: errors.As(&AppendIndefiniteFailureError) = %v, want %v (err: %v)", i, got, tc.wantIndefiniteWrapped, result.err)
				}
				var s2Err *S2Error
				if !errors.As(result.err, &s2Err) {
					t.Fatalf("batch %d: errors.As(&S2Error) = false, want true (err: %v)", i, result.err)
				}
				final := tc.responses[len(tc.responses)-1]
				if s2Err != final {
					t.Fatalf("batch %d: reachable *S2Error is not the final attempt's error: got %p, want %p (err: %v)", i, s2Err, final, result.err)
				}
				// The rendered envelope string must still inline the final
				// *S2Error verbatim (multi-%w does not change the text), so
				// log lines and string-matching callers see no change.
				if want := "last error: " + final.Error(); !strings.Contains(result.err.Error(), want) {
					t.Fatalf("batch %d: envelope message changed: got %q, want substring %q", i, result.err.Error(), want)
				}
			}
		})
	}
}

// TestAppendSession_StartMaxAttemptsExhaustedUncertainty exercises the
// handleSessionStartError max-attempts branch (reachable when session
// establishment itself fails on every attempt). priorUncertainty stays false
// because no batch was ever sent, so the all-definite HasNoSideEffects
// contract must hold on this path too.
func TestAppendSession_StartMaxAttemptsExhaustedUncertainty(t *testing.T) {
	session, _, entries := newAppendAckDrainSession(t, &RetryConfig{
		AppendRetryPolicy: AppendRetryPolicyAll,
		MaxAttempts:       3,
	}, 2)

	// handleSessionStartError never sends batches, so priorUncertainty stays
	// false for every entry. All attempts are rate_limited (noSideEffects=true),
	// so the whole operation is side-effect free.
	var final *S2Error
	for range 3 {
		final = serverError(429, "rate_limited")
		session.handleSessionStartError(final)
	}

	for i, entry := range entries {
		result := <-entry.resultCh
		if result == nil || result.err == nil {
			t.Fatalf("batch %d: expected error, got %+v", i, result)
		}
		if !errors.Is(result.err, ErrMaxAttemptsExhausted) {
			t.Fatalf("batch %d: expected ErrMaxAttemptsExhausted in chain, got %v", i, result.err)
		}
		if got := HasNoSideEffects(result.err); !got {
			t.Fatalf("batch %d: HasNoSideEffects = false, want true (err: %v)", i, result.err)
		}
		var s2Err *S2Error
		if !errors.As(result.err, &s2Err) {
			t.Fatalf("batch %d: errors.As(&S2Error) = false, want true (err: %v)", i, result.err)
		}
		if s2Err != final {
			t.Fatalf("batch %d: reachable *S2Error is not the final attempt's error: got %p, want %p (err: %v)", i, s2Err, final, result.err)
		}
		// No batch was ever sent, so the result must NOT be wrapped as
		// indefinite (priorUncertainty stayed false).
		var indefinite *AppendIndefiniteFailureError
		if errors.As(result.err, &indefinite) {
			t.Fatalf("batch %d: unexpected *AppendIndefiniteFailureError wrap (err: %v)", i, result.err)
		}
		// The rendered envelope string must still inline the final *S2Error
		// verbatim so log lines and string-matching callers see no change.
		if want := "last error: " + final.Error(); !strings.Contains(result.err.Error(), want) {
			t.Fatalf("batch %d: envelope message changed: got %q, want substring %q", i, result.err.Error(), want)
		}
	}
}
