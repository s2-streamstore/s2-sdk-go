package s2

import (
	"log/slog"
	"net/http"
	"time"
)

// Basin client.
type BasinClient struct {
	client             *Client // Reference to parent Client for shared resources
	name               string
	baseURL            string
	accessToken        string
	httpClient         *http.Client
	retryConfig        *RetryConfig
	logger             *slog.Logger
	includeBasinHeader bool
	connectionTimeout  time.Duration
	requestTimeout     time.Duration
	compression        CompressionType

	Streams *StreamsClient
}

// Name of the basin.
func (b *BasinClient) Name() string {
	return b.name
}

// basinHeaderValue returns the basin name if includeBasinHeader is true, otherwise empty string.
func (b *BasinClient) basinHeaderValue() string {
	if b.includeBasinHeader {
		return b.name
	}
	return ""
}
