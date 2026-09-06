package s2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const testHost = "example.com"

// recordingTransport is a fake http.RoundTripper that serves a fresh streaming
// body per RoundTrip call and counts CloseIdleConnections invocations. It
// stands in for a real *http2.Transport so tests can assert on the reclaim
// path driven by releaseOnClose.Close without standing up a real HTTP/2
// server. The body it serves is irrelevant to the leak logic: what matters is
// the session counter and poisoned flag on the owning spreadEntry, and that
// closeIdleConnections is invoked at the right moments.
type recordingTransport struct {
	mu             sync.Mutex
	closeIdleCalls int
	roundTrips     int
	newBody        func() io.ReadCloser
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{
		newBody: func() io.ReadCloser {
			return io.NopCloser(bytes.NewReader(nil))
		},
	}
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.roundTrips++
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       r.newBody(),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *recordingTransport) CloseIdleConnections() {
	r.mu.Lock()
	r.closeIdleCalls++
	r.mu.Unlock()
}

func (r *recordingTransport) CloseIdleCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeIdleCalls
}

func (r *recordingTransport) RoundTrips() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roundTrips
}

// staticTransport returns a fixed error from RoundTrip; used to exercise the
// RoundTrip error path which decrements the session counter directly.
type staticErrTransport struct {
	err error
}

func (s *staticErrTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, s.err
}

func mustNewStreamingRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestSpreadTransport_CheckoutReusesEntryUntilMaxSessions(t *testing.T) {
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	for i := range maxSessionsPerConnection {
		req := mustNewStreamingRequest(t, "https://"+host+"/")
		resp, err := pool.RoundTrip(req)
		if err != nil {
			t.Fatalf("roundtrip %d: %v", i, err)
		}
		defer resp.Body.Close()
	}

	entries := pool.hosts[host]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after %d sessions, got %d", maxSessionsPerConnection, len(entries))
	}
	if got := entries[0].sessions.Load(); got != maxSessionsPerConnection {
		t.Fatalf("expected %d sessions on shared entry, got %d", maxSessionsPerConnection, got)
	}

	// One more session pushes past the limit; a second entry is created.
	req := mustNewStreamingRequest(t, "https://"+host+"/")
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip overflow: %v", err)
	}
	defer resp.Body.Close()

	entries = pool.hosts[host]
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after overflow, got %d", len(entries))
	}
	if got := entries[0].sessions.Load(); got != maxSessionsPerConnection {
		t.Fatalf("expected first entry full at %d, got %d", maxSessionsPerConnection, got)
	}
	if got := entries[1].sessions.Load(); got != 1 {
		t.Fatalf("expected second entry at 1 session, got %d", got)
	}
}

func TestSpreadTransport_DistinctHostsGetDistinctEntries(t *testing.T) {
	var transports []*recordingTransport
	pool := newSpreadTransport(func() http.RoundTripper {
		rt := newRecordingTransport()
		transports = append(transports, rt)
		return rt
	})

	for _, host := range []string{"a.example.com", "b.example.com"} {
		req := mustNewStreamingRequest(t, "https://"+host+"/")
		resp, err := pool.RoundTrip(req)
		if err != nil {
			t.Fatalf("roundtrip %s: %v", host, err)
		}
		defer resp.Body.Close()
	}

	if len(pool.hosts["a.example.com"]) != 1 || len(pool.hosts["b.example.com"]) != 1 {
		t.Fatalf("expected 1 entry per host, got a=%v b=%v",
			pool.hosts["a.example.com"], pool.hosts["b.example.com"])
	}
	if len(transports) != 2 {
		t.Fatalf("expected 2 transports created (one per host), got %d", len(transports))
	}
}

func TestSpreadTransport_RoundTripErrorDecrementsSessions(t *testing.T) {
	rt := &staticErrTransport{err: errors.New("boom")}
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	req := mustNewStreamingRequest(t, "https://"+host+"/")
	_, err := pool.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error from roundtrip")
	}

	entries := pool.hosts[host]
	if len(entries) != 1 {
		t.Fatalf("expected entry retained after error, got %d", len(entries))
	}
	if got := entries[0].sessions.Load(); got != 0 {
		t.Fatalf("expected 0 sessions after error path decrement, got %d", got)
	}
}

