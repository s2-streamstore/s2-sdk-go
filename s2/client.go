package s2

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type Cloud uint

const (
	CloudAWS Cloud = iota
	// More clouds to come...
)

func (c Cloud) String() string {
	switch c {
	case CloudAWS:
		return "aws"
	default:
		return "<unknown cloud>"
	}
}

func ParseCloud(s string) (Cloud, error) {
	switch strings.ToLower(s) {
	case "aws":
		return CloudAWS, nil
	default:
		return 0, fmt.Errorf("unsupported cloud %q", s)
	}
}

type Endpoints struct {
	Account string
	Basin   string
}

func EndpointsForCloud(cloud Cloud) *Endpoints {
	return &Endpoints{
		Account: fmt.Sprintf("%s.s2.dev", cloud),
		Basin:   fmt.Sprintf("{basin}.b.%s.s2.dev", cloud),
	}
}

func EndpointsForCell(cloud Cloud, cellID string) *Endpoints {
	endpoint := fmt.Sprintf("%s.o.%s.s2.dev", cellID, cloud)
	return &Endpoints{
		Account: endpoint,
		Basin:   endpoint,
	}
}

func EndpointsFromEnv() (*Endpoints, error) {
	cloudStr := os.Getenv("S2_CLOUD")
	cloud := CloudAWS
	if cloudStr != "" {
		var err error
		cloud, err = ParseCloud(cloudStr)
		if err != nil {
			return nil, err
		}
	}

	endpoints := EndpointsForCloud(cloud)

	accountEndpoint := os.Getenv("S2_ACCOUNT_ENDPOINT")
	if accountEndpoint != "" {
		endpoints.Account = accountEndpoint
	}

	basinEndpoint := os.Getenv("S2_BASIN_ENDPOINT")
	if basinEndpoint != "" {
		endpoints.Basin = basinEndpoint
	}

	return endpoints, nil
}

type ConfigParam interface {
	apply(*clientConfig) error
}

type applyConfigParamFunc func(*clientConfig) error

func (f applyConfigParamFunc) apply(cc *clientConfig) error {
	return f(cc)
}

func WithEndpoints(e *Endpoints) ConfigParam {
	return applyConfigParamFunc(func(cc *clientConfig) error {
		// TODO: Validate endpoints
		cc.endpoints = e
		return nil
	})
}

func WithConnectTimeout(d time.Duration) ConfigParam {
	return applyConfigParamFunc(func(cc *clientConfig) error {
		cc.connectTimeout = d
		return nil
	})
}

func WithUserAgent(u string) ConfigParam {
	return applyConfigParamFunc(func(cc *clientConfig) error {
		cc.userAgent = u
		return nil
	})
}

func WithRetryBackoffDuration(d time.Duration) ConfigParam {
	return applyConfigParamFunc(func(cc *clientConfig) error {
		cc.retryBackoffDuration = d
		return nil
	})
}

func WithMaxRetryAttempts(n uint) ConfigParam {
	return applyConfigParamFunc(func(cc *clientConfig) error {
		cc.maxRetryAttempts = n
		return nil
	})
}

type Client struct {
	inner *clientInner
}

func NewClient(authToken string, params ...ConfigParam) (*Client, error) {
	config, err := newClientConfig(authToken, params...)
	if err != nil {
		return nil, err
	}

	inner, err := newClientInner(config, "" /* = basin */)
	if err != nil {
		return nil, err
	}

	return &Client{
		inner: inner,
	}, nil
}

func (c *Client) listBasins(ctx context.Context, req *ListBasinsRequest) (*ListBasinsResponse, error) {
	r := listBasinsServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}
	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) createBasin(ctx context.Context, req *CreateBasinRequest) (*BasinInfo, error) {
	r := createBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
		ReqID:  uuid.New(),
	}
	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) deleteBasin(ctx context.Context, req *DeleteBasinRequest) error {
	r := deleteBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}
	_, err := sendRetryable(ctx, c.inner, &r)
	return err
}

func (c *Client) reconfigureBasin(ctx context.Context, req *ReconfigureBasinRequest) (*BasinConfig, error) {
	r := reconfigureBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}
	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) getBasinConfig(ctx context.Context, basin string) (*BasinConfig, error) {
	r := getBasinConfigRequest{
		Client: c.inner.AccountServiceClient(),
		Basin:  basin,
	}
	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) BasinClient(basin string) (*BasinClient, error) {
	inner, err := c.inner.BasinClient(basin)
	if err != nil {
		return nil, err
	}
	return &BasinClient{
		inner: inner,
	}, nil
}

type BasinClient struct {
	inner *clientInner
}

func NewBasinClient(basin, authToken string, params ...ConfigParam) (*BasinClient, error) {
	config, err := newClientConfig(authToken, params...)
	if err != nil {
		return nil, err
	}

	inner, err := newClientInner(config, basin)
	if err != nil {
		return nil, err
	}

	return &BasinClient{
		inner: inner,
	}, nil
}

func (b *BasinClient) listStreams(ctx context.Context, req *ListStreamsRequest) (*ListStreamsResponse, error) {
	r := listStreamsServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
	}
	return sendRetryable(ctx, b.inner, &r)
}

func (b *BasinClient) createStream(ctx context.Context, req *CreateStreamRequest) (*StreamInfo, error) {
	r := createStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
		ReqID:  uuid.New(),
	}
	return sendRetryable(ctx, b.inner, &r)
}

func (b *BasinClient) deleteStream(ctx context.Context, req *DeleteStreamRequest) error {
	r := deleteStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
	}
	_, err := sendRetryable(ctx, b.inner, &r)
	return err
}

func (b *BasinClient) reconfigureStream(ctx context.Context, req *ReconfigureStreamRequest) (*StreamConfig, error) {
	r := reconfigureStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
	}
	return sendRetryable(ctx, b.inner, &r)
}

