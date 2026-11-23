package s2

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newHTTP2TLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "expected HTTP/2", http.StatusHTTPVersionNotSupported)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func trustingTransport(servers ...*httptest.Server) *http.Transport {
	roots := x509.NewCertPool()
	for _, server := range servers {
		roots.AddCert(server.Certificate())
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
}

func streamingGet(t *testing.T, client *Client, target string) error {
	t.Helper()
	// The streaming client has no timeout; fail fast instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.streamingClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %s", resp.Status)
	}
	return nil
}

func TestStreamingClientUsesUserTLSConfig(t *testing.T) {
	server := newHTTP2TLSServer(t)

	client := New("token", &ClientOptions{HTTPClient: &http.Client{Transport: trustingTransport(server)}})
	if err := streamingGet(t, client, server.URL); err != nil {
		t.Fatalf("streaming request with user TLS config failed: %v", err)
	}

	// Without the user's roots the test certificate is not trusted.
	if err := streamingGet(t, New("token", nil), server.URL); err == nil {
		t.Fatal("expected certificate verification to fail with default TLS config")
	}
}

func TestStreamingClientUsesUserProxy(t *testing.T) {
	server := newHTTP2TLSServer(t)

	var connects atomic.Int32
	// An HTTPS proxy trusted only through the user's RootCAs.
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" || r.Header.Get("X-Proxy-Token") != "secret" {
			http.Error(w, "bad credentials", http.StatusProxyAuthRequired)
			return
		}
		connects.Add(1)
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		go func() {
			defer upstream.Close()
			io.Copy(upstream, buf)
		}()
		go func() {
			defer conn.Close()
			io.Copy(conn, upstream)
		}()
	}))
	proxy.StartTLS()
	t.Cleanup(proxy.Close)

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("user", "pass")
	transport := trustingTransport(server, proxy)
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.ProxyConnectHeader = http.Header{"X-Proxy-Token": {"secret"}}
	var connectResponses atomic.Int32
	transport.OnProxyConnectResponse = func(context.Context, *url.URL, *http.Request, *http.Response) error {
		connectResponses.Add(1)
		return nil
	}

	client := New("token", &ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err := streamingGet(t, client, server.URL); err != nil {
		t.Fatalf("streaming request through proxy failed: %v", err)
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("expected 1 CONNECT through the proxy, got %d", got)
	}
	if got := connectResponses.Load(); got != 1 {
		t.Fatalf("expected OnProxyConnectResponse to see 1 CONNECT response, got %d", got)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked, requests atomic.Int32
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(s2EncryptionKeyHeader) != "" {
			leaked.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	target.EnableHTTP2 = true
	target.StartTLS()
	t.Cleanup(target.Close)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	t.Cleanup(origin.Close)

	key, err := NewEncryptionKeyFromBytes([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	newStream := func(policy AppendRetryPolicy) *StreamClient {
		client := New("token", &ClientOptions{
			BaseURL:          origin.URL,
			MakeBasinBaseURL: func(string) string { return origin.URL },
			HTTPClient:       &http.Client{Transport: trustingTransport(origin, target)},
			RetryConfig: &RetryConfig{
				MaxAttempts:       3,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: policy,
			},
		})
		return client.Basin("test-basin").StreamWithOptions("test", &StreamOptions{EncryptionKey: &key})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A redirect fails the operation with its status, on the first attempt.
	assertRedirectError := func(name string, err error) {
		t.Helper()
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Status != http.StatusTemporaryRedirect {
			t.Errorf("%s: expected S2Error with status 307, got %v", name, err)
		}
		if got := requests.Swap(0); got != 1 {
			t.Errorf("%s: expected 1 request, got %d", name, got)
		}
	}
	stream := newStream(AppendRetryPolicyAll)
	_, err = stream.Read(ctx, &ReadOptions{Count: Uint64(1)})
	assertRedirectError("read", err)

	for _, policy := range []AppendRetryPolicy{AppendRetryPolicyAll, AppendRetryPolicyNoSideEffects} {
		_, err = newStream(policy).Append(ctx, &AppendInput{Records: []AppendRecord{{Body: []byte("hello")}}})
		assertRedirectError("append "+string(policy), err)
	}

	session, err := stream.ReadSession(ctx, &ReadOptions{Count: Uint64(1)})
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	defer session.Close()
	if session.Next() {
		t.Fatal("unexpected record")
	}
	assertRedirectError("read session", session.Err())

	if got := leaked.Load(); got != 0 {
		t.Fatalf("encryption key sent to redirect target %d times", got)
	}
}
