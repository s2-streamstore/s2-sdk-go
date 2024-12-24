package s2

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

type Client struct {
	inner *clientInner
}

type ConfigParam interface {
	apply(*clientConfig) error
}

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

func WithCloud(cloud Cloud) *Endpoints {
	return &Endpoints{
		Account: fmt.Sprintf("%s.s2.dev", cloud),
		Basin:   fmt.Sprintf("{basin}.b.%s.s2.dev", cloud),
	}
}

func WithCloudAndCell(cloud Cloud, cellID string) *Endpoints {
	panic("todo")
}

func WithEndpointsFromEnv() (*Endpoints, error) {
	panic("todo")
}

func WithRawEndpoints(account string, basin string) *Endpoints {
	return &Endpoints{
		Account: account,
		Basin:   basin,
	}
}

func (e *Endpoints) apply(config *clientConfig) error {
	if config.endpoints != nil {
		return fmt.Errorf("multiple attempts to set endpoints")
	}

	// TODO: Validate endpoints
	config.endpoints = e
	return nil
}

func NewClient(authToken string, params ...ConfigParam) (*Client, error) {
	config, err := newClientConfig(authToken, params...)
	if err != nil {
		return nil, err
	}

	inner, err := newClientInner(config, config.endpoints.Account)
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
	resp, err := r.send(ctx)
	if err != nil {
		return nil, err
	}
	return assertType[*ListBasinsResponse](resp), nil
}

type clientConfig struct {
	authToken string
	endpoints *Endpoints
}

func newClientConfig(authToken string, params ...ConfigParam) (*clientConfig, error) {
	config := &clientConfig{
		authToken: authToken,
	}

	for _, param := range params {
		if err := param.apply(config); err != nil {
			return nil, err
		}
	}

	if authToken == "" {
		return nil, fmt.Errorf("auth token cannot be empty")
	}

	if config.endpoints == nil {
		config.endpoints = WithCloud(CloudAWS)
	}

	return config, nil
}

type clientInner struct {
	conn   grpc.ClientConnInterface
	config *clientConfig
}

func newClientInner(config *clientConfig, endpoint string) (*clientInner, error) {
	// TODO: Configure dial options
	creds := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, // Ensure HTTP/2 support
	})

	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:443", endpoint),
		grpc.WithTransportCredentials(creds),
		grpc.WithUnaryInterceptor(authInterceptor(config.authToken)),
	)
	if err != nil {
		return nil, err
	}

	return &clientInner{
		conn:   conn,
		config: config,
	}, nil
}

func (c *clientInner) accountServiceClient() pb.AccountServiceClient {
	return pb.NewAccountServiceClient(c.conn)
}

func authInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req interface{},
		reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		md := metadata.Pairs("authorization", fmt.Sprintf("Bearer %s", token))
		newCtx := metadata.NewOutgoingContext(ctx, md)
		return invoker(newCtx, method, req, reply, cc, opts...)
	}
}
