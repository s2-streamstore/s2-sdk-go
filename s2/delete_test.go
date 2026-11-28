package s2

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestDeleteSucceedsOnOwnNotFoundAfterRetry(t *testing.T) {
	newClient := func(responses ...*http.Response) *Client {
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodDelete {
				t.Fatalf("expected DELETE, got %s", req.Method)
			}
			resp := responses[0]
			responses = responses[1:]
			return resp, nil
		})
		return New("token", &ClientOptions{
			BaseURL:          "http://account.test",
			MakeBasinBaseURL: testBasinBaseURL,
			HTTPClient:       &http.Client{Transport: rt, Timeout: time.Second},
			RetryConfig:      &RetryConfig{MaxAttempts: 2, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
		})
	}
	notFound := func(code string) *http.Response {
		return jsonResponse(http.StatusNotFound, "", `{"code":"`+code+`","message":"gone"}`)
	}
	unavailable := func() *http.Response {
		return jsonResponse(http.StatusServiceUnavailable, "", `{"code":"unavailable","message":"retry"}`)
	}
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		code   string
		delete func(*Client) error
	}{
		{"basin", "basin_not_found", func(c *Client) error { return c.Basins.Delete(ctx, "my-basin") }},
		{"stream", "stream_not_found", func(c *Client) error { return c.Basin("my-basin").Streams.Delete(ctx, "s") }},
		{"access token", "access_token_not_found", func(c *Client) error {
			return c.AccessTokens.Revoke(ctx, RevokeAccessTokenArgs{ID: "token-id"})
		}},
		{"network policy", "network_policy_not_found", func(c *Client) error { return c.NetworkPolicies.Delete(ctx, "policy") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.delete(newClient(unavailable(), notFound(tc.code))); err != nil {
				t.Fatalf("expected %s after a retry to succeed, got %v", tc.code, err)
			}
		})
	}

	t.Run("another resource's not-found fails", func(t *testing.T) {
		client := newClient(notFound("basin_not_found"))
		if err := client.Basin("my-basin").Streams.Delete(ctx, "s"); err == nil {
			t.Fatal("expected basin_not_found on a stream delete to fail")
		}
	})

	t.Run("own not-found code with another status fails", func(t *testing.T) {
		client := newClient(jsonResponse(http.StatusConflict, "", `{"code":"basin_not_found","message":"gone"}`))
		if err := client.Basins.Delete(ctx, "my-basin"); err == nil {
			t.Fatal("expected basin_not_found with a 409 to fail")
		}
	})
}
