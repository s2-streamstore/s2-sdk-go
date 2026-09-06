package s2

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"
)

// These tests reproduce and guard the streaming truncation classification bug
// in internal/framing/framing.go's FrameReader.ReadFrame, exercised through
// the same layers the SDK uses in production: real HTTP bodies through to the
// public ReadSession API.
//
// The bug: when the HTTP body returns a clean io.EOF while a partial S2S frame
// is still buffered (a graceful HTTP end-of-stream mid-record -- e.g. an
// HTTP/2 END_STREAM after a partial frame, or a proxy/LB that flushes then
// closes during a drain), ReadFrame reported io.EOF instead of
// io.ErrUnexpectedEOF. Downstream, the read session and append-ack reader
// both treat io.EOF as an expected shutdown, so a truncated stream completed
// silently with Err() == nil. The fix translates a clean io.EOF into
// io.ErrUnexpectedEOF whenever the parser still has a partial frame buffered,
// reserving io.EOF for an end that falls exactly on a frame boundary.

// TestEOFTruncation_HTTP2_TLS_EndStreamAfterPartialFrame is the end-to-end
// reproduction of the bug against a real HTTP/2 over TLS server (the same
// HTTP/2 transport shape the SDK uses for https:// endpoints).
//
// The handler writes the first len(complete)-2 bytes of a complete S2S frame
// and returns normally, so the HTTP/2 server emits a final DATA frame with
// END_STREAM after the partial S2S frame -- a clean HTTP end-of-stream while
// the S2S protocol is mid-record. http.Response.Body.Read returns io.EOF (not
// io.ErrUnexpectedEOF) for this mode, so the framing layer must translate it.
func TestEOFTruncation_HTTP2_TLS_EndStreamAfterPartialFrame(t *testing.T) {
	complete := buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("hello")},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "s2s/proto")
		w.WriteHeader(http.StatusOK)
		// Truncate the frame by 2 trailing bytes and end the HTTP transfer
		// cleanly so the transport emits END_STREAM after the partial frame.
		_, _ = w.Write(complete[:len(complete)-2])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	// httptest does not configure the inner http.Server for h2 itself; install
	// the HTTP/2 handler explicitly so the server actually serves h2 over the
	// TLS listener it advertises via ALPN.
	if err := http2.ConfigureServer(server.Config, &http2.Server{}); err != nil {
		t.Fatalf("configure http2 server: %v", err)
	}
	server.StartTLS()
	defer server.Close()

	// An h2-only transport over TLS, matching the SDK's https transport shape
	// (internalframing is fed resp.Body exactly as s2/read.go does).
	transport := &http2.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // httptest self-signed cert
			NextProtos:         []string{"h2"},
		},
	}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("expected HTTP/2.0 negotiated via ALPN, got proto=%q", resp.Proto)
	}

	frameReader := internalframing.NewFrameReader(resp.Body)
	frame, err := frameReader.ReadFrame()

	if frame != nil {
		t.Errorf("expected no frame on truncated stream, got %v", frame)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true -- clean END_STREAM after a partial S2S frame must be reported as truncation (err=%v)", err)
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = true, want false -- mid-frame truncation must not be reported as clean EOF (err=%v)", err)
	}
}

