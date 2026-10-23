package faulttest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// h2cServer is a shared HTTP/2 server config for the cleartext (h2c) test
// proxies, mirroring the scheme the SDK's streaming transport uses for http://.
var h2cServer = http2.Server{}

// drainingProxy responds to every append-session request with HTTP 503 and a
// JSON body decoding to the server_draining S2 error code, mimicking a server
// that is persistently draining. It speaks h2c so the SDK's HTTP/2 streaming
// transport can connect.
type drainingProxy struct {
	endpoint string
	requests atomic.Int32
	server   *httptest.Server
}

func newDrainingProxy(t *testing.T) *drainingProxy {
	t.Helper()
	p := &drainingProxy{}
	mux := http.NewServeMux()
	// The append session POSTs to {basinBaseURL}/streams/{name}/records.
	mux.HandleFunc("/v1/streams/", func(w http.ResponseWriter, req *http.Request) {
		p.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"server_draining","message":"server is draining"}`)
	})
	server := httptest.NewUnstartedServer(h2c.NewHandler(mux, &h2cServer))
	server.Start()
	p.server = server
	p.endpoint = server.URL
	t.Cleanup(func() { server.Close() })
	return p
}

// TestLiteAppendSessionPersistentServerDrainingIsBounded drives a real append
// session through the HTTP transport against a proxy that answers every
// append request with HTTP 503 server_draining. It verifies the end-to-end
// guarantees: reconnects are bounded, the inflight append resolves with
// ErrMaxAttemptsExhausted, and Close() returns.
func TestLiteAppendSessionPersistentServerDrainingIsBounded(t *testing.T) {
	proxy := newDrainingProxy(t)

	client := s2.New("ignored", &s2.ClientOptions{
		BaseURL:          proxy.endpoint,
		MakeBasinBaseURL: func(string) string { return proxy.endpoint + "/v1" },
		RequestTimeout:   2 * time.Second,
		RetryConfig: &s2.RetryConfig{
			MaxAttempts:       3,
			MinBaseDelay:      time.Millisecond,
			MaxBaseDelay:      time.Millisecond,
			AppendRetryPolicy: s2.AppendRetryPolicyAll,
		},
	})
	stream := client.Basin("drain-basin").Stream(s2.StreamName("drain-stream"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := stream.AppendSession(ctx, &s2.AppendSessionOptions{
		MaxInflightBatches: 8,
		RetryConfig: &s2.RetryConfig{
			MaxAttempts:       3,
			MinBaseDelay:      time.Millisecond,
			MaxBaseDelay:      time.Millisecond,
			AppendRetryPolicy: s2.AppendRetryPolicyAll,
		},
	})
	if err != nil {
		t.Fatalf("AppendSession: %v", err)
	}

	future, err := session.Submit(&s2.AppendInput{Records: []s2.AppendRecord{{Body: []byte("x")}}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	ack, err := ticket.Ack(ctx)
	if err == nil {
		t.Fatalf("expected Ack to fail during persistent draining, got ack=%+v", ack)
	}
	if !errors.Is(err, s2.ErrMaxAttemptsExhausted) {
		t.Fatalf("expected ErrMaxAttemptsExhausted, got %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// First drain is a free immediate reconnect, then 3 attempts consume the
	// budget; the inflight batch is failed. Bounded, not hot-looping.
	got := proxy.requests.Load()
	t.Logf("append requests seen by draining proxy: %d", got)
	if got > 10 {
		t.Fatalf("persistent draining hot-looped against the server: %d requests, want <= 10", got)
	}
	if got < 1 {
		t.Fatalf("expected at least one append request, got %d", got)
	}
}

// The transient-drain-recovery control (single drain then succeed) is covered
// by the unit tests TestAppendSession_ServerDrainingReconnectsOnceThenSucceeds
// and TestAppendSession_ReadAcksBufferedReconnect/server_draining_after_advice
// in package s2; reproducing it end-to-end requires a streaming h2c reverse
// proxy that faithfully forwards interleaved ACK frames, which is out of scope
// for this bug fix.
