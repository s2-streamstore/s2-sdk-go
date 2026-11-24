package s2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

type basinHeaderRoundTripper struct {
	header   string
	url      string
	requests int
}

type stubRoundTripper struct{}

func (*stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("unexpected request")
}

func (r *basinHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.header = req.Header.Get("s2-basin")
	r.url = req.URL.String()
	body := io.NopCloser(strings.NewReader(`{"streams":[],"has_more":false}`))
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestClient_DefaultBaseURL(t *testing.T) {
	client := New("token", nil)
	if client.baseURL != DefaultBaseURL {
		t.Fatalf("expected base URL %q, got %q", DefaultBaseURL, client.baseURL)
	}
}

func TestClient_DefaultHTTPTransport(t *testing.T) {
	const requestTimeout = 17 * time.Second
	client := New("token", &ClientOptions{
		RequestTimeout:    requestTimeout,
		ConnectionTimeout: 11 * time.Second,
	})
	defaultTransport := http.DefaultTransport.(*http.Transport)
	if client.httpClient.Timeout != requestTimeout {
		t.Fatalf("expected request timeout %s, got %s", requestTimeout, client.httpClient.Timeout)
	}

	userAgentTransport, ok := client.httpClient.Transport.(userAgentRoundTripper)
	if !ok {
		t.Fatalf("expected user-agent transport, got %T", client.httpClient.Transport)
	}

	transport, ok := userAgentTransport.base.(*http.Transport)
	if !ok {
		t.Fatalf("expected HTTP transport, got %T", userAgentTransport.base)
	}
	if transport == http.DefaultTransport {
		t.Fatal("expected the default transport to be cloned")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("expected HTTP/2 attempts to be enabled")
	}
	if transport.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Fatalf("expected %d idle connections per host, got %d", defaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if transport.MaxIdleConns != defaultTransport.MaxIdleConns {
		t.Fatalf("expected default global idle connection limit %d, got %d", defaultTransport.MaxIdleConns, transport.MaxIdleConns)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("expected default idle connection timeout %s, got %s", defaultTransport.IdleConnTimeout, transport.IdleConnTimeout)
	}
	if transport.DialContext == nil {
		t.Fatal("expected a configured dialer")
	}
}

func TestUnaryHTTPTransport_FallsBackForCustomDefault(t *testing.T) {
	var nilTransport *http.Transport
	testCases := []struct {
		name string
		base http.RoundTripper
	}{
		{name: "custom round tripper", base: &stubRoundTripper{}},
		{name: "typed nil transport", base: nilTransport},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			transport := newUnaryHTTPTransport(tc.base, 11*time.Second)

			if transport.DialContext == nil {
				t.Fatal("expected a configured dialer")
			}
			if !transport.ForceAttemptHTTP2 {
				t.Fatal("expected HTTP/2 attempts to be enabled")
			}
			if transport.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
				t.Fatalf("expected %d idle connections per host, got %d", defaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
			}
		})
	}
}

func TestClient_CustomHTTPClientPreserved(t *testing.T) {
	customTransport := &stubRoundTripper{}
	customClient := &http.Client{
		Transport: customTransport,
		Timeout:   23 * time.Second,
	}

	client := New("token", &ClientOptions{
		HTTPClient:        customClient,
		RequestTimeout:    time.Second,
		ConnectionTimeout: time.Second,
	})
	if client.httpClient != customClient {
		t.Fatal("expected the caller-provided HTTP client to be retained")
	}
	if client.httpClient.Timeout != 23*time.Second {
		t.Fatalf("expected caller-provided timeout to be retained, got %s", client.httpClient.Timeout)
	}

	userAgentTransport, ok := client.httpClient.Transport.(userAgentRoundTripper)
	if !ok {
		t.Fatalf("expected user-agent transport, got %T", client.httpClient.Transport)
	}
	if userAgentTransport.base != customTransport {
		t.Fatalf("expected caller-provided transport to be retained, got %T", userAgentTransport.base)
	}
}

func TestClient_NormalizesBaseURL(t *testing.T) {
	testCases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "infers /v1 when path missing",
			baseURL: "example.com:8443",
			want:    "https://example.com:8443/v1",
		},
		{
			name:    "preserves explicit path",
			baseURL: "https://example.com/test/here",
			want:    "https://example.com/test/here",
		},
		{
			name:    "preserves trailing slash",
			baseURL: "https://example.com/",
			want:    "https://example.com/",
		},
		{
			name:    "defaults localhost to http",
			baseURL: "localhost:8080",
			want:    "http://localhost:8080/v1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := New("token", &ClientOptions{BaseURL: tc.baseURL, MakeBasinBaseURL: testBasinBaseURL})
			if client.baseURL != tc.want {
				t.Fatalf("expected base URL %q, got %q", tc.want, client.baseURL)
			}
		})
	}
}

func TestClient_NormalizesMakeBasinBaseURL(t *testing.T) {
	testCases := []struct {
		name             string
		makeBasinBaseURL func(string) string
		want             string
	}{
		{
			name: "infers /v1 when path missing",
			makeBasinBaseURL: func(string) string {
				return "https://shared.test"
			},
			want: "https://shared.test/v1",
		},
		{
			name: "preserves explicit path",
			makeBasinBaseURL: func(string) string {
				return "https://shared.test/api"
			},
			want: "https://shared.test/api",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := New("token", &ClientOptions{BaseURL: "http://account.test", MakeBasinBaseURL: tc.makeBasinBaseURL})
			if client.Basin("my-basin").baseURL != tc.want {
				t.Fatalf("expected basin base URL %q, got %q", tc.want, client.Basin("my-basin").baseURL)
			}
		})
	}
}

