package s2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// lpClient builds an s2.Client whose basin endpoint points at the given
// httptest server. MakeBasinBaseURL is required so basin-scoped requests
// (Stream.Read) hit the test server instead of the default basin endpoint.
func lpClient(serverURL string, opts *s2.ClientOptions) *s2.Client {
	if opts == nil {
		opts = &s2.ClientOptions{}
	}
	opts.BaseURL = serverURL
	opts.MakeBasinBaseURL = func(string) string { return serverURL }
	return s2.New("test-token", opts)
}

// lpTestCtx returns a generously-bounded context so a bug cannot hang the
// whole test suite. The context deadline is intentionally larger than any
// deadline derived from Wait below.
func lpTestCtx(t *testing.T) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx, cancel
}

// emptyBatchServer replies with a valid (empty) protobuf ReadBatch after the
// given delay and records the number of requests received. An empty body is a
// valid encoding of an empty ReadBatch message.
func emptyBatchServer(hits *int32, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/protobuf")
		w.WriteHeader(http.StatusOK)
	}))
}

// hangServer accepts the request but never responds until the client
// disconnects (its request context is cancelled), modelling a server that
// hangs past the long-poll deadline.
func hangServer(hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		<-r.Context().Done()
	}))
}

// TestRead_LongPollSucceeds_WaitExceedsRequestTimeout pins the long-poll
// invariant of unary Read: a non-streaming read with Wait > 0 must wait on the
// server up to the Wait budget even when that exceeds the configured
// RequestTimeout. The server delay (1s) is greater than RequestTimeout (200ms)
// yet within Wait+RequestTimeout, so the read must succeed in a single attempt.
//
// This reproduces the reported bug (a long-poll read canceled by the unary
// http.Client.Timeout before the server can respond) with a shrunken timeout
// so the test fails fast (~200ms) without the fix instead of hanging for ~15s.
func TestRead_LongPollSucceeds_WaitExceedsRequestTimeout(t *testing.T) {
	var hits int32
	server := emptyBatchServer(&hits, 1000*time.Millisecond)
	defer server.Close()

	c := lpClient(server.URL, &s2.ClientOptions{
		RequestTimeout: 200 * time.Millisecond,
		RetryConfig:    &s2.RetryConfig{MaxAttempts: 1},
	})

	ctx, _ := lpTestCtx(t)
	start := time.Now()
	batch, err := c.Basin("b").Stream("s").Read(ctx, &s2.ReadOptions{
		Wait: s2.Ptr[int32](3),
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected long-poll read to succeed, got err=%v (elapsed=%s hits=%d)",
			err, elapsed, atomic.LoadInt32(&hits))
	}
	if batch == nil {
		t.Fatal("expected non-nil batch")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 server hit, got %d", got)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("expected read to complete promptly, elapsed=%s", elapsed)
	}
}

// TestReadWithoutWait_StillBoundedAndRetries guards against a naive "fix" that
// lifts the request timeout for all reads: with Wait unset, RequestTimeout
// must still bound the request and net.Error timeouts must still be retried.
func TestReadWithoutWait_StillBoundedAndRetries(t *testing.T) {
	var hits int32
	server := hangServer(&hits)
	defer server.Close()

	c := lpClient(server.URL, &s2.ClientOptions{
		RequestTimeout: 100 * time.Millisecond,
		RetryConfig:    &s2.RetryConfig{MaxAttempts: 3},
	})

	ctx, _ := lpTestCtx(t)
	start := time.Now()
	_, err := c.Basin("b").Stream("s").Read(ctx, &s2.ReadOptions{
		SeqNum: s2.Ptr[uint64](0),
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error for non-long-poll read against a hanging server")
	}
	// No Wait: RequestTimeout bounds each attempt, and net.Error timeouts are
	// retried as before, so all 3 attempts fire.
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (default retry preserved), got %d (elapsed=%s)",
			got, elapsed)
	}
}

// TestRead_LongPollHang_NoRetries pins the asymmetry introduced by the fix:
// unlike the no-Wait path above, a long-poll read whose server hangs past the
// Wait+RequestTimeout budget must not waste retries on a deterministic
// timeout. The same deadline that bounds the request also bounds the retry
// loop, so only one attempt fires.
func TestRead_LongPollHang_NoRetries(t *testing.T) {
	var hits int32
	server := hangServer(&hits)
	defer server.Close()

	// Wait=1 + RequestTimeout=100ms => long-poll budget ~1.1s. 3 attempts are
	// permitted, but the deadline must cap it at a single attempt.
	c := lpClient(server.URL, &s2.ClientOptions{
		RequestTimeout: 100 * time.Millisecond,
		RetryConfig:    &s2.RetryConfig{MaxAttempts: 3},
	})

	ctx, _ := lpTestCtx(t)
	start := time.Now()
	_, err := c.Basin("b").Stream("s").Read(ctx, &s2.ReadOptions{
		Wait: s2.Ptr[int32](1),
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when the server hangs past the long-poll deadline")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 server hit (no retries on long-poll timeout), got %d (elapsed=%s)",
			got, elapsed)
	}
	// The deadline is Wait + RequestTimeout ~= 1.1s; assert we did not wait a
	// full extra Wait cycle (which a retry would incur).
	if elapsed >= 2*time.Second {
		t.Fatalf("expected the deadline to bound the call, elapsed=%s", elapsed)
	}
}
