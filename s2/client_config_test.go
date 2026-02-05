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
	requests int
}

func (r *basinHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.header = req.Header.Get("s2-basin")
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

func TestSharedBasinEndpoint_UsesBasinHeader(t *testing.T) {
	rt := &basinHeaderRoundTripper{}
	httpClient := &http.Client{Transport: rt, Timeout: 1 * time.Second}

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test/v1",
		HTTPClient: httpClient,
		MakeBasinBaseURL: func(_ string) string {
			return "http://shared.test/v1"
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
}
