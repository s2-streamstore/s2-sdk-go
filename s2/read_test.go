package s2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func newUnaryTestStreamClient(client *http.Client, maxAttempts int) *StreamClient {
	basin := &BasinClient{
		baseURL:     "http://example.com/v1",
		accessToken: "token",
		httpClient:  client,
		retryConfig: &RetryConfig{MaxAttempts: maxAttempts, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond},
	}
	return basin.Stream("test")
}

func okResponse(req *http.Request, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestUnaryReadWaitExtendsRequestTimeout(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		select {
		case <-time.After(300 * time.Millisecond):
			return okResponse(req, bytes.NewReader(nil)), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})
	stream := newUnaryTestStreamClient(&http.Client{Transport: rt, Timeout: 100 * time.Millisecond}, 1)

	if _, err := stream.Read(context.Background(), &ReadOptions{Wait: Int32(1)}); err != nil {
		t.Fatalf("long-poll read failed: %v", err)
	}
}
