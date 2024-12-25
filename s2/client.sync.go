// Auto generated file. DO NOT EDIT.

package s2

import (
	"context"
)

// List basins.
func (c *Client) ListBasins(ctx context.Context, req *ListBasinsRequest) (*ListBasinsResponse, error) {
	return c.listBasins(ctx, req)
}

// List streams.
func (b *BasinClient) ListStreams(ctx context.Context, req *ListStreamsRequest) (*ListStreamsResponse, error) {
	return b.listStreams(ctx, req)
}

// Check the sequence number that will be assigned to the next record on a stream.
func (s *StreamClient) CheckTail(ctx context.Context) (uint64, error) {
	return s.checkTail(ctx)
}