package s2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
)

// s2-attempt is one more than the retries consumed from the current budget,
// which resets on progress (spec T4).
func TestAttemptHeaderCountsRetriesInBudget(t *testing.T) {
	retryCfg := &RetryConfig{MaxAttempts: 3, MinBaseDelay: time.Millisecond, MaxBaseDelay: time.Millisecond}
	respond := func(req *http.Request, status int, body []byte) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}
	}

	unaryCalls := []struct {
		name string
		call func(context.Context, *Client) error
	}{
		{"unary", func(ctx context.Context, c *Client) error {
			return c.Basins.Delete(ctx, "my-basin")
		}},
		{"unary append", func(ctx context.Context, c *Client) error {
			_, err := c.Basin("my-basin").Stream("s").Append(ctx, &AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
			return err
		}},
	}
	for _, tc := range unaryCalls {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				got = append(got, req.Header.Get("s2-attempt"))
				if len(got) < 3 {
					return jsonResponse(http.StatusServiceUnavailable, "", `{"code":"unavailable","message":"retry"}`), nil
				}
				return respond(req, http.StatusOK, nil), nil
			})
			client := New("token", &ClientOptions{
				BaseURL:          "http://account.test",
				MakeBasinBaseURL: testBasinBaseURL,
				HTTPClient:       &http.Client{Transport: rt, Timeout: time.Second},
				RetryConfig:      retryCfg,
			})
			if err := tc.call(context.Background(), client); err != nil {
				t.Fatalf("call: %v", err)
			}
			if want := []string{"1", "2", "3"}; !slices.Equal(got, want) {
				t.Fatalf("s2-attempt=%v, want %v", got, want)
			}
		})
	}

	t.Run("read session resets after progress", func(t *testing.T) {
		frame := buildReadBatchFrame(t, []*pb.SequencedRecord{{SeqNum: 0, Body: []byte("a")}})
		var mu sync.Mutex
		var got []string
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			got = append(got, req.Header.Get("s2-attempt"))
			n := len(got)
			mu.Unlock()
			switch n {
			case 1:
				return respond(req, http.StatusServiceUnavailable, []byte(`{"code":"unavailable","message":"retry"}`)), nil
			case 2:
				return respond(req, http.StatusOK, frame), nil // a record, then the response ends early
			default:
				return respond(req, http.StatusBadRequest, []byte(`{"code":"invalid","message":"stop"}`)), nil
			}
		})
		streamClient := newFrameServedStreamClient(withUserAgent(rt))
		streamClient.basinClient.retryConfig = retryCfg
		session, err := streamClient.ReadSession(context.Background(), &ReadOptions{})
		if err != nil {
			t.Fatalf("open read session: %v", err)
		}
		defer session.Close()
		for session.Next() {
		}
		mu.Lock()
		defer mu.Unlock()
		if want := []string{"1", "2", "2"}; !slices.Equal(got, want) {
			t.Fatalf("s2-attempt=%v, want %v", got, want)
		}
	})

	t.Run("append session", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stream := newTestStreamClientForAppend(retryCfg)
		var got []int
		stream.appendSessionFactory = func(ctx context.Context) (*transportAppendSession, error) {
			got = append(got, attemptFromContext(ctx))
			transport := newTransportSession(stream, &signalWriteCloser{})
			transport.reportError(&S2Error{Status: 503, Code: "server_draining", Origin: "server"})
			return transport, nil
		}
		session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
		if err != nil {
			t.Fatal(err)
		}
		future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = ticket.Ack(ctx)
		_ = session.Close()
		if want := []int{1, 2, 3}; !slices.Equal(got, want) {
			t.Fatalf("attempts=%v, want %v", got, want)
		}
	})
}

// withUserAgent wraps rt the way New wraps transports, so test requests get
// the SDK's headers.
func withUserAgent(rt http.RoundTripper) http.RoundTripper {
	return userAgentRoundTripper{base: rt, userAgent: "test"}
}
