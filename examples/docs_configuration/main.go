// Documentation examples for Configuration page.
//
// Run with: go run ./examples/docs_configuration
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	// Example: Custom endpoints (e.g., for s2-lite local dev)
	{
		// ANCHOR: custom-endpoints
		client := s2.New("local-token", &s2.ClientOptions{
			BaseURL: "http://localhost:8080",
			MakeBasinBaseURL: func(basin string) string {
				return "http://localhost:8080"
			},
		})
		// ANCHOR_END: custom-endpoints
		fmt.Printf("Created client with custom endpoints: %+v\n", client)
	}

	// Example: Custom retry configuration
	{
		token := os.Getenv("S2_ACCESS_TOKEN")
		if token == "" {
			token = "demo"
		}
		// ANCHOR: retry-config
		client := s2.New(token, &s2.ClientOptions{
			RetryConfig: &s2.RetryConfig{
				MaxAttempts:  5,
				MinBaseDelay: 100 * time.Millisecond,
				MaxBaseDelay: 2 * time.Second,
			},
		})
		// ANCHOR_END: retry-config
		fmt.Printf("Created client with retry config: %+v\n", client)
	}

	// Example: Custom timeout configuration
	{
		token := os.Getenv("S2_ACCESS_TOKEN")
		if token == "" {
			token = "demo"
		}
		// ANCHOR: timeout-config
		client := s2.New(token, &s2.ClientOptions{
			ConnectionTimeout: 5 * time.Second,
			RequestTimeout:    30 * time.Second,
		})
		// ANCHOR_END: timeout-config
		fmt.Printf("Created client with timeout config: %+v\n", client)
	}
}
