package s2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const accessTokenTestTimeout = 60 * time.Second

func uniqueTokenID(prefix string) s2.AccessTokenID {
	return s2.AccessTokenID(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

func revokeToken(ctx context.Context, client *s2.Client, id s2.AccessTokenID) {
	_ = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: id})
}

// --- List Access Tokens Tests ---

func TestListAccessTokens_All(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: List all access tokens")

	client := testClient(t)
	resp, err := client.AccessTokens.List(ctx, nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	t.Logf("Listed %d tokens, has_more=%v", len(resp.AccessTokens), resp.HasMore)
}

func TestListAccessTokens_WithPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with prefix filter")

	client := testClient(t)
	prefix := fmt.Sprintf("test-prefix-%d", time.Now().UnixNano())

	tokenID1 := s2.AccessTokenID(prefix + "-tok1")
	tokenID2 := s2.AccessTokenID(prefix + "-tok2")
	otherID := uniqueTokenID("other")

	defer revokeToken(ctx, client, tokenID1)
	defer revokeToken(ctx, client, tokenID2)
	defer revokeToken(ctx, client, otherID)

	for _, id := range []s2.AccessTokenID{tokenID1, tokenID2, otherID} {
		_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
			ID:    id,
			Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
		})
		if err != nil {
			t.Fatalf("Issue token %s failed: %v", id, err)
		}
	}

	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: prefix,
	})
	if err != nil {
		t.Fatalf("List with prefix failed: %v", err)
	}
	if len(resp.AccessTokens) != 2 {
		t.Errorf("Expected 2 tokens with prefix, got %d", len(resp.AccessTokens))
	}
	for _, tok := range resp.AccessTokens {
		if string(tok.ID)[:len(prefix)] != prefix {
			t.Errorf("Token %s does not have prefix %s", tok.ID, prefix)
		}
	}
	t.Logf("Listed %d tokens with prefix %s", len(resp.AccessTokens), prefix)
}

func TestListAccessTokens_WithLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with limit")

	client := testClient(t)
	prefix := fmt.Sprintf("test-limit-%d", time.Now().UnixNano())

	for i := range 5 {
		id := s2.AccessTokenID(fmt.Sprintf("%s-tok%d", prefix, i))
		defer revokeToken(ctx, client, id)

		_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
			ID:    id,
			Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
		})
		if err != nil {
			t.Fatalf("Issue token %s failed: %v", id, err)
		}
	}

	limit := 2
	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: prefix,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List with limit failed: %v", err)
	}
	if len(resp.AccessTokens) > 2 {
		t.Errorf("Expected at most 2 tokens, got %d", len(resp.AccessTokens))
	}
	t.Logf("Listed %d tokens with limit=%d, has_more=%v", len(resp.AccessTokens), limit, resp.HasMore)
}

func TestListAccessTokens_Pagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens pagination")

	client := testClient(t)
	prefix := fmt.Sprintf("test-page-%d", time.Now().UnixNano())

	for i := range 5 {
		id := s2.AccessTokenID(fmt.Sprintf("%s-tok%d", prefix, i))
		defer revokeToken(ctx, client, id)

		_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
			ID:    id,
			Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
		})
		if err != nil {
			t.Fatalf("Issue token %s failed: %v", id, err)
		}
	}

	var allTokens []s2.AccessTokenInfo
	startAfter := ""
	limit := 2
	for {
		resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
			Prefix:     prefix,
			StartAfter: startAfter,
			Limit:      &limit,
		})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		allTokens = append(allTokens, resp.AccessTokens...)
		if !resp.HasMore || len(resp.AccessTokens) == 0 {
			break
		}
		startAfter = string(resp.AccessTokens[len(resp.AccessTokens)-1].ID)
	}
	if len(allTokens) != 5 {
		t.Errorf("Expected 5 tokens through pagination, got %d", len(allTokens))
	}
	t.Logf("Paginated through %d tokens", len(allTokens))
}

