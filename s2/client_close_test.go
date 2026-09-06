package s2

import (
	"io"
	"net/http"
	"sync"
	"testing"

	"golang.org/x/net/http2"
)

// closeIdleCountingTransport records CloseIdleConnections calls while still
// serving real requests so Client.Close can be exercised through the
// streaming/unary HTTP client transports it owns.
type closeIdleCountingTransport struct {
	mu             sync.Mutex
	closeIdleCalls int
}

func (r *closeIdleCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(nil),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (r *closeIdleCountingTransport) CloseIdleConnections() {
	r.mu.Lock()
	r.closeIdleCalls++
	r.mu.Unlock()
}

func (r *closeIdleCountingTransport) CloseIdleCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeIdleCalls
}

func TestClient_Close_SweepsStreamingAndUnaryTransports(t *testing.T) {
	streamingRT := &closeIdleCountingTransport{}
	unaryRT := &closeIdleCountingTransport{}

	c := &Client{
		streamingClient: &http.Client{Transport: streamingRT},
		httpClient:      &http.Client{Transport: unaryRT},
	}

	c.Close()

	if got := streamingRT.CloseIdleCalls(); got != 1 {
		t.Fatalf("expected 1 CloseIdleConnections call on streaming transport, got %d", got)
	}
	if got := unaryRT.CloseIdleCalls(); got != 1 {
		t.Fatalf("expected 1 CloseIdleConnections call on unary transport, got %d", got)
	}
}

func TestClient_Close_Idempotent(t *testing.T) {
	streamingRT := &closeIdleCountingTransport{}
	c := &Client{
		streamingClient: &http.Client{Transport: streamingRT},
		httpClient:      &http.Client{Transport: &closeIdleCountingTransport{}},
	}

	c.Close()
	c.Close()

	// Each transport is swept once per Close(); two Close() calls yield two
	// sweeps of the streaming transport. This is acceptable (CloseIdleConnections
	// is idempotent at the transport layer) and matches the contract that Close
	// is safe to call multiple times.
	if got := streamingRT.CloseIdleCalls(); got != 2 {
		t.Fatalf("expected 2 sweeps after 2 Close calls, got %d", got)
	}
}

func TestClient_Close_NilSafe(t *testing.T) {
	var c *Client
	c.Close() // must not panic
}

func TestClient_Close_SkipsTransportsWithoutCloseIdleConnections(t *testing.T) {
	// A transport that does not implement CloseIdleConnections must not cause
	// Close to panic or error.
	c := &Client{
		streamingClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("RoundTrip should not be called")
			return nil, nil
		})},
		httpClient: &http.Client{Transport: http.DefaultTransport},
	}
	c.Close()
}

func TestNewStreamingTransport_SetsIdleConnTimeout(t *testing.T) {
	// Defense-in-depth: the streaming *http2.Transport must set IdleConnTimeout
	// so that an idle connection (orphaned or in-pool) is auto-reaped by http2's
	// own idleTimer regardless of whether any SDK code path drives a sweep.
	rt := newStreamingTransport(defaultConnectionTimeout)
	scheme, ok := rt.(*schemeAwareTransport)
	if !ok {
		t.Fatalf("expected *schemeAwareTransport, got %T", rt)
	}
	for name, tpt := range map[string]*http2.Transport{"https": scheme.https, "h2c": scheme.h2c} {
		if tpt.IdleConnTimeout != http2IdleConnTimeout {
			t.Fatalf("%s transport IdleConnTimeout = %s, want %s", name, tpt.IdleConnTimeout, http2IdleConnTimeout)
		}
		if tpt.ReadIdleTimeout != http2ReadIdleTimeout {
			t.Fatalf("%s transport ReadIdleTimeout = %s, want %s", name, tpt.ReadIdleTimeout, http2ReadIdleTimeout)
		}
		// IdleConnTimeout must exceed ReadIdleTimeout so a healthy idle conn is
		// not closed before a health-check PING can confirm it.
		if tpt.IdleConnTimeout <= tpt.ReadIdleTimeout {
			t.Fatalf("%s transport IdleConnTimeout %s must exceed ReadIdleTimeout %s",
				name, tpt.IdleConnTimeout, tpt.ReadIdleTimeout)
		}
	}
}

func TestNewStreamingTransport_SchemeRouting(t *testing.T) {
	// Ensure the streaming transport still routes by scheme (h2c vs https) and
	// that IdleConnTimeout did not disturb the RoundTrip wiring.
	rt := newStreamingTransport(defaultConnectionTimeout)
	scheme, ok := rt.(*schemeAwareTransport)
	if !ok {
		t.Fatalf("expected *schemeAwareTransport, got %T", rt)
	}
	if scheme.https == nil || scheme.h2c == nil {
		t.Fatal("expected both https and h2c transports to be constructed")
	}
}

func TestClient_Close_IntegrationWithSpreadTransport(t *testing.T) {
	// Wire Client.Close end-to-end through the real transport stack so a sweep
	// actually reaches pooled entries via the delegation chain:
	//   Client.Close
	//     -> streamingClient.Transport (userAgentRoundTripper).CloseIdleConnections
	//     -> spreadTransport.CloseIdleConnections
	//     -> schemeAwareTransport.CloseIdleConnections
	//     -> (*http2.Transport).CloseIdleConnections
	// We assert the sweep reaches the in-pool entry without panicking. With a
	// real http2 transport against no live server, the sweep is a no-op, so we
	// additionally verify by replacing the spread transport with an entry that
	// holds a counting transport and confirming CloseIdleConnections fires.
	counting := &closeIdleCountingTransport{}
	pool := newSpreadTransport(func() http.RoundTripper { return counting })
	c := &Client{
		streamingClient: &http.Client{
			Transport: userAgentRoundTripper{base: pool, userAgent: "test"},
		},
		httpClient: &http.Client{Transport: &closeIdleCountingTransport{}},
	}

	// Put one entry in the pool. The entry's transport is `counting`.
	req := mustNewStreamingRequest(t, "https://example.com/")
	resp, err := pool.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	defer resp.Body.Close()

	c.Close()
	if got := counting.CloseIdleCalls(); got != 1 {
		t.Fatalf("expected Client.Close to sweep the in-pool entry once, got %d", got)
	}
}

// StreamClient creation is independent of transport lifecycle; this test pins
// that constructing a Client wires the streaming and unary transports so that
// Close reclaims every transport-level idle connection without leaking.
func TestClient_Construction_WiresTransportsForClose(t *testing.T) {
	c := New("token", nil)
	if c.streamingClient == nil {
		t.Fatal("expected streamingClient to be wired")
	}
	if c.httpClient == nil {
		t.Fatal("expected httpClient to be wired")
	}
	// Close must be callable on a freshly-constructed client without panicking
	// and must reach a userAgentRoundTripper that delegates to a spreadTransport.
	c.Close()

	// Verify the streaming transport is the expected chain so Close can sweep.
	uaRT, ok := c.streamingClient.Transport.(userAgentRoundTripper)
	if !ok {
		t.Fatalf("expected streaming transport to be userAgentRoundTripper, got %T", c.streamingClient.Transport)
	}
	if _, ok := uaRT.base.(*spreadTransport); !ok {
		t.Fatalf("expected userAgentRoundTripper base to be *spreadTransport, got %T", uaRT.base)
	}
}
