package s2

import (
	"context"
	"fmt"
	"net/url"
	"unicode/utf8"
)

type StreamsClient struct {
	basin *BasinClient
}

// Iterates over streams returned by [StreamsClient.Iter].
// Use Next to advance, Value to get the current item, and Err to check for errors.
type StreamsIterator struct {
	pager *pager[StreamInfo]
}

// List streams.
func (s *StreamsClient) List(ctx context.Context, args *ListStreamsArgs) (*ListStreamsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = &ListStreamsArgs{}
	}

	path := "/streams" + buildQuery(args.Prefix, args.StartAfter, args.Limit)

	return withRetries(ctx, s.basin.retryConfig, s.basin.logger, func() (*ListStreamsResponse, error) {
		httpClient := &httpClient{
			client:      s.basin.httpClient,
			baseURL:     s.basin.baseURL,
			accessToken: s.basin.accessToken,
			logger:      s.basin.logger,
			basinName:   s.basin.basinHeaderValue(),
			compression: s.basin.compression,
		}

		var result ListStreamsResponse
		err := httpClient.request(ctx, "GET", path, nil, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Iterate over streams in a basin.
// By default, streams that are being deleted are excluded. Set IncludeDeleted to true to include them.
func (s *StreamsClient) Iter(ctx context.Context, args *ListStreamsArgs) *StreamsIterator {
	base := ListStreamsArgs{}
	if args != nil {
		base = *args
	}
	includeDeleted := base.IncludeDeleted
	remaining := copyLimit(base.Limit)
	base.Limit = nil
	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[StreamInfo], error) {
		if remainingDepleted(remaining) {
			return &pagedResponse[StreamInfo]{}, nil
		}
		params := base
		params.StartAfter = startAfter
		if limit, ok := nextRequestLimit(remaining); ok {
			params.Limit = &limit
		}
		resp, err := s.List(ctx, &params)
		if err != nil {
			return nil, err
		}
		// Filter out deleted streams unless IncludeDeleted is true
		streams := resp.Streams
		if !includeDeleted {
			streams = make([]StreamInfo, 0, len(resp.Streams))
			for _, stream := range resp.Streams {
				if stream.DeletedAt == nil {
					streams = append(streams, stream)
				}
			}
		}
		next := ""
		if resp.HasMore && len(resp.Streams) > 0 {
			next = string(resp.Streams[len(resp.Streams)-1].Name)
		}
		if consumeRemaining(remaining, len(streams)) {
			next = ""
		}
		return &pagedResponse[StreamInfo]{items: streams, nextKey: next}, nil
	}
	p := newPager(ctx, fetch)
	if args != nil {
		p.startAfter = args.StartAfter
	}
	return &StreamsIterator{pager: p}
}

// Advances the iterator to the next stream.
// Returns true if there is a next item, false when iteration is complete or an error occurred.
// Call Err after iteration to check for errors.
func (it *StreamsIterator) Next() bool {
	return it.pager.Next()
}

// Returns the current stream. Only valid after a successful call to Next.
func (it *StreamsIterator) Value() StreamInfo {
	return it.pager.Value()
}

// Returns any error encountered during iteration.
// Should be called after Next returns false to check if iteration stopped due to an error.
func (it *StreamsIterator) Err() error {
	return it.pager.Err()
}

// Create a stream.
func (s *StreamsClient) Create(ctx context.Context, args CreateStreamArgs) (*StreamInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateStreamName(args.Stream); err != nil {
		return nil, err
	}

	requestToken := newRequestToken()

	return withRetries(ctx, s.basin.retryConfig, s.basin.logger, func() (*StreamInfo, error) {
		httpClient := &httpClient{
			client:      s.basin.httpClient,
			baseURL:     s.basin.baseURL,
			accessToken: s.basin.accessToken,
			logger:      s.basin.logger,
			basinName:   s.basin.basinHeaderValue(),
			compression: s.basin.compression,
		}

		headers := map[string]string{
			"s2-request-token": requestToken,
		}

		var result StreamInfo
		if err := httpClient.requestWithHeaders(ctx, "POST", "/streams", args, &result, headers); err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Delete a stream.
func (s *StreamsClient) Delete(ctx context.Context, streamName StreamName) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateStreamName(streamName); err != nil {
		return err
	}

	path := fmt.Sprintf("/streams/%s", url.PathEscape(string(streamName)))

	_, err := withRetries(ctx, s.basin.retryConfig, s.basin.logger, func() (*struct{}, error) {
		httpClient := &httpClient{
			client:      s.basin.httpClient,
			baseURL:     s.basin.baseURL,
			accessToken: s.basin.accessToken,
			logger:      s.basin.logger,
			basinName:   s.basin.basinHeaderValue(),
			compression: s.basin.compression,
		}

		err := httpClient.request(ctx, "DELETE", path, nil, nil)
		if err != nil {
			return nil, err
		}
		return &struct{}{}, nil
	})
	return err
}

// Get stream configuration.
func (s *StreamsClient) GetConfig(ctx context.Context, streamName StreamName) (*StreamConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateStreamName(streamName); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/streams/%s", url.PathEscape(string(streamName)))

	return withRetries(ctx, s.basin.retryConfig, s.basin.logger, func() (*StreamConfig, error) {
		httpClient := &httpClient{
			client:      s.basin.httpClient,
			baseURL:     s.basin.baseURL,
			accessToken: s.basin.accessToken,
			logger:      s.basin.logger,
			basinName:   s.basin.basinHeaderValue(),
			compression: s.basin.compression,
		}

		var result StreamConfig
		err := httpClient.request(ctx, "GET", path, nil, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Reconfigure a stream.
func (s *StreamsClient) Reconfigure(ctx context.Context, args ReconfigureStreamArgs) (*StreamConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateStreamName(args.Stream); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/streams/%s", url.PathEscape(string(args.Stream)))

	return withRetries(ctx, s.basin.retryConfig, s.basin.logger, func() (*StreamConfig, error) {
		httpClient := &httpClient{
			client:      s.basin.httpClient,
			baseURL:     s.basin.baseURL,
			accessToken: s.basin.accessToken,
			logger:      s.basin.logger,
			basinName:   s.basin.basinHeaderValue(),
			compression: s.basin.compression,
		}

		var result StreamConfig
		if err := httpClient.request(ctx, "PATCH", path, args.Config, &result); err != nil {
			return nil, err
		}

		return &result, nil
	})
}

func validateStreamName(name StreamName) error {
	length := utf8.RuneCountInString(string(name))
	if length < 1 || length > 512 {
		return fmt.Errorf("stream name must be between 1 and 512 characters")
	}
	return nil
}