func (b *BasinClient) getStreamConfig(ctx context.Context, stream string) (*StreamConfig, error) {
	r := getStreamConfigServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Stream: stream,
	}
	return sendRetryable(ctx, b.inner, &r)
}

func (b *BasinClient) StreamClient(stream string) *StreamClient {
	return &StreamClient{
		stream: stream,
		inner:  b.inner,
	}
}

type StreamClient struct {
	stream string
	inner  *clientInner
}

func NewStreamClient(basin, stream, authToken string, params ...ConfigParam) (*StreamClient, error) {
	basinClient, err := NewBasinClient(basin, authToken, params...)
	if err != nil {
		return nil, err
	}
	return basinClient.StreamClient(stream), nil
}

func (s *StreamClient) checkTail(ctx context.Context) (uint64, error) {
	r := checkTailServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
	}
	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) append(ctx context.Context, input *AppendInput) (*AppendOutput, error) {
	r := appendServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
		Input:  input,
	}
	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) appendSession(ctx context.Context) (Sender[*AppendInput], Receiver[*AppendOutput], error) {
	r := appendSessionServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
	}
	channel, err := sendRetryable(ctx, s.inner, &r)
	if err != nil {
		return nil, nil, err
	}
	return channel.Sender, channel.Receiver, nil
}

func (s *StreamClient) read(ctx context.Context, req *ReadRequest) (ReadOutput, error) {
	r := readServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
		Req:    req,
	}
	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) readSession(ctx context.Context, req *ReadSessionRequest) (Receiver[ReadOutput], error) {
	r := readSessionServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
		Req:    req,
	}
	return sendRetryable(ctx, s.inner, &r)
}

type clientConfig struct {
	authToken            string
	endpoints            *Endpoints
	connectTimeout       time.Duration
	userAgent            string
	retryBackoffDuration time.Duration
	maxRetryAttempts     uint
}

func newClientConfig(authToken string, params ...ConfigParam) (*clientConfig, error) {
	if authToken == "" {
		return nil, fmt.Errorf("auth token cannot be empty")
	}

	// Default configuration
	config := &clientConfig{
		authToken:            authToken,
		endpoints:            EndpointsForCloud(CloudAWS),
		connectTimeout:       3 * time.Second,
		userAgent:            "s2-sdk-go",
		retryBackoffDuration: 100 * time.Millisecond,
		maxRetryAttempts:     3,
	}

	for _, param := range params {
		if err := param.apply(config); err != nil {
			return nil, err
		}
	}

	return config, nil
}

type clientInner struct {
	conn   *grpc.ClientConn
	config *clientConfig
	basin  string
}

func newClientInner(config *clientConfig, basin string) (*clientInner, error) {
	// TODO: Configure dial options
	creds := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, // Ensure HTTP/2 support
	})

	endpoint := config.endpoints.Account
	if basin != "" {
		endpoint = config.endpoints.Basin
		if strings.HasPrefix(endpoint, "{basin}.") {
			endpoint = basin + strings.TrimPrefix(endpoint, "{basin}")
			basin = "" // No need to set header
		}
	}

	connectParams := grpc.ConnectParams{
		MinConnectTimeout: config.connectTimeout,
		Backoff:           backoff.DefaultConfig,
	}

	conn, err := grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(connectParams),
	)
	if err != nil {
		return nil, err
	}

	return &clientInner{
		conn:   conn,
		config: config,
		basin:  basin,
	}, nil
}

func (c *clientInner) BasinClient(basin string) (*clientInner, error) {
	if c.config.endpoints.Account == c.config.endpoints.Basin {
		// No need to reconnect
		//
		// NOTE: Create a new "clientInner" since we don't want the old one
		// to send basin header.
		return &clientInner{
			conn:   c.conn,
			config: c.config,
			basin:  basin,
		}, nil
	}

	return newClientInner(c.config, basin)
}

func (c *clientInner) AccountServiceClient() pb.AccountServiceClient {
	return pb.NewAccountServiceClient(c.conn)
}

func (c *clientInner) BasinServiceClient() pb.BasinServiceClient {
	return pb.NewBasinServiceClient(c.conn)
}

func (c *clientInner) StreamServiceClient() pb.StreamServiceClient {
	return pb.NewStreamServiceClient(c.conn)
}

func sendRetryable[T any](ctx context.Context, c *clientInner, r serviceRequest[T]) (T, error) {
	// Add required headers
	ctx = ctxWithHeaders(
		ctx,
		"user-agent", c.config.userAgent,
		"authorization", fmt.Sprintf("Bearer %s", c.config.authToken),
	)
	if c.basin != "" {
		ctx = ctxWithHeaders(ctx, "s2-basin", c.basin)
	}

	return sendRetryableInner(ctx, r, c.config.retryBackoffDuration, c.config.maxRetryAttempts)
}

func sendRetryableInner[T any](
	ctx context.Context,
	r serviceRequest[T],
	retryBackoffDuration time.Duration,
	maxRetryAttempts uint,
) (T, error) {
	var finalErr error

	for i := uint(0); i < maxRetryAttempts; i++ {
		v, err := r.Send(ctx)
		if err == nil {
			return v, nil
		}

		// Figure out if need to retry
		if !r.IdempotencyLevel().IsIdempotent() {
			return v, err
		}

		statusErr, ok := status.FromError(err)
		if !ok {
			return v, err
		}

		switch statusErr.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown:
			// We should retry
		default:
			return v, err
		}

		finalErr = err

		select {
		case <-time.After(retryBackoffDuration):
		case <-ctx.Done():
			return v, ctx.Err()
		}
	}

	var v T
	return v, finalErr
}
