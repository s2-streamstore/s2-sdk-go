package s2_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

// TestBaseURLOnlyAgainstS2Lite reproduces the buggy configuration against a real
// S2-Lite server: ONLY BaseURL is set (no MakeBasinBaseURL). Before the fix,
// basin-scoped requests would go to *.b.s2.dev and fail with a 401 from the cloud.
// After the fix, basin-scoped requests go to the Lite server and succeed.
func TestBaseURLOnlyAgainstS2Lite(t *testing.T) {
	endpoint, ok := os.LookupEnv("S2_LITE_ENDPOINT")
	if !ok || endpoint == "" {
		t.Skip("S2_LITE_ENDPOINT not set")
	}
	endpoint = strings.TrimRight(endpoint, "/")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := s2.New("ignored", &s2.ClientOptions{
		BaseURL:        endpoint,
		RequestTimeout: 5 * time.Second,
	})
	basinName := uniqueBasinName("baseurlonly")
	defer deleteBasin(ctx, client, basinName)

	// Basin-scoped Ensure must hit the Lite server (via shared endpoint + s2-basin header).
	if _, err := client.Basins.Ensure(ctx, s2.EnsureBasinArgs{Basin: basinName}); err != nil {
		t.Fatalf("basin ensure against lite with BaseURL-only: %v", err)
	}
	basin := client.Basin(string(basinName))
	streamName := s2.StreamName("baseurlonly-stream")
	defer func() { _ = basin.Streams.Delete(ctx, streamName) }()

	if _, err := basin.Streams.Ensure(ctx, s2.EnsureStreamArgs{Stream: streamName}); err != nil {
		t.Fatalf("stream ensure against lite with BaseURL-only: %v", err)
	}

	if _, err := basin.Streams.List(ctx, nil); err != nil {
		t.Fatalf("stream list against lite with BaseURL-only: %v", err)
	}
	t.Logf("BaseURL-only configuration succeeded against S2-Lite at %s (basin=%s)", endpoint, basinName)
}