// TestEOFTruncation_HTTP11_ContentLength_AbruptClose guards the abrupt
// transport-failure mode: an HTTP/1.1 response that advertises a
// Content-Length but closes mid-body. The transport surfaces
// io.ErrUnexpectedEOF from body.Read itself, so the framing layer must forward
// it unchanged (the bug only affects the clean-end-of-transfer mode).
func TestEOFTruncation_HTTP11_ContentLength_AbruptClose(t *testing.T) {
	complete := buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("hello")},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Send a raw HTTP/1.1 response with a Content-Length that exceeds the
		// bytes we actually write, then close the connection abruptly.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		header := "HTTP/1.1 200 OK\r\n" +
			"Content-Type: s2s/proto\r\n" +
			"Content-Length: " + strconv.Itoa(len(complete)) + "\r\n\r\n"
		_, _ = rw.WriteString(header)
		_, _ = rw.Write(complete[:len(complete)-4])
		_ = rw.Flush()
	}))
	defer server.Close()

	resp, err := http.DefaultTransport.(*http.Transport).Clone().RoundTrip(newGetRequest(t, server.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	frameReader := internalframing.NewFrameReader(resp.Body)
	frame, err := frameReader.ReadFrame()

	if frame != nil {
		t.Errorf("expected no frame on abruptly-closed stream, got %v", frame)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true -- abrupt Content-Length close must surface truncation (err=%v)", err)
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = true, want false -- abrupt close must not be reported as clean EOF (err=%v)", err)
	}
}

// TestEOFTruncation_HTTP11_Chunked_AbruptClose guards the chunked-encoding
// variant of the abrupt close above.
func TestEOFTruncation_HTTP11_Chunked_AbruptClose(t *testing.T) {
	complete := buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("hello")},
	})
	partial := complete[:len(complete)-4]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		header := "HTTP/1.1 200 OK\r\n" +
			"Content-Type: s2s/proto\r\n" +
			"Transfer-Encoding: chunked\r\n\r\n"
		_, _ = rw.WriteString(header)
		// Open the first chunk with the partial S2S frame, and close the
		// connection without the terminating zero-length chunk.
		_, _ = fmt.Fprintf(rw, "%x\r\n", len(partial))
		_, _ = rw.Write(partial)
		_, _ = rw.WriteString("\r\n")
		_ = rw.Flush()
	}))
	defer server.Close()

	resp, err := http.DefaultTransport.(*http.Transport).Clone().RoundTrip(newGetRequest(t, server.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	frameReader := internalframing.NewFrameReader(resp.Body)
	frame, err := frameReader.ReadFrame()

	if frame != nil {
		t.Errorf("expected no frame on abruptly-closed stream, got %v", frame)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true -- abrupt chunked close must surface truncation (err=%v)", err)
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = true, want false -- abrupt close must not be reported as clean EOF (err=%v)", err)
	}
}

// TestReadSession_SilentSuccessOnPartialFrame_EndStream is the public-API
// symptom: previously, a ReadSession served a partial S2S frame via a body
// that ends cleanly (io.EOF) completed silently with Err() == nil, masking
// the truncation as success. After the fix, the truncation is surfaced as a
// "read frame error: unexpected EOF" non-nil error.
func TestReadSession_SilentSuccessOnPartialFrame_EndStream(t *testing.T) {
	complete := buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
		{SeqNum: 1, Body: []byte("b")},
	})

	// Truncate the only frame; the round tripper's body is a bytes.Reader,
	// which returns a clean io.EOF on exhaustion (the same end-of-stream
	// shape the HTTP body produces after END_STREAM on the wire).
	body := complete[:len(complete)-2]
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	for session.Next() {
		// A truncated first frame must not deliver any records; if it does,
		// the framing layer has been bypassed or misclassified.
		t.Errorf("unexpected record delivered from truncated stream: seq_num=%d", session.Record().SeqNum)
	}

	err = session.Err()
	if err == nil {
		t.Fatal("expected a non-nil error for a truncated stream, got nil (silent success regression)")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true (err=%v)", err)
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = true, want false -- truncation must not be classified as a clean EOF (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "read frame error") {
		t.Errorf("expected error to be wrapped as a read frame error, got %q", err.Error())
	}
}

// TestReadSession_CleanEOFOnFrameBoundary_ErrNil guards the happy path: a
// stream that ends cleanly exactly on a frame boundary (no partial frame
// buffered) must still complete with Err() == nil. This confirms the fix is
// surgical -- it only translates io.EOF when a partial frame remains.
func TestReadSession_CleanEOFOnFrameBoundary_ErrNil(t *testing.T) {
	// A single complete non-terminal batch then a clean end-of-body on the
	// frame boundary (no terminal frame, no trailing partial bytes).
	body := buildReadBatchFrame(t, []*pb.SequencedRecord{
		{SeqNum: 0, Body: []byte("a")},
	})
	rt := &staticStatusRoundTripper{status: http.StatusOK, body: body}
	streamClient := newFrameServedStreamClient(rt)

	session, err := streamClient.ReadSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to open read session: %v", err)
	}
	defer session.Close()

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	if err := session.Err(); err != nil {
		t.Fatalf("expected nil error on a clean frame-boundary EOF, got %v", err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("expected to deliver record seq_num 0, got %v", got)
	}
}

