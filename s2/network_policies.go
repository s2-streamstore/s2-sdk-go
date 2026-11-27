package s2

import (
	"context"
	"fmt"
	"net/url"
)

type NetworkPoliciesClient struct {
	client *Client
}

// Iterator over network policies returned by [NetworkPoliciesClient.Iter].
// Use Next to advance, Value to get the current item, and Err to check for errors.
type NetworkPolicyIterator struct {
	pager *pager[NetworkPolicyInfo]
}

func (n *NetworkPoliciesClient) http() *httpClient {
	return &httpClient{
		client:      n.client.httpClient,
		baseURL:     n.client.baseURL,
		accessToken: n.client.accessToken,
		logger:      n.client.logger,
		compression: n.client.compression,
	}
}

// List network policies.
func (n *NetworkPoliciesClient) List(ctx context.Context, args *ListNetworkPoliciesArgs) (*ListNetworkPoliciesResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = &ListNetworkPoliciesArgs{}
	}

	path := "/network-policies" + buildQuery(args.Prefix, args.StartAfter, args.Limit)

	return withRetries(ctx, n.client.retryConfig, n.client.logger, func(ctx context.Context) (*ListNetworkPoliciesResponse, error) {
		var result ListNetworkPoliciesResponse
		if err := n.http().request(ctx, "GET", path, nil, &result); err != nil {
			return nil, err
		}
		return &result, nil
	})
}

// Iterate over network policies.
// Limit, when set, is the page size for each underlying List call, not a total cap.
// Iteration drains all matching network policies; break out of the loop to stop early, or use [NetworkPoliciesClient.List] for a capped single-page result.
func (n *NetworkPoliciesClient) Iter(ctx context.Context, args *ListNetworkPoliciesArgs) *NetworkPolicyIterator {
	base := ListNetworkPoliciesArgs{}
	if args != nil {
		base = *args
	}
	fetch := func(ctx context.Context, startAfter string) (*pagedResponse[NetworkPolicyInfo], error) {
		params := base
		params.StartAfter = startAfter
		resp, err := n.List(ctx, &params)
		if err != nil {
			return nil, err
		}
		next := ""
		if resp.HasMore && len(resp.NetworkPolicies) > 0 {
			// Policies are listed by slug, not by their full reference.
			next = resp.NetworkPolicies[len(resp.NetworkPolicies)-1].Slug
		}
		return &pagedResponse[NetworkPolicyInfo]{items: resp.NetworkPolicies, nextKey: next}, nil
	}
	p := newPager(ctx, fetch)
	if args != nil {
		p.startAfter = args.StartAfter
	}
	return &NetworkPolicyIterator{pager: p}
}

// Advances the iterator to the next network policy.
// Returns true if there is a next item, false when iteration is complete or an error occurred.
// Call Err after iteration to check for errors.
func (it *NetworkPolicyIterator) Next() bool {
	return it.pager.Next()
}

// Returns the current network policy. Only valid after a successful call to Next.
func (it *NetworkPolicyIterator) Value() NetworkPolicyInfo {
	return it.pager.Value()
}

// Returns any error encountered during iteration.
// Should be called after Next returns false to check if iteration stopped due to an error.
func (it *NetworkPolicyIterator) Err() error {
	return it.pager.Err()
}

// Create a network policy.
// Policies are immutable; create a new one to change the restriction.
func (n *NetworkPoliciesClient) Create(ctx context.Context, args CreateNetworkPolicyArgs) (*NetworkPolicyInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	reqBody, err := snapshotBody(args)
	if err != nil {
		return nil, err
	}
	return withDefiniteRetries(ctx, n.client.retryConfig, n.client.logger, func(ctx context.Context) (*NetworkPolicyInfo, error) {
		var result NetworkPolicyInfo
		if err := n.http().request(ctx, "POST", "/network-policies", reqBody, &result); err != nil {
			return nil, err
		}
		return &result, nil
	})
}

// Get a network policy by its slug or full `<slug>.<nonce>` reference.
func (n *NetworkPoliciesClient) Get(ctx context.Context, id string) (*NetworkPolicyInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	path := fmt.Sprintf("/network-policies/%s", url.PathEscape(id))

	return withRetries(ctx, n.client.retryConfig, n.client.logger, func(ctx context.Context) (*NetworkPolicyInfo, error) {
		var result NetworkPolicyInfo
		if err := n.http().request(ctx, "GET", path, nil, &result); err != nil {
			return nil, err
		}
		return &result, nil
	})
}

// Delete a network policy by its slug or full `<slug>.<nonce>` reference.
func (n *NetworkPoliciesClient) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	path := fmt.Sprintf("/network-policies/%s", url.PathEscape(id))

	_, err := withRetries(ctx, n.client.retryConfig, n.client.logger, func(ctx context.Context) (*struct{}, error) {
		if err := n.http().request(ctx, "DELETE", path, nil, nil); err != nil {
			return nil, err
		}
		return &struct{}{}, nil
	})
	return deleteResult(err, "network_policy_not_found")
}
