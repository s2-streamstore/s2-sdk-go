package s2

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// net/http would otherwise fall back to HTTP/1.1 when the server negotiates no
// ALPN protocol, and a streaming request on HTTP/1.1 hangs: the server waits for
// the request body to end while the client waits for acks.
func TestStreamingTransportRejectsNonHTTP2Server(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server received %s request", r.Proto)
	}))
	srv.TLS = &tls.Config{NextProtos: []string{}} // non-nil so httptest leaves it empty
	srv.StartTLS()
	defer srv.Close()

	rt := newStreamingTransport(2*time.Second, &tls.Config{InsecureSkipVerify: true}, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected error, got %s response", resp.Proto)
	}
}

// Sessions that start concurrently on one pooled transport must share a single
// connection (one dial, and all streams multiplexed on it) rather than each
// dialing their own.
func TestStreamingTransportCoalescesConcurrentDials(t *testing.T) {
	const n = 12
	release := make(chan struct{})
	var inflight, peak, accepted atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inflight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			accepted.Add(1)
		}
	}
	srv.TLS = &tls.Config{
		NextProtos: []string{"h2"},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			time.Sleep(100 * time.Millisecond) // widen the window where all dials are in flight
			return nil, nil
		},
	}
	srv.StartTLS()
	defer srv.Close()

	rt := newStreamingTransport(5*time.Second, &tls.Config{InsecureSkipVerify: true}, nil)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for inflight.Load() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	if got := accepted.Load(); got != 1 {
		t.Errorf("server accepted %d connections, want 1", got)
	}
	if got := peak.Load(); got != n {
		t.Errorf("peak concurrent streams %d, want %d", got, n)
	}
}
