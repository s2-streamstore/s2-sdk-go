// Auto generated file. DO NOT EDIT.

package s2

import (
	"context"
)

// List basins.
func (c *Client) ListBasins(ctx context.Context, req *ListBasinsRequest) (*ListBasinsResponse, error) {
	return c.listBasins(ctx, req)
}

// Create a new basin.
// Provide a client request token with the `S2-Request-Token` header for idempotent retry behaviour.
func (c *Client) CreateBasin(ctx context.Context, req *CreateBasinRequest) (*BasinInfo, error) {
	return c.createBasin(ctx, req)
}

// Delete a basin.
// Basin deletion is asynchronous, and may take a few minutes to complete.
func (c *Client) DeleteBasin(ctx context.Context, req *DeleteBasinRequest) error {
	return c.deleteBasin(ctx, req)
}

// Update basin configuration.
func (c *Client) ReconfigureBasin(ctx context.Context, req *ReconfigureBasinRequest) (*BasinConfig, error) {
	return c.reconfigureBasin(ctx, req)
}

// Get basin configuration.
func (c *Client) GetBasinConfig(ctx context.Context, basin string) (*BasinConfig, error) {
	return c.getBasinConfig(ctx, basin)
}

// List access tokens.
func (c *Client) ListAccessTokens(ctx context.Context, req *ListAccessTokensRequest) (*ListAccessTokensResponse, error) {
	return c.listAccessTokens(ctx, req)
}

// Issue a new access token.
func (c *Client) IssueAccessToken(ctx context.Context, info *AccessTokenInfo) (string, error) {
	return c.issueAccessToken(ctx, info)
}

// Revoke an access token.
func (c *Client) RevokeAccessToken(ctx context.Context, id string) (*AccessTokenInfo, error) {
	return c.revokeAccessToken(ctx, id)
}

// List streams.
func (b *BasinClient) ListStreams(ctx context.Context, req *ListStreamsRequest) (*ListStreamsResponse, error) {
	return b.listStreams(ctx, req)
}

// Create a stream.
// Provide a client request token with the `S2-Request-Token` header for idempotent retry behaviour.
func (b *BasinClient) CreateStream(ctx context.Context, req *CreateStreamRequest) (*StreamInfo, error) {
	return b.createStream(ctx, req)
}

// Delete a stream.
// Stream deletion is asynchronous, and may take a few minutes to complete.
func (b *BasinClient) DeleteStream(ctx context.Context, req *DeleteStreamRequest) error {
	return b.deleteStream(ctx, req)
}

// Update stream configuration.
func (c *BasinClient) ReconfigureStream(ctx context.Context, req *ReconfigureStreamRequest) (*StreamConfig, error) {
	return c.reconfigureStream(ctx, req)
}

// Get stream configuration.
func (b *BasinClient) GetStreamConfig(ctx context.Context, basin string) (*StreamConfig, error) {
	return b.getStreamConfig(ctx, basin)
}

// Check the tail of the stream.
func (s *StreamClient) CheckTail(ctx context.Context) (*CheckTailResponse, error) {
	return s.checkTail(ctx)
}

// Append a batch of records to a stream.
func (s *StreamClient) Append(ctx context.Context, input *AppendInput) (*AppendOutput, error) {
	return s.append(ctx, input)
}

// Append batches of records to a stream continuously, while guaranteeing pipelined requests are processed in order.
// If any request fails, the session is terminated.
func (s *StreamClient) AppendSession(ctx context.Context) (Sender[*AppendInput], Receiver[*AppendOutput], error) {
	return s.appendSession(ctx)
}

// Retrieve a batch of records from a stream.
func (s *StreamClient) Read(ctx context.Context, req *ReadRequest) (ReadOutput, error) {
	return s.read(ctx, req)
}

// Retrieve batches of records from a stream continuously.
func (s *StreamClient) ReadSession(ctx context.Context, req *ReadSessionRequest) (Receiver[ReadOutput], error) {
	return s.readSession(ctx, req)
}
