package s2_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const tokenTestTimeout = 60 * time.Second

func uniqueTokenID(prefix string) s2.AccessTokenID {
	return s2.AccessTokenID(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

func revokeToken(ctx context.Context, client *s2.Client, id s2.AccessTokenID) {
	_ = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: id})
}

// --- List Access Tokens Tests ---

func TestListAccessTokens_All(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List all access tokens")

	client := testClient(t)
	resp, err := client.AccessTokens.List(ctx, nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	t.Logf("Listed %d access tokens, has_more=%v", len(resp.AccessTokens), resp.HasMore)
}

func TestListAccessTokens_WithPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with prefix")

	client := testClient(t)
	tokenID := uniqueTokenID("test-list")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Logf("Issued token: %s", tokenID)

	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: "test-list",
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, tok := range resp.AccessTokens {
		if tok.ID == tokenID {
			found = true
			break
		}
		if !strings.HasPrefix(string(tok.ID), "test-list") {
			t.Errorf("Token %s does not match prefix test-list", tok.ID)
		}
	}
	if !found {
		t.Error("Created token not found in list")
	}
	t.Logf("Found %d tokens with prefix", len(resp.AccessTokens))
}

func TestListAccessTokens_WithLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with limit")

	client := testClient(t)
	limit := 5
	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.AccessTokens) > limit {
		t.Errorf("Expected at most %d tokens, got %d", limit, len(resp.AccessTokens))
	}
	t.Logf("Listed %d tokens with limit=%d, has_more=%v", len(resp.AccessTokens), limit, resp.HasMore)
}

func TestListAccessTokens_Pagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with pagination")

	client := testClient(t)

	for i := range 3 {
		id := s2.AccessTokenID(fmt.Sprintf("page-tok-%d-%d", time.Now().UnixNano(), i))
		_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
			ID: id,
			Scope: s2.AccessTokenScope{
				Ops: []string{s2.OperationListBasins},
			},
		})
		if err != nil {
			t.Fatalf("Issue token %d failed: %v", i, err)
		}
		defer revokeToken(ctx, client, id)
	}

	limit := 2
	resp1, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: "page-tok",
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("First list failed: %v", err)
	}

	if len(resp1.AccessTokens) == 0 {
		t.Skip("No tokens to paginate")
	}

	lastID := string(resp1.AccessTokens[len(resp1.AccessTokens)-1].ID)
	resp2, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix:     "page-tok",
		StartAfter: lastID,
		Limit:      &limit,
	})
	if err != nil {
		t.Fatalf("Second list failed: %v", err)
	}

	for _, tok := range resp2.AccessTokens {
		if string(tok.ID) <= lastID {
			t.Errorf("Token %s should be after %s", tok.ID, lastID)
		}
	}
	t.Logf("Page 1: %d tokens, Page 2: %d tokens", len(resp1.AccessTokens), len(resp2.AccessTokens))
}

func TestListAccessTokens_Iterator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Iterate over access tokens")

	client := testClient(t)
	limit := 3
	iter := client.AccessTokens.Iter(ctx, &s2.ListAccessTokensArgs{
		Limit: &limit,
	})

	count := 0
	for iter.Next() {
		token := iter.Value()
		if token.ID == "" {
			t.Error("Token ID should not be empty")
		}
		count++
	}

	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	t.Logf("Iterated over %d tokens", count)
}

func TestListAccessTokens_LimitZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with limit=0 (treated as default)")

	client := testClient(t)
	limit := 0
	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.AccessTokens) > 1000 {
		t.Errorf("Expected at most 1000 tokens, got %d", len(resp.AccessTokens))
	}
	t.Logf("Listed %d tokens with limit=0 (treated as default 1000)", len(resp.AccessTokens))
}

func TestListAccessTokens_InvalidStartAfterLessThanPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with start_after < prefix")

	client := testClient(t)
	_, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix:     "zzzzzzzz",
		StartAfter: "aaaaaaaa",
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Issue Access Token Tests ---

