package s2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestNetworkPolicy_RestrictsAccessToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()

	client := testClient(t)
	slug := fmt.Sprintf("policy-%d", time.Now().UnixNano())
	// Documentation-only range, which this client is outside of.
	policy := s2.NetworkPolicy{PublicNet: &s2.PublicNetPolicy{Cidrs: []string{"203.0.113.0/24"}}}

	created, err := client.NetworkPolicies.Create(ctx, s2.CreateNetworkPolicyArgs{Slug: slug, Policy: policy})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() { _ = client.NetworkPolicies.Delete(ctx, slug) }()

	fetched, err := client.NetworkPolicies.Get(ctx, slug)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if fetched.ID != created.ID || fetched.Policy.PublicNet == nil || fetched.Policy.PublicNet.Cidrs[0] != policy.PublicNet.Cidrs[0] {
		t.Fatalf("Get returned %#v, created %#v", fetched, created)
	}

	it := client.NetworkPolicies.Iter(ctx, &s2.ListNetworkPoliciesArgs{Prefix: slug})
	if !it.Next() || it.Value().ID != created.ID {
		t.Fatalf("Iter did not return the created policy: %v", it.Err())
	}

	tokenID := uniqueTokenID("policy-token")
	defer revokeToken(ctx, client, tokenID)
	issued, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:            tokenID,
		Scope:         s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
		NetworkPolicy: slug,
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	tokens, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{Prefix: string(tokenID)})
	if err != nil {
		t.Fatalf("List tokens failed: %v", err)
	}
	if len(tokens.AccessTokens) != 1 || tokens.AccessTokens[0].NetworkPolicy == nil || *tokens.AccessTokens[0].NetworkPolicy != created.ID {
		t.Fatalf("token is not bound to %q: %#v", created.ID, tokens.AccessTokens)
	}

	restricted := testClientWithToken(t, issued.AccessToken)
	_, err = restricted.Basins.List(ctx, nil)
	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Fatalf("expected 403 from outside the policy, got %v", err)
	}
}
