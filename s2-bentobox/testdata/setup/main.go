// Tool to setup/teardown test resources for bento integration tests
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: setup <create|delete> [basin-name]")
		os.Exit(1)
	}

	action := os.Args[1]
	accessToken := os.Getenv("S2_ACCESS_TOKEN")
	if accessToken == "" {
		fmt.Fprintln(os.Stderr, "S2_ACCESS_TOKEN not set")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := s2.New(accessToken, nil)

	switch action {
	case "create":
		basinName := fmt.Sprintf("bento-ci-%d", time.Now().UnixNano())
		if len(os.Args) > 2 {
			basinName = os.Args[2]
		}

		_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
			Basin: s2.BasinName(basinName),
			Config: &s2.BasinConfig{
				CreateStreamOnAppend: s2.Ptr(true),
				CreateStreamOnRead:   s2.Ptr(true),
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create basin: %v\n", err)
			os.Exit(1)
		}

		// Output basin name for CI to capture
		fmt.Println(basinName)

	case "delete":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: setup delete <basin-name>")
			os.Exit(1)
		}
		basinName := os.Args[2]

		err := client.Basins.Delete(ctx, s2.BasinName(basinName))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to delete basin: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Deleted basin: %s\n", basinName)

	default:
		fmt.Fprintf(os.Stderr, "Unknown action: %s\n", action)
		os.Exit(1)
	}
}
