// Tool to setup/teardown test resources for bento integration tests
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const streamName = "test-stream"

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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := s2.New(accessToken, nil)

	switch action {
	case "create":
		basinName := fmt.Sprintf("bento-ci-%d", time.Now().UnixNano())
		if len(os.Args) > 2 {
			basinName = os.Args[2]
		}

		// Create basin
		_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
			Basin: s2.BasinName(basinName),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create basin: %v\n", err)
			os.Exit(1)
		}

		// Wait for basin to be active
		if err := waitForBasinActive(ctx, client, s2.BasinName(basinName)); err != nil {
			fmt.Fprintf(os.Stderr, "Failed waiting for basin to be active: %v\n", err)
			os.Exit(1)
		}

		// Create stream
		basin := client.Basin(basinName)
		_, err = basin.Streams.Create(ctx, s2.CreateStreamArgs{
			Stream: streamName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create stream: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "Deleted basin: %s\n", basinName)

	default:
		fmt.Fprintf(os.Stderr, "Unknown action: %s\n", action)
		os.Exit(1)
	}
}

func waitForBasinActive(ctx context.Context, client *s2.Client, name s2.BasinName) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for basin %s to become active", name)
		default:
		}

		_, err := client.Basins.GetConfig(ctx, name)
		if err == nil {
			return nil
		}

		var s2Err *s2.S2Error
		if errors.As(err, &s2Err) && s2Err.Code == "unavailable" {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// Non-unavailable error or success
		return nil
	}
}
