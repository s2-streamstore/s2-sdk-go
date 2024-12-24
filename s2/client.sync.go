// Auto generated file. DO NOT EDIT.

package s2

import (
	"context"
)

// List basins.
func (c *Client) ListBasins(ctx context.Context, req *ListBasinsRequest) (*ListBasinsResponse, error) {
	return c.listBasins(ctx, req)
}