func TestListAccessTokens_Iterator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Access token iterator")

	client := testClient(t)
	prefix := fmt.Sprintf("test-iter-%d", time.Now().UnixNano())

	for i := range 3 {
		id := s2.AccessTokenID(fmt.Sprintf("%s-tok%d", prefix, i))
		defer revokeToken(ctx, client, id)

		_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
			ID:    id,
			Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
		})
		if err != nil {
			t.Fatalf("Issue token %s failed: %v", id, err)
		}
	}

	count := 0
	iter := client.AccessTokens.Iter(ctx, &s2.ListAccessTokensArgs{Prefix: prefix})
	for iter.Next() {
		tok := iter.Value()
		if string(tok.ID)[:len(prefix)] != prefix {
			t.Errorf("Token %s does not have prefix %s", tok.ID, prefix)
		}
		count++
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 tokens from iterator, got %d", count)
	}
	t.Logf("Iterated through %d tokens", count)
}

func TestListAccessTokens_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: List access tokens with non-matching prefix returns empty")

	client := testClient(t)
	resp, err := client.AccessTokens.List(ctx, &s2.ListAccessTokensArgs{
		Prefix: "zzz-nonexistent-prefix-12345",
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.AccessTokens) != 0 {
		t.Errorf("Expected 0 tokens, got %d", len(resp.AccessTokens))
	}
	if resp.HasMore {
		t.Error("Expected has_more=false")
	}
	t.Log("Empty list returned as expected")
}

// --- Issue Access Token Tests ---

func TestIssueAccessToken_WithOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with specific ops")

	client := testClient(t)
	tokenID := uniqueTokenID("test-ops")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins, s2.OperationListStreams},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with ops: %s", tokenID)
}

func TestIssueAccessToken_WithOpGroups_ReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with account read-only op_groups")

	client := testClient(t)
	tokenID := uniqueTokenID("test-opgroups-read")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
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
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with account read-only: %s", tokenID)
}

func TestIssueAccessToken_WithOpGroups_ReadWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with stream read/write op_groups")

	client := testClient(t)
	tokenID := uniqueTokenID("test-opgroups-rw")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Prefix: s2.Ptr("")},
			Streams: &s2.ResourceSet{Prefix: s2.Ptr("")},
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true, Write: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with stream read/write: %s", tokenID)
}

func TestIssueAccessToken_WithBasinExactScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with basin exact scope")

	client := testClient(t)
	tokenID := uniqueTokenID("test-basin-exact")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Exact: s2.Ptr("my-test-basin")},
			Ops:    []string{s2.OperationListStreams},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with basin exact scope: %s", tokenID)
}

func TestIssueAccessToken_WithBasinPrefixScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with basin prefix scope")

	client := testClient(t)
	tokenID := uniqueTokenID("test-basin-prefix")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: s2.Ptr("test-")},
			Ops:    []string{s2.OperationCreateBasin},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with basin prefix scope: %s", tokenID)
}

func TestIssueAccessToken_WithStreamExactScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with stream exact scope")

	client := testClient(t)
	tokenID := uniqueTokenID("test-stream-exact")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Prefix: s2.Ptr("")},
			Streams: &s2.ResourceSet{Exact: s2.Ptr("my-stream")},
			Ops:     []string{s2.OperationAppend},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with stream exact scope: %s", tokenID)
}

func TestIssueAccessToken_WithStreamPrefixScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with stream prefix scope")

	client := testClient(t)
	tokenID := uniqueTokenID("test-stream-prefix")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Prefix: s2.Ptr("")},
			Streams: &s2.ResourceSet{Prefix: s2.Ptr("logs-")},
			Ops:     []string{s2.OperationCreateStream},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with stream prefix scope: %s", tokenID)
}

func TestIssueAccessToken_WithAutoPrefixStreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with auto_prefix_streams")

	client := testClient(t)
	tokenID := uniqueTokenID("test-autoprefix")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Prefix: s2.Ptr("")},
			Streams: &s2.ResourceSet{Prefix: s2.Ptr("tenant/")},
			Ops:     []string{s2.OperationCreateStream, s2.OperationListStreams},
		},
		AutoPrefixStreams: true,
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with auto_prefix_streams: %s", tokenID)
}

