package faulttest

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type faultProxy struct {
	endpoint  string
	upstream  *url.URL
	transport *http.Transport
	ops       int
}

func newFaultProxy(t *testing.T, endpoint string, operations int) *faultProxy {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{upstream: u, ops: operations}
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	p.transport = &http.Transport{Protocols: protocols, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(p.serveHTTP))
	serverProtocols := new(http.Protocols)
	serverProtocols.SetHTTP1(true)
	serverProtocols.SetHTTP2(true)
	serverProtocols.SetUnencryptedHTTP2(true)
	server.Config.Protocols = serverProtocols
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
	if err != nil || len(parts) != 2 || id < 0 || id >= p.ops {
		http.Error(w, "invalid operation", http.StatusBadRequest)
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
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(response.StatusCode)
	w.(http.Flusher).Flush()
	_, _ = io.Copy(flushWriter{w}, response.Body)
}

type flushWriter struct {
	http.ResponseWriter
}

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.ResponseWriter.(http.Flusher).Flush()
	return n, err
}
