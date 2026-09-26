package s2

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// streamingTransportFor returns the *schemeAwareTransport the streaming client
// would use for the given host, exercising the real spreadTransport.checkout
// path that materializes a fresh transport via newStreamingTransport.
func streamingTransportFor(t *testing.T, client *Client, host string) *schemeAwareTransport {
	t.Helper()
	streamUA, ok := client.streamingClient.Transport.(userAgentRoundTripper)
	if !ok {
		t.Fatalf("streaming: expected userAgentRoundTripper, got %T", client.streamingClient.Transport)
	}
	spread, ok := streamUA.base.(*spreadTransport)
	if !ok {
		t.Fatalf("streaming: expected *spreadTransport, got %T", streamUA.base)
	}
	entry := spread.checkout(host)
	entry.sessions.Add(-1) // do not keep the checked-out entry pinned
	schemeAware, ok := entry.rt.(*schemeAwareTransport)
	if !ok {
		t.Fatalf("streaming: expected *schemeAwareTransport, got %T", entry.rt)
	}
	return schemeAware
}

func TestStreamingTransport_PropagatesCustomHTTPClientTLSConfig(t *testing.T) {
	const sentinelServerName = "self-hosted.s2.internal"

	userTLSConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         sentinelServerName,
		MinVersion:         tls.VersionTLS12,
	}
	userTransport := &http.Transport{TLSClientConfig: userTLSConfig}
	userClient := &http.Client{Transport: userTransport, Timeout: 5}

	client := New("token", &ClientOptions{
		HTTPClient:        userClient,
		RequestTimeout:    5 * time.Second,
		ConnectionTimeout: 3 * time.Second,
	})

	unaryUA, ok := client.httpClient.Transport.(userAgentRoundTripper)
	if !ok {
		t.Fatalf("unary: expected userAgentRoundTripper, got %T", client.httpClient.Transport)
	}
	unaryBase, ok := unaryUA.base.(*http.Transport)
	if !ok {
		t.Fatalf("unary: expected *http.Transport, got %T", unaryUA.base)
	}
	if unaryBase.TLSClientConfig != userTLSConfig {
		t.Fatalf("unary: user TLS config should be preserved, got %#v", unaryBase.TLSClientConfig)
	}

	schemeAware := streamingTransportFor(t, client, sentinelServerName+":443")
	httpsTransport := schemeAware.https

	if httpsTransport.TLSClientConfig == nil {
		t.Fatal("streaming: expected http2.Transport.TLSClientConfig to be propagated, got nil")
	}
	if httpsTransport.TLSClientConfig == userTLSConfig {
		t.Fatal("streaming: expected a clone of the user TLS config, not the same pointer")
	}
	if !httpsTransport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("streaming: expected InsecureSkipVerify to be propagated, got %#v", httpsTransport.TLSClientConfig)
	}
	if httpsTransport.TLSClientConfig.ServerName != sentinelServerName {
		t.Fatalf("streaming: expected ServerName %q to be propagated, got %q", sentinelServerName, httpsTransport.TLSClientConfig.ServerName)
	}
	if httpsTransport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("streaming: expected MinVersion to be propagated, got %x", httpsTransport.TLSClientConfig.MinVersion)
	}

	if schemeAware.h2c.TLSClientConfig != nil {
		t.Fatalf("streaming: h2c transport must not carry a TLS config, got %#v", schemeAware.h2c.TLSClientConfig)
	}

	if client.streamingClient == client.httpClient {
		t.Fatal("streaming: streamingClient should be a distinct http.Client from the user-provided httpClient")
	}
	if client.streamingClient.Transport == userTransport {
		t.Fatal("streaming: streamingClient should not use the user-provided transport directly")
	}
}

func TestStreamingTransport_NilHTTPClientKeepsDefaultTLSConfig(t *testing.T) {
	client := New("token", &ClientOptions{
		RequestTimeout:    5 * time.Second,
		ConnectionTimeout: 3 * time.Second,
	})

	schemeAware := streamingTransportFor(t, client, "a.s2.dev:443")
	if schemeAware.https.TLSClientConfig != nil {
		t.Fatalf("streaming: expected nil TLSClientConfig by default, got %#v", schemeAware.https.TLSClientConfig)
	}
	if schemeAware.h2c.TLSClientConfig != nil {
		t.Fatalf("streaming: expected nil h2c TLSClientConfig by default, got %#v", schemeAware.h2c.TLSClientConfig)
	}
}

func TestStreamingTransport_CustomRoundTripperKeepsDefaultTLSConfig(t *testing.T) {
	client := New("token", &ClientOptions{
		HTTPClient:        &http.Client{Transport: &stubRoundTripper{}},
		RequestTimeout:    5 * time.Second,
		ConnectionTimeout: 3 * time.Second,
	})

	schemeAware := streamingTransportFor(t, client, "a.s2.dev:443")
	if schemeAware.https.TLSClientConfig != nil {
		t.Fatalf("streaming: expected nil TLSClientConfig for non-*http.Transport, got %#v", schemeAware.https.TLSClientConfig)
	}
}

