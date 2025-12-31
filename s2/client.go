package s2

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultBaseURL           = "https://aws.s2.dev/v1"
	defaultRequestTimeout    = 30 * time.Second
	defaultConnectionTimeout = 10 * time.Second
)

type ClientOptions struct {
	// URL to connect to S2.
	// Defaults to "https://aws.s2.dev/v1".
	BaseURL string
	// HTTP client used for requests.
	HTTPClient *http.Client
	// Allows customizing how basin endpoints are constructed.
	// If provided, this function is used to derive the base URL for a given basin.
	MakeBasinBaseURL func(basin string) string
	// Retry configuration.
	RetryConfig *RetryConfig
	// Get SDK level logs.
	Logger *slog.Logger
	// Sends an "s2-basin" HTTP header with the basin name on all basin-scoped requests.
	IncludeBasinHeader bool
	// AllowH2C enables HTTP/2 over cleartext (h2c) connections.
	// Defaults to false
	AllowH2C bool
	// Overall timeout for HTTP requests.
	// Defaults to 30 seconds.
	RequestTimeout time.Duration
	// Timeout for establishing TCP connections.
	// Defaults to 10 seconds.
	ConnectionTimeout time.Duration
}

type Client struct {
	accessToken        string
	baseURL            string
	httpClient         *http.Client
	makeBasinBaseURL   func(basin string) string
	retryConfig        *RetryConfig
	logger             *slog.Logger
	includeBasinHeader bool
	allowH2C           bool
	requestTimeout     time.Duration
	connectionTimeout  time.Duration

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
		makeBasinBaseURL:   makeBasinBaseURL,
		retryConfig:        retryConfig,
		logger:             opts.Logger,
		includeBasinHeader: opts.IncludeBasinHeader,
		allowH2C:           opts.AllowH2C,
		connectionTimeout:  connectionTimeout,
		requestTimeout:     requestTimeout,
	}

	c.AccessTokens = &AccessTokensClient{client: c}
	c.Basins = &BasinsClient{client: c}
	c.Metrics = &MetricsClient{client: c}

	return c
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
		HTTPClient:         opts.HTTPClient,
		RetryConfig:        opts.RetryConfig,
		Logger:             opts.Logger,
		IncludeBasinHeader: opts.IncludeBasinHeader,
		AllowH2C:           opts.AllowH2C,
		RequestTimeout:     opts.RequestTimeout,
		ConnectionTimeout:  opts.ConnectionTimeout,
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
		name:               name,
		baseURL:            c.makeBasinBaseURL(name),
		accessToken:        c.accessToken,
		httpClient:         c.httpClient,
		retryConfig:        c.retryConfig,
		logger:             c.logger,
		includeBasinHeader: c.includeBasinHeader,
		allowH2C:           c.allowH2C,
		connectionTimeout:  c.connectionTimeout,
		requestTimeout:     c.requestTimeout,
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
	return fmt.Sprintf("s2-go-sdk/%s (%s)", ver, runtime.Version())
}

var moduleVersion = ""