func TestIssueAccessToken_WithExpiresAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with expires_at")

	client := testClient(t)
	tokenID := uniqueTokenID("test-expires")
	defer revokeToken(ctx, client, tokenID)

	expiresAt := time.Now().Add(24 * time.Hour)
	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Expected non-empty access token")
	}
	t.Logf("Issued token with expires_at: %s", tokenID)
}

func TestIssueAccessToken_DuplicateID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with duplicate ID (expects 409)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-dup")
	defer revokeToken(ctx, client, tokenID)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    tokenID,
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
	})
	if err != nil {
		t.Fatalf("First issue failed: %v", err)
	}

	_, err = client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    tokenID,
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 409 {
		t.Errorf("Expected 409 error, got: %v", err)
	}
	t.Logf("Got expected 409 error for duplicate token ID: %v", err)
}

func TestIssueAccessToken_InvalidID_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with empty ID (expects validation error)")

	client := testClient(t)

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    "",
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
	})
	if err == nil {
		t.Error("Expected error for empty token ID")
	}
	t.Logf("Got expected error for empty token ID: %v", err)
}

func TestIssueAccessToken_InvalidID_TooLong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Issue access token with ID > 96 chars (expects validation error)")

	client := testClient(t)
	longID := s2.AccessTokenID(string(make([]byte, 97)))

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    longID,
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
	})
	if err == nil {
		t.Error("Expected error for token ID > 96 characters")
	}
	t.Logf("Got expected error for too long token ID: %v", err)
}

// --- Revoke Access Token Tests ---

func TestRevokeAccessToken_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke existing access token")

	client := testClient(t)
	tokenID := uniqueTokenID("test-revoke")

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    tokenID,
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	err = client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	t.Logf("Revoked token: %s", tokenID)
}

func TestRevokeAccessToken_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke non-existent access token (expects 404)")

	client := testClient(t)
	tokenID := uniqueTokenID("nonexistent")

	err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: tokenID})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected 404 error for non-existent token: %v", err)
}

func TestRevokeAccessToken_AlreadyRevoked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke already revoked access token (expects 404)")

	client := testClient(t)
	tokenID := uniqueTokenID("test-double-revoke")

	_, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID:    tokenID,
		Scope: s2.AccessTokenScope{Ops: []string{s2.OperationListBasins}},
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
	t.Logf("Got expected 404 error for already revoked token: %v", err)
}

func TestRevokeAccessToken_InvalidID_Empty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Revoke access token with empty ID (expects validation error)")

	client := testClient(t)

	err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: ""})
	if err == nil {
		t.Error("Expected error for empty token ID")
	}
	t.Logf("Got expected error for empty token ID: %v", err)
}

// --- Authorization & Scope Tests ---

func TestAccessToken_BasinPrefixScope_Allowed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token with basin prefix scope can access matching basin")

	client := testClient(t)
	tokenID := uniqueTokenID("test-basin-scope")
	basinName := uniqueBasinName("testscope")
	defer revokeToken(ctx, client, tokenID)
	defer deleteBasin(ctx, client, basinName)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: s2.Ptr("testscope-")},
			Ops:    []string{s2.OperationCreateBasin, s2.OperationCreateStream},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)

	_, err = limitedClient.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		var s2Err *s2.S2Error
		if errors.As(err, &s2Err) && s2Err.Code == "resource_already_exists" {
			t.Log("Basin already exists (ok)")
		} else if isFreeTierLimitation(err) {
			t.Skip("Skipping due to free tier limitation")
		} else {
			t.Fatalf("Create basin failed: %v", err)
		}
	}
	t.Logf("Successfully created basin with limited token: %s", basinName)
}

