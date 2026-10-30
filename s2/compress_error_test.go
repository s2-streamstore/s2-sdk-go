package s2

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

// Shared error payload used across the compressed-error-body tests. Built
// from these vars (rather than a repeated raw string) so no single string
// literal repeats enough to trip goconst, and so the decoded S2Error is
// asserted against the exact values the server sent.
var (
	compressedErrCode    = "invalid"
	compressedErrMessage = "kaboom: stream config rejected"
)

func compressedErrPayload() []byte {
	b, _ := json.Marshal(ErrorInfo{Code: compressedErrCode, Message: compressedErrMessage})
	return b
}

// gzipBytes compresses data with gzip for use as a compressed HTTP error body.
func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// zstdBytes compresses data with zstd for use as a compressed HTTP error body.
func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := internalframing.Compress(data, internalframing.CompressionZstd)
	if err != nil {
		t.Fatalf("zstd compress: %v", err)
	}
	return out
}

// newCompressedErrorResponse builds a non-2xx HTTP response whose body is
// served verbatim and optionally advertises a Content-Encoding.
func newCompressedErrorResponse(status int, encoding string, body []byte, req *http.Request) *http.Response {
	h := make(http.Header)
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}

func newTestRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/v1/streams/test/records", nil)
	return req
}

// newCompressionStreamClient returns a StreamClient whose streaming HTTP
// transport is rt and which advertises the given compression on its requests,
// matching a real client built with Compression enabled.
func newCompressionStreamClient(rt http.RoundTripper, compression CompressionType) *StreamClient {
	streamClient := newFrameServedStreamClient(rt)
	streamClient.basinClient.compression = compression
	return streamClient
}

// assertDecompressedS2Error verifies err is an *S2Error decoded from the
// shared compressed error payload: compressed bytes must not leak through as
// the message (which would start with gzip/zstd magic bytes).
func assertDecompressedS2Error(t *testing.T, err error) {
	t.Helper()
	var s2Err *S2Error
	if !errors.As(err, &s2Err) {
		t.Fatalf("expected *S2Error, got %v", err)
	}
	if s2Err.Status != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", s2Err.Status)
	}
	if s2Err.Code != compressedErrCode {
		t.Fatalf("code: got %q want %q", s2Err.Code, compressedErrCode)
	}
	if s2Err.Message != compressedErrMessage {
		t.Fatalf("message: got %q want %q (raw compressed bytes would start with gzip/zstd magic)",
			s2Err.Message, compressedErrMessage)
	}
}

// errorStatusRoundTripper responds to every request with a fixed non-2xx
// status and an optional Content-Encoding/body. When consumeBody is true it
// drains the request body first, mirroring a real server that consumes the
// pipelined append frame before returning the error; this keeps the append
// session's request pipe from racing the error report.
type errorStatusRoundTripper struct {
	status      int
	encoding    string
	body        []byte
	consumeBody bool
	calls       int
	sawHeaders  http.Header
}

func (r *errorStatusRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	r.sawHeaders = req.Header.Clone()
	if r.consumeBody && req.Body != nil {
		buf := make([]byte, 64*1024)
		// Block until the client writes its pipelined frame, then return the
		// error so the write succeeds and the error surfaces via the read path.
		_, _ = req.Body.Read(buf)
	}
	return newCompressedErrorResponse(r.status, r.encoding, r.body, req), nil
}

func TestDecompressBody(t *testing.T) {
	payload := compressedErrPayload()

	t.Run("no encoding returns body untouched", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		got, err := decompressBody(resp, payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("body changed: got %q want %q", got, payload)
		}
	})

	t.Run("gzip", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "gzip")
		got, err := decompressBody(resp, gzipBytes(t, payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("decompressed body mismatch: got %q want %q", got, payload)
		}
	})

	t.Run("zstd", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "zstd")
		got, err := decompressBody(resp, zstdBytes(t, payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("decompressed body mismatch: got %q want %q", got, payload)
		}
	})

	t.Run("gzip uppercase casing", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "GZIP")
		got, err := decompressBody(resp, gzipBytes(t, payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("uppercase Content-Encoding not decompressed: got %q want %q", got, payload)
		}
	})

	t.Run("multi token gzip first", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "gzip, zstd")
		got, err := decompressBody(resp, gzipBytes(t, payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("multi-token Content-Encoding not decompressed: got %q want %q", got, payload)
		}
	})

	t.Run("unknown encoding returns body untouched", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "br")
		got, err := decompressBody(resp, payload)
		if err != nil {
			t.Fatalf("unexpected error for unknown encoding: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("body changed for unknown encoding: got %q want %q", got, payload)
		}
	})

	t.Run("corrupt gzip returns error", func(t *testing.T) {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Encoding", "gzip")
		_, err := decompressBody(resp, []byte("definitely not gzip"))
		if err == nil {
			t.Fatal("expected error for corrupt gzip body, got nil")
		}
	})
}

