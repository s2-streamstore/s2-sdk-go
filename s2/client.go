package s2

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

const (
	DefaultBaseURL             = "https://a.s2.dev/v1"
	defaultRequestTimeout      = 5 * time.Second
	defaultConnectionTimeout   = 3 * time.Second
	defaultTCPKeepAlive        = 30 * time.Second
	defaultMaxIdleConnsPerHost = 32

	// HTTP/2 transport settings for streaming operations
	http2MaxReadFrameSize  = 16 * 1024 * 1024
	http2MaxHeaderListSize = 10 * 1024 * 1024
	http2ReadIdleTimeout   = 30 * time.Second
	http2PingTimeout       = 15 * time.Second
	http2WriteByteTimeout  = 30 * time.Second
)

type ClientOptions struct {
	// Endpoint to connect to S2.
	// Defaults to "https://a.s2.dev/v1".
	BaseURL string
	// HTTP client used for requests.
	HTTPClient *http.Client
	// Allows customizing how basin endpoints are constructed.
	// If provided, this function is used to derive the endpoint for a given basin.
	// When provided, the "s2-basin" HTTP header is automatically included in basin-scoped requests.
	MakeBasinBaseURL func(basin string) string
	// Retry configuration.
	RetryConfig *RetryConfig
	// Get SDK level logs.
	Logger *slog.Logger
	// Overall timeout for HTTP requests.
	// Defaults to 5 seconds.
	RequestTimeout time.Duration
	// Timeout for establishing TCP connections.
	// For streaming HTTPS requests, this also includes the TLS handshake.
	// Defaults to 3 seconds.
	ConnectionTimeout time.Duration
	// Compression algorithm for request bodies.
	// Defaults to CompressionNone (no compression).
	Compression CompressionType
}

type Client struct {
	accessToken        string
	baseURL            string
	httpClient         *http.Client
	streamingClient    *http.Client
	makeBasinBaseURL   func(basin string) string
	retryConfig        *RetryConfig
	logger             *slog.Logger
	includeBasinHeader bool
	requestTimeout     time.Duration
	connectionTimeout  time.Duration
	compression        CompressionType

	// Client for access tokens.
	AccessTokens *AccessTokensClient
	// Client for basins.
	Basins *BasinsClient
	// Client for metrics.
	Metrics *MetricsClient
	// Client for locations.
	Locations *LocationsClient
}

// Create a new Client.
func New(accessToken string, opts *ClientOptions) *Client {
	if accessToken == "" {
		panic("access token cannot be empty")
	}
	if opts == nil {
		opts = &ClientOptions{}
	}

	baseURL := DefaultBaseURL
	if opts.BaseURL != "" {
		baseURL = normalizeBaseURL(opts.BaseURL)
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		requestTimeout := opts.RequestTimeout
		if requestTimeout <= 0 {
			requestTimeout = defaultRequestTimeout
		}
		connectionTimeout := opts.ConnectionTimeout
		if connectionTimeout <= 0 {
			connectionTimeout = defaultConnectionTimeout
		}

		httpClient = &http.Client{
			Timeout:   requestTimeout,
			Transport: newUnaryHTTPTransport(http.DefaultTransport, connectionTimeout),
		}
	}
	baseTransport := httpClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	httpClient.Transport = userAgentRoundTripper{
		base:      baseTransport,
		userAgent: defaultUserAgent(),
	}

	// Streaming honors the user-provided transport's TLS and proxy settings only
	// when the caller supplies an HTTPClient. When HTTPClient is nil the streaming
	// transport is built from defaults, preserving prior behavior.
	var streamingBase http.RoundTripper
	if opts.HTTPClient != nil {
		streamingBase = baseTransport
	}

	makeBasinBaseURL := opts.MakeBasinBaseURL
	if makeBasinBaseURL == nil {
		makeBasinBaseURL = func(basin string) string {
			return fmt.Sprintf("https://%s.b.s2.dev/v1", basin)
		}
	} else {
		makeBasinBaseURL = func(basin string) string {
			return normalizeBaseURL(opts.MakeBasinBaseURL(basin))
		}
	}

	retryConfig := opts.RetryConfig
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig
	}

	connectionTimeout := opts.ConnectionTimeout
	if connectionTimeout <= 0 {
		connectionTimeout = defaultConnectionTimeout
	}

	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}

	c := &Client{
		accessToken:        accessToken,
		baseURL:            baseURL,
		httpClient:         httpClient,
		streamingClient:    createStreamingClient(connectionTimeout, streamingBase),
		makeBasinBaseURL:   makeBasinBaseURL,
		retryConfig:        retryConfig,
		logger:             opts.Logger,
		includeBasinHeader: opts.MakeBasinBaseURL != nil,
		connectionTimeout:  connectionTimeout,
		requestTimeout:     requestTimeout,
		compression:        opts.Compression,
	}

	c.AccessTokens = &AccessTokensClient{client: c}
	c.Basins = &BasinsClient{client: c}
	c.Metrics = &MetricsClient{client: c}
	c.Locations = &LocationsClient{client: c}

	return c
}

