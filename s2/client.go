package s2

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const (
	DefaultBaseURL             = "https://a.s2.dev/v1"
	defaultRequestTimeout      = 5 * time.Second
	defaultConnectionTimeout   = 3 * time.Second
	defaultTCPKeepAlive        = 30 * time.Second
	defaultMaxIdleConnsPerHost = 32

	// HTTP/2 transport settings for streaming operations
	http2MaxReadFrameSize  = 1<<24 - 1 // HTTP/2's maximum; net/http ignores larger values
	http2MaxHeaderListSize = 10 * 1024 * 1024
	http2ReadIdleTimeout   = 30 * time.Second
	http2PingTimeout       = 15 * time.Second
	http2WriteByteTimeout  = 30 * time.Second
	// Closes streaming connections that stay idle, including ones already
	// removed from the pool after reconnect advice.
	http2IdleConnTimeout = 90 * time.Second
)

type ClientOptions struct {
	// Endpoint to connect to S2.
	// Defaults to "https://a.s2.dev/v1".
	// A custom BaseURL requires MakeBasinBaseURL, and vice versa.
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
	// Client for network policies.
	NetworkPolicies *NetworkPoliciesClient
}

func validateAccessToken(token string) error {
	if token == "" {
		return newValidationError("access token cannot be empty")
	}
	// Reject control bytes (including CR, LF, NUL) invalid in HTTP header values (spec C1).
	for i := range len(token) {
		b := token[i]
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return newValidationError("access token contains characters invalid in an HTTP header value")
		}
	}
	return nil
}

// Create a new Client.
func New(accessToken string, opts *ClientOptions) *Client {
	if err := validateAccessToken(accessToken); err != nil {
		panic(err)
	}
	if opts == nil {
		opts = &ClientOptions{}
	}

	baseURL := DefaultBaseURL
	if opts.BaseURL != "" {
		baseURL = normalizeBaseURL(opts.BaseURL)
	}
	// Either endpoint alone would send the other kind of request, with this
	// token, to S2 Cloud. An explicit DefaultBaseURL is S2 Cloud already.
	if (opts.MakeBasinBaseURL == nil && baseURL != DefaultBaseURL) ||
		(opts.MakeBasinBaseURL != nil && opts.BaseURL == "") {
		panic(newValidationError("BaseURL and MakeBasinBaseURL must be set together"))
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
	// A client shared with an earlier New is already wrapped.
	if wrapped, ok := baseTransport.(userAgentRoundTripper); ok {
		baseTransport = wrapped.base
	}
	// Streaming uses its own HTTP/2 transport; carry over the TLS and proxy
	// settings of a user-provided transport so it reaches the same endpoints.
	var streamingTLSConfig *tls.Config
	var streamingProxy proxyConfig
	if t, ok := baseTransport.(*http.Transport); ok && opts.HTTPClient != nil {
		streamingTLSConfig = t.TLSClientConfig
		streamingProxy = proxyConfigFromTransport(t)
	}
	httpClient.Transport = userAgentRoundTripper{
		base:      baseTransport,
		userAgent: defaultUserAgent(),
	}

	customMakeBasinBaseURL := opts.MakeBasinBaseURL
	var makeBasinBaseURL func(basin string) string
	if customMakeBasinBaseURL == nil {
		makeBasinBaseURL = func(basin string) string {
			return fmt.Sprintf("https://%s.b.s2.dev/v1", basin)
		}
	} else {
		makeBasinBaseURL = func(basin string) string {
			return normalizeBaseURL(customMakeBasinBaseURL(basin))
		}
	}

	var retryConfig *RetryConfig
	if opts.RetryConfig != nil {
		cfgCopy := *opts.RetryConfig
		retryConfig = &cfgCopy
	} else if DefaultRetryConfig != nil {
		cfgCopy := *DefaultRetryConfig
		retryConfig = &cfgCopy
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
		streamingClient:    createStreamingClient(connectionTimeout, streamingTLSConfig, streamingProxy),
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
	c.NetworkPolicies = &NetworkPoliciesClient{client: c}

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
	transport.TLSHandshakeTimeout = connectionTimeout
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	return transport
}

type schemeAwareTransport struct {
	https *http.Transport
	h2c   *http.Transport
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

func createStreamingClient(connectionTimeout time.Duration, tlsConfig *tls.Config, proxy proxyConfig) *http.Client {
	newTransport := func() http.RoundTripper {
		return newStreamingTransport(connectionTimeout, tlsConfig, proxy)
	}

	return &http.Client{
		Transport: userAgentRoundTripper{
			base:      newSpreadTransport(newTransport),
			userAgent: defaultUserAgent(),
		},
		Timeout: 0, // No timeout for streaming
	}
}

func newStreamingTransport(connectionTimeout time.Duration, tlsConfig *tls.Config, proxy proxyConfig) http.RoundTripper {
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
	}
	dialer := &streamingDialer{
		proxyConfig: proxy,
		dialer:      &net.Dialer{Timeout: connectionTimeout},
		tlsConfig:   tlsConfig,
	}

	newHTTP2Config := func() *http.HTTP2Config {
		return &http.HTTP2Config{
			MaxReadFrameSize:            http2MaxReadFrameSize,
			SendPingTimeout:             http2ReadIdleTimeout,
			PingTimeout:                 http2PingTimeout,
			WriteByteTimeout:            http2WriteByteTimeout,
			StrictMaxConcurrentRequests: false,
		}
	}
	dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
		defer cancel()
		conn, err := dialer.dial(dialCtx, schemeHTTP, network, addr)
		if err != nil {
			if ctx.Err() == nil {
				return nil, &connectError{err: err}
			}
			return nil, err
		}
		return conn, nil
	}
	// Handshake here rather than via TLSClientConfig so that dial, proxy and
	// TLS share one ConnectionTimeout, and so that a server that does not
	// negotiate h2 is rejected instead of net/http falling back to HTTP/1.1.
	dialTLSContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
		defer cancel()

		wrapConnectErr := func(err error) error {
			if ctx.Err() == nil {
				return &connectError{err: err}
			}
			return err
		}

		conn, err := dialer.dial(dialCtx, schemeHTTPS, network, addr)
		if err != nil {
			return nil, wrapConnectErr(err)
		}
		cfg := &tls.Config{}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				cfg.ServerName = host
			} else {
				cfg.ServerName = addr
			}
		}
		cfg.NextProtos = []string{"h2"}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			conn.Close()
			return nil, wrapConnectErr(err)
		}
		if proto := tlsConn.ConnectionState().NegotiatedProtocol; proto != "h2" {
			tlsConn.Close()
			return nil, wrapConnectErr(fmt.Errorf("server at %s did not negotiate HTTP/2 (got %q)", addr, proto))
		}
		return tlsConn, nil
	}

	// Each pooled transport carries one HTTP/2 connection (spreadTransport
	// adds transports as sessions grow), so limit it to a single dial; without
	// this, net/http dials once per concurrent request during a cold start.
	h2cProtocols := new(http.Protocols)
	h2cProtocols.SetUnencryptedHTTP2(true)
	h2cTransport := &http.Transport{
		Protocols:              h2cProtocols,
		HTTP2:                  newHTTP2Config(),
		MaxResponseHeaderBytes: http2MaxHeaderListSize,
		IdleConnTimeout:        http2IdleConnTimeout,
		MaxConnsPerHost:        1,
		DialContext:            dialContext,
	}

	httpsProtocols := new(http.Protocols)
	httpsProtocols.SetHTTP2(true)
	httpsTransport := &http.Transport{
		Protocols:              httpsProtocols,
		HTTP2:                  newHTTP2Config(),
		MaxResponseHeaderBytes: http2MaxHeaderListSize,
		IdleConnTimeout:        http2IdleConnTimeout,
		MaxConnsPerHost:        1,
		TLSClientConfig:        tlsConfig,
		DialTLSContext:         dialTLSContext,
	}

	return &schemeAwareTransport{https: httpsTransport, h2c: h2cTransport}
}

