package faulttest

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	"github.com/s2-streamstore/s2-sdk-go/s2"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

type faultPlan struct {
	Fault string
}

type faultProxy struct {
	endpoint  string
	upstream  *url.URL
	transport *http2.Transport
	mu        sync.Mutex
	attempts  map[int]int
	fired     map[int]bool
	wire      []string
	ops       []faultPlan
	corrupt   func(proto.Message)
}

func newFaultProxy(t *testing.T, endpoint string, plans []faultPlan) *faultProxy {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{upstream: u, ops: plans, attempts: make(map[int]int), fired: make(map[int]bool)}
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
	parts := strings.SplitN(strings.TrimPrefix(req.URL.EscapedPath(), "/op/"), "/", 2)
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
	plan := p.ops[id]
	fault := plan.Fault
	if attempt != 1 {
		fault = ""
	}
	if fault == beforeCommit {
		p.injected(id, fault)
		p.replyError(w, false, http.StatusTooManyRequests, "rate_limited")
		return
	}
	request := req.Clone(req.Context())
	request.URL.Scheme, request.URL.Host = p.upstream.Scheme, p.upstream.Host
	request.URL.RawPath = "/" + parts[1]
	request.URL.Path, _ = url.PathUnescape(request.URL.RawPath)
	request.Host = p.upstream.Host
	request.RequestURI = ""
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		p.replyError(w, false, http.StatusServiceUnavailable, "unavailable")
		return
	}
	defer response.Body.Close()
	streaming := req.Header.Get("Content-Type") == "s2s/proto"
	appendRequest := req.Method == http.MethodPost
	if response.StatusCode == http.StatusOK && appendRequest && fault == afterCommit {
		var data []byte
		if streaming {
			frame, readErr := framing.NewFrameReader(response.Body).ReadFrame()
			if readErr != nil || frame.Terminal {
				panic(http.ErrAbortHandler)
			}
			data, err = frame.DecompressedBody()
		} else {
			data, err = io.ReadAll(response.Body)
		}
		var ack pb.AppendAck
		if err != nil || proto.Unmarshal(data, &ack) != nil || ack.Start == nil || ack.End == nil || ack.Tail == nil {
			panic(http.ErrAbortHandler)
		}
		p.injected(id, fault)
		p.replyError(w, streaming, http.StatusServiceUnavailable, "unavailable")
		return
	}
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(response.StatusCode)
	w.(http.Flusher).Flush()
	if response.StatusCode != http.StatusOK || (fault == "" && p.corrupt == nil) {
		_, _ = io.Copy(flushWriter{w}, response.Body)
		return
	}
	if !streaming {
		data, err := io.ReadAll(response.Body)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		if p.corrupt != nil {
			if appendRequest {
				data = p.rewrite(data, &pb.AppendAck{})
			} else if !strings.HasSuffix(req.URL.Path, "/tail") {
				data = p.rewrite(data, &pb.ReadBatch{})
			}
		}
		_, _ = (flushWriter{w}).Write(data)
		return
	}
	_, _ = io.Copy(flushWriter{w}, response.Body)
}

func (p *faultProxy) rewrite(data []byte, message proto.Message) []byte {
	if proto.Unmarshal(data, message) != nil {
		panic(http.ErrAbortHandler)
	}
	p.corrupt(message)
	data, err := proto.Marshal(message)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	return data
}

func (p *faultProxy) replyError(w http.ResponseWriter, streaming bool, status int, code string) {
	data, _ := json.Marshal(s2.ErrorInfo{Code: code, Message: code})
	if streaming {
		p.writeFrame(w, data, true, status, false)
	} else {
		w.WriteHeader(status)
		_, _ = (flushWriter{w}).Write(data)
	}
}

func (p *faultProxy) writeFrame(w http.ResponseWriter, data []byte, terminal bool, status int, reconnect bool) bool {
	wire := framing.CreateFrameWithStatus(data, terminal, framing.CompressionNone, status)
	if reconnect {
		wire[3] |= 0x10
	}
	_, err := (flushWriter{w}).Write(wire)
	return err == nil
}

func (p *faultProxy) injected(id int, fault string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fired[id] = true
	p.wire = append(p.wire, fmt.Sprintf("op=%d injected=%s", id, fault))
}

type flushWriter struct {
	http.ResponseWriter
}

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.ResponseWriter.(http.Flusher).Flush()
	return n, err
}
