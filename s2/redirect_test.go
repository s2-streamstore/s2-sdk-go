package s2

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"google.golang.org/protobuf/proto"
)

func TestCheckRedirect(t *testing.T) {
	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}
	wantKey := key.headerValue()

	cases := []struct {
		name     string
		prev     string
		next     string
		wantKeep bool
	}{
		{"cross host strips", "https://example.com/v1/a", "https://other.test/v1/a", false},
		{"cross port strips", "http://example.com:8080/v1/a", "http://example.com:9090/v1/a", false},
		{"subdomain strips", "https://example.com/v1/a", "https://sub.example.com/v1/a", false},
		{"same origin preserves", "https://example.com/v1/a", "https://example.com/v1/b", true},
		{"https default port same origin", "https://example.com/v1/a", "https://example.com:443/v1/b", true},
		{"explicit same port preserves", "http://example.com:8080/v1/a", "http://example.com:8080/v1/b", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev, err := url.Parse(tc.prev)
			if err != nil {
				t.Fatalf("parse prev: %v", err)
			}
			next, err := url.Parse(tc.next)
			if err != nil {
				t.Fatalf("parse next: %v", err)
			}
			req := &http.Request{URL: next, Header: http.Header{}}
			req.Header.Set(s2EncryptionKeyHeader, wantKey)

			if err := checkRedirect(req, []*http.Request{{URL: prev}}); err != nil {
				t.Fatalf("checkRedirect returned error: %v", err)
			}

			got := req.Header.Get(s2EncryptionKeyHeader)
			if tc.wantKeep {
				if got != wantKey {
					t.Fatalf("expected encryption key preserved %q, got %q", wantKey, got)
				}
			} else {
				if got != "" {
					t.Fatalf("expected encryption key stripped, got %q", got)
				}
			}
		})
	}

	t.Run("empty via is no-op", func(t *testing.T) {
		next, _ := url.Parse("https://other.test/v1/a")
		req := &http.Request{URL: next, Header: http.Header{}}
		req.Header.Set(s2EncryptionKeyHeader, wantKey)
		if err := checkRedirect(req, nil); err != nil {
			t.Fatalf("checkRedirect returned error: %v", err)
		}
		if got := req.Header.Get(s2EncryptionKeyHeader); got != wantKey {
			t.Fatalf("empty via should not strip, got %q", got)
		}
	})

	t.Run("absent header is no-op", func(t *testing.T) {
		prev, _ := url.Parse("https://example.com/v1/a")
		next, _ := url.Parse("https://other.test/v1/a")
		req := &http.Request{URL: next, Header: http.Header{}}
		if err := checkRedirect(req, []*http.Request{{URL: prev}}); err != nil {
			t.Fatalf("checkRedirect returned error: %v", err)
		}
		if got := req.Header.Get(s2EncryptionKeyHeader); got != "" {
			t.Fatalf("expected no header, got %q", got)
		}
	})
}

func TestUnaryClientRedirectStripsEncryptionKey(t *testing.T) {
	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}
	wantKey := key.headerValue()

	t.Run("cross host strips encryption key", func(t *testing.T) {
		var mu sync.Mutex
		var redirectHeader, targetHeader http.Header

		// Bind the target on a distinct loopback address so the redirect
		// crosses hosts (127.0.0.1 -> 127.0.0.2) without relying on DNS.
		targetLn, err := net.Listen("tcp", "127.0.0.2:0")
		if err != nil {
			t.Fatalf("listen 127.0.0.2: %v", err)
		}
		target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			targetHeader = r.Header.Clone()
			mu.Unlock()
			data, _ := proto.Marshal(&pb.ReadBatch{})
			w.Header().Set("Content-Type", "application/protobuf")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		}))
		target.Listener.Close()
		target.Listener = targetLn
		target.Start()
		defer target.Close()

		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			redirectHeader = r.Header.Clone()
			mu.Unlock()
			http.Redirect(w, r, target.URL+"/v1/streams/test/records", http.StatusFound)
		}))
		defer redirector.Close()

		client := New("token", &ClientOptions{
			MakeBasinBaseURL: func(string) string { return redirector.URL },
		})
		basin := client.Basin("demo")
		stream := basin.StreamWithOptions("test", &StreamOptions{EncryptionKey: &key})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := stream.Read(ctx, &ReadOptions{Count: Uint64(1)}); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		mu.Lock()
		gotRedirect := redirectHeader.Clone()
		gotTarget := targetHeader.Clone()
		mu.Unlock()

		if got := gotRedirect.Get(s2EncryptionKeyHeader); got != wantKey {
			t.Fatalf("expected original request to carry encryption key %q, got %q", wantKey, got)
		}
		if got := gotTarget.Get(s2EncryptionKeyHeader); got != "" {
			t.Fatalf("cross-host redirect forwarded encryption key %q to target", got)
		}
		// Guarantee #5: Go's built-in Authorization stripping on cross-host
		// redirects is preserved by the new policy (it only deletes the
		// encryption-key header and returns nil).
		if got := gotTarget.Get("Authorization"); got != "" {
			t.Fatalf("cross-host redirect forwarded Authorization %q to target; Go default stripping regressed", got)
		}
	})

	t.Run("same origin preserves encryption key", func(t *testing.T) {
		var mu sync.Mutex
		var finalHeader http.Header

		var redirector *httptest.Server
		redirector = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/final" {
				mu.Lock()
				finalHeader = r.Header.Clone()
				mu.Unlock()
				data, _ := proto.Marshal(&pb.ReadBatch{})
				w.Header().Set("Content-Type", "application/protobuf")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
			http.Redirect(w, r, redirector.URL+"/v1/final", http.StatusFound)
		}))
		defer redirector.Close()

		client := New("token", &ClientOptions{
			MakeBasinBaseURL: func(string) string { return redirector.URL },
		})
		basin := client.Basin("demo")
		stream := basin.StreamWithOptions("test", &StreamOptions{EncryptionKey: &key})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := stream.Read(ctx, &ReadOptions{Count: Uint64(1)}); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		mu.Lock()
		got := finalHeader.Clone()
		mu.Unlock()
		if v := got.Get(s2EncryptionKeyHeader); v != wantKey {
			t.Fatalf("same-origin redirect should preserve encryption key %q, got %q", wantKey, v)
		}
	})
}

