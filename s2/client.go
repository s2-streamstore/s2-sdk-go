package s2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/s2-streamstore/s2-sdk-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

// Errors.
var (
	ErrUnspportedCloud = errors.New("unsupported cloud")
	ErrEmptyAuthToken  = errors.New("auth token cannot be empty")
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
		return 0, fmt.Errorf("%w: %q", ErrUnspportedCloud, s)
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

type ClientConfigParam interface {
	apply(*clientConfig) error
}

type applyClientConfigParamFunc func(*clientConfig) error

func (f applyClientConfigParamFunc) apply(cc *clientConfig) error {
	return f(cc)
}

func WithEndpoints(e *Endpoints) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		// TODO: Validate endpoints
		cc.Endpoints = e

		return nil
	})
}

func WithConnectTimeout(d time.Duration) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.ConnectTimeout = d

		return nil
	})
}

func WithUserAgent(u string) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.UserAgent = u

		return nil
	})
}

func WithRetryBackoffDuration(d time.Duration) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.RetryBackoffDuration = d

		return nil
	})
}

func WithMaxRetryAttempts(n uint) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.MaxRetryAttempts = n

		return nil
	})
}

type Client struct {
	inner *clientInner
}

func NewClient(authToken string, params ...ClientConfigParam) (*Client, error) {
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

func NewBasinClient(basin, authToken string, params ...ClientConfigParam) (*BasinClient, error) {
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

func NewStreamClient(basin, stream, authToken string, params ...ClientConfigParam) (*StreamClient, error) {
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

	recv, err := sendRetryable(ctx, s.inner, &r)
	if err != nil {
		return nil, err
	}

	return &readSessionReceiver{
		ServiceReq: &r,
		ReqCtx:     ctx,
		Client:     s.inner,
		RecvInner:  recv,
	}, nil
}

type clientConfig struct {
	AuthToken            string
	Endpoints            *Endpoints
	ConnectTimeout       time.Duration
	UserAgent            string
	RetryBackoffDuration time.Duration
	MaxRetryAttempts     uint
}

func newClientConfig(authToken string, params ...ClientConfigParam) (*clientConfig, error) {
	if authToken == "" {
		return nil, ErrEmptyAuthToken
	}

	// Default configuration
	config := &clientConfig{
		AuthToken:            authToken,
		Endpoints:            EndpointsForCloud(CloudAWS),
		ConnectTimeout:       3 * time.Second,
		UserAgent:            "s2-sdk-go",
		RetryBackoffDuration: 100 * time.Millisecond,
		MaxRetryAttempts:     3,
	}

	for _, param := range params {
		if err := param.apply(config); err != nil {
			return nil, err
		}
	}

	return config, nil
}

type clientInner struct {
	Conn   *grpc.ClientConn
	Config *clientConfig
	Basin  string
}

func newClientInner(config *clientConfig, basin string) (*clientInner, error) {
	// TODO: Configure dial options
	creds := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, // Ensure HTTP/2 support
	})

	endpoint := config.Endpoints.Account
	if basin != "" {
		endpoint = config.Endpoints.Basin
		if strings.HasPrefix(endpoint, "{basin}.") {
			endpoint = basin + strings.TrimPrefix(endpoint, "{basin}")
			basin = "" // No need to set header
		}
	}

	connectParams := grpc.ConnectParams{
		MinConnectTimeout: config.ConnectTimeout,
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
		Conn:   conn,
		Config: config,
		Basin:  basin,
	}, nil
}

func (c *clientInner) BasinClient(basin string) (*clientInner, error) {
	if c.Config.Endpoints.Account == c.Config.Endpoints.Basin {
		// No need to reconnect
		//
		// NOTE: Create a new "clientInner" since we don't want the old one
		// to send basin header.
		return &clientInner{
			Conn:   c.Conn,
			Config: c.Config,
			Basin:  basin,
		}, nil
	}

	return newClientInner(c.Config, basin)
}

func (c *clientInner) AccountServiceClient() pb.AccountServiceClient {
	return pb.NewAccountServiceClient(c.Conn)
}

func (c *clientInner) BasinServiceClient() pb.BasinServiceClient {
	return pb.NewBasinServiceClient(c.Conn)
}

func (c *clientInner) StreamServiceClient() pb.StreamServiceClient {
	return pb.NewStreamServiceClient(c.Conn)
}

func sendRetryable[T any](ctx context.Context, c *clientInner, r serviceRequest[T]) (T, error) {
	// Add required headers
	ctx = ctxWithHeaders(
		ctx,
		"user-agent", c.Config.UserAgent,
		"authorization", fmt.Sprintf("Bearer %s", c.Config.AuthToken),
	)
	if c.Basin != "" {
		ctx = ctxWithHeaders(ctx, "s2-basin", c.Basin)
	}

	return sendRetryableInner(ctx, r, c.Config.RetryBackoffDuration, c.Config.MaxRetryAttempts)
}

func sendRetryableInner[T any](
	ctx context.Context,
	r serviceRequest[T],
	retryBackoffDuration time.Duration,
	maxRetryAttempts uint,
) (T, error) {
	var finalErr error

	for range maxRetryAttempts {
		v, err := r.Send(ctx)
		if err == nil {
			return v, nil
		}

		if !shouldRetry(r, err) {
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

type readSessionReceiver struct {
	ServiceReq *readSessionServiceRequest
	ReqCtx     context.Context
	Client     *clientInner
	RecvInner  Receiver[ReadOutput]
}

func (r *readSessionReceiver) Recv() (ReadOutput, error) {
	var finalErr error

	for range r.Client.Config.MaxRetryAttempts {
		next, err := r.RecvInner.Recv()
		if err == nil {
			if batch, ok := next.(ReadOutputBatch); ok && len(batch.Records) > 0 {
				r.ServiceReq.Req.StartSeqNum = 1 + batch.Records[len(batch.Records)-1].SeqNum
			}

			return next, nil
		}

		if !shouldRetry(r.ServiceReq, err) {
			return nil, err
		}

		finalErr = err

		select {
		case <-time.After(r.Client.Config.RetryBackoffDuration):
			newRecv, newErr := sendRetryable(r.ReqCtx, r.Client, r.ServiceReq)
			if newErr != nil {
				return nil, newErr
			}

			r.RecvInner = newRecv

		case <-r.ReqCtx.Done():
			return nil, r.ReqCtx.Err()
		}
	}

	return nil, finalErr
}
