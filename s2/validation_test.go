package s2

import (
	"errors"
	"strings"
	"testing"
)

// Identifier validation must return a structured *S2Error with Origin "sdk" and
// Code "VALIDATION", so callers can handle SDK-originated validation errors
// uniformly via errors.As (the same pattern as fencing token and encryption key
// validation).
func TestIdentifierValidationReturnsS2Error(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantSub string
	}{
		{"access token ID too long", validateAccessTokenID(AccessTokenID(strings.Repeat("a", 97))), "access token ID"},
		{"access token ID empty", validateAccessTokenID(AccessTokenID("")), "access token ID"},
		{"stream name too long", validateStreamName(StreamName(strings.Repeat("a", 513))), "stream name"},
		{"stream name empty", validateStreamName(StreamName("")), "stream name"},
		{"basin name too short", validateBasinName(BasinName("short")), "basin name"},
		{"basin name bad chars", validateBasinName(BasinName("Invalid_Basin")), "basin name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("expected validation error, got nil")
			}
			var s2Err *S2Error
			if !errors.As(tc.err, &s2Err) {
				t.Fatalf("expected *S2Error, got %T", tc.err)
			}
			if s2Err.Origin != "sdk" {
				t.Fatalf("expected Origin %q, got %q", "sdk", s2Err.Origin)
			}
			if s2Err.Code != "VALIDATION" {
				t.Fatalf("expected Code %q, got %q", "VALIDATION", s2Err.Code)
			}
			if !strings.Contains(s2Err.Message, tc.wantSub) {
				t.Fatalf("expected message to contain %q, got %q", tc.wantSub, s2Err.Message)
			}
		})
	}
}
