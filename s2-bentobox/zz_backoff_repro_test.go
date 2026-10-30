package bentobox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	s2 "github.com/s2-streamstore/s2-sdk-go/s2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// newH2CErrorServer stands up a cleartext HTTP/2 (h2c, prior-knowledge)
// server that answers every request with the given status and S2 error code,
// and counts the requests it receives. The SDK's streaming read transport
// (schemeAwareTransport with AllowHTTP, h2c) reaches it over prior-knowledge
// h2c, so this exercises the real read-session path that the backoff bug
// affects.
func newH2CErrorServer(t *testing.T, status int, code string) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code":    code,
			"message": "nope",
		})
	})
	srv := httptest.NewUnstartedServer(h2c.NewHandler(handler, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &requests
}

// drainReads consumes from msi until ctx is done, returning once the input is
// closed or the context expires. It tolerates the expected forwarded *s2.S2Error.
func drainReads(ctx context.Context, msi *MultiStreamInput) {
	for {
		_, _, _, rerr := msi.ReadBatch(ctx)
		if rerr != nil {
			if errors.Is(rerr, ErrInputClosed) || errors.Is(rerr, context.Canceled) ||
				errors.Is(rerr, context.DeadlineExceeded) {
				return
			}
			var s2Err *s2.S2Error
			if errors.As(rerr, &s2Err) {
				// Forwarded read-level error: expected, keep draining.
				if ctx.Err() != nil {
					return
				}
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// runBackoffStormRepro drives the public ConnectMultiStreamInput against an
// h2c error server for runFor with the given backoff, and returns the number
// of read requests the server saw. Each reconnect cycle is exactly one HTTP
// request (the status is a terminal, non-retryable *s2.S2Error), so the count
// is the number of reconnect cycles.
func runBackoffStormRepro(t *testing.T, status int, code string, backoff, runFor time.Duration) int32 {
	t.Helper()
	srv, requests := newH2CErrorServer(t, status, code)
	t.Setenv("S2_BASIN_ENDPOINT", srv.URL)
	t.Setenv("S2_ACCESS_TOKEN", "test-token")

	config := &InputConfig{
		Config: &Config{
			Basin:       "test-basin",
			AccessToken: "test-token",
		},
		Streams:               StaticInputStreams{Streams: []string{"s"}},
		MaxInFlight:           1024,
		UpdateStreamsInterval: time.Hour, // don't churn the worker set during the run
		BackoffDuration:       backoff,
		StartSeqNum:           InputStartSeqNumEarliest,
		Logger:                silentLogger{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), runFor)
	defer cancel()

	msi, err := ConnectMultiStreamInput(ctx, config)
	if err != nil {
		t.Fatalf("ConnectMultiStreamInput: %v", err)
	}

	go drainReads(ctx, msi)

	<-ctx.Done()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	_ = msi.Close(closeCtx)

	return atomic.LoadInt32(requests)
}

// End-to-end regression for the reconnect-backoff bug (the report's repro).
// A 404 stream_not_found is a terminal, non-retryable error: each reconnect
// cycle is exactly one HTTP request. With a working BackoffDuration the rate
// is bounded to ~1/BackoffDuration; without it the loop hot-loops at network
// speed (~8000 req/s on loopback) — the request storm reported in the bug.
//
// Preconditions: S2_BASIN_ENDPOINT (set via t.Setenv) points the SDK's
// streaming client at the h2c test server; S2_ACCESS_TOKEN is set so newClient
// does not panic. No real S2 endpoint required.
func TestMultiStreamInputBackoffThrottlesRequestStorm(t *testing.T) {
	const backoff = 2 * time.Second
	const runFor = 3 * time.Second
	// With backoff=2s over 3s: immediate attempt (t=0), then ~t=2s -> 2
	// requests. Allow a generous ceiling well below the unthrottled thousands
	// the bug produces (~8000 req/s on loopback).
	const maxReqs = 10

	cases := []struct {
		name   string
		status int
		code   string
	}{
		{"404_stream_not_found", http.StatusNotFound, "stream_not_found"},
		{"401_unauthenticated", http.StatusUnauthorized, "unauthenticated"},
		{"403_permission_denied", http.StatusForbidden, "permission_denied"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reqs := runBackoffStormRepro(t, c.status, c.code, backoff, runFor)
			t.Logf("requests: %d over %v with BackoffDuration=%v (status=%d, code=%s)",
				reqs, runFor, backoff, c.status, c.code)
			if reqs == 0 {
				t.Fatalf("no requests were made at all; the read session never connected — test setup is broken")
			}
			if reqs > maxReqs {
				t.Fatalf("UNTHROTTLED: %d requests over %v with BackoffDuration=%v (status=%d, code=%s; expected <= %d, ~1/backoff, not a request storm); bug is present",
					reqs, runFor, backoff, c.status, c.code, maxReqs)
			}
		})
	}
}
