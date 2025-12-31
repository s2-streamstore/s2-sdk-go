package main

import (
	"context"
	"fmt"
	"log"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	client := s2.NewFromEnvironment(nil)
	ctx := context.Background()
	limit := 3

	fmt.Println("Basins:")

	basins := client.Basins.Iter(ctx, &s2.ListBasinsArgs{
		Limit:  &limit,
		Prefix: "h",
	})

	for basins.Next() {
		fmt.Printf("  - %s\n", basins.Value().Name)
	}

	if err := basins.Err(); err != nil {
		log.Fatalf("basin iterator error: %v", err)
	}

	fmt.Println("Streams:")

	basin := client.Basin("hello-world")
	streams := basin.Streams.Iter(ctx, &s2.ListStreamsArgs{Limit: &limit})

	for streams.Next() {
		fmt.Printf("  - %s\n", streams.Value().Name)
	}

	if err := streams.Err(); err != nil {
		log.Fatalf("stream iterator error: %v", err)
	}

	fmt.Println("Access tokens:")

	tokens := client.AccessTokens.Iter(ctx, nil)

	for tokens.Next() {
		fmt.Printf("  - %s\n", tokens.Value().ID)
	}

	if err := tokens.Err(); err != nil {
		log.Fatalf("token iterator error: %v", err)
	}
}
