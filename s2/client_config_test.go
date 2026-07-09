package s2

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type basinHeaderRoundTripper struct {
	header   string
	url      string
	requests int
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

func TestClient_CustomHTTPClientPreserved(t *testing.T) {
	customTransport := &basinHeaderRoundTripper{}
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
			client := New("token", &ClientOptions{BaseURL: tc.baseURL})
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
			client := New("token", &ClientOptions{MakeBasinBaseURL: tc.makeBasinBaseURL})
			if client.Basin("demo").baseURL != tc.want {
				t.Fatalf("expected basin base URL %q, got %q", tc.want, client.Basin("demo").baseURL)
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

	_, err := client.Basin("demo").Streams.List(ctx, nil)
	if err != nil {
		t.Fatalf("list streams failed: %v", err)
	}
	if rt.requests != 1 {
		t.Fatalf("expected 1 request, got %d", rt.requests)
	}
	if rt.header != "demo" {
		t.Fatalf("expected basin header %q, got %q", "demo", rt.header)
	}
	if rt.url != "http://shared.test/v1/streams" {
		t.Fatalf("expected request URL %q, got %q", "http://shared.test/v1/streams", rt.url)
	}
}