func TestSpreadTransport_PoisonRemovesEntryAndMarksPoisoned(t *testing.T) {
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	req := mustNewStreamingRequest(t, "https://"+host+"/")
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}

	entries := pool.hosts[host]
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]

	pool.poison(host, entry)

	if got := pool.hosts[host]; len(got) != 0 {
		t.Fatalf("poison should remove entry from t.hosts, got %d", len(got))
	}
	if !entry.poisoned.Load() {
		t.Fatal("poison should mark the entry as poisoned")
	}
	// poison opportunistically sweeps the now-orphaned transport. While the
	// stream is still active this is a no-op against a real *http2.Transport,
	// but it must still be invoked so the slow-path (idle conn) reaps on close.
	if got := rt.CloseIdleCalls(); got != 1 {
		t.Fatalf("expected 1 CloseIdleConnections call from poison, got %d", got)
	}

	// The stream is still open; closing the body later is what triggers the
	// reclaim, asserted in TestReleaseOnClose_ReclaimsPoisonedOrphan.
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
}

func TestSpreadTransport_PoisonUnknownEntryIsNoOp(t *testing.T) {
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	req := mustNewStreamingRequest(t, "https://"+host+"/")
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()

	entry := pool.hosts[host][0]

	// A spreadEntry not in the pool must not be marked poisoned and must not
	// trigger a sweep. This guards against misuse from concurrent poison calls.
	stranger := &spreadEntry{rt: newRecordingTransport()}
	pool.poison(host, stranger)

	if stranger.poisoned.Load() {
		t.Fatal("poison of an unknown entry must not mark it poisoned")
	}
	if got := pool.hosts[host]; len(got) != 1 {
		t.Fatalf("poison of unknown entry must not modify the pool, got %d", len(got))
	}
	if entry.poisoned.Load() {
		t.Fatal("poison of unknown entry must not poison the live entry")
	}
}

func TestReleaseOnClose_ReclaimsPoisonedOrphan(t *testing.T) {
	// This is the core regression: an orphaned (poison-removed) transport is
	// unreachable from spreadTransport.CloseIdleConnections, which iterates
	// t.hosts. The only client-side reclaim path is releaseOnClose.Close
	// firing closeIdleConnections when the last stream drains. Before the fix,
	// releaseOnClose.Close only decremented the session counter and the orphan
	// leaked until the server closed TCP / sent a pre-close GOAWAY.
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	ctx, capture := capturePoisonHandle(context.Background())
	req := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)

	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}

	// Sanity: the captured handle targets the entry that served the stream.
	if h := capture.handle.Load(); h == nil {
		t.Fatal("expected poison handle to be captured during RoundTrip")
	}

	capture.poison()
	if got := pool.hosts[host]; len(got) != 0 {
		t.Fatalf("poison should remove entry from pool, got %d entries", len(got))
	}
	poisonSweepCalls := rt.CloseIdleCalls()
	if poisonSweepCalls != 1 {
		t.Fatalf("expected exactly 1 CloseIdleConnections call from poison, got %d", poisonSweepCalls)
	}

	// Closing the body is the moment the connection becomes idle. This must
	// drive a second CloseIdleConnections call on the (now orphaned) transport.
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if got := rt.CloseIdleCalls(); got != 2 {
		t.Fatalf("expected releaseOnClose.Close to reclaim the orphan (2 CloseIdleConnections calls total), got %d", got)
	}

	// A subsequent driver-level sweep must do nothing for the orphan because
	// the entry is no longer in t.hosts; it must not double-reap or reach it.
	pool.CloseIdleConnections()
	if got := rt.CloseIdleCalls(); got != 2 {
		t.Fatalf("spreadTransport.CloseIdleConnections must not re-reach the orphan, got %d", got)
	}
}

func TestReleaseOnClose_DoesNotReclaimNonPoisonedEntry(t *testing.T) {
	// A session that drains an entry still in the pool must NOT call
	// closeIdleConnections from releaseOnClose: the entry remains reachable
	// from spreadTransport.CloseIdleConnections, which is the proper sweeper.
	// This preserves existing in-pool reuse behaviour.
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	ctx, _ := capturePoisonHandle(context.Background()) // capture but never poison
	req := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)

	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}

	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if got := rt.CloseIdleCalls(); got != 0 {
		t.Fatalf("non-poisoned entry must not be reaped by releaseOnClose, got %d CloseIdleConnections calls", got)
	}

	// The entry remains in the pool for reuse / driver sweeps.
	if got := pool.hosts[host]; len(got) != 1 {
		t.Fatalf("non-poisoned entry must remain in pool, got %d entries", len(got))
	}
}