func TestIssueAccessToken_EmptyScopeFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with empty scope fails")

	client := testClient(t)
	tokenID := uniqueTokenID("test-imin")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    tokenID,
		Scope: s2.AccessTokenScope{},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error for empty scope, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestIssueAccessToken_WithOpGroupsAccountRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with op_groups.account.read")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ogar")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			OpGroups: &s2.PermittedOperationGroups{
				Account: &s2.ReadWritePermissions{Read: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with account read permission")
}

func TestIssueAccessToken_WithOpGroupsAccountWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with op_groups.account.write")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ogaw")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			OpGroups: &s2.PermittedOperationGroups{
				Account: &s2.ReadWritePermissions{Write: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with account write permission")
}

func TestIssueAccessToken_WithOpGroupsBasinRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with op_groups.basin.read")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ogbr")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			OpGroups: &s2.PermittedOperationGroups{
				Basin: &s2.ReadWritePermissions{Read: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with basin read permission")
}

func TestIssueAccessToken_WithOpGroupsStreamRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with op_groups.stream.read")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ogsr")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with stream read permission")
}

func TestIssueAccessToken_WithSpecificOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with specific ops")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ops")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins, s2.OperationRead},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with specific ops: list-basins, read")
}

func TestIssueAccessToken_WithBasinsExact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with basins.exact")

	client := testClient(t)
	tokenID := uniqueTokenID("test-bex")
	defer revokeToken(ctx, client, tokenID)

	basinName := "my-test-basin"
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Exact: &basinName},
			Ops:    []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with basins.exact scope")
}

func TestIssueAccessToken_WithBasinsPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with basins.prefix")

	client := testClient(t)
	tokenID := uniqueTokenID("test-bpx")
	defer revokeToken(ctx, client, tokenID)

	prefix := "test-"
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: &prefix},
			Ops:    []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with basins.prefix scope")
}

func TestIssueAccessToken_WithStreamsExact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with streams.exact")

	client := testClient(t)
	tokenID := uniqueTokenID("test-sex")
	defer revokeToken(ctx, client, tokenID)

	streamName := "my-stream"
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Streams: &s2.ResourceSet{Exact: &streamName},
			Ops:     []string{s2.OperationRead},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with streams.exact scope")
}

func TestIssueAccessToken_WithStreamsPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with streams.prefix")

	client := testClient(t)
	tokenID := uniqueTokenID("test-spx")
	defer revokeToken(ctx, client, tokenID)

	prefix := "logs/"
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Streams: &s2.ResourceSet{Prefix: &prefix},
			Ops:     []string{s2.OperationRead},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with streams.prefix scope")
}

func TestIssueAccessToken_WithBasinsExactEmpty_DenyAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with basins.exact='' (deny all basins)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-bxe")
	defer revokeToken(ctx, client, tokenID)

	empty := ""
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Exact: &empty},
			Ops:    []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with basins.exact='' (no basin access)")
}

func TestIssueAccessToken_WithBasinsPrefixEmpty_AllowAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with basins.prefix='' (allow all basins)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-bpe")
	defer revokeToken(ctx, client, tokenID)

	empty := ""
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: &empty},
			Ops:    []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with basins.prefix='' (all basin access)")
}

func TestIssueAccessToken_WithStreamsExactEmpty_DenyAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with streams.exact='' (deny all streams)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-sxe")
	defer revokeToken(ctx, client, tokenID)

	empty := ""
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Streams: &s2.ResourceSet{Exact: &empty},
			Ops:     []string{s2.OperationRead},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with streams.exact='' (no stream access)")
}

func TestIssueAccessToken_WithStreamsPrefixEmpty_AllowAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with streams.prefix='' (allow all streams)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-spe")
	defer revokeToken(ctx, client, tokenID)

	empty := ""
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Streams: &s2.ResourceSet{Prefix: &empty},
			Ops:     []string{s2.OperationRead},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with streams.prefix='' (all stream access)")
}

func TestIssueAccessToken_WithAutoPrefixStreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with auto_prefix_streams=true")

	client := testClient(t)
	tokenID := uniqueTokenID("test-aps")
	defer revokeToken(ctx, client, tokenID)

	prefix := "ns/"
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Streams: &s2.ResourceSet{Prefix: &prefix},
			Ops:     []string{s2.OperationRead},
		},
		AutoPrefixStreams: true,
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with auto_prefix_streams=true")
}

func TestIssueAccessToken_WithExpiresAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with expires_at")

	client := testClient(t)
	tokenID := uniqueTokenID("test-exp")
	defer revokeToken(ctx, client, tokenID)

	expiresAt := time.Now().Add(24 * time.Hour)
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Logf("Issued token with expires_at=%v", expiresAt)
}

