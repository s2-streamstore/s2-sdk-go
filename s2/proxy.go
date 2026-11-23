package s2

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// proxyConfig holds the proxy settings of the user's http.Transport.
type proxyConfig struct {
	proxy             func(*http.Request) (*url.URL, error)
	connectHeader     http.Header
	getConnectHeader  func(ctx context.Context, proxyURL *url.URL, target string) (http.Header, error)
	onConnectResponse func(ctx context.Context, proxyURL *url.URL, connectReq *http.Request, connectRes *http.Response) error
}

func proxyConfigFromTransport(t *http.Transport) proxyConfig {
	return proxyConfig{
		proxy:             t.Proxy,
		connectHeader:     t.ProxyConnectHeader.Clone(),
		getConnectHeader:  t.GetProxyConnectHeader,
		onConnectResponse: t.OnProxyConnectResponse,
	}
}

// streamingDialer dials streaming connections directly or through the proxy
// that the user's transport selects for the target.
type streamingDialer struct {
	proxyConfig
	dialer *net.Dialer
	// tlsConfig is the user's TLS config, also used for HTTPS proxies as
	// net/http does.
	tlsConfig *tls.Config
}

func (d *streamingDialer) dial(ctx context.Context, scheme, network, addr string) (net.Conn, error) {
	if d.proxy != nil {
		proxyURL, err := d.proxy(&http.Request{URL: &url.URL{Scheme: scheme, Host: addr}})
		if err != nil {
			return nil, fmt.Errorf("resolve proxy for %s: %w", addr, err)
		}
		if proxyURL != nil {
			return d.dialThroughProxy(ctx, proxyURL, addr)
		}
	}
	return d.dialer.DialContext(ctx, network, addr)
}

// dialThroughProxy opens a TCP tunnel to addr through proxyURL. HTTP/2 needs a
// raw tunnel, so HTTP(S) proxies are asked to CONNECT rather than forward.
func (d *streamingDialer) dialThroughProxy(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	dialer, tlsConfig := d.dialer, d.tlsConfig
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		socks, err := proxy.FromURL(proxyURL, dialer)
		if err != nil {
			return nil, err
		}
		return socks.(proxy.ContextDialer).DialContext(ctx, "tcp", addr)
	case schemeHTTP, schemeHTTPS:
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}

	proxyAddr := proxyURL.Host
	if proxyURL.Port() == "" {
		port := "80"
		if proxyURL.Scheme == schemeHTTPS {
			port = "443"
		}
		proxyAddr = net.JoinHostPort(proxyURL.Hostname(), port)
	}

	rawConn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}
	conn := rawConn
	// Bound the proxy handshake by ctx; a deadline in the past unblocks I/O.
	stop := context.AfterFunc(ctx, func() { rawConn.SetDeadline(time.Unix(1, 0)) })
	fail := func(err error) (net.Conn, error) {
		stop()
		conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}

	if proxyURL.Scheme == schemeHTTPS {
		cfg := &tls.Config{}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		// The CONNECT exchange is HTTP/1.1 with the proxy itself.
		cfg.ServerName = proxyURL.Hostname()
		cfg.NextProtos = nil
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fail(fmt.Errorf("proxy %s TLS handshake: %w", proxyAddr, err))
		}
		conn = tlsConn
	}

	// Build CONNECT headers as net/http does.
	header := d.connectHeader
	if d.getConnectHeader != nil {
		header, err = d.getConnectHeader(ctx, proxyURL, addr)
		if err != nil {
			return fail(err)
		}
	}
	header = header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: header,
	}
	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(user.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := req.Write(conn); err != nil {
		return fail(fmt.Errorf("write CONNECT to proxy %s: %w", proxyAddr, err))
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fail(fmt.Errorf("read CONNECT response from proxy %s: %w", proxyAddr, err))
	}
	if d.onConnectResponse != nil {
		if err := d.onConnectResponse(ctx, proxyURL, req, resp); err != nil {
			resp.Body.Close()
			return fail(err)
		}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fail(fmt.Errorf("proxy %s refused CONNECT to %s: %s", proxyAddr, addr, resp.Status))
	}
	if !stop() {
		// ctx ended after the handshake; its deadline is already set on conn.
		conn.Close()
		return nil, ctx.Err()
	}

	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn returns bytes the proxy sent past its CONNECT response first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
