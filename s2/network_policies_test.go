package s2

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The service pages network policies by slug, while the reference tokens are bound to is `<slug>.<nonce>`.
func TestNetworkPoliciesIterPagesBySlug(t *testing.T) {
	var startAfters []string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		startAfter := req.URL.Query().Get("start_after")
		startAfters = append(startAfters, startAfter)
		if startAfter == "" {
			return jsonResponse(http.StatusOK, "", `{"has_more":true,"network_policies":[
				{"id":"a.n1","slug":"a","created_at":"2026-01-01T00:00:00Z","policy":{"public_net":{"cidrs":["203.0.113.0/24"]}}}]}`), nil
		}
		return jsonResponse(http.StatusOK, "", `{"has_more":false,"network_policies":[
			{"id":"b.n2","slug":"b","created_at":"2026-01-01T00:00:00Z","policy":{"public_net":{"some-future-kind":{}},"private_net":{"aws":{"vpce_ids":["vpce-0123456789abcdef0"]}}}}]}`), nil
	})

	client := New("token", &ClientOptions{
		BaseURL:          "http://account.test",
		MakeBasinBaseURL: testBasinBaseURL,
		HTTPClient:       &http.Client{Transport: rt, Timeout: time.Second},
	})

	var policies []NetworkPolicyInfo
	it := client.NetworkPolicies.Iter(context.Background(), nil)
	for it.Next() {
		policies = append(policies, it.Value())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate network policies: %v", err)
	}

	if len(startAfters) != 2 || startAfters[1] != "a" {
		t.Fatalf("expected the second page to start after slug %q, got %q", "a", startAfters)
	}
	if len(policies) != 2 || policies[0].Policy.PublicNet.Cidrs[0] != "203.0.113.0/24" {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	// A kind this SDK version does not know still decodes, with no known field set.
	second := policies[1].Policy
	if second.PublicNet == nil || second.PublicNet.Cidrs != nil {
		t.Fatalf("unexpected public_net for an unknown kind: %#v", second.PublicNet)
	}
	if second.PrivateNet.Aws.VpceIDs[0] != "vpce-0123456789abcdef0" {
		t.Fatalf("unexpected private_net: %#v", second.PrivateNet)
	}
}