func TestReleaseOnClose_MultipleSessionsReapOnlyOnLastClose(t *testing.T) {
	// Several streams share one poisoned entry. The orphan is reclaimed only
	// when the LAST stream drains, so still-active siblings are not torn down
	// prematurely. closeIdleConnections fires exactly once, on the last close.
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	ctx, capture := capturePoisonHandle(context.Background())

	req1 := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)
	resp1, err := pool.RoundTrip(req1)
	if err != nil {
		t.Fatalf("roundtrip 1: %v", err)
	}

	req2 := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)
	resp2, err := pool.RoundTrip(req2)
	if err != nil {
		t.Fatalf("roundtrip 2: %v", err)
	}

	// Both trips share one entry (sessions=2, below maxSessionsPerConnection).
	entries := pool.hosts[host]
	if len(entries) != 1 {
		t.Fatalf("expected 1 shared entry, got %d", len(entries))
	}
	if got := entries[0].sessions.Load(); got != 2 {
		t.Fatalf("expected 2 sessions on shared entry, got %d", got)
	}

	capture.poison() // closeIdleConnections call #1 (no-op on active stream)
	if got := pool.hosts[host]; len(got) != 0 {
		t.Fatalf("poison should remove entry from pool, got %d", len(got))
	}

	// Close the first body: sessions 2 -> 1, not last, must not reclaim.
	if err := resp1.Body.Close(); err != nil {
		t.Fatalf("close body 1: %v", err)
	}
	if got := rt.CloseIdleCalls(); got != 1 {
		t.Fatalf("first close must not reclaim while a sibling stream is open, got %d CloseIdleConnections calls", got)
	}

	// Close the second body: sessions 1 -> 0 and poisoned, reclaim now.
	if err := resp2.Body.Close(); err != nil {
		t.Fatalf("close body 2: %v", err)
	}
	if got := rt.CloseIdleCalls(); got != 2 {
		t.Fatalf("last close must reclaim the orphan, got %d CloseIdleConnections calls", got)
	}
}

func TestReleaseOnClose_CloseIsIdempotent(t *testing.T) {
	// releaseOnClose.Close must decrement the session counter and reclaim at
	// most once even if Close is invoked multiple times (defense against
	// double-close in callers / deferred cleanup paths).
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	ctx, capture := capturePoisonHandle(context.Background())
	req := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	entry := capture.handle.Load().entry

	capture.poison()

	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("second close body: %v", err)
	}

	if got := entry.sessions.Load(); got != 0 {
		t.Fatalf("sessions should be 0 after close, got %d", got)
	}
	// poison's sweep (1) + releaseOnClose's reclaim (1) = 2. The second Close
	// must not trigger another reclaim.
	if got := rt.CloseIdleCalls(); got != 2 {
		t.Fatalf("expected 2 total CloseIdleConnections calls after double-close, got %d", got)
	}
}

func TestSpreadTransport_CloseIdleConnectionsSweepsInPoolEntries(t *testing.T) {
	// A driver-level sweep must still reach entries that remain in the pool
	// (the non-orphan path). This behaviour is unchanged by the fix and must
	// not regress.
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	req := mustNewStreamingRequest(t, "https://"+host+"/")
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()

	pool.CloseIdleConnections()
	if got := rt.CloseIdleCalls(); got != 1 {
		t.Fatalf("expected sweep to call CloseIdleConnections on in-pool entry, got %d", got)
	}
}

func TestCapturePoisonHandle_PoisonWiresToPool(t *testing.T) {
	// End-to-end wiring: capturePoisonHandle injects a *poisonCapture into the
	// request context, spreadTransport.RoundTrip publishes the serving entry
	// into it, and (*poisonCapture).poison() removes that entry from the pool.
	rt := newRecordingTransport()
	pool := newSpreadTransport(func() http.RoundTripper { return rt })

	host := testHost
	ctx, capture := capturePoisonHandle(context.Background())
	req := mustNewStreamingRequest(t, "https://"+host+"/").WithContext(ctx)

	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()

	if capture.handle.Load() == nil {
		t.Fatal("expected poison handle to be captured during RoundTrip")
	}

	capture.poison()
	if got := pool.hosts[host]; len(got) != 0 {
		t.Fatalf("capture.poison should remove entry from pool, got %d", len(got))
	}
}

func TestPoisonCapture_NilSafe(t *testing.T) {
	// (*poisonCapture)(nil).poison() must not panic; call sites in read.go and
	// append.go rely on this nil-safety.
	var nilCapture *poisonCapture
	nilCapture.poison()
}

func TestPoisonCapture_NoHandleIsNoOp(t *testing.T) {
	capture := &poisonCapture{}
	capture.poison() // no handle published yet; must not panic
}

func TestCloseIdleConnections_SkipsNonImplementingTransport(t *testing.T) {
	// closeIdleConnections tolerates transports that do not implement the
	// optional interface; it must not panic or error (e.g. a bare roundTripFunc).
	called := false
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})
	closeIdleConnections(rt)
	// RoundTrip must not be invoked by CloseIdleConnections; only the type
	// assertion is performed.
	if called {
		t.Fatal("closeIdleConnections must not call RoundTrip")
	}
}
