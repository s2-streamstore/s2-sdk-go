package s2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/s2-streamstore/optr"
	"github.com/s2-streamstore/s2-sdk-go/internal/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

// Client errors.
var (
	ErrUnspportedCloud = errors.New("unsupported cloud")
	ErrEmptyAuthToken  = errors.New("auth token cannot be empty")
	ErrNumTriesZero    = errors.New("number of tries must be more than 0")
)

// S2 cloud environment to connect with.
type Cloud uint

const (
	// S2 running on AWS.
	CloudAWS Cloud = iota
)

func (c Cloud) String() string {
	switch c {
	case CloudAWS:
		return "aws"
	default:
		return "<unknown cloud>"
	}
}

// Parse S2 cloud from a string.
func ParseCloud(s string) (Cloud, error) {
	switch strings.ToLower(s) {
	case "aws":
		return CloudAWS, nil
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnspportedCloud, s)
	}
}

// Endpoints for the S2 environment.
//
// You can find the S2 endpoints in our [documentation].
//
// [documentation]: https://s2.dev/docs/interface/endpoints
type Endpoints struct {
	// Used by AccountService requests.
	Account string
	// Used by BasinService and StreamService requests.
	//
	// The prefix "{basin}." indicates the basin endpoint is parent zone else direct.
	Basin string
}

// Get S2 endpoints for the specified cloud.
func EndpointsForCloud(cloud Cloud) *Endpoints {
	return &Endpoints{
		Account: fmt.Sprintf("%s.s2.dev", cloud),
		Basin:   fmt.Sprintf("{basin}.b.%s.s2.dev", cloud),
	}
}

// Get S2 endpoints for the specified cell.
func EndpointsForCell(cloud Cloud, cellID string) *Endpoints {
	endpoint := fmt.Sprintf("%s.o.%s.s2.dev", cellID, cloud)

	return &Endpoints{
		Account: endpoint,
		Basin:   endpoint,
	}
}

// Get S2 endpoints from environment variables.
//
// The following environment variables are used:
//   - S2_CLOUD: Valid S2 cloud name. Defaults to AWS.
//   - S2_ACCOUNT_ENDPOINT: Overrides the account endpoint.
//   - S2_BASIN_ENDPOINT: Overrides the basin endpoint.
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

// A configuration parameter for creating an S2 client.
type ClientConfigParam interface {
	apply(*clientConfig) error
}

type applyClientConfigParamFunc func(*clientConfig) error

func (f applyClientConfigParamFunc) apply(cc *clientConfig) error {
	return f(cc)
}

// S2 endpoints to connect to.
//
// Defaults to endpoints for AWS.
func WithEndpoints(e *Endpoints) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		// TODO: Validate endpoints
		cc.Endpoints = e

		return nil
	})
}

// Timeout for connecting and transparently reconnecting.
//
// Defaults to 3s.
func WithConnectTimeout(d time.Duration) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.ConnectTimeout = d

		return nil
	})
}

// User agent.
//
// Defaults to s2-sdk-go. Feel free to say hi.
func WithUserAgent(u string) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.UserAgent = u

		return nil
	})
}

// Backoff duration when retrying.
//
// Defaults to 100ms.
func WithRetryBackoffDuration(d time.Duration) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.RetryBackoffDuration = d

		return nil
	})
}

// Maximum number of attempts per request.
//
// Setting it to 1 disables retrying. The default is to make 3 attempts.
func WithMaxAttempts(n uint) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		if n == 0 {
			return ErrNumTriesZero
		}

		cc.MaxAttempts = n

		return nil
	})
}

// Configure compression for requests and response.
//
// Disabled by default.
func WithCompression(c bool) ClientConfigParam {
	return applyClientConfigParamFunc(func(cc *clientConfig) error {
		cc.Compression = c

		return nil
	})
}

// Client for account-level operations.
type Client struct {
	inner *clientInner
}

// Create a new SDK client.
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

func (c *Client) listAccessTokens(
	ctx context.Context,
	req *ListAccessTokensRequest,
) (*ListAccessTokensResponse, error) {
	r := listAccessTokensRequest{
		Client: c.inner.AccountServiceClient(),
		Req:    req,
	}

	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) issueAccessToken(ctx context.Context, info *AccessTokenInfo) (string, error) {
	r := issueAccessTokenRequest{
		Client: c.inner.AccountServiceClient(),
		Info:   info,
	}

	return sendRetryable(ctx, c.inner, &r)
}

func (c *Client) revokeAccessToken(ctx context.Context, id string) (*AccessTokenInfo, error) {
	r := revokeAccessTokenRequest{
		Client: c.inner.AccountServiceClient(),
		ID:     id,
	}

	return sendRetryable(ctx, c.inner, &r)
}

// Create basin client from the given name.
func (c *Client) BasinClient(basin string) (*BasinClient, error) {
	inner, err := c.inner.BasinClient(basin)
	if err != nil {
		return nil, err
	}

	return &BasinClient{
		inner: inner,
	}, nil
}

// Client for basin-level operations.
type BasinClient struct {
	inner *clientInner
}

