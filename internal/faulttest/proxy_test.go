package faulttest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	pb "github.com/s2-streamstore/s2-sdk-go/generated"
	"github.com/s2-streamstore/s2-sdk-go/internal/framing"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type faultProxy struct {
	endpoint  string
	upstream  *url.URL
	transport *http2.Transport
	mu        sync.Mutex
	attempts  map[int]int
	fired     map[int]bool
	committed []observedAppend
	wire      []string
	ops       []operation
	readSeen  []chan struct{}
	crash     func() error
}

func newFaultProxy(t *testing.T, endpoint string, script concurrentTrace, crash func() error) *faultProxy {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{upstream: u, attempts: make(map[int]int), fired: make(map[int]bool), crash: crash}
	for _, wave := range script.Waves {
		p.ops = append(p.ops, wave...)
	}
	for range p.ops {
		p.readSeen = append(p.readSeen, make(chan struct{}, 1))
	}
	p.transport = &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
		if u.Scheme == "https" {
			return (&tls.Dialer{Config: cfg}).DialContext(ctx, network, addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	server := httptest.NewUnstartedServer(h2c.NewHandler(http.HandlerFunc(p.serveHTTP), &http2.Server{}))
	var conns []net.Conn
	var connMu sync.Mutex
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
		}
	}
	server.Start()
	p.endpoint = server.URL
	t.Cleanup(func() {
		server.Close()
		connMu.Lock()
		defer connMu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		p.transport.CloseIdleConnections()
	})
	return p
}

func (p *faultProxy) serveHTTP(w http.ResponseWriter, req *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/op/"), "/", 2)
	id, err := strconv.Atoi(parts[0])
	if err != nil || len(parts) != 2 || id < 0 || id >= len(p.ops) {
		http.Error(w, "invalid operation", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.attempts[id]++
	attempt := p.attempts[id]
	p.wire = append(p.wire, fmt.Sprintf("op=%d attempt=%d %s %s", id, attempt, req.Method, req.URL.RawQuery))
	p.mu.Unlock()
	fault := p.ops[id].Fault
	if attempt != 1 {
		fault = ""
	}
	if fault == beforeCommit {
		p.injected(id, fault)
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "rate_limited", "message": "injected before commit"})
		return
	}
	request := req.Clone(req.Context())
	request.URL.Scheme, request.URL.Host = p.upstream.Scheme, p.upstream.Host
	request.URL.Path = "/" + parts[1]
	request.Host = p.upstream.Host
	request.RequestURI = ""
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"unavailable","message":"upstream unavailable"}`)
		return
	}
	defer response.Body.Close()
	streaming := req.Header.Get("Content-Type") == "s2s/proto"
	if response.StatusCode == http.StatusOK && (fault == afterCommit || fault == crashAfterCommit) {
		var data []byte
		if streaming {
			frame, readErr := framing.NewFrameReader(response.Body).ReadFrame()
			if readErr != nil || frame.Terminal {
				http.Error(w, "expected append acknowledgement", http.StatusBadGateway)
				return
			}
			data, err = frame.DecompressedBody()
		} else {
			data, err = io.ReadAll(response.Body)
		}
		var ack pb.AppendAck
		if err != nil || proto.Unmarshal(data, &ack) != nil || ack.Start == nil || ack.End == nil || ack.Tail == nil {
			http.Error(w, "incomplete acknowledgement", http.StatusBadGateway)
			return
		}
		p.mu.Lock()
		p.committed = append(p.committed, observedAppend{input: p.ops[id].Input, start: ack.Start.SeqNum, end: ack.End.SeqNum, tail: ack.Tail.SeqNum})
		p.mu.Unlock()
		if fault == crashAfterCommit {
			if err := p.crash(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		p.injected(id, fault)
		panic(http.ErrAbortHandler)
	}
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	w.(http.Flusher).Flush()
	if fault == readReset && streaming && response.StatusCode == http.StatusOK {
		frames := framing.NewFrameReader(response.Body)
		for {
			frame, err := frames.ReadFrame()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			data, err := frame.DecompressedBody()
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			status := 0
			if frame.StatusCode != nil {
				status = *frame.StatusCode
			}
			wire := framing.CreateFrameWithStatus(data, frame.Terminal, framing.CompressionNone, status)
			if frame.ReconnectAdvised {
				wire[3] |= 0x10
			}
			_, _ = w.Write(wire)
			w.(http.Flusher).Flush()
			if frame.Terminal {
				return
			}
			var batch pb.ReadBatch
			if proto.Unmarshal(data, &batch) != nil {
				panic(http.ErrAbortHandler)
			}
			if len(batch.Records) == 0 {
				continue
			}
			select {
			case <-p.readSeen[id]:
			case <-req.Context().Done():
				return
			}
			p.injected(id, fault)
			panic(http.ErrAbortHandler)
		}
	}
	_, _ = io.Copy(flushWriter{w}, response.Body)
}

func (p *faultProxy) injected(id int, fault string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fired[id] = true
	p.wire = append(p.wire, fmt.Sprintf("op=%d injected=%s", id, fault))
}

type flushWriter struct{ http.ResponseWriter }

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.ResponseWriter.(http.Flusher).Flush()
	return n, err
}
