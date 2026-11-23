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

func TestUnaryCallRetriesTruncatedResponseBody(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		call func(*StreamClient) error
	}{
		{
			name: "proto",
			call: func(s *StreamClient) error {
				_, err := s.Read(context.Background(), &ReadOptions{Count: Uint64(1)})
				return err
			},
		},
		{
			name: "json",
			body: []byte(`{"tail":{"seq_num":1,"timestamp":2}}`),
			call: func(s *StreamClient) error {
				_, err := s.CheckTail(context.Background())
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return okResponse(req, io.MultiReader(bytes.NewReader([]byte("{")), bodyErrReader{io.ErrUnexpectedEOF})), nil
				}
				return okResponse(req, bytes.NewReader(tt.body)), nil
			})
			stream := newUnaryTestStreamClient(&http.Client{Transport: rt}, 2)

			if err := tt.call(stream); err != nil {
				t.Fatalf("expected retry to succeed, got %v", err)
			}
			if calls != 2 {
				t.Fatalf("expected 2 attempts, got %d", calls)
			}
		})
	}
}

type bodyErrReader struct{ err error }

func (r bodyErrReader) Read([]byte) (int, error) { return 0, r.err }
