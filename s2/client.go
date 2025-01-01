package s2

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc"
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
	panic("todo")
}

func EndpointsFromEnv() (*Endpoints, error) {
	panic("todo")
}

func (e *Endpoints) apply(config *clientConfig) error {
	// TODO: Validate endpoints
	config.endpoints = e
	return nil
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
	err := c.inner.SendRetryable(ctx, &r)
	return r.Resp, err
}

func (c *Client) createBasin(ctx context.Context, req *CreateBasinRequest) (*BasinInfo, error) {
	r := createBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
		ReqID:  uuid.New(),
	}
	err := c.inner.SendRetryable(ctx, &r)
	return r.Info, err
}

func (c *Client) deleteBasin(ctx context.Context, req *DeleteBasinRequest) error {
	r := deleteBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}
	return c.inner.SendRetryable(ctx, &r)
}

func (c *Client) reconfigureBasin(ctx context.Context, req *ReconfigureBasinRequest) (*BasinConfig, error) {
	r := reconfigureBasinServiceRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}
	err := c.inner.SendRetryable(ctx, &r)
	return r.UpdatedConfig, err
}

func (c *Client) getBasinConfig(ctx context.Context, basin string) (*BasinConfig, error) {
	r := getBasinConfigRequest{
		Client: c.inner.AccountServiceClient(),
		Basin:  basin,
	}
	err := c.inner.SendRetryable(ctx, &r)
	return r.Config, err
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
	err := b.inner.SendRetryable(ctx, &r)
	return r.Resp, err
}

func (b *BasinClient) createStream(ctx context.Context, req *CreateStreamRequest) (*StreamInfo, error) {
	r := createStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
		ReqID:  uuid.New(),
	}
	err := b.inner.SendRetryable(ctx, &r)
	return r.Info, err
}

func (b *BasinClient) deleteStream(ctx context.Context, req *DeleteStreamRequest) error {
	r := deleteStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
	}
	return b.inner.SendRetryable(ctx, &r)
}

func (b *BasinClient) reconfigureStream(ctx context.Context, req *ReconfigureStreamRequest) (*StreamConfig, error) {
	r := reconfigureStreamServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Req:    req,
	}
	err := b.inner.SendRetryable(ctx, &r)
	return r.UpdatedConfig, err
}

func (b *BasinClient) getStreamConfig(ctx context.Context, stream string) (*StreamConfig, error) {
	r := getStreamConfigServiceRequest{
		Client: b.inner.BasinServiceClient(),
		Stream: stream,
	}
	err := b.inner.SendRetryable(ctx, &r)
	return r.Config, err
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
	err := s.inner.SendRetryable(ctx, &r)
	return r.Tail, err
}

func (s *StreamClient) append(ctx context.Context, input *AppendInput) (*AppendOutput, error) {
	r := appendServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
		Input:  input,
	}
	err := s.inner.SendRetryable(ctx, &r)
	return r.Output, err
}

func (s *StreamClient) read(ctx context.Context, req *ReadRequest) (ReadOutput, error) {
	r := readServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
		Req:    req,
	}
	err := s.inner.SendRetryable(ctx, &r)
	return r.Output, err
}

type clientConfig struct {
	authToken            string
	endpoints            *Endpoints
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

	conn, err := grpc.NewClient(
		endpoint,
		grpc.WithTransportCredentials(creds),
		grpc.WithUnaryInterceptor(authHeaderInterceptor(config.authToken)),
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

func (c *clientInner) SendRetryable(ctx context.Context, r serviceRequest) error {
	// Add required headers
	if c.basin != "" {
		ctx = ctxWithHeader(ctx, "s2-basin", c.basin)
	}

	return sendRetryableInner(ctx, r, c.config.retryBackoffDuration, c.config.maxRetryAttempts)
}

func sendRetryableInner(
	ctx context.Context,
	r serviceRequest,
	retryBackoffDuration time.Duration,
	maxRetryAttempts uint,
) error {
	var finalErr error

	for i := uint(0); i < maxRetryAttempts; i++ {
		err := r.Send(ctx)
		if err == nil {
			return nil
		}

		// Figure out if need to retry
		if !r.IdempotencyLevel().IsIdempotent() {
			return err
		}

		statusErr, ok := status.FromError(err)
		if !ok {
			return err
		}

		switch statusErr.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown:
			// We should retry
		default:
			return err
		}

		finalErr = err

		select {
		case <-time.After(retryBackoffDuration):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return finalErr
}

func authHeaderInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req any,
		reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx = ctxWithHeader(ctx, "authorization", fmt.Sprintf("Bearer %s", token))
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