// Create a client using configuration from environment variables.
// Environment variables: S2_ACCESS_TOKEN, S2_ACCOUNT_ENDPOINT, S2_BASIN_ENDPOINT.
// ClientOptions fields override environment variables.
// Panics if S2_ACCESS_TOKEN is not set, or if only one of S2_ACCOUNT_ENDPOINT
// and S2_BASIN_ENDPOINT is set.
func NewFromEnvironment(opts *ClientOptions) *Client {
	if opts == nil {
		opts = &ClientOptions{}
	}

	envConfig := loadConfigFromEnv()

	if envConfig.AccessToken == "" {
		panic(newValidationError("S2_ACCESS_TOKEN environment variable is required"))
	}
	if opts.BaseURL == "" && opts.MakeBasinBaseURL == nil {
		envConfig.requireEndpointPair()
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

	// The name becomes part of the request host, so an invalid one could send
	// requests and the access token to another host. Calls through this
	// handle fail with the ValidationError instead (spec C9).
	if err := validateBasinName(BasinName(name)); err != nil {
		basin.invalidNameErr = err
		basin.baseURL = "https://invalid-basin-name.invalid/v1"
		basin.httpClient = &http.Client{Transport: rejectTransport{err: err}}
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
	resp, err := rt.base.RoundTrip(reqCtx)
	if err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// S2 never redirects. Fail here, below http.Client's redirect policy
		// (which may be the caller's), so headers such as s2-encryption-key
		// are never forwarded to the redirect target.
		return nil, parseHTTPError(resp)
	}
	return resp, err
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

// Format keeps the access token out of every fmt verb, including %+v and %#v,
// which would otherwise print unexported fields (spec L3).
func (c Client) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "s2.Client{baseURL: %q}", c.baseURL)
}