func TestAccessToken_BasinPrefixScope_Denied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token with basin prefix scope denied for non-matching basin")

	client := testClient(t)
	tokenID := uniqueTokenID("test-basin-denied")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins: &s2.ResourceSet{Prefix: s2.Ptr("allowed-")},
			Ops:    []string{s2.OperationCreateBasin},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)
	basinName := uniqueBasinName("denied")

	_, err = limitedClient.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Errorf("Expected 403 permission denied, got: %v", err)
	}
	t.Logf("Got expected 403 for non-matching basin prefix: %v", err)
}

func TestAccessToken_OperationDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token without operation is denied")

	client := testClient(t)
	tokenID := uniqueTokenID("test-op-denied")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)

	_, err = limitedClient.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: uniqueBasinName("shouldfail"),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Errorf("Expected 403 permission denied, got: %v", err)
	}
	t.Logf("Got expected 403 for unauthorized operation: %v", err)
}

func TestAccessToken_EscalationDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token cannot escalate permissions")

	client := testClient(t)
	tokenID1 := uniqueTokenID("test-limited")
	tokenID2 := uniqueTokenID("test-escalate")
	defer revokeToken(ctx, client, tokenID1)
	defer revokeToken(ctx, client, tokenID2)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID1,
		Scope: s2.AccessTokenScope{
			Ops:          []string{s2.OperationListBasins, s2.OperationIssueAccessToken},
			AccessTokens: &s2.ResourceSet{Prefix: s2.Ptr("")},
		},
	})
	if err != nil {
		t.Fatalf("Issue limited token failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)

	_, err = limitedClient.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID2,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationCreateBasin},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Errorf("Expected 403 for escalation attempt, got: %v", err)
	}
	t.Logf("Got expected 403 for escalation attempt: %v", err)
}

func TestAccessToken_ListWithoutPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token without list-access-tokens permission is denied")

	client := testClient(t)
	tokenID := uniqueTokenID("test-no-list")
	defer revokeToken(ctx, client, tokenID)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Ops: []string{s2.OperationListBasins},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)

	_, err = limitedClient.AccessTokens.List(ctx, nil)

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Errorf("Expected 403 permission denied, got: %v", err)
	}
	t.Logf("Got expected 403 for list without permission: %v", err)
}

func TestAccessToken_CrossBasinDenied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), accessTokenTestTimeout)
	defer cancel()
	t.Log("Testing: Token scoped to basin A cannot access basin B")

	client := testClient(t)
	tokenID := uniqueTokenID("test-cross-basin")
	basinA := uniqueBasinName("basina")
	basinB := uniqueBasinName("basinb")
	defer revokeToken(ctx, client, tokenID)
	defer deleteBasin(ctx, client, basinA)
	defer deleteBasin(ctx, client, basinB)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinA})
	if err != nil && !isResourceAlreadyExists(err) {
		if isFreeTierLimitation(err) {
			t.Skip("Skipping due to free tier limitation")
		}
		t.Fatalf("Create basin A failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinA)

	_, err = client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinB})
	if err != nil && !isResourceAlreadyExists(err) {
		if isFreeTierLimitation(err) {
			t.Skip("Skipping due to free tier limitation")
		}
		t.Fatalf("Create basin B failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinB)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: tokenID,
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Exact: s2.Ptr(string(basinA))},
			Streams: &s2.ResourceSet{Prefix: s2.Ptr("")},
			Ops:     []string{s2.OperationCreateStream},
		},
	})
	if err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	limitedClient := s2.New(resp.AccessToken, nil)

	_, err = limitedClient.Basin(string(basinB)).Streams.Create(ctx, s2.CreateStreamArgs{Stream: "test-stream"})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 403 {
		t.Errorf("Expected 403 for cross-basin access, got: %v", err)
	}
	t.Logf("Got expected 403 for cross-basin access: %v", err)
}

func isResourceAlreadyExists(err error) bool {
	var s2Err *s2.S2Error
	return errors.As(err, &s2Err) && s2Err.Code == "resource_already_exists"
}
