package s2_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// TestStreamingAgainstSelfSignedLite verifies the bug fix end-to-end: when the
// user supplies an HTTPClient whose TLS config trusts a private/self-signed CA
// (or skips verification), BOTH unary and streaming operations (AppendSession
// and ReadSession) succeed against that endpoint. Before the fix, only unary
// calls succeeded while streaming failed TLS verification.
//
// Preconditions: a self-signed-TLS S2 Lite server, pointed at by S2_LITE_TLS_ENDPOINT
// (e.g. "https://localhost:18443"). Set S2_LITE_TLS_INSECURE=1 to use
// InsecureSkipVerify (default); otherwise set S2_LITE_TLS_ROOT_AAS to a PEM file.
func TestStreamingAgainstSelfSignedLite(t *testing.T) {
	endpoint := os.Getenv("S2_LITE_TLS_ENDPOINT")
	if endpoint == "" {
		t.Skip("S2_LITE_TLS_ENDPOINT not set (set to https://localhost:<port> with s2 lite --tls-self)")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	insecure := os.Getenv("S2_LITE_TLS_INSECURE") != "0"
	rootCAs := os.Getenv("S2_LITE_TLS_ROOT_AAS")

	tlsCfg := &tls.Config{}
	if rootCAs != "" {
		t.Fatalf("PEM root CAs path not supported by this harness; use S2_LITE_TLS_INSECURE=1")
	}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	}

	customHTTP := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}

	client := s2.New("ignored", &s2.ClientOptions{
		BaseURL: endpoint,
		MakeBasinBaseURL: func(string) string {
			return endpoint
		},
		HTTPClient:        customHTTP,
		RequestTimeout:    30 * time.Second,
		ConnectionTimeout: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	basinName := s2.BasinName("strm-tls-" + uniqueSuffix())
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = client.Basins.Delete(cleanupCtx, basinName)
	})
	if _, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName}); err != nil {
		t.Fatalf("create basin: %v", err)
	}

	basin := client.Basin(string(basinName))
	streamName := s2.StreamName("records")
	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	stream := basin.Stream(streamName)

	if _, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("first")}},
	}); err != nil {
		t.Fatalf("unary append before streaming: %v", err)
	}

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("open append session: %v", err)
	}
	for i := range 3 {
		future, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte("session-batch")}},
		})
		if err != nil {
			t.Fatalf("streaming append submit %d: %v", i, err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatalf("streaming append ticket %d: %v", i, err)
		}
		if _, err := ticket.Ack(ctx); err != nil {
			t.Fatalf("streaming append ack %d: %v", i, err)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close append session: %v", err)
	}

	readSession, err := stream.ReadSession(ctx, &s2.ReadOptions{SeqNum: s2.Ptr(uint64(0))})
	if err != nil {
		t.Fatalf("open read session: %v", err)
	}
	defer readSession.Close()
	var got int
	for readSession.Next() {
		got++
		if got >= 4 {
			break
		}
	}
	if err := readSession.Err(); err != nil {
		t.Fatalf("read session error: %v", err)
	}
	if got == 0 {
		t.Fatal("expected to read back records via streaming read against the self-signed endpoint")
	}
}

func uniqueSuffix() string {
	return time.Now().Format("0102150405")
}

// inProcessCONNECTProxy is a forward HTTP CONNECT proxy: it tunnels the raw
// TCP stream to the CONNECT target so the SDK can run HTTP/2 (TLS or h2c)
// through it. It records every CONNECT target. Used to prove plan step 21
// (corporate-proxy-only deployment) end-to-end through the full SDK.
type inProcessCONNECTProxy struct {
	t       *testing.T
	targets chan string
	ln      net.Listener
	wg      sync.WaitGroup
	connMu  sync.Mutex
	conns   []net.Conn
}

