// Documentation examples for Account and Basins page.
//
// Run with: go run ./examples/docs_account_and_basins
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx := context.Background()
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		log.Fatal("S2_ACCESS_TOKEN is required")
	}
	basinName := os.Getenv("S2_BASIN")
	if basinName == "" {
		log.Fatal("S2_BASIN is required")
	}

	client := s2.New(token, nil)

	{
		// ANCHOR: basin-operations
		// List basins
		basins, _ := client.Basins.List(ctx, nil)

		// Create a basin
		client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: "my-events"})

		// Get configuration
		config, _ := client.Basins.GetConfig(ctx, "my-events")

		// Delete
		client.Basins.Delete(ctx, "my-events")
		// ANCHOR_END: basin-operations
		fmt.Printf("Basins: %+v, config: %+v\n", basins, config)
	}

	basin := client.Basin(basinName)

	{
		// ANCHOR: stream-operations
		// List streams
		streams, _ := basin.Streams.List(ctx, &s2.ListStreamsArgs{Prefix: "user-"})

		// Create a stream
		basin.Streams.Create(ctx, s2.CreateStreamArgs{
			Stream: "user-actions",
			Config: &s2.StreamConfig{ /* optional */ },
		})

		// Get configuration
		config, _ := basin.Streams.GetConfig(ctx, "user-actions")

		// Delete
		basin.Streams.Delete(ctx, "user-actions")
		// ANCHOR_END: stream-operations
		fmt.Printf("Streams: %+v, config: %+v\n", streams, config)
	}

	// ANCHOR: access-token-basic
	// List tokens (returns metadata, not the secret)
	tokens, _ := client.AccessTokens.List(ctx, nil)

	// Issue a token scoped to streams under "users/1234/"
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	result, _ := client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: "user-1234-rw-token",
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Prefix: s2.String("")}, // all basins
			Streams: &s2.ResourceSet{Prefix: s2.String("users/1234/")},
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true, Write: true},
			},
		},
		ExpiresAt: &expires,
	})

	// Revoke a token
	client.AccessTokens.Revoke(ctx, s2.RevokeAccessTokenArgs{ID: "user-1234-rw-token"})
	// ANCHOR_END: access-token-basic
	fmt.Printf("Tokens: %+v, issued: %+v\n", tokens, result)

	// ANCHOR: access-token-restricted
	client.AccessTokens.Issue(ctx, s2.IssueAccessTokenArgs{
		ID: "restricted-token",
		Scope: s2.AccessTokenScope{
			Basins:  &s2.ResourceSet{Exact: s2.String("production")},
			Streams: &s2.ResourceSet{Prefix: s2.String("logs/")},
			OpGroups: &s2.PermittedOperationGroups{
				Stream: &s2.ReadWritePermissions{Read: true},
			},
		},
	})
	// ANCHOR_END: access-token-restricted

	// Pagination example - not executed by default
	if false {
		// ANCHOR: pagination
		// Iterate through all streams with automatic pagination
		iter := basin.Streams.Iter(ctx, nil)
		for iter.Next() {
			stream := iter.Value()
			fmt.Println(stream.Name)
		}
		if err := iter.Err(); err != nil {
			log.Fatal(err)
		}
		// ANCHOR_END: pagination
	}
}