func newUnaryHTTPTransport(base http.RoundTripper, connectionTimeout time.Duration) *http.Transport {
	transport, ok := base.(*http.Transport)
	if ok && transport != nil {
		// Preserve the base transport's proxy, TLS, connection-pooling, and timeout settings.
		transport = transport.Clone()
	} else {
		// http.DefaultTransport is replaceable, so its concrete type is not guaranteed.
		transport = &http.Transport{}
	}

	transport.DialContext = (&net.Dialer{
		Timeout:   connectionTimeout,
		KeepAlive: defaultTCPKeepAlive,
	}).DialContext
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	return transport
}

type schemeAwareTransport struct {
	https *http2.Transport
	h2c   *http2.Transport
}

func (t *schemeAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == schemeHTTP {
		return t.h2c.RoundTrip(req)
	}
	return t.https.RoundTrip(req)
}

func (t *schemeAwareTransport) CloseIdleConnections() {
	t.https.CloseIdleConnections()
	t.h2c.CloseIdleConnections()
}

func createStreamingClient(connectionTimeout time.Duration, base http.RoundTripper) *http.Client {
	newTransport := func() http.RoundTripper {
		return newStreamingTransport(connectionTimeout, base)
	}

	return &http.Client{
		Transport: userAgentRoundTripper{
			base:      newSpreadTransport(newTransport),
			userAgent: defaultUserAgent(),
		},
		Timeout: 0, // No timeout for streaming
	}
}

func newStreamingTransport(connectionTimeout time.Duration, base http.RoundTripper) http.RoundTripper {
	dialer := &net.Dialer{
		Timeout: connectionTimeout,
	}

	// Derive TLS and proxy configuration from the user-provided transport (if
	// any) so streaming requests honor custom RootCAs, mTLS client certs,
	// custom SNI, and proxy settings, mirroring the unary path's intent to
	// preserve base transport proxy, TLS, and dial settings. When base is not
	// an *http.Transport (or is nil), both values are nil and behavior is
	// unchanged from the original defaults.
	tlsConfig, proxyFn := streamingTransportSettings(base)
	streamingDialer := &streamingDialer{dialer: dialer, proxy: proxyFn}

	h2cTransport := &http2.Transport{
		AllowHTTP:                  true,
		MaxReadFrameSize:           http2MaxReadFrameSize,
		MaxHeaderListSize:          http2MaxHeaderListSize,
		ReadIdleTimeout:            http2ReadIdleTimeout,
		PingTimeout:                http2PingTimeout,
		WriteByteTimeout:           http2WriteByteTimeout,
		StrictMaxConcurrentStreams: false,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return streamingDialer.dial(ctx, schemeHTTP, network, addr)
		},
	}

	// http2.Transport clones TLSClientConfig per connection (see newTLSConfig),
	// so the user's RootCAs, client certs, InsecureSkipVerify, and SNI reach
	// the cfg passed into DialTLSContext and thus the TLS handshake.
	var httpsTLSConfig *tls.Config
	if tlsConfig != nil {
		httpsTLSConfig = tlsConfig.Clone()
	}

	httpsTransport := &http2.Transport{
		AllowHTTP:                  false,
		MaxReadFrameSize:           http2MaxReadFrameSize,
		MaxHeaderListSize:          http2MaxHeaderListSize,
		ReadIdleTimeout:            http2ReadIdleTimeout,
		PingTimeout:                http2PingTimeout,
		WriteByteTimeout:           http2WriteByteTimeout,
		StrictMaxConcurrentStreams: false,
		TLSClientConfig:            httpsTLSConfig,
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, connectionTimeout)
			defer cancel()

			conn, err := streamingDialer.dial(ctx, schemeHTTPS, network, addr)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(conn, cfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}

	return &schemeAwareTransport{https: httpsTransport, h2c: h2cTransport}
}

// streamingTransportSettings derives the TLS client config and proxy resolver
// from a user-provided base transport so streaming requests honor custom
// RootCAs, mTLS client certs, custom SNI, and proxy configuration. Both
// returned values are nil when base is not an *http.Transport (or is nil),
// preserving the prior default behavior.
func streamingTransportSettings(base http.RoundTripper) (*tls.Config, func(*http.Request) (*url.URL, error)) {
	transport, ok := base.(*http.Transport)
	if !ok || transport == nil {
		return nil, nil
	}
	return transport.TLSClientConfig, transport.Proxy
}

// streamingDialer dials streaming endpoints, tunneling through an HTTP proxy
// (via CONNECT) when a proxy resolver is configured. When no proxy resolver is
// configured (or the resolver returns nil for a target), it dials the target
// directly, matching the original behavior.
type streamingDialer struct {
	dialer *net.Dialer
	proxy  func(*http.Request) (*url.URL, error)
}

