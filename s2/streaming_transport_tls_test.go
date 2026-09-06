package s2

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

type stalledTLSListener struct {
	net.Listener
	closeOnce sync.Once
	closed    chan struct{}
}

func newStalledTLSListener(t *testing.T) *stalledTLSListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &stalledTLSListener{Listener: ln, closed: make(chan struct{})}
	go s.acceptLoop()
	t.Cleanup(s.close)
	return s
}

func (s *stalledTLSListener) acceptLoop() {
	for {
		c, err := s.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			<-s.closed
			c.Close()
		}(c)
	}
}

func (s *stalledTLSListener) close() {
	s.closeOnce.Do(func() {
		s.Close()
		close(s.closed)
	})
}

func newSelfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to add cert to pool")
	}
	return cert, pool
}

func newH2TLSServer(t *testing.T, handler http.Handler) (string, *x509.CertPool) {
	t.Helper()
	cert, pool := newSelfSignedCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}
	srv := &http.Server{Handler: handler, TLSConfig: tlsCfg}
	if err := http2.ConfigureServer(srv, nil); err != nil {
		t.Fatalf("configure http2 server: %v", err)
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, tlsCfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "https://" + ln.Addr().String(), pool
}

func TestStreamingTransportTLSHandshakeBoundedOnStalledServer(t *testing.T) {
	ln := newStalledTLSListener(t)
	const connectionTimeout = 750 * time.Millisecond

	transport, ok := newStreamingTransport(connectionTimeout).(*schemeAwareTransport)
	if !ok {
		t.Fatalf("unexpected transport type %T", transport)
	}
	client := &http.Client{Transport: transport, Timeout: 0}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://"+ln.Addr().String()+"/v1/probe", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	type doResult struct {
		resp *http.Response
		err  error
	}
	resCh := make(chan doResult, 1)
	start := time.Now()
	go func() {
		resp, err := client.Do(req)
		resCh <- doResult{resp: resp, err: err}
	}()

	hangGuard := connectionTimeout * 6
	select {
	case res := <-resCh:
		elapsed := time.Since(start)
		if res.err == nil {
			res.resp.Body.Close()
			t.Fatal("expected request to fail against a stalled-TLS server")
		}
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Fatalf("expected a deadline-exceeded error, got %v", res.err)
		}
		if elapsed < connectionTimeout/2 {
			t.Fatalf("returned too fast (%v); TLS stall did not occur", elapsed)
		}
		if elapsed > connectionTimeout*3 {
			t.Fatalf("handshake not bounded by connectionTimeout: took %v (connectionTimeout=%v)",
				elapsed, connectionTimeout)
		}
	case <-time.After(hangGuard):
		t.Fatalf("request hung for %v; TLS handshake not bounded (connectionTimeout=%v)",
			hangGuard, connectionTimeout)
	}
}

func TestStreamingTransportTLSHandshakeCompletesHappyPath(t *testing.T) {
	baseURL, pool := newH2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))

	const connectionTimeout = 2 * time.Second
	transport, ok := newStreamingTransport(connectionTimeout).(*schemeAwareTransport)
	if !ok {
		t.Fatalf("unexpected transport type %T", transport)
	}
	transport.https.TLSClientConfig = &tls.Config{RootCAs: pool}
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{Transport: transport, Timeout: 0}

	start := time.Now()
	resp, err := client.Get(baseURL + "/v1/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", body)
	}
	if elapsed := time.Since(start); elapsed > connectionTimeout/2 {
		t.Fatalf("happy-path connect+handshake too slow: %v", elapsed)
	}
}
