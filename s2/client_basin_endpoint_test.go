package s2

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

// blockingDialerTransport returns an HTTP transport whose DialContext rejects
// any host that is not loopback. It is used to prove that basin-scoped requests
// never escape to the public cloud (*.b.s2.dev); if they did, the dialer would
// return an error and the request would fail loudly instead of silently leaking
// the access token.
func blockingDialerTransport(t *testing.T) *http.Transport {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return nil, fmt.Errorf("blocked dial to non-local host %s:%s", host, port)
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
	}
	return transport
}

type recordedRequest struct {
	path   string
	authz  string
	basin  string
	method string
}

func newRecordingServer() (*httptest.Server, *[]recordedRequest, *sync.Mutex) {
	var (
		mu       sync.Mutex
		requests []recordedRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, recordedRequest{
			path:   r.URL.Path,
			authz:  r.Header.Get("Authorization"),
			basin:  r.Header.Get("s2-basin"),
			method: r.Method,
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"basins": []any{}, "streams": []any{}, "has_more": false})
	}))
	return srv, &requests, &mu
}

// TestClient_BaseURLOnly_BasinRequestsHitCustomEndpoint is the regression test
// for the bug: when only ClientOptions.BaseURL is set (no MakeBasinBaseURL),
// basin-scoped requests were silently routed to https://<basin>.b.s2.dev/v1
// instead of the configured endpoint. This test starts a local server and uses
// a dialer that blocks any non-loopback host, so a regression would fail the
// basin-scoped request rather than leak the token off-host.
func TestClient_BaseURLOnly_BasinRequestsHitCustomEndpoint(t *testing.T) {
	srv, requestsPtr, mu := newRecordingServer()
	defer srv.Close()

	client := New("test-token", &ClientOptions{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Transport: blockingDialerTransport(t), Timeout: 5 * time.Second},
	})

	// Account-scoped request must hit the local endpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Basins.List(ctx, nil); err != nil {
		t.Fatalf("account-scoped Basins.List failed: %v", err)
	}

	// Basin-scoped request must hit the same local endpoint, not *.b.s2.dev.
	if _, err := client.Basin("my-basin").Streams.List(ctx, nil); err != nil {
		t.Fatalf("basin-scoped Streams.List failed (likely misrouted to s2.dev): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	requests := *requestsPtr
	want := []recordedRequest{
		{path: "/v1/basins", authz: "Bearer test-token", basin: "", method: http.MethodGet},
		{path: "/v1/streams", authz: "Bearer test-token", basin: "my-basin", method: http.MethodGet},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("expected requests %+v, got %+v", want, requests)
	}
}

// TestClient_BaseURLOnly_BasinUsesSharedEndpoint checks the constructed
// BasinClient.baseURL and includeBasinHeader flag for the BaseURL-only case.
func TestClient_BaseURLOnly_BasinUsesSharedEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "explicit path", baseURL: "http://shared.test/api", want: "http://shared.test/api"},
		{name: "infers /v1", baseURL: "shared.test:8443", want: "https://shared.test:8443/v1"},
		{name: "localhost http", baseURL: "localhost:8080", want: "http://localhost:8080/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := New("token", &ClientOptions{BaseURL: tc.baseURL})
			if got := client.Basin("demo").baseURL; got != tc.want {
				t.Fatalf("expected basin baseURL %q, got %q", tc.want, got)
			}
			if !client.includeBasinHeader {
				t.Fatal("expected s2-basin header to be enabled for BaseURL-only configuration")
			}
		})
	}
}

// TestClient_DefaultConfig_BasinUsesCloudSubdomain_NoHeader guards against
// regressing the default cloud behavior: basins are addressed per-subdomain and
// no s2-basin header is sent.
func TestClient_DefaultConfig_BasinUsesCloudSubdomain_NoHeader(t *testing.T) {
	client := New("token", nil)
	if got, want := client.baseURL, DefaultBaseURL; got != want {
		t.Fatalf("expected account baseURL %q, got %q", want, got)
	}
	if got, want := client.Basin("demo").baseURL, "https://demo.b.s2.dev/v1"; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be disabled for the default cloud configuration")
	}
	if got := client.Basin("demo").basinHeaderValue(); got != "" {
		t.Fatalf("expected empty basin header value, got %q", got)
	}
}

