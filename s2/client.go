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

	"golang.org/x/net/http2"
)

const (
	DefaultBaseURL           = "https://aws.s2.dev/v1"
	defaultRequestTimeout    = 30 * time.Second
	defaultConnectionTimeout = 10 * time.Second

	// HTTP/2 transport settings for streaming operations
	http2MaxReadFrameSize  = 16 * 1024 * 1024
	http2MaxHeaderListSize = 10 * 1024 * 1024
	http2ReadIdleTimeout   = 30 * time.Second
	http2PingTimeout       = 15 * time.Second
	http2WriteByteTimeout  = 30 * time.Second
)

type ClientOptions struct {
	// URL to connect to S2.
	// Defaults to "https://aws.s2.dev/v1".
	BaseURL string
	// HTTP client used for requests.
	HTTPClient *http.Client
	// Allows customizing how basin endpoints are constructed.
	// If provided, this function is used to derive the base URL for a given basin.
	// When provided, the "s2-basin" HTTP header is automatically included in basin-scoped requests.
	MakeBasinBaseURL func(basin string) string
	// Retry configuration.
	RetryConfig *RetryConfig
	// Get SDK level logs.
	Logger *slog.Logger
	// Overall timeout for HTTP requests.
	// Defaults to 30 seconds.
	RequestTimeout time.Duration
	// Timeout for establishing TCP connections.
	// Defaults to 10 seconds.
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
}

// Create a new Client.
func New(accessToken string, opts *ClientOptions) *Client {
	if accessToken == "" {
		panic("access token cannot be empty")
	}
	if opts == nil {
		opts = &ClientOptions{}
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
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
			Timeout: requestTimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: connectionTimeout,
				}).DialContext,
			},
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

	makeBasinBaseURL := opts.MakeBasinBaseURL
	if makeBasinBaseURL == nil {
		makeBasinBaseURL = func(basin string) string {
			return fmt.Sprintf("https://%s.b.aws.s2.dev/v1", basin)
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
		streamingClient:    createStreamingClient(connectionTimeout),
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

	return c
}

type schemeAwareTransport struct {
	https *http2.Transport
	h2c   *http2.Transport
}

func (t *schemeAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "http" {
		return t.h2c.RoundTrip(req)
	}
	return t.https.RoundTrip(req)
}

func createStreamingClient(connectionTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: connectionTimeout,
	}

	h2cTransport := &http2.Transport{
		AllowHTTP:                  true,
		MaxReadFrameSize:           http2MaxReadFrameSize,
		MaxHeaderListSize:          http2MaxHeaderListSize,
		ReadIdleTimeout:            http2ReadIdleTimeout,
		PingTimeout:                http2PingTimeout,
		WriteByteTimeout:           http2WriteByteTimeout,
		StrictMaxConcurrentStreams: false,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}

	httpsTransport := &http2.Transport{
		AllowHTTP:                  false,
		MaxReadFrameSize:           http2MaxReadFrameSize,
		MaxHeaderListSize:          http2MaxHeaderListSize,
		ReadIdleTimeout:            http2ReadIdleTimeout,
		PingTimeout:                http2PingTimeout,
		WriteByteTimeout:           http2WriteByteTimeout,
		StrictMaxConcurrentStreams: false,
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
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

	return &http.Client{
		Transport: userAgentRoundTripper{
			base:      &schemeAwareTransport{https: httpsTransport, h2c: h2cTransport},
			userAgent: defaultUserAgent(),
		},
		Timeout: 0, // No timeout for streaming
	}
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
	}

	if opts.BaseURL != "" {
		effectiveOpts.BaseURL = opts.BaseURL
	} else if envConfig.AccountEndpoint != "" {
		effectiveOpts.BaseURL = envConfig.AccountEndpoint + "/v1"
	}

	if opts.MakeBasinBaseURL != nil {
		effectiveOpts.MakeBasinBaseURL = opts.MakeBasinBaseURL
	} else if envConfig.BasinEndpoint != "" {
		effectiveOpts.MakeBasinBaseURL = makeBasinURLFunc(envConfig.BasinEndpoint)
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
