package s2

import (
	"context"
	"fmt"
	"net/url"
	"unicode/utf8"
)

// Access tokens client.
type AccessTokensClient struct {
	client *Client
}

// Iterator over access tokens returned by [AccessTokensClient.Iter].
// Use Next to advance, Value to get the current item, and Err to check for errors.
type AccessTokenIterator struct {
	pager *pager[AccessTokenInfo]
}

// List access tokens.
func (a *AccessTokensClient) List(ctx context.Context, args *ListAccessTokensArgs) (*ListAccessTokensResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = &ListAccessTokensArgs{}
	}

	path := "/access-tokens" + buildQuery(args.Prefix, args.StartAfter, args.Limit)

	return withRetries(ctx, a.client.retryConfig, a.client.logger, func() (*ListAccessTokensResponse, error) {
		httpClient := &httpClient{
			client:      a.client.httpClient,
			baseURL:     a.client.baseURL,
			accessToken: a.client.accessToken,
			logger:      a.client.logger,
		}

		var result ListAccessTokensResponse
		err := httpClient.request(ctx, "GET", path, nil, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Iterate over access tokens.
func (a *AccessTokensClient) Iter(ctx context.Context, args *ListAccessTokensArgs) *AccessTokenIterator {
	base := ListAccessTokensArgs{}
	if args != nil {
		base = *args
	}
	remaining := copyLimit(base.Limit)
	base.Limit = nil
	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[AccessTokenInfo], error) {
		if remainingDepleted(remaining) {
			return &pagedResponse[AccessTokenInfo]{}, nil
		}
		params := base
		params.StartAfter = startAfter
		if limit, ok := nextRequestLimit(remaining); ok {
			params.Limit = &limit
		}
		resp, err := a.List(ctx, &params)
		if err != nil {
			return nil, err
		}
		next := ""
		if resp.HasMore && len(resp.AccessTokens) > 0 {
			next = string(resp.AccessTokens[len(resp.AccessTokens)-1].ID)
		}
		if consumeRemaining(remaining, len(resp.AccessTokens)) {
			next = ""
		}
		return &pagedResponse[AccessTokenInfo]{items: resp.AccessTokens, nextKey: next}, nil
	}
	p := newPager(ctx, fetch)
	if args != nil {
		p.startAfter = args.StartAfter
	}
	return &AccessTokenIterator{pager: p}
}

// Advances the iterator to the next access token.
// Returns true if there is a next item, false when iteration is complete or an error occurred.
// Call Err after iteration to check for errors.
func (it *AccessTokenIterator) Next() bool {
	return it.pager.Next()
}

// Returns the current access token. Only valid after a successful call to Next.
func (it *AccessTokenIterator) Value() AccessTokenInfo {
	return it.pager.Value()
}

// Returns any error encountered during iteration.
// Should be called after Next returns false to check if iteration stopped due to an error.
func (it *AccessTokenIterator) Err() error {
	return it.pager.Err()
}

// Issue an access token.
func (a *AccessTokensClient) Issue(ctx context.Context, args IssueAccessTokenArgs) (*IssueAccessTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAccessTokenID(args.ID); err != nil {
		return nil, err
	}

	return withRetries(ctx, a.client.retryConfig, a.client.logger, func() (*IssueAccessTokenResponse, error) {
		httpClient := &httpClient{
			client:      a.client.httpClient,
			baseURL:     a.client.baseURL,
			accessToken: a.client.accessToken,
			logger:      a.client.logger,
		}

		var result IssueAccessTokenResponse
		err := httpClient.request(ctx, "POST", "/access-tokens", args, &result)
		if err != nil {
			return nil, err
		}

		return &result, nil
	})
}

// Revoke an access token.
func (a *AccessTokensClient) Revoke(ctx context.Context, args RevokeAccessTokenArgs) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAccessTokenID(args.ID); err != nil {
		return err
	}

	path := fmt.Sprintf("/access-tokens/%s", url.PathEscape(string(args.ID)))

	_, err := withRetries(ctx, a.client.retryConfig, a.client.logger, func() (*struct{}, error) {
		httpClient := &httpClient{
			client:      a.client.httpClient,
			baseURL:     a.client.baseURL,
			accessToken: a.client.accessToken,
			logger:      a.client.logger,
		}

		err := httpClient.request(ctx, "DELETE", path, nil, nil)
		if err != nil {
			return nil, err
		}
		return &struct{}{}, nil
	})
	return err
}

func validateAccessTokenID(id AccessTokenID) error {
	length := utf8.RuneCountInString(string(id))
	if length < 1 || length > 96 {
		return fmt.Errorf("access token ID must be between 1 and 96 characters")
	}
	return nil
}