func TestIssueAccessToken_IDMinLength(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with minimum ID length (1 byte)")

	client := testClient(t)
	tokenID := s2.AccessTokenID("a")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with 1-byte ID")
}

func TestIssueAccessToken_IDMaxLength(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with maximum ID length (96 bytes)")

	client := testClient(t)
	tokenID := s2.AccessTokenID(strings.Repeat("a", 96))
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with 96-byte ID")
}

func TestIssueAccessToken_IDEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with empty ID")

	client := testClient(t)
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    "",
		Scope: s2.AccessTokenScope{},
	})

	if err == nil {
		t.Error("Expected error for empty ID")
	}
	t.Logf("Got expected error: %v", err)
}

func TestIssueAccessToken_IDTooLong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with ID too long (97 bytes)")

	client := testClient(t)
	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    s2.AccessTokenID(strings.Repeat("a", 97)),
		Scope: s2.AccessTokenScope{},
	})

	if err == nil {
		t.Error("Expected error for ID too long")
	}
	t.Logf("Got expected error: %v", err)
}

func TestIssueAccessToken_DuplicateID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with duplicate ID")

	client := testClient(t)
	tokenID := uniqueTokenID("test-dup")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("First issue failed: %v", err)
	}

	_, err = client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 409 {
		t.Errorf("Expected 409 conflict, got: %v", err)
	}
	if s2Err != nil && s2Err.Code != "resource_already_exists" {
		t.Errorf("Expected error code resource_already_exists, got: %s", s2Err.Code)
	}
	t.Logf("Got expected conflict error: %v", err)
}

func TestIssueAccessToken_WithFullScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue token with full scope")

	client := testClient(t)
	tokenID := uniqueTokenID("test-full")
	defer revokeToken(ctx, client, tokenID)

	basinPrefix := "test-"
	streamPrefix := "logs/"
	tokenPrefix := "child-"

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:       &s2.ResourceSet{Prefix: &basinPrefix},
			Streams:      &s2.ResourceSet{Prefix: &streamPrefix},
			AccessTokens: &s2.ResourceSet{Prefix: &tokenPrefix},
			OpGroups: &s2.PermittedOperationGroups{
				Account: &s2.ReadWritePermissions{Read: true, Write: true},
				Basin:   &s2.ReadWritePermissions{Read: true, Write: true},
				Stream:  &s2.ReadWritePermissions{Read: true, Write: true},
			},
			Ops: []string{s2.OperationAccountMetrics, s2.OperationBasinMetrics},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Log("Issued token with full scope")
}

// --- Revoke Access Token Tests ---

func TestRevokeAccessToken_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke existing access token")

	client := testClient(t)
	tokenID := uniqueTokenID("test-rev")

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	t.Logf("Issued token: %s", tokenID)

	err = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	t.Log("Token revoked successfully")
}

func TestRevokeAccessToken_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke non-existent access token")

	client := testClient(t)
	err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{
		ID: "nonexistent-token-12345",
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestRevokeAccessToken_AlreadyRevoked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke already revoked token")

	client := testClient(t)
	tokenID := uniqueTokenID("test-revr")

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	err = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})
	if err != nil {
		t.Fatalf("First revoke failed: %v", err)
	}

	err = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})
	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error for already revoked token, got: %v", err)
	}
	t.Logf("Got expected error for already revoked token: %v", err)
}

func TestRevokeAccessToken_IDEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke with empty ID")

	client := testClient(t)
	err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: ""})

	if err == nil {
		t.Error("Expected error for empty ID")
	}
	t.Logf("Got expected error: %v", err)
}

// --- Token Verification Tests ---

func TestIssueAccessToken_VerifyListReturnsToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Verify issued token appears in list")

	client := testClient(t)
	tokenID := uniqueTokenID("test-vlst")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: string(tokenID),
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, tok := range resp.AccessTokens {
		if tok.ID == tokenID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Issued token not found in list")
	}
	t.Log("Verified token appears in list after issue")
}

func TestRevokeAccessToken_VerifyNotInList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTestTimeout)
	defer cancel()
	t.Log("Testing: Verify revoked token not in list")

	client := testClient(t)
	tokenID := uniqueTokenID("test-vrnl")

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	err = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: string(tokenID),
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	for _, tok := range resp.AccessTokens {
		if tok.ID == tokenID {
			t.Error("Revoked token should not appear in list")
		}
	}
	t.Log("Verified token not in list after revoke")
}