// TestClient_MakeBasinBaseURLOnly_PreservesBehavior ensures the existing
// MakeBasinBaseURL-only configuration is unchanged.
func TestClient_MakeBasinBaseURLOnly_PreservesBehavior(t *testing.T) {
	client := New("token", &ClientOptions{
		MakeBasinBaseURL: func(basin string) string {
			return fmt.Sprintf("https://%s.cell.test", basin)
		},
	})
	if got, want := client.baseURL, DefaultBaseURL; got != want {
		t.Fatalf("expected account baseURL %q, got %q", want, got)
	}
	if got, want := client.Basin("demo").baseURL, "https://demo.cell.test/v1"; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if !client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be enabled when MakeBasinBaseURL is provided")
	}
}

// TestClient_BothOptions_PreservesBehavior ensures setting both BaseURL and
// MakeBasinBaseURL keeps the explicitly-provided basin endpoint.
func TestClient_BothOptions_PreservesBehavior(t *testing.T) {
	client := New("token", &ClientOptions{
		BaseURL: "http://account.test",
		MakeBasinBaseURL: func(basin string) string {
			return fmt.Sprintf("https://%s.basin.test", basin)
		},
	})
	if got, want := client.baseURL, "http://account.test/v1"; got != want {
		t.Fatalf("expected account baseURL %q, got %q", want, got)
	}
	if got, want := client.Basin("demo").baseURL, "https://demo.basin.test/v1"; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if !client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be enabled when MakeBasinBaseURL is provided")
	}
}

// TestNewFromEnvironment_AccountEndpointOnly_BasinUsesAccountEndpoint verifies
// the env path: setting only S2_ACCOUNT_ENDPOINT routes basin traffic through
// the account endpoint with the s2-basin header instead of the public cloud.
func TestNewFromEnvironment_AccountEndpointOnly_BasinUsesAccountEndpoint(t *testing.T) {
	srv, _, _ := newRecordingServer()
	defer srv.Close()

	t.Setenv("S2_ACCESS_TOKEN", "env-token")
	t.Setenv("S2_ACCOUNT_ENDPOINT", srv.URL)
	t.Setenv("S2_BASIN_ENDPOINT", "")

	client := NewFromEnvironment(nil)

	want := normalizeBaseURL(srv.URL)
	if got := client.Basin("demo").baseURL; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if !client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be enabled when only S2_ACCOUNT_ENDPOINT is set")
	}
}

// TestNewFromEnvironment_BothEndpoints_PreservesBehavior ensures that when both
// env endpoints are set, the basin endpoint is taken from S2_BASIN_ENDPOINT.
func TestNewFromEnvironment_BothEndpoints_PreservesBehavior(t *testing.T) {
	t.Setenv("S2_ACCESS_TOKEN", "env-token")
	t.Setenv("S2_ACCOUNT_ENDPOINT", "http://account.test")
	t.Setenv("S2_BASIN_ENDPOINT", "http://{basin}.basin.test")

	client := NewFromEnvironment(nil)

	if got, want := client.baseURL, "http://account.test/v1"; got != want {
		t.Fatalf("expected account baseURL %q, got %q", want, got)
	}
	if got, want := client.Basin("demo").baseURL, "http://demo.basin.test/v1"; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if !client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be enabled when S2_BASIN_ENDPOINT is set")
	}
}

// TestNewFromEnvironment_BasinEndpointOnly_PreservesBehavior ensures that when
// only S2_BASIN_ENDPOINT is set, account traffic uses the cloud default and
// basin traffic uses the configured basin endpoint.
func TestNewFromEnvironment_BasinEndpointOnly_PreservesBehavior(t *testing.T) {
	t.Setenv("S2_ACCESS_TOKEN", "env-token")
	t.Setenv("S2_BASIN_ENDPOINT", "http://{basin}.basin.test")
	t.Setenv("S2_ACCOUNT_ENDPOINT", "")

	client := NewFromEnvironment(nil)

	if got, want := client.baseURL, DefaultBaseURL; got != want {
		t.Fatalf("expected account baseURL %q, got %q", want, got)
	}
	if got, want := client.Basin("demo").baseURL, "http://demo.basin.test/v1"; got != want {
		t.Fatalf("expected basin baseURL %q, got %q", want, got)
	}
	if !client.includeBasinHeader {
		t.Fatal("expected s2-basin header to be enabled when S2_BASIN_ENDPOINT is set")
	}
}