// TestStreamingTransport_PropagatesProxyResolver verifies the user-provided
// Transport.Proxy function is consulted when dialing a streaming https endpoint,
// even though the dial itself is allowed to fail (the proxy rejects the tunnel).
func TestStreamingTransport_PropagatesProxyResolver(t *testing.T) {
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, acceptErr := proxyLn.Accept()
			if acceptErr != nil {
				return
			}
			c.Close()
		}
	}()
	defer proxyLn.Close()

	captured := make(chan *http.Request, 1)
	proxyFn := func(req *http.Request) (*url.URL, error) {
		select {
		case captured <- req:
		default:
		}
		return &url.URL{Scheme: "http", Host: proxyLn.Addr().String()}, nil
	}

	client := New("token", &ClientOptions{
		HTTPClient:        &http.Client{Transport: &http.Transport{Proxy: proxyFn}},
		ConnectionTimeout: 3 * time.Second,
	})
	schemeAware := streamingTransportFor(t, client, "self-hosted.s2.internal:443")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://self-hosted.s2.internal:443/streams/x/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err = schemeAware.https.RoundTrip(req); err == nil {
		t.Fatal("expected RoundTrip to fail because the proxy rejects the tunnel")
	}

	select {
	case got := <-captured:
		if got.URL.Scheme != "https" {
			t.Fatalf("expected proxy resolver to receive an https request, got scheme %q", got.URL.Scheme)
		}
		if got.URL.Host != "self-hosted.s2.internal:443" {
			t.Fatalf("expected proxy resolver to receive the dial host, got %q", got.URL.Host)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected the user-provided proxy resolver to be consulted for streaming https")
	}
}

// --- end-to-end tests through the real streaming *http.Client ---

func selfSignedCert(t *testing.T, extraDNSNames ...string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dnsNames := append([]string{"localhost"}, extraDNSNames...)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "s2-sdk-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, parsed
}

func rootCAsPool(t *testing.T, certs ...*x509.Certificate) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool
}

// serveH2Conn speaks HTTP/2 over an already-established conn. If serverTLS is
// non-nil, it performs the server-side TLS handshake first (real HTTPS);
// otherwise it speaks h2c (HTTP/2 cleartext, prior knowledge). sni, if non-nil,
// records the client hello server name. Callers own WaitGroup accounting.
func serveH2Conn(t *testing.T, raw net.Conn, serverTLS *tls.Config, handler http.Handler, sni chan<- string) {
	t.Helper()
	defer raw.Close()
	conn := raw
	if serverTLS != nil {
		cfg := serverTLS.Clone()
		baseGet := cfg.GetConfigForClient
		cfg.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			if sni != nil {
				select {
				case sni <- chi.ServerName:
				default:
				}
			}
			if baseGet != nil {
				return baseGet(chi)
			}
			return nil, nil
		}
		tlsConn := tls.Server(raw, cfg)
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		conn = tlsConn
	}
	srv := &http2.Server{}
	srv.ServeConn(conn, &http2.ServeConnOpts{Handler: handler})
}

func startH2Server(t *testing.T, serverTLS *tls.Config, handler http.Handler, sni chan<- string, wg *sync.WaitGroup) string {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		defer ln.Close()
		for {
			raw, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				serveH2Conn(t, raw, serverTLS, handler, sni)
			}()
		}
	}()
	return ln.Addr().String()
}

// startCONNECTProxy listens on a random local port and acts as a CONNECT proxy.
// For each CONNECT it records the target on connects and then serves HTTP/2
// (TLS if serverTLS non-nil, else h2c) over the tunnel to the target host.
func startCONNECTProxy(t *testing.T, serverTLS *tls.Config, handler http.Handler, connects chan<- string, wg *sync.WaitGroup) *url.URL {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	go func() {
		defer ln.Close()
		for {
			raw, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleProxyConnect(t, raw, serverTLS, handler, connects)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

func handleProxyConnect(t *testing.T, raw net.Conn, serverTLS *tls.Config, handler http.Handler, connects chan<- string) {
	t.Helper()
	br := bufio.NewReader(raw)
	req, err := http.ReadRequest(br)
	if err != nil {
		raw.Close()
		return
	}
	if req.Method != http.MethodConnect {
		raw.Close()
		return
	}
	target := req.Host
	select {
	case connects <- target:
	default:
	}
	if _, err := raw.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		raw.Close()
		return
	}
	serveH2Conn(t, raw, serverTLS, handler, nil)
}

func streamingDo(t *testing.T, client *Client, target string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.streamingClient.Do(req)
	if err != nil {
		t.Fatalf("streamingClient.Do(%s): %v", target, err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp
}

func recvSoon(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		return ""
	}
}

func TestStreamingClient_HonorsCustomRootCAs(t *testing.T) {
	cert, parsed := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}

	var wg sync.WaitGroup
	defer wg.Wait()
	addr := startH2Server(t, serverTLS, nil, nil, &wg)

	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: rootCAsPool(t, parsed)},
		}},
		ConnectionTimeout: 3 * time.Second,
	})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "https://"+addr+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from streaming read against a private-CA endpoint, got %d", resp.StatusCode)
	}
}

