package s2

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

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
	r := &listBasinsServiceRequest{
		client: c.inner.accountServiceClient(),
		req:    req,
	}
	return sendRetryable[*ListBasinsResponse](ctx, c.inner, r)
}

func (c *Client) BasinClient(basin string) (*BasinClient, error) {
	inner, err := c.inner.basinClient(basin)
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

func (b *BasinClient) listStreams(ctx context.Context, req *ListStreamsRequest) (*ListStreamsResponse, error) {
	r := &listStreamsServiceRequest{
		client: b.inner.basinServiceClient(),
		req:    req,
	}
	return sendRetryable[*ListStreamsResponse](ctx, b.inner, r)
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

func (s *StreamClient) checkTail(ctx context.Context) (uint64, error) {
	r := &checkTailServiceRequest{
		client: s.inner.streamServiceClient(),
		stream: s.stream,
	}
	return sendRetryable[uint64](ctx, s.inner, r)
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

func (c *clientInner) basinClient(basin string) (*clientInner, error) {
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

func (c *clientInner) accountServiceClient() pb.AccountServiceClient {
	return pb.NewAccountServiceClient(c.conn)
}

func (c *clientInner) basinServiceClient() pb.BasinServiceClient {
	return pb.NewBasinServiceClient(c.conn)
}

func (c *clientInner) streamServiceClient() pb.StreamServiceClient {
	return pb.NewStreamServiceClient(c.conn)
}

func sendRetryable[T any](ctx context.Context, inner *clientInner, r serviceRequest) (T, error) {
	ret, err := sendRetryableInner(ctx, inner.basin, inner.config, r)
	if err != nil {
		var def T
		return def, err
	}
	return assertType[T](ret), nil
}

func sendRetryableInner(ctx context.Context, basin string, config *clientConfig, r serviceRequest) (any, error) {
	// Add required headers
	if basin != "" {
		ctx = ctxWithHeader(ctx, "s2-basin", basin)
	}

	var finalErr error

	for i := uint(0); i < config.maxRetryAttempts; i++ {
		ret, err := r.send(ctx)
		if err == nil {
			return ret, nil
		}

		// Figure out if need to retry
		if !r.idempotencyLevel().isIdempotent() {
			return nil, err
		}

		statusErr, ok := status.FromError(err)
		if !ok {
			return nil, err
		}

		switch statusErr.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown:
			// We should retry
		default:
			return nil, err
		}

		finalErr = err

		select {
		case <-time.After(config.retryBackoffDuration):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, finalErr
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