func TestSharedBasinEndpoint_UsesBasinHeader(t *testing.T) {
	rt := &basinHeaderRoundTripper{}
	httpClient := &http.Client{Transport: rt, Timeout: 1 * time.Second}

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test",
		HTTPClient: httpClient,
		MakeBasinBaseURL: func(_ string) string {
			return "http://shared.test"
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.Basin("my-basin").Streams.List(ctx, nil)
	if err != nil {
		t.Fatalf("list streams failed: %v", err)
	}
	if rt.requests != 1 {
		t.Fatalf("expected 1 request, got %d", rt.requests)
	}
	if rt.header != "my-basin" {
		t.Fatalf("expected basin header %q, got %q", "my-basin", rt.header)
	}
	if rt.url != "http://shared.test/v1/streams" {
		t.Fatalf("expected request URL %q, got %q", "http://shared.test/v1/streams", rt.url)
	}
}

func TestClient_RequiresBothEndpoints(t *testing.T) {
	expectValidationPanic := func(name string, construct func()) {
		t.Helper()
		defer func() {
			err, _ := recover().(*S2Error)
			if err == nil || err.Code != "VALIDATION" {
				t.Errorf("%s: expected validation panic, got %v", name, err)
			}
		}()
		construct()
	}

	expectValidationPanic("account only", func() {
		New("token", &ClientOptions{BaseURL: "http://localhost:8080"})
	})
	expectValidationPanic("basin only", func() {
		New("token", &ClientOptions{MakeBasinBaseURL: testBasinBaseURL})
	})
	t.Setenv(envAccessToken, "token")
	t.Setenv(envAccountEndpoint, "")
	t.Setenv(envBasinEndpoint, "localhost:8080")
	expectValidationPanic("basin env only", func() { NewFromEnvironment(nil) })
	NewFromEnvironment(&ClientOptions{BaseURL: "http://localhost:9000", MakeBasinBaseURL: testBasinBaseURL})

	if got := New("token", &ClientOptions{BaseURL: DefaultBaseURL}).Basin("my-basin").baseURL; got != "https://my-basin.b.s2.dev/v1" {
		t.Fatalf("expected default basin endpoint with explicit DefaultBaseURL, got %q", got)
	}
}

func TestClient_CompressesJSONRequests(t *testing.T) {
	var gotContentEncoding, gotAcceptEncoding string
	var gotBody []byte
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotContentEncoding = req.Header.Get("Content-Encoding")
		gotAcceptEncoding = req.Header.Get("Accept-Encoding")
		var err error
		gotBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"name":"my-basin","scope":"aws:us-east-1","state":"active"}`)),
			Request:    req,
		}, nil
	})
	client := New("token", &ClientOptions{
		HTTPClient:  &http.Client{Transport: rt},
		Compression: CompressionZstd,
	})
	if _, err := client.Basins.Create(context.Background(), CreateBasinArgs{Basin: "my-basin"}); err != nil {
		t.Fatalf("create basin: %v", err)
	}
	if gotContentEncoding != "zstd" || gotAcceptEncoding != "zstd" {
		t.Fatalf("expected Content-Encoding=zstd Accept-Encoding=zstd, got %q / %q", gotContentEncoding, gotAcceptEncoding)
	}
	decompressed, err := internalframing.Decompress(gotBody, internalframing.CompressionZstd)
	if err != nil || !bytes.Contains(decompressed, []byte(`"my-basin"`)) {
		t.Fatalf("expected valid zstd JSON body, err=%v body=%q", err, decompressed)
	}
}

func TestClient_InvalidBasinNameSendsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sent atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sent.Add(1)
		return nil, errors.New("request sent to " + req.URL.Host)
	})
	t.Setenv(envAccessToken, "token")
	t.Setenv(envAccountEndpoint, "https://a.example.com")
	t.Setenv(envBasinEndpoint, "https://{basin}.b.example.com")
	client := NewFromEnvironment(&ClientOptions{
		HTTPClient:  &http.Client{Transport: transport},
		RetryConfig: &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	})
	client.streamingClient = &http.Client{Transport: transport}

	expectValidation := func(name string, err error) {
		t.Helper()
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Code != validationErrorCode {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}

	for _, name := range []string{"evil.com#", "a b", ""} {
		t.Run(name, func(t *testing.T) {
			basin := client.Basin(name)
			stream := basin.Stream("s")

			_, err := basin.Streams.List(ctx, nil)
			expectValidation("list streams", err)
			_, err = stream.CheckTail(ctx)
			expectValidation("check tail", err)
			_, err = stream.Append(ctx, &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
			expectValidation("append", err)
			_, err = stream.AppendSession(ctx, nil)
			expectValidation("append session", err)
			_, err = stream.ReadSession(ctx, nil)
			expectValidation("read session", err)
		})
	}

	if n := sent.Load(); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestClientFormattingRedactsAccessToken(t *testing.T) {
	client := New("secret-token", nil)
	basin := client.Basin("my-basin")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
		for name, value := range map[string]any{"client": client, "*client": *client, "basin": basin, "*basin": *basin} {
			if out := fmt.Sprintf(verb, value); strings.Contains(out, "secret-token") || strings.Contains(out, fmt.Sprintf("%x", "secret-token")) {
				t.Errorf("%s with %s leaks the token: %s", name, verb, out)
			}
		}
	}
}