func TestStreamingClient_HonorsInsecureSkipVerifyAndCustomSNI(t *testing.T) {
	const customSNI = "self-hosted.s2.internal"
	cert, _ := selfSignedCert(t, customSNI)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}

	sni := make(chan string, 1)
	var wg sync.WaitGroup
	defer wg.Wait()
	addr := startH2Server(t, serverTLS, nil, sni, &wg)

	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: customSNI},
		}},
		ConnectionTimeout: 3 * time.Second,
	})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "https://"+addr+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with InsecureSkipVerify propagated, got %d", resp.StatusCode)
	}

	if got := recvSoon(t, sni); got != customSNI {
		t.Fatalf("expected custom SNI %q to reach the streaming handshake, got %q", customSNI, got)
	}
}

// TestStreamingClient_DefaultConfigRejectsSelfSigned confirms the default
// (nil HTTPClient) streaming path still verifies TLS, so the success of the
// RootCAs test above is due to propagation rather than disabled verification.
func TestStreamingClient_DefaultConfigRejectsSelfSigned(t *testing.T) {
	cert, _ := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}

	var wg sync.WaitGroup
	defer wg.Wait()
	addr := startH2Server(t, serverTLS, nil, nil, &wg)

	client := New("token", &ClientOptions{ConnectionTimeout: 3 * time.Second})
	defer client.streamingClient.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil)
	_, err := client.streamingClient.Do(req)
	if err == nil {
		t.Fatal("expected a default-config streaming dial to reject a self-signed cert")
	}
}

func TestStreamingClient_HonorsProxyWithTLS(t *testing.T) {
	const target = "fake.example:443"
	cert, _ := selfSignedCert(t, "fake.example")
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}

	connects := make(chan string, 4)
	var wg sync.WaitGroup
	defer wg.Wait()
	proxyURL := startCONNECTProxy(t, serverTLS, nil, connects, &wg)

	proxyFn := func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			Proxy:           proxyFn,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}},
		ConnectionTimeout: 3 * time.Second,
	})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "https://"+target+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 through TLS proxy, got %d", resp.StatusCode)
	}

	if got := recvSoon(t, connects); got != target {
		t.Fatalf("expected proxy to receive CONNECT for %q, got %q", target, got)
	}
}

func TestStreamingClient_HonorsProxyH2C(t *testing.T) {
	const target = "fake.example:80"
	connects := make(chan string, 4)
	var wg sync.WaitGroup
	defer wg.Wait()
	proxyURL := startCONNECTProxy(t, nil, nil, connects, &wg)

	proxyFn := func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			Proxy: proxyFn,
		}},
		ConnectionTimeout: 3 * time.Second,
	})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "http://"+target+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 through h2c proxy, got %d", resp.StatusCode)
	}

	if got := recvSoon(t, connects); got != target {
		t.Fatalf("expected proxy to receive CONNECT for %q, got %q", target, got)
	}
}

func TestStreamingClient_NoProxyDialsDirectly(t *testing.T) {
	cert, parsed := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}

	var wg sync.WaitGroup
	defer wg.Wait()
	addr := startH2Server(t, serverTLS, nil, nil, &wg)

	proxyFn := func(*http.Request) (*url.URL, error) { return nil, nil }
	client := New("token", &ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			Proxy:           proxyFn,
			TLSClientConfig: &tls.Config{RootCAs: rootCAsPool(t, parsed)},
		}},
		ConnectionTimeout: 3 * time.Second,
	})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "https://"+addr+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 via direct dial when proxy resolver returns nil, got %d", resp.StatusCode)
	}
}

func TestStreamingClient_NilHTTPClientDialsDirectlyOverH2C(t *testing.T) {
	var wg sync.WaitGroup
	defer wg.Wait()
	addr := startH2Server(t, nil, nil, nil, &wg)

	client := New("token", &ClientOptions{ConnectionTimeout: 3 * time.Second})
	defer client.streamingClient.CloseIdleConnections()

	resp := streamingDo(t, client, "http://"+addr+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected default streaming client to dial directly over h2c, got %d", resp.StatusCode)
	}
}