// newGetRequest is a small helper to build a GET request for the abrupt-close
// tests, which dial a plain HTTP/1.1 endpoint.
func newGetRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// TestAppendAckLoop_TruncatedFinalAckFrame_SurfacesError reproduces the
// streaming-append variant of the bug. The ack reader (readAcksLoop) treats
// io.EOF as an expected shutdown via isExpectedFrameReadError; before the
// fix, a truncated final ack frame delivered with a clean HTTP
// end-of-stream was silently swallowed, leaving in-flight batches
// unacknowledged and the session torn down as if it shut down gracefully.
// After the fix, io.EOF is translated to io.ErrUnexpectedEOF,
// isExpectedFrameReadError returns false, and the ack loop surfaces
// "read frame error: unexpected EOF" via reportErrorIfOpen.
func TestAppendAckLoop_TruncatedFinalAckFrame_SurfacesError(t *testing.T) {
	ackData, err := proto.Marshal(&pb.AppendAck{
		Start: &pb.StreamPosition{SeqNum: 0},
		End:   &pb.StreamPosition{SeqNum: 2},
	})
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	complete := internalframing.CreateFrame(ackData, false, internalframing.CompressionNone)

	// Clean end-of-stream after a partial ack frame: a bytes.Reader returns
	// io.EOF on exhaustion, the same shape http.Response.Body produces after
	// a graceful HTTP/2 END_STREAM.
	partial := complete[:len(complete)-2]

	stream := newTestStreamClientForAppend(&RetryConfig{MaxAttempts: 1})
	transport := newTransportSession(stream, &signalWriteCloser{})

	go transport.readAcksLoop(&http.Response{Body: io.NopCloser(bytes.NewReader(partial))})

	select {
	case ack := <-transport.acksCh:
		t.Fatalf("expected no ack on a truncated stream, got %+v", ack)
	case err := <-transport.errorsCh:
		if err == nil {
			t.Fatal("expected a non-nil error for a truncated ack stream, got nil (silent shutdown regression)")
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("errors.Is(err, io.ErrUnexpectedEOF) = false, want true (err=%v)", err)
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("errors.Is(err, io.EOF) = true, want false -- truncation must not be classified as a clean EOF (err=%v)", err)
		}
		if !strings.Contains(err.Error(), "read frame error") {
			t.Errorf("expected error wrapped as a read frame error, got %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ack loop to terminate on a truncated stream")
	}
}

// TestAppendAckLoop_CleanEOFAfterCompleteAck_NoError guards the append-side
// happy path: a clean end-of-stream that falls exactly on an ack-frame
// boundary (a complete ack frame, then a clean io.EOF with no partial frame
// buffered) must be treated as an expected shutdown -- the ack is delivered
// and no error is reported. This confirms the fix is surgical on the
// append-ack path too.
func TestAppendAckLoop_CleanEOFAfterCompleteAck_NoError(t *testing.T) {
	ackData, err := proto.Marshal(&pb.AppendAck{
		Start: &pb.StreamPosition{SeqNum: 0},
		End:   &pb.StreamPosition{SeqNum: 2},
	})
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	complete := internalframing.CreateFrame(ackData, false, internalframing.CompressionNone)

	stream := newTestStreamClientForAppend(&RetryConfig{MaxAttempts: 1})
	transport := newTransportSession(stream, &signalWriteCloser{})

	go transport.readAcksLoop(&http.Response{Body: io.NopCloser(bytes.NewReader(complete))})

	select {
	case ack := <-transport.acksCh:
		if ack == nil || ack.Start.SeqNum != 0 || ack.End.SeqNum != 2 {
			t.Fatalf("unexpected ack: %+v", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ack to be delivered")
	}

	// After the sole complete ack, the next ReadFrame returns a clean io.EOF
	// on the frame boundary; the loop must treat it as an expected shutdown
	// and close without reporting an error.
	select {
	case err := <-transport.errorsCh:
		if err != nil {
			t.Fatalf("expected no error on a clean frame-boundary EOF on the append path, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ack loop to shut down cleanly")
	}
}
