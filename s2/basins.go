package s2

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"unicode/utf8"
)

type BasinsClient struct {
	client *Client
}

// Iterator over basins returned by [BasinsClient.Iter].
// Use Next to advance, Value to get the current item, and Err to check for errors.
type BasinsIterator struct {
	pager *pager[BasinInfo]
}

// List basins.
func (b *BasinsClient) List(ctx context.Context, args *ListBasinsArgs) (*ListBasinsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = &ListBasinsArgs{}
	}

	path := "/basins" + buildQuery(args.Prefix, args.StartAfter, args.Limit)

	return withRetries(ctx, b.client.retryConfig, b.client.logger, func() (*ListBasinsResponse, error) {
		httpClient := &httpClient{
			client:      b.client.httpClient,
			baseURL:     b.client.baseURL,
			accessToken: b.client.accessToken,
			logger:      b.client.logger,
			compression: b.client.compression,
		}

		var result ListBasinsResponse
		err := httpClient.request(ctx, "GET", path, nil, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Iterate over basins.
func (b *BasinsClient) Iter(ctx context.Context, args *ListBasinsArgs) *BasinsIterator {
	base := ListBasinsArgs{}
	if args != nil {
		base = *args
	}
	remaining := copyLimit(base.Limit)
	base.Limit = nil
	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[BasinInfo], error) {
		if remainingDepleted(remaining) {
			return &pagedResponse[BasinInfo]{}, nil
		}
		params := base
		params.StartAfter = startAfter
		if limit, ok := nextRequestLimit(remaining); ok {
			params.Limit = &limit
		}
		resp, err := b.List(ctx, &params)
		if err != nil {
			return nil, err
		}
		next := ""
		if resp.HasMore && len(resp.Basins) > 0 {
			next = string(resp.Basins[len(resp.Basins)-1].Name)
		}
		if consumeRemaining(remaining, len(resp.Basins)) {
			next = ""
		}
		return &pagedResponse[BasinInfo]{items: resp.Basins, nextKey: next}, nil
	}
	p := newPager(ctx, fetch)
	if args != nil {
		p.startAfter = args.StartAfter
	}
	return &BasinsIterator{pager: p}
}

// Advances the iterator to the next basin.
// Returns true if there is a next item, false when iteration is complete or an error occurred.
// Call Err after iteration to check for errors.
func (it *BasinsIterator) Next() bool {
	return it.pager.Next()
}

// Returns the current basin. Only valid after a successful call to Next.
func (it *BasinsIterator) Value() BasinInfo {
	return it.pager.Value()
}

// Returns any error encountered during iteration.
// Should be called after Next returns false to check if iteration stopped due to an error.
func (it *BasinsIterator) Err() error {
	return it.pager.Err()
}

// Create a basin.
func (b *BasinsClient) Create(ctx context.Context, args CreateBasinArgs) (*BasinInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateBasinName(args.Basin); err != nil {
		return nil, err
	}

	requestToken := newRequestToken()

	return withRetries(ctx, b.client.retryConfig, b.client.logger, func() (*BasinInfo, error) {
		httpClient := &httpClient{
			client:      b.client.httpClient,
			baseURL:     b.client.baseURL,
			accessToken: b.client.accessToken,
			logger:      b.client.logger,
			compression: b.client.compression,
		}

		var result BasinInfo
		if err := httpClient.requestWithHeaders(ctx, "POST", "/basins", args, &result, map[string]string{
			"s2-request-token": requestToken,
		}); err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Delete a basin.
func (b *BasinsClient) Delete(ctx context.Context, basinName BasinName) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateBasinName(basinName); err != nil {
		return err
	}

	path := fmt.Sprintf("/basins/%s", url.PathEscape(string(basinName)))

	_, err := withRetries(ctx, b.client.retryConfig, b.client.logger, func() (*struct{}, error) {
		httpClient := &httpClient{
			client:      b.client.httpClient,
			baseURL:     b.client.baseURL,
			accessToken: b.client.accessToken,
			logger:      b.client.logger,
			compression: b.client.compression,
		}

		err := httpClient.request(ctx, "DELETE", path, nil, nil)
		if err != nil {
			return nil, err
		}
		return &struct{}{}, nil
	})
	return err
}

// Get basin configuration.
func (b *BasinsClient) GetConfig(ctx context.Context, basinName BasinName) (*BasinConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateBasinName(basinName); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/basins/%s", url.PathEscape(string(basinName)))

	return withRetries(ctx, b.client.retryConfig, b.client.logger, func() (*BasinConfig, error) {
		httpClient := &httpClient{
			client:      b.client.httpClient,
			baseURL:     b.client.baseURL,
			accessToken: b.client.accessToken,
			logger:      b.client.logger,
			compression: b.client.compression,
		}

		var result BasinConfig
		err := httpClient.request(ctx, "GET", path, nil, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Reconfigure a basin.
func (b *BasinsClient) Reconfigure(ctx context.Context, args ReconfigureBasinArgs) (*BasinConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateBasinName(args.Basin); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/basins/%s", url.PathEscape(string(args.Basin)))

	return withRetries(ctx, b.client.retryConfig, b.client.logger, func() (*BasinConfig, error) {
		httpClient := &httpClient{
			client:      b.client.httpClient,
			baseURL:     b.client.baseURL,
			accessToken: b.client.accessToken,
			logger:      b.client.logger,
			compression: b.client.compression,
		}

		var result BasinConfig
		if err := httpClient.request(ctx, "PATCH", path, args.Config, &result); err != nil {
			return nil, err
		}

		return &result, nil
	})
}

var basinNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

func validateBasinName(name BasinName) error {
	length := utf8.RuneCountInString(string(name))
	if length < 8 || length > 48 {
		return fmt.Errorf("basin name must be between 8 and 48 characters, got %d", length)
	}
	if !basinNameRegex.MatchString(string(name)) {
		return fmt.Errorf("basin name must contain only lowercase letters, numbers, and hyphens, and cannot begin or end with a hyphen")
	}
	return nil
}