// Create a new basin client.
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

// Create a new client for stream-level operations.
func (b *BasinClient) StreamClient(stream string) *StreamClient {
	return &StreamClient{
		stream: stream,
		inner:  b.inner,
	}
}

// Client for stream-level operations.
type StreamClient struct {
	stream string
	inner  *clientInner
}

// Create a new stream client.
func NewStreamClient(basin, stream, authToken string, params ...ClientConfigParam) (*StreamClient, error) {
	basinClient, err := NewBasinClient(basin, authToken, params...)
	if err != nil {
		return nil, err
	}

	return basinClient.StreamClient(stream), nil
}

func (s *StreamClient) checkTail(ctx context.Context) (*CheckTailResponse, error) {
	r := checkTailServiceRequest{
		Client: s.inner.StreamServiceClient(),
		Stream: s.stream,
	}

	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) append(ctx context.Context, input *AppendInput) (*AppendOutput, error) {
	r := appendServiceRequest{
		Client:      s.inner.StreamServiceClient(),
		Stream:      s.stream,
		Input:       input,
		Compression: s.inner.Config.Compression,
	}

	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) appendSession(ctx context.Context) (Sender[*AppendInput], Receiver[*AppendOutput], error) {
	r := appendSessionServiceRequest{
		Client:      s.inner.StreamServiceClient(),
		Stream:      s.stream,
		Compression: s.inner.Config.Compression,
	}

	channel, err := sendRetryable(ctx, s.inner, &r)
	if err != nil {
		return nil, nil, err
	}

	return channel.Sender, channel.Receiver, nil
}

func (s *StreamClient) read(ctx context.Context, req *ReadRequest) (ReadOutput, error) {
	r := readServiceRequest{
		Client:      s.inner.StreamServiceClient(),
		Stream:      s.stream,
		Req:         req,
		Compression: s.inner.Config.Compression,
	}

	return sendRetryable(ctx, s.inner, &r)
}

func (s *StreamClient) readSession(ctx context.Context, req *ReadSessionRequest) (Receiver[ReadOutput], error) {
	r := readSessionServiceRequest{
		Client:      s.inner.StreamServiceClient(),
		Stream:      s.stream,
		Req:         req,
		Compression: s.inner.Config.Compression,
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
	RequestTimeout       time.Duration
	UserAgent            string
	RetryBackoffDuration time.Duration
	MaxAttempts          uint
	Compression          bool
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
		RequestTimeout:       5 * time.Second,
		UserAgent:            "s2-sdk-go",
		RetryBackoffDuration: 100 * time.Millisecond,
		MaxAttempts:          3,
		Compression:          false,
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

	return sendRetryableInner(ctx, r, c.Config.RequestTimeout, c.Config.RetryBackoffDuration, c.Config.MaxAttempts)
}

func sendAttempt[T any](ctx context.Context, r serviceRequest[T], requestTimeout time.Duration) (T, error) {
	if !r.IsStreaming() {
		// Only set the context timeout if the request isn't streaming.
		// Otherwise the cancellation of request timeout exits the stream too.
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, requestTimeout)
		defer cancelAttempt()

		ctx = attemptCtx
	}

	return r.Send(ctx)
}

func sendRetryableInner[T any](
	ctx context.Context,
	r serviceRequest[T],
	requestTimeout time.Duration,
	retryBackoffDuration time.Duration,
	maxRetryAttempts uint,
) (T, error) {
	var finalErr error

	for range maxRetryAttempts {
		v, err := sendAttempt(ctx, r, requestTimeout)
		if err == nil {
			return v, nil
		}

		if !shouldRetry(r, err) {
			return v, err
		}

		finalErr = err

		jitter := time.Duration(rand.Int64N(int64(10 * time.Millisecond)))

		select {
		case <-time.After(retryBackoffDuration + jitter):
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

	for range r.Client.Config.MaxAttempts {
		next, err := r.RecvInner.Recv()
		if err == nil {
			if batch, ok := next.(ReadOutputBatch); ok && len(batch.Records) > 0 {
				r.ServiceReq.Req.Start = ReadStartSeqNum(1 + batch.Records[len(batch.Records)-1].SeqNum)

				r.ServiceReq.Req.Limit.Bytes = optr.Map(r.ServiceReq.Req.Limit.Bytes, func(b uint64) uint64 {
					batchBytes := uint64(batch.MeteredBytes())
					if b < batchBytes {
						return 0
					}

					return b - batchBytes
				})

				r.ServiceReq.Req.Limit.Count = optr.Map(r.ServiceReq.Req.Limit.Count, func(c uint64) uint64 {
					batchSize := uint64(len(batch.Records))
					if c < batchSize {
						return 0
					}

					return c - batchSize
				})
			}

			return next, nil
		}

		if !shouldRetry(r.ServiceReq, err) {
			return nil, err
		}

		finalErr = err

		jitter := time.Duration(rand.Int64N(int64(10 * time.Millisecond)))

		select {
		case <-time.After(r.Client.Config.RetryBackoffDuration + jitter):
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
