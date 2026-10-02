package faulttest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"github.com/s2-streamstore/s2-sdk-go/s2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// readFaultProxy proxies read-records requests to S2 Lite, forwarding every
// data frame verbatim and replacing Lite's terminal OK frame with a terminal
// 400 BAD_READ frame. This simulates a fatal server error arriving during
// catch-up while records are already buffered on the wire.
type readFaultProxy struct {
	endpoint  string
	upstream  *url.URL
	transport *http2.Transport
}

func newReadFaultProxy(t *testing.T, upstream string) *readFaultProxy {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	p := &readFaultProxy{upstream: u}
	p.transport = &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	server := httptest.NewUnstartedServer(h2c.NewHandler(http.HandlerFunc(p.serveHTTP), &http2.Server{}))
	server.Start()
	p.endpoint = server.URL
	t.Cleanup(func() {
		server.Close()
		p.transport.CloseIdleConnections()
	})
	return p
}

// readS2SFrame reads one raw S2S wire frame (3-byte length + flag + payload)
// from r, returning the exact bytes that would be forwarded unchanged.
func readS2SFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	length := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
	if length < 1 || length > framing.MaxFrameSize {
		return nil, fmt.Errorf("invalid s2s frame length %d", length)
	}
	frame := make([]byte, 3+length)
	frame[0], frame[1], frame[2], frame[3] = hdr[0], hdr[1], hdr[2], hdr[3]
	if _, err := io.ReadFull(r, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func (p *readFaultProxy) serveHTTP(w http.ResponseWriter, req *http.Request) {
	upCtx, cancel := context.WithCancel(req.Context())
	defer cancel()
	upReq := req.Clone(upCtx)
	upReq.URL.Scheme = p.upstream.Scheme
	upReq.URL.Host = p.upstream.Host
	upReq.Host = p.upstream.Host
	upReq.RequestURI = ""

	resp, err := p.transport.RoundTrip(upReq)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	fw := flushWriter{w}

	// Forward every catch-up data frame verbatim, then inject a fatal BAD_READ
	// terminal frame when Lite ends the upstream (either by closing the stream
	// after the count bound, or by sending its own terminal frame).
	injectFatal := func() {
		inj := framing.CreateFrameWithStatus(
			[]byte(`{"message":"bad read","code":"BAD_READ"}`),
			true, framing.CompressionNone, http.StatusBadRequest,
		)
		_, _ = fw.Write(inj)
	}
	for {
		raw, err := readS2SFrame(resp.Body)
		if err != nil {
			injectFatal()
			return
		}
		if raw[3]&0x80 != 0 {
			injectFatal()
			return
		}
		if _, err := fw.Write(raw); err != nil {
			return
		}
	}
}

// TestLiteReadSessionDrainsBufferedRecordsOnFatalError drives the public
// ReadSession through an HTTP/2 fault proxy in front of a real S2 Lite server.
// It verifies that records already pulled from the wire are delivered before
// the fatal error surfaces, and that NextReadPosition resumes at the first
// not-yet-delivered record.
func TestLiteReadSessionDrainsBufferedRecordsOnFatalError(t *testing.T) {
	lite := newLite(t)
	basin, name := provision(t, lite.endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const total = 12
	const readCount = 10

	// Seed the stream with `total` records directly against Lite.
	seedStream := testClient(lite.endpoint, s2.CompressionNone).Basin(basin).Stream(s2.StreamName(name))
	records := make([]s2.AppendRecord, total)
	for i := range records {
		records[i] = s2.AppendRecord{Body: []byte(fmt.Sprintf("rec-%d", i))}
	}
	ack, err := seedStream.Append(ctx, &s2.AppendInput{Records: records})
	if err != nil {
		t.Fatalf("append seed records: %v", err)
	}
	if ack.End.SeqNum != total {
		t.Fatalf("expected end seq %d, got %d", total, ack.End.SeqNum)
	}

	// Read the first `readCount` records through the fault proxy, which replaces
	// the server's terminal OK frame with a fatal BAD_READ frame after the
	// catch-up data frames. The batch is non-caught-up (stream tail is `total`
	// > readCount), so deliveries buffer in recordsCh before the error fires.
	proxy := newReadFaultProxy(t, lite.endpoint)
	readStream := testClient(proxy.endpoint, s2.CompressionNone).Basin(basin).Stream(s2.StreamName(name))
	session, err := readStream.ReadSession(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(0), Count: s2.Uint64(readCount)})
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer session.Close()

	var got []uint64
	for session.Next() {
		got = append(got, session.Record().SeqNum)
	}
	var s2Err *s2.S2Error
	if !errors.As(session.Err(), &s2Err) || s2Err.Code != "BAD_READ" {
		t.Fatalf("expected BAD_READ fatal error, got %v", session.Err())
	}
	if len(got) != readCount {
		t.Fatalf("expected all %d buffered records delivered before the fatal error, got %d: %v (Err=%v)",
			readCount, len(got), got, session.Err())
	}
	for i, seq := range got {
		if seq != uint64(i) {
			t.Fatalf("record %d: expected seq %d, got %d", i, i, seq)
		}
	}
	pos := session.NextReadPosition()
	if pos == nil || pos.SeqNum != readCount {
		t.Fatalf("expected next read position %d, got %v", readCount, pos)
	}

	// Resume: the record at NextReadPosition must be the first not-yet-delivered
	// record, proving the position neither skips nor replays.
	resume, err := seedStream.Read(ctx, &s2.ReadOptions{SeqNum: s2.Uint64(readCount), Count: s2.Uint64(total - readCount)})
	if err != nil {
		t.Fatalf("resume read: %v", err)
	}
	if len(resume.Records) != total-readCount {
		t.Fatalf("expected %d remaining records on resume, got %d", total-readCount, len(resume.Records))
	}
	for i, r := range resume.Records {
		if r.SeqNum != uint64(readCount+i) {
			t.Fatalf("resume record %d: expected seq %d, got %d", i, readCount+i, r.SeqNum)
		}
	}
}
