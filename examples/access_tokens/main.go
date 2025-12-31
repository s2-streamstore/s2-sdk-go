package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := s2.NewFromEnvironment(nil)

	resp, err := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: "demo-token",
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Exact: s2.String("hello-world")},
			Streams: &s2.ResourceSet{Prefix: s2.String("example-")},
			Ops: []string{
				s2.OperationAppend,
				s2.OperationRead,
				s2.OperationListStreams,
			},
		},
	})
	if err != nil {
		log.Fatalf("issue token: %v", err)
	}

	fmt.Printf("issued: %s\n", resp.AccessToken)

	if err := client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: "demo-token"}); err != nil {
		log.Fatalf("revoke token: %v", err)
	}

	fmt.Println("revoked")
}
