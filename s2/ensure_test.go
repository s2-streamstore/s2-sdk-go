package s2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, provisionResult ProvisionResult, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if provisionResult != "" {
		header.Set(provisionResultHeader, string(provisionResult))
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestBasinsEnsure(t *testing.T) {
	createOnAppend := false
	scope := BasinScopeAwsUsWest2

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", req.Method)
		}
		if req.URL.String() != "http://account.test/v1/basins/ensure-basin" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}

		var payload ensureBasinRequest
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload.Scope == nil || *payload.Scope != scope {
			t.Fatalf("unexpected scope: %#v", payload.Scope)
		}
		if payload.Config == nil || payload.Config.CreateStreamOnAppend == nil || *payload.Config.CreateStreamOnAppend {
			t.Fatalf("unexpected config: %#v", payload.Config)
		}

		return jsonResponse(http.StatusOK, ProvisionResultNoop, `{"name":"ensure-basin","scope":"aws:us-west-2"}`), nil
	})

	client := New("token", &ClientOptions{
		BaseURL:    "http://account.test",
		HTTPClient: &http.Client{Transport: rt, Timeout: time.Second},
	})

	resp, err := client.Basins.Ensure(context.Background(), EnsureBasinArgs{
		Basin:  "ensure-basin",
		Config: &BasinConfig{CreateStreamOnAppend: &createOnAppend},
		Scope:  &scope,
	})
	if err != nil {
		t.Fatalf("ensure basin: %v", err)
	}
	if resp.Result != ProvisionResultNoop {
		t.Fatalf("expected noop result, got %q", resp.Result)
	}
	if resp.Basin.Name != "ensure-basin" {
		t.Fatalf("unexpected basin: %#v", resp.Basin)
	}
}

func TestStreamsEnsure(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", req.Method)
		}
		if req.URL.String() != "http://basin.test/v1/streams/demo" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}
		if got := req.Header.Get("s2-basin"); got != "ensure-basin" {
			t.Fatalf("expected basin header, got %q", got)
		}
		if req.Header.Get("Content-Type") != "" {
			t.Fatalf("expected no content type for nil config, got %q", req.Header.Get("Content-Type"))
		}

		return jsonResponse(http.StatusCreated, ProvisionResultCreated, `{"name":"demo"}`), nil
	})

	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: rt, Timeout: time.Second},
		MakeBasinBaseURL: func(string) string {
			return "http://basin.test"
		},
	})

	resp, err := client.Basin("ensure-basin").Streams.Ensure(context.Background(), EnsureStreamArgs{
		Stream: "demo",
	})
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	if resp.Result != ProvisionResultCreated {
		t.Fatalf("expected created result, got %q", resp.Result)
	}
	if resp.Stream.Name != "demo" {
		t.Fatalf("unexpected stream: %#v", resp.Stream)
	}
}
