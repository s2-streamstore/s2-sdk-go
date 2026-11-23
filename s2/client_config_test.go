package s2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
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

	if got := New("token", &ClientOptions{BaseURL: DefaultBaseURL}).Basin("demo").baseURL; got != "https://demo.b.s2.dev/v1" {
		t.Fatalf("expected default basin endpoint with explicit DefaultBaseURL, got %q", got)
	}
}

func TestClientConfig_SpecConformance(t *testing.T) {
	t.Run("C1 rejects empty and control-byte access tokens", func(t *testing.T) {
		for _, tok := range []string{"", "bad\ntoken", "bad\rtoken", "bad\x00token"} {
			func() {
				defer func() {
					err, _ := recover().(*S2Error)
					if err == nil || err.Code != validationErrorCode {
						t.Errorf("token %q: expected VALIDATION panic, got %v", tok, err)
					}
				}()
				New(tok, nil)
			}()
		}
	})

	t.Run("C3 rejects empty host, trailing colon, malformed IPv6, and ftp scheme", func(t *testing.T) {
		for _, raw := range []string{"https://", "http://example.com:", "http://[not-ipv6]", "ftp://example.com"} {
			_, err := newEndpointTemplate(raw)
			var s2Err *S2Error
			if !errors.As(err, &s2Err) || s2Err.Code != validationErrorCode {
				t.Errorf("endpoint %q: expected ValidationError, got %v", raw, err)
			}
		}
	})

	t.Run("C10 snapshots RetryConfig and MakeBasinBaseURL", func(t *testing.T) {
		retryCfg := &RetryConfig{MaxAttempts: 5, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}
		opts := &ClientOptions{
			BaseURL:     "http://account.test",
			RetryConfig: retryCfg,
			MakeBasinBaseURL: func(basin string) string {
				return "http://first.test/" + basin
			},
		}
		client := New("token", opts)
		retryCfg.MaxAttempts = 1
		opts.MakeBasinBaseURL = func(string) string {
			return "http://mutated.test"
		}
		if client.retryConfig.MaxAttempts != 5 {
			t.Fatalf("RetryConfig mutated after New: got MaxAttempts=%d, want 5", client.retryConfig.MaxAttempts)
		}
		if got := client.Basin("demo").baseURL; got != "http://first.test/demo" {
			t.Fatalf("MakeBasinBaseURL mutated after New: got %q", got)
		}
	})

	t.Run("C6 JSON requests compress body and send matching Accept-Encoding", func(t *testing.T) {
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
	})

	t.Run("D4 validates metrics timestamps and access token expires_at year", func(t *testing.T) {
		client := New("token", &ClientOptions{HTTPClient: &http.Client{Transport: &stubRoundTripper{}}})
		neg := int64(-1)
		over := int64(math.MaxUint32) + 1
		for _, ts := range []*int64{&neg, &over} {
			_, err := client.Metrics.Account(context.Background(), &AccountMetricsArgs{
				Set:   AccountMetricSetActiveBasins,
				Start: ts,
			})
			var s2Err *S2Error
			if !errors.As(err, &s2Err) || s2Err.Code != validationErrorCode {
				t.Errorf("metrics start=%d: expected ValidationError, got %v", *ts, err)
			}
		}
		future := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		_, err := client.AccessTokens.Issue(context.Background(), IssueAccessTokenArgs{
			ID:        "tok-1",
			Scope:     AccessTokenScope{},
			ExpiresAt: &future,
		})
		var s2Err *S2Error
		if !errors.As(err, &s2Err) || s2Err.Code != validationErrorCode {
			t.Errorf("expires_at year 10000: expected ValidationError, got %v", err)
		}
	})
}