// redirectTransport simulates a 3xx redirect chain without dialing any real
// host. On the first request it returns a 302 to targetURL; on every subsequent
// request it records the received s2-encryption-key header (so the test can
// assert whether key material was forwarded to the redirect target) and
// returns a 200 with an empty body.
type redirectTransport struct {
	mu        sync.Mutex
	first     bool
	targetURL string
	gotKeyCh  chan string
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	isFirst := t.first
	if isFirst {
		t.first = false
	}
	t.mu.Unlock()

	if isFirst {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{t.targetURL}},
			Body:       io.NopCloser(&bytes.Buffer{}),
			Request:    req,
		}, nil
	}

	key := req.Header.Get(s2EncryptionKeyHeader)
	select {
	case t.gotKeyCh <- key:
	default:
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"s2s/proto"}},
		Body:       io.NopCloser(&bytes.Buffer{}),
		Request:    req,
	}, nil
}

// newRedirectStreamingClient builds the SDK's streaming client (which carries
// the production CheckRedirect policy) with its transport swapped for a
// redirectTransport, so a redirect can be exercised end-to-end without a real
// HTTP/2 server.
func newRedirectStreamingClient(t *testing.T, targetURL string) (*http.Client, <-chan string) {
	t.Helper()
	gotKeyCh := make(chan string, 1)
	streamingClient := createStreamingClient(time.Second)
	streamingClient.Transport = &redirectTransport{
		first:     true,
		targetURL: targetURL,
		gotKeyCh:  gotKeyCh,
	}
	return streamingClient, gotKeyCh
}

func TestStreamingReadSessionRedirectStripsEncryptionKey(t *testing.T) {
	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}

	streamingClient, gotKeyCh := newRedirectStreamingClient(t, "http://cross-host.test/v1/streams/test/records")

	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
	}
	basin.client = &Client{streamingClient: streamingClient}
	stream := basin.StreamWithOptions("test", &StreamOptions{EncryptionKey: &key})

	reader := &streamReader{streamClient: stream, logger: stream.logger}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reader.runOnce(ctx, &ReadOptions{Count: Uint64(1)}); err != nil {
		t.Fatalf("runOnce failed: %v", err)
	}

	select {
	case got := <-gotKeyCh:
		if got != "" {
			t.Fatalf("cross-host redirect forwarded encryption key %q to target, want empty", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for redirect target request")
	}
}

func TestStreamingAppendSessionRedirectStripsEncryptionKey(t *testing.T) {
	key, err := NewEncryptionKeyFromBytes(testEncryptionKeyBytes)
	if err != nil {
		t.Fatalf("new encryption key failed: %v", err)
	}

	streamingClient, gotKeyCh := newRedirectStreamingClient(t, "http://cross-host.test/v1/streams/test/records")

	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
	}
	basin.client = &Client{streamingClient: streamingClient}
	stream := basin.StreamWithOptions("test", &StreamOptions{EncryptionKey: &key})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := stream.createAppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("create append session failed: %v", err)
	}
	defer session.Close()

	select {
	case got := <-gotKeyCh:
		if got != "" {
			t.Fatalf("cross-host redirect forwarded encryption key %q to target, want empty", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for redirect target request")
	}
}
