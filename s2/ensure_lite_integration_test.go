package s2_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestEnsureAgainstS2Lite(t *testing.T) {
	endpoint, ok := os.LookupEnv("S2_LITE_ENDPOINT")
	if !ok || endpoint == "" {
		t.Skip("S2_LITE_ENDPOINT not set")
	}
	endpoint = strings.TrimRight(endpoint, "/")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	client := s2.New("ignored", &s2.ClientOptions{
		BaseURL: endpoint,
		MakeBasinBaseURL: func(string) string {
			return endpoint
		},
		RequestTimeout: 5 * time.Second,
	})

	basinName := uniqueBasinName("ensure")
	defer deleteBasin(ctx, client, basinName)

	createdBasin, err := client.Basins.Ensure(ctx, s2.EnsureBasinArgs{
		Basin: basinName,
	})
	if err != nil {
		t.Fatalf("ensure basin create: %v", err)
	}
	if createdBasin.Result != s2.ProvisionResultCreated {
		t.Fatalf("ensure basin create result: got %q, want %q", createdBasin.Result, s2.ProvisionResultCreated)
	}

	noopBasin, err := client.Basins.Ensure(ctx, s2.EnsureBasinArgs{
		Basin: basinName,
	})
	if err != nil {
		t.Fatalf("ensure basin noop: %v", err)
	}
	if noopBasin.Result != s2.ProvisionResultNoop {
		t.Fatalf("ensure basin noop result: got %q, want %q", noopBasin.Result, s2.ProvisionResultNoop)
	}

	createOnAppend := true
	updatedBasin, err := client.Basins.Ensure(ctx, s2.EnsureBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: &createOnAppend,
		},
	})
	if err != nil {
		t.Fatalf("ensure basin update: %v", err)
	}
	if updatedBasin.Result != s2.ProvisionResultUpdated {
		t.Fatalf("ensure basin update result: got %q, want %q", updatedBasin.Result, s2.ProvisionResultUpdated)
	}

	noopUpdatedBasin, err := client.Basins.Ensure(ctx, s2.EnsureBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: &createOnAppend,
		},
	})
	if err != nil {
		t.Fatalf("ensure basin updated noop: %v", err)
	}
	if noopUpdatedBasin.Result != s2.ProvisionResultNoop {
		t.Fatalf("ensure basin updated noop result: got %q, want %q", noopUpdatedBasin.Result, s2.ProvisionResultNoop)
	}

	basin := client.Basin(string(basinName))
	streamName := s2.StreamName("ensure-stream")
	defer func() {
		_ = basin.Streams.Delete(ctx, streamName)
	}()

	createdStream, err := basin.Streams.Ensure(ctx, s2.EnsureStreamArgs{
		Stream: streamName,
	})
	if err != nil {
		t.Fatalf("ensure stream create: %v", err)
	}
	if createdStream.Result != s2.ProvisionResultCreated {
		t.Fatalf("ensure stream create result: got %q, want %q", createdStream.Result, s2.ProvisionResultCreated)
	}

	noopStream, err := basin.Streams.Ensure(ctx, s2.EnsureStreamArgs{
		Stream: streamName,
	})
	if err != nil {
		t.Fatalf("ensure stream noop: %v", err)
	}
	if noopStream.Result != s2.ProvisionResultNoop {
		t.Fatalf("ensure stream noop result: got %q, want %q", noopStream.Result, s2.ProvisionResultNoop)
	}

	retentionAge := int64(3600)
	updatedStream, err := basin.Streams.Ensure(ctx, s2.EnsureStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{Age: &retentionAge},
		},
	})
	if err != nil {
		t.Fatalf("ensure stream update: %v", err)
	}
	if updatedStream.Result != s2.ProvisionResultUpdated {
		t.Fatalf("ensure stream update result: got %q, want %q", updatedStream.Result, s2.ProvisionResultUpdated)
	}

	noopUpdatedStream, err := basin.Streams.Ensure(ctx, s2.EnsureStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{Age: &retentionAge},
		},
	})
	if err != nil {
		t.Fatalf("ensure stream updated noop: %v", err)
	}
	if noopUpdatedStream.Result != s2.ProvisionResultNoop {
		t.Fatalf("ensure stream updated noop result: got %q, want %q", noopUpdatedStream.Result, s2.ProvisionResultNoop)
	}

	t.Logf("basin results: create=%s noop=%s update=%s updated-noop=%s",
		createdBasin.Result, noopBasin.Result, updatedBasin.Result, noopUpdatedBasin.Result)
	t.Logf("stream results: create=%s noop=%s update=%s updated-noop=%s",
		createdStream.Result, noopStream.Result, updatedStream.Result, noopUpdatedStream.Result)
}