func startInProcessCONNECTProxy(t *testing.T) *inProcessCONNECTProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	p := &inProcessCONNECTProxy{t: t, targets: make(chan string, 16), ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			p.connMu.Lock()
			p.conns = append(p.conns, raw)
			p.connMu.Unlock()
			p.wg.Add(1)
			go p.handle(raw)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.connMu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.connMu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *inProcessCONNECTProxy) handle(raw net.Conn) {
	defer p.wg.Done()
	defer raw.Close()
	br := bufio.NewReader(raw)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		return
	}
	target := req.Host
	select {
	case p.targets <- target:
	default:
	}
	targetConn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		return
	}
	defer targetConn.Close()
	if _, err := raw.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	// The HTTP/2 client sends its preface immediately after the CONNECT
	// response; those bytes may already be buffered in br. Forward them
	// before entering the bidirectional copy so the tunnel stays framed.
	if n := br.Buffered(); n > 0 {
		buf := make([]byte, n)
		_, _ = io.ReadFull(br, buf)
		_, _ = targetConn.Write(buf)
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(targetConn, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(raw, targetConn); done <- struct{}{} }()
	<-done
}

// TestStreamingAgainstSelfSignedLiteViaProxy proves the bug fix end-to-end for
// the corporate-proxy scenario (plan step 21): streaming AppendSession +
// ReadSession route through a CONNECT proxy (via Transport.Proxy) to a
// self-signed-TLS s2-lite, using a custom HTTPClient that carries InsecureSkipVerify
// and the proxy resolver. Both unary and streaming calls succeed.
func TestStreamingAgainstSelfSignedLiteViaProxy(t *testing.T) {
	endpoint := os.Getenv("S2_LITE_TLS_ENDPOINT")
	if endpoint == "" {
		t.Skip("S2_LITE_TLS_ENDPOINT not set (set to https://localhost:<port> with s2 lite --tls-self)")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	insecure := os.Getenv("S2_LITE_TLS_INSECURE") != "0"

	proxy := startInProcessCONNECTProxy(t)
	proxyURL := &url.URL{Scheme: "http", Host: proxy.ln.Addr().String()}

	tlsCfg := &tls.Config{}
	if insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	customHTTP := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
			Proxy:           http.ProxyURL(proxyURL),
		},
	}

	client := s2.New("ignored", &s2.ClientOptions{
		BaseURL:           endpoint,
		MakeBasinBaseURL:  func(string) string { return endpoint },
		HTTPClient:        customHTTP,
		RequestTimeout:    30 * time.Second,
		ConnectionTimeout: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	basinName := s2.BasinName("strm-tls-proxy-" + uniqueSuffix())
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = client.Basins.Delete(cleanupCtx, basinName)
	})
	if _, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName}); err != nil {
		t.Fatalf("create basin: %v", err)
	}
	basin := client.Basin(string(basinName))
	streamName := s2.StreamName("records")
	if _, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	stream := basin.Stream(streamName)

	if _, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("first")}},
	}); err != nil {
		t.Fatalf("unary append through proxy: %v", err)
	}

	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("open append session through proxy: %v", err)
	}
	for i := range 3 {
		future, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte("session-batch")}},
		})
		if err != nil {
			t.Fatalf("streaming append submit %d: %v", i, err)
		}
		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatalf("streaming append ticket %d: %v", i, err)
		}
		if _, err := ticket.Ack(ctx); err != nil {
			t.Fatalf("streaming append ack %d: %v", i, err)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close append session: %v", err)
	}

	readSession, err := stream.ReadSession(ctx, &s2.ReadOptions{SeqNum: s2.Ptr(uint64(0))})
	if err != nil {
		t.Fatalf("open read session through proxy: %v", err)
	}
	defer readSession.Close()
	var got int
	for readSession.Next() {
		got++
		if got >= 4 {
			break
		}
	}
	if err := readSession.Err(); err != nil {
		t.Fatalf("read session error through proxy: %v", err)
	}
	if got == 0 {
		t.Fatal("expected to read back records via streaming read through the proxy")
	}

	// Confirm the proxy actually saw CONNECT requests for the lite endpoint.
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	if waitErr := waitForConnectTarget(proxy, endpointURL.Host, 5*time.Second); waitErr != nil {
		t.Fatalf("proxy never observed CONNECT for %q: %v", endpointURL.Host, waitErr)
	}
}

func waitForConnectTarget(p *inProcessCONNECTProxy, hostPrefix string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		select {
		case got := <-p.targets:
			if strings.HasPrefix(got, hostPrefix) {
				return nil
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("no CONNECT for %q seen within %s", hostPrefix, d)
}