func TestParseHTTPErrorDecompresses(t *testing.T) {
	payload := compressedErrPayload()

	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"plain json", "", payload},
		{"gzip", "gzip", gzipBytes(t, payload)},
		{"zstd", "zstd", zstdBytes(t, payload)},
		{"gzip uppercase", "GZIP", gzipBytes(t, payload)},
		{"gzip with whitespace", " gzip ", gzipBytes(t, payload)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := newCompressedErrorResponse(http.StatusBadRequest, tc.encoding, tc.body, newTestRequest())
			assertDecompressedS2Error(t, parseHTTPError(resp))
		})
	}

	t.Run("corrupt gzip returns decompress error", func(t *testing.T) {
		resp := newCompressedErrorResponse(http.StatusBadRequest, "gzip", []byte("not gzip"), newTestRequest())
		err := parseHTTPError(resp)
		if err == nil {
			t.Fatal("expected decompress error, got nil")
		}
		if !strings.Contains(err.Error(), "decompress error response body") {
			t.Fatalf("expected a decompress error response body message, got %v", err)
		}
	})

	t.Run("body read error short circuits before decompression", func(t *testing.T) {
		resp := newCompressedErrorResponse(http.StatusBadRequest, "gzip", nil, newTestRequest())
		resp.Body = &readErrorCloser{err: errors.New("read boom")}
		err := parseHTTPError(resp)

		var s2Err *S2Error
		if !errors.As(err, &s2Err) {
			t.Fatalf("expected *S2Error, got %v", err)
		}
		if s2Err.Origin != "network" {
			t.Fatalf("expected network origin for body read error, got %q", s2Err.Origin)
		}
	})
}

type readErrorCloser struct{ err error }

func (r *readErrorCloser) Read(p []byte) (int, error) { return 0, r.err }
func (r *readErrorCloser) Close() error               { return nil }

// TestTransportAppendSession_ConnectAndReadDecompresses verifies the append
// session's connectAndRead >= 400 path decompresses the error body before
// decoding it, the exact path that previously surfaced garbled gzip/zstd
// bytes as the S2Error message.
func TestTransportAppendSession_ConnectAndReadDecompresses(t *testing.T) {
	payload := compressedErrPayload()

	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"gzip", "gzip", gzipBytes(t, payload)},
		{"zstd", "zstd", zstdBytes(t, payload)},
		{"gzip uppercase", "GZIP", gzipBytes(t, payload)},
		{"no encoding", "", payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &errorStatusRoundTripper{
				status:   http.StatusBadRequest,
				encoding: tc.encoding,
				body:     tc.body,
			}
			stream := newCompressionStreamClient(rt, CompressionGzip)

			session := &transportAppendSession{
				streamClient: stream,
				acksCh:       make(chan *AppendAck, 1),
				errorsCh:     make(chan error, 1),
				closed:       make(chan struct{}),
			}
			if err := session.start(context.Background()); err != nil {
				t.Fatalf("start: %v", err)
			}
			defer session.Close()

			select {
			case err := <-session.errorsCh:
				if rt.sawHeaders.Get("Accept-Encoding") == "" {
					t.Fatal("append session request did not advertise Accept-Encoding")
				}
				assertDecompressedS2Error(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for connectAndRead to report the error")
			}
		})
	}
}

// TestReadSessionHTTPErrorDecompresses verifies the streaming read session
// surfaces a decompressed S2Error via the public ReadSession API when the
// server returns a compressed error body.
func TestReadSessionHTTPErrorDecompresses(t *testing.T) {
	payload := compressedErrPayload()

	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"gzip", "gzip", gzipBytes(t, payload)},
		{"zstd", "zstd", zstdBytes(t, payload)},
		{"gzip uppercase", "GZIP", gzipBytes(t, payload)},
		{"no encoding", "", payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &errorStatusRoundTripper{
				status:   http.StatusBadRequest,
				encoding: tc.encoding,
				body:     tc.body,
			}
			stream := newCompressionStreamClient(rt, CompressionGzip)

			session, err := stream.ReadSession(context.Background(), nil)
			if err != nil {
				t.Fatalf("ReadSession: %v", err)
			}
			defer session.Close()

			for session.Next() {
				t.Fatalf("unexpected record: %+v", session.Record())
			}
			assertDecompressedS2Error(t, session.Err())
		})
	}
}

// TestAppendSessionHTTPErrorDecompresses verifies that a compressed error
// body surfaces as a decoded S2Error through the public AppendSession Submit
// -> Ack path. The round tripper consumes the pipelined request frame before
// returning the error, exactly as a real S2 server does, so the error is
// reported through the read path as a plain *S2Error.
func TestAppendSessionHTTPErrorDecompresses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	payload := compressedErrPayload()

	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{"gzip", "gzip", gzipBytes(t, payload)},
		{"zstd", "zstd", zstdBytes(t, payload)},
		{"no encoding", "", payload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &errorStatusRoundTripper{
				status:      http.StatusBadRequest,
				encoding:    tc.encoding,
				body:        tc.body,
				consumeBody: true,
			}
			retryCfg := &RetryConfig{
				MaxAttempts:       1,
				MinBaseDelay:      time.Millisecond,
				MaxBaseDelay:      time.Millisecond,
				AppendRetryPolicy: AppendRetryPolicyAll,
			}
			basin := &BasinClient{
				baseURL:     "http://example.com/v1",
				accessToken: "token",
				compression: CompressionGzip,
				retryConfig: retryCfg,
			}
			basin.client = &Client{streamingClient: &http.Client{Transport: rt}}
			stream := &StreamClient{name: StreamName("test"), basinClient: basin}

			session, err := stream.AppendSession(ctx, &AppendSessionOptions{RetryConfig: retryCfg})
			if err != nil {
				t.Fatalf("AppendSession: %v", err)
			}
			defer session.Close()

			future, err := session.Submit(&AppendInput{Records: []AppendRecord{{Body: []byte("x")}}})
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			ticket, err := future.Wait(ctx)
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}

			_, ackErr := ticket.Ack(ctx)
			assertDecompressedS2Error(t, ackErr)
		})
	}
}