func (d *streamingDialer) dial(ctx context.Context, scheme, network, addr string) (net.Conn, error) {
	if d.proxy != nil {
		proxyURL, err := d.proxy(&http.Request{URL: &url.URL{Scheme: scheme, Host: addr}})
		if err != nil {
			return nil, fmt.Errorf("resolve proxy for %s: %w", addr, err)
		}
		if proxyURL != nil {
			return dialProxyTunnel(ctx, d.dialer, proxyURL, addr)
		}
	}
	return d.dialer.DialContext(ctx, network, addr)
}

// dialProxyTunnel dials an HTTP proxy and requests a CONNECT tunnel to target
// (host:port), returning the tunneled connection. Any bytes the proxy sends
// immediately after the 200 response (e.g. an HTTP/2 server preface on the far
// side of an h2c tunnel) are preserved via prefixConn, so both TLS and h2c
// streaming tunnels stay framed correctly.
func dialProxyTunnel(ctx context.Context, dialer *net.Dialer, proxyURL *url.URL, target string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if proxyAddr == "" {
		return nil, fmt.Errorf("proxy URL is missing a host: %q", proxyURL.String())
	}

	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}

	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if u := proxyURL.User; u != nil && u.Username() != "" {
		connectReq.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.String())))
	}
	if err := connectReq.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT to proxy %s: %w", proxyAddr, err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response from %s: %w", proxyAddr, err)
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s failed: %s", target, resp.Status)
	}
	// Preserve any bytes the proxy already sent right after the 200 response
	// (e.g. an HTTP/2 server preface on the far side of an h2c tunnel). The TLS
	// case never has buffered bytes since the TLS server does not speak first,
	// so this is a no-op there.
	return &prefixConn{Conn: conn, r: br}, nil
}

// prefixConn is a net.Conn whose Read drains a bufio.Reader first (preserving
// bytes already buffered during the CONNECT handshake) before reading from the
// underlying connection.
type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// Create a client using configuration from environment variables.
// Environment variables: S2_ACCESS_TOKEN, S2_ACCOUNT_ENDPOINT, S2_BASIN_ENDPOINT.
// ClientOptions fields override environment variables.
// Panics if S2_ACCESS_TOKEN is not set.
func NewFromEnvironment(opts *ClientOptions) *Client {
	if opts == nil {
		opts = &ClientOptions{}
	}

	envConfig := loadConfigFromEnv()

	if envConfig.AccessToken == "" {
		panic("S2_ACCESS_TOKEN environment variable is required")
	}

	effectiveOpts := &ClientOptions{
		HTTPClient:        opts.HTTPClient,
		RetryConfig:       opts.RetryConfig,
		Logger:            opts.Logger,
		RequestTimeout:    opts.RequestTimeout,
		ConnectionTimeout: opts.ConnectionTimeout,
		Compression:       opts.Compression,
	}

	if opts.BaseURL != "" {
		effectiveOpts.BaseURL = opts.BaseURL
	} else if envConfig.AccountTemplate != nil {
		effectiveOpts.BaseURL = envConfig.AccountTemplate.baseURL("")
	}

	if opts.MakeBasinBaseURL != nil {
		effectiveOpts.MakeBasinBaseURL = opts.MakeBasinBaseURL
	} else if envConfig.BasinTemplate != nil {
		effectiveOpts.MakeBasinBaseURL = envConfig.BasinTemplate.baseURL
	}

	return New(envConfig.AccessToken, effectiveOpts)
}

// Create a new BasinClient.
func (c *Client) Basin(name string) *BasinClient {
	if name == "" {
		panic("basin name cannot be empty")
	}
	basin := &BasinClient{
		client:             c,
		name:               name,
		baseURL:            c.makeBasinBaseURL(name),
		accessToken:        c.accessToken,
		httpClient:         c.httpClient,
		retryConfig:        c.retryConfig,
		logger:             c.logger,
		includeBasinHeader: c.includeBasinHeader,
		connectionTimeout:  c.connectionTimeout,
		requestTimeout:     c.requestTimeout,
		compression:        c.compression,
	}

	basin.Streams = &StreamsClient{basin: basin}
	return basin
}

type userAgentRoundTripper struct {
	base      http.RoundTripper
	userAgent string
}

func (rt userAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	reqCtx := req.Clone(req.Context())
	reqCtx.Header.Set("User-Agent", rt.userAgent)
	return rt.base.RoundTrip(reqCtx)
}

func (rt userAgentRoundTripper) CloseIdleConnections() {
	if closer, ok := rt.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func defaultUserAgent() string {
	ver := strings.TrimSpace(moduleVersion)
	if ver == "" {
		ver = "dev"
	}
	return fmt.Sprintf("s2-sdk-go/%s (%s)", ver, runtime.Version())
}

var moduleVersion = func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/s2-streamstore/s2-sdk-go" {
				return dep.Version
			}
		}
	}
	return ""
}()
