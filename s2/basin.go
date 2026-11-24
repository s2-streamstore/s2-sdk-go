package s2

import (
	"fmt"
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
	// Set when the basin name is invalid; calls fail with it before sending.
	invalidNameErr error

	Streams *StreamsClient
}

// Name of the basin.
func (b *BasinClient) Name() string {
	return b.name
}

type rejectTransport struct{ err error }

func (t rejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

// basinHeaderValue returns the basin name if includeBasinHeader is true, otherwise empty string.
func (b *BasinClient) basinHeaderValue() string {
	if b.includeBasinHeader {
		return b.name
	}
	return ""
}

// Format keeps the access token out of every fmt verb (spec L3).
func (b BasinClient) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "s2.BasinClient{name: %q, baseURL: %q}", b.name, b.baseURL)
}
