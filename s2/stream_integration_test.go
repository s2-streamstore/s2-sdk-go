package s2_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const streamTestTimeout = 60 * time.Second

func streamTestClient(t *testing.T) *s2.Client {
	t.Helper()
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set")
	}
	return s2.NewFromEnvironment(&s2.ClientOptions{
		IncludeBasinHeader: true,
	})
}

func uniqueStreamName(prefix string) s2.StreamName {
	return s2.StreamName(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

func createTestBasin(ctx context.Context, t *testing.T, client *s2.Client) s2.BasinName {
	t.Helper()
	basinName := s2.BasinName(fmt.Sprintf("test-strm-%d", time.Now().UnixNano()))
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Failed to create test basin: %v", err)
	}
	waitForBasinReady(ctx, t, client, basinName)
	return basinName
}

func waitForBasinReady(ctx context.Context, t *testing.T, client *s2.Client, name s2.BasinName) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for basin %s to become active", name)
		default:
		}
		_, err := client.Basins.GetConfig(ctx, name)
		if err == nil {
			return
		}
		var s2Err *s2.S2Error
		if errors.As(err, &s2Err) && s2Err.Code == "basin_creating" {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return
	}
}

func deleteTestBasin(ctx context.Context, client *s2.Client, name s2.BasinName) {
	_ = client.Basins.Delete(ctx, name)
}

func deleteStream(ctx context.Context, basin *s2.BasinClient, name s2.StreamName) {
	_ = basin.Streams.Delete(ctx, name)
}

func ptrU64(v uint64) *uint64 {
	return &v
}

func ptrI64(v int64) *int64 {
	return &v
}

func ptrStr(v string) *string {
	return &v
}

func ptrBool(v bool) *bool {
	return &v
}

func isStreamFreeTierLimitation(err error) bool {
	var s2Err *s2.S2Error
	if errors.As(err, &s2Err) && (s2Err.Code == "invalid_stream_config" || s2Err.Code == "bad_config") {
		msg := strings.ToLower(s2Err.Message)
		return strings.Contains(msg, "free tier")
	}
	return false
}

// --- List Streams Tests ---

func TestListStreams_All(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List all streams")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	resp, err := basin.Streams.List(ctx, nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	t.Logf("Listed %d streams, has_more=%v", len(resp.Streams), resp.HasMore)
}

func TestListStreams_WithPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with prefix")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-list")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Logf("Created stream: %s", streamName)

	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix: "test-list",
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, s := range resp.Streams {
		if s.Name == streamName {
			found = true
			break
		}
		if !strings.HasPrefix(string(s.Name), "test-list") {
			t.Errorf("Stream %s does not match prefix test-list", s.Name)
		}
	}
	if !found {
		t.Error("Created stream not found in list")
	}
	t.Logf("Found %d streams with prefix", len(resp.Streams))
}

func TestListStreams_WithLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with limit")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	limit := 5
	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Streams) > limit {
		t.Errorf("Expected at most %d streams, got %d", limit, len(resp.Streams))
	}
	t.Logf("Listed %d streams with limit=%d, has_more=%v", len(resp.Streams), limit, resp.HasMore)
}

func TestListStreams_Pagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with pagination")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))

	for i := 0; i < 3; i++ {
		name := s2.StreamName(fmt.Sprintf("page-test-%d", i))
		_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: name})
		if err != nil {
			t.Fatalf("Create stream %d failed: %v", i, err)
		}
		defer deleteStream(ctx, basin, name)
	}

	limit := 2
	resp1, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix: "page-test",
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("First list failed: %v", err)
	}

	if len(resp1.Streams) == 0 {
		t.Skip("No streams to paginate")
	}

	lastName := string(resp1.Streams[len(resp1.Streams)-1].Name)
	resp2, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix:     "page-test",
		StartAfter: lastName,
		Limit:      &limit,
	})
	if err != nil {
		t.Fatalf("Second list failed: %v", err)
	}

	for _, s := range resp2.Streams {
		if string(s.Name) <= lastName {
			t.Errorf("Stream %s should be after %s", s.Name, lastName)
		}
	}
	t.Logf("Page 1: %d streams, Page 2: %d streams", len(resp1.Streams), len(resp2.Streams))
}

func TestListStreams_Iterator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Iterate over streams")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	limit := 3
	iter := basin.Streams.Iter(ctx, &s2.ListStreamsArgs{
		Limit: &limit,
	})

	count := 0
	for iter.Next() {
		stream := iter.Value()
		if stream.Name == "" {
			t.Error("Stream name should not be empty")
		}
		count++
	}

	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	t.Logf("Iterated over %d streams", count)
}

// --- Create Stream Tests ---

func TestCreateStream_Minimal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create minimal stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-cmin")
	defer deleteStream(ctx, basin, streamName)

	info, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if info.Name != streamName {
		t.Errorf("Expected name %s, got %s", streamName, info.Name)
	}
	t.Logf("Created stream: %s", info.Name)
}

func TestCreateStream_WithFullConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with full config")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-full")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	timestampMode := s2.TimestampingModeClientRequire
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(86400),
			},
			Timestamping: &s2.TimestampingConfig{
				Mode:     &timestampMode,
				Uncapped: ptrBool(true),
			},
			DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
				MinAgeSecs: ptrI64(3600),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.StorageClass == nil || *config.StorageClass != s2.StorageClassStandard {
		t.Error("Expected storage_class=standard")
	}
	t.Logf("Created stream %s with full config", streamName)
}

func TestCreateStream_StorageClassStandard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with storage_class=standard")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-scst")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.StorageClass == nil || *config.StorageClass != s2.StorageClassStandard {
		t.Errorf("Expected storage_class=standard")
	}
	t.Log("Verified storage_class=standard")
}

func TestCreateStream_StorageClassExpress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with storage_class=express")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-scex")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassExpress
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
		},
	})
	if err != nil {
		if isStreamFreeTierLimitation(err) {
			t.Log("Skipped: express storage class not available on free tier")
			return
		}
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.StorageClass != nil && *config.StorageClass != s2.StorageClassExpress {
		t.Errorf("Expected storage_class=express, got %s", *config.StorageClass)
	}
	t.Log("Verified storage_class=express")
}

func TestCreateStream_RetentionPolicyAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with retention_policy.age")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rpag")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(86400),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.RetentionPolicy == nil || config.RetentionPolicy.Age == nil {
		t.Fatal("Expected retention_policy.age")
	}
	if *config.RetentionPolicy.Age != 86400 {
		t.Errorf("Expected age=86400, got %d", *config.RetentionPolicy.Age)
	}
	t.Log("Verified retention_policy.age=86400")
}

func TestCreateStream_RetentionPolicyInfinite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with retention_policy.infinite")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rpin")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{
				Infinite: &s2.InfiniteRetention{},
			},
		},
	})
	if err != nil {
		if isStreamFreeTierLimitation(err) {
			t.Log("Skipped: infinite retention not available on free tier")
			return
		}
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.RetentionPolicy == nil || config.RetentionPolicy.Infinite == nil {
		t.Error("Expected retention_policy.infinite")
	}
	t.Log("Verified retention_policy.infinite")
}

func TestCreateStream_TimestampingModes(t *testing.T) {
	modes := []struct {
		mode s2.TimestampingMode
		name string
	}{
		{s2.TimestampingModeClientPrefer, "client-prefer"},
		{s2.TimestampingModeClientRequire, "client-require"},
		{s2.TimestampingModeArrival, "arrival"},
	}

	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Create stream with timestamping.mode=%s", tc.name)

			client := streamTestClient(t)
			basinName := createTestBasin(ctx, t, client)
			defer deleteTestBasin(ctx, client, basinName)

			basin := client.Basin(string(basinName))
			streamName := uniqueStreamName("test-tsm")
			defer deleteStream(ctx, basin, streamName)

			mode := tc.mode
			_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
				Stream: streamName,
				Config: &s2.StreamConfig{
					Timestamping: &s2.TimestampingConfig{
						Mode: &mode,
					},
				},
			})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			config, err := basin.Streams.GetConfig(ctx, streamName)
			if err != nil {
				t.Fatalf("GetConfig failed: %v", err)
			}

			if config.Timestamping != nil && config.Timestamping.Mode != nil {
				if *config.Timestamping.Mode != tc.mode {
					t.Errorf("Expected mode=%s, got %s", tc.mode, *config.Timestamping.Mode)
				}
			}
			t.Logf("Verified timestamping.mode=%s", tc.mode)
		})
	}
}

func TestCreateStream_DeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with delete_on_empty.min_age_secs")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-doe")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
				MinAgeSecs: ptrI64(3600),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DeleteOnEmpty == nil || config.DeleteOnEmpty.MinAgeSecs == nil {
		t.Fatal("Expected delete_on_empty.min_age_secs")
	}
	if *config.DeleteOnEmpty.MinAgeSecs != 3600 {
		t.Errorf("Expected min_age_secs=3600, got %d", *config.DeleteOnEmpty.MinAgeSecs)
	}
	t.Log("Verified delete_on_empty.min_age_secs=3600")
}

func TestCreateStream_DuplicateName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with duplicate name")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-dup")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("First create failed: %v", err)
	}

	_, err = basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 409 {
		t.Errorf("Expected 409 conflict, got: %v", err)
	}
	if s2Err != nil && s2Err.Code != "stream_exists" {
		t.Errorf("Expected error code stream_exists, got: %s", s2Err.Code)
	}
	t.Logf("Got expected conflict error: %v", err)
}

func TestCreateStream_InvalidRetentionAgeZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with retention_policy.age=0")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-raz")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(0),
			},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 400 {
		t.Errorf("Expected 400 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Get Stream Config Tests ---

func TestGetStreamConfig_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get existing stream config")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-gcfg")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.GetConfig(ctx, streamName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.StorageClass == nil || *config.StorageClass != s2.StorageClassStandard {
		t.Error("Expected storage_class=standard")
	}
	t.Logf("Got config: storage_class=%s", *config.StorageClass)
}

func TestGetStreamConfig_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get non-existent stream config")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	_, err := basin.Streams.GetConfig(ctx, "nonexistent-stream-12345")

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	if s2Err != nil && s2Err.Code != "stream_not_found" {
		t.Errorf("Expected error code stream_not_found, got: %s", s2Err.Code)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Delete Stream Tests ---

func TestDeleteStream_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Delete existing stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-del")

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Logf("Created stream: %s", streamName)

	err = basin.Streams.Delete(ctx, streamName)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	t.Log("Stream deleted successfully")
}

func TestDeleteStream_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Delete non-existent stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	err := basin.Streams.Delete(ctx, "nonexistent-stream-12345")

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestDeleteStream_VerifyNotListable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Verify stream not listable after delete")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-dvnl")

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = basin.Streams.Delete(ctx, streamName)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix: string(streamName),
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	for _, s := range resp.Streams {
		if s.Name == streamName && s.DeletedAt == nil {
			t.Error("Deleted stream should not be in list without deleted_at")
		}
	}
	t.Log("Verified stream not listable after delete")
}

// --- Reconfigure Stream Tests ---

func TestReconfigureStream_ChangeStorageClass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream storage_class")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rcsc")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	newStorageClass := s2.StorageClassExpress
	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			StorageClass: &newStorageClass,
		},
	})
	if err != nil {
		if isStreamFreeTierLimitation(err) {
			t.Log("Skipped: express storage class not available on free tier")
			return
		}
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.StorageClass != nil && *config.StorageClass != s2.StorageClassExpress {
		t.Errorf("Expected storage_class=express, got %s", *config.StorageClass)
	}
	t.Log("Verified storage_class changed to express")
}

func TestReconfigureStream_ChangeRetentionToAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream retention_policy to age-based")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rra")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(3600),
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.RetentionPolicy == nil || config.RetentionPolicy.Age == nil {
		t.Fatal("Expected retention_policy.age")
	}
	if *config.RetentionPolicy.Age != 3600 {
		t.Errorf("Expected age=3600, got %d", *config.RetentionPolicy.Age)
	}
	t.Log("Verified retention_policy changed to age=3600")
}

func TestReconfigureStream_ChangeRetentionToInfinite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream retention_policy to infinite")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rri")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(86400),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			RetentionPolicy: &s2.RetentionPolicy{
				Infinite: &s2.InfiniteRetention{},
			},
		},
	})
	if err != nil {
		if isStreamFreeTierLimitation(err) {
			t.Log("Skipped: infinite retention not available on free tier")
			return
		}
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.RetentionPolicy == nil || config.RetentionPolicy.Infinite == nil {
		t.Error("Expected retention_policy.infinite")
	}
	t.Log("Verified retention_policy changed to infinite")
}

func TestReconfigureStream_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure non-existent stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: "nonexistent-stream-12345",
		Config: s2.StreamReconfiguration{
			StorageClass: &storageClass,
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestReconfigureStream_EmptyConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream with empty config")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-remp")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.StorageClass == nil || *config.StorageClass != s2.StorageClassStandard {
		t.Error("Expected storage_class to remain standard")
	}
	t.Log("Verified empty reconfigure preserves config")
}

// --- Check Tail Tests ---

func TestCheckTail_EmptyStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Check tail on empty stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-tail")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	tail, err := stream.CheckTail(ctx)
	if err != nil {
		t.Fatalf("CheckTail failed: %v", err)
	}

	if tail.Tail.SeqNum != 0 {
		t.Errorf("Expected seq_num=0 for empty stream, got %d", tail.Tail.SeqNum)
	}
	t.Logf("Tail: seq_num=%d, timestamp=%d", tail.Tail.SeqNum, tail.Tail.Timestamp)
}

func TestCheckTail_AfterAppends(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Check tail after appends")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-taila")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("record1")},
			{Body: []byte("record2")},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	tail, err := stream.CheckTail(ctx)
	if err != nil {
		t.Fatalf("CheckTail failed: %v", err)
	}

	if tail.Tail.SeqNum != 2 {
		t.Errorf("Expected seq_num=2 after 2 appends, got %d", tail.Tail.SeqNum)
	}
	t.Logf("Tail after appends: seq_num=%d, timestamp=%d", tail.Tail.SeqNum, tail.Tail.Timestamp)
}

func TestCheckTail_NonExistentStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Check tail on non-existent stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	stream := basin.Stream("nonexistent-stream-12345")

	_, err := stream.CheckTail(ctx)
	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Append Tests ---

func TestAppend_SingleRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append single record")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-app1")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte("hello world")},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if ack.Start.SeqNum != 0 {
		t.Errorf("Expected start seq_num=0, got %d", ack.Start.SeqNum)
	}
	if ack.End.SeqNum != 1 {
		t.Errorf("Expected end seq_num=1, got %d", ack.End.SeqNum)
	}
	t.Logf("Appended: start=%d, end=%d", ack.Start.SeqNum, ack.End.SeqNum)
}

func TestAppend_MultipleRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append multiple records")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-appm")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	records := make([]s2.AppendRecord, 10)
	for i := range records {
		records[i] = s2.AppendRecord{Body: []byte(fmt.Sprintf("record-%d", i))}
	}

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: records,
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if ack.End.SeqNum-ack.Start.SeqNum != 10 {
		t.Errorf("Expected 10 records appended, got %d", ack.End.SeqNum-ack.Start.SeqNum)
	}
	t.Logf("Appended %d records: start=%d, end=%d", ack.End.SeqNum-ack.Start.SeqNum, ack.Start.SeqNum, ack.End.SeqNum)
}

func TestAppend_WithHeaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append record with headers")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-apph")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{
					{Name: []byte("content-type"), Value: []byte("application/json")},
					{Name: []byte("x-custom"), Value: []byte("value")},
				},
				Body: []byte(`{"key": "value"}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	t.Logf("Appended record with headers: seq_num=%d", ack.Start.SeqNum)
}

func TestAppend_EmptyBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append record with empty body")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-appe")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{Body: []byte{}},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	t.Logf("Appended empty body record: seq_num=%d", ack.Start.SeqNum)
}

func TestAppend_WithMatchSeqNum_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with match_seq_num (success)")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-amsn")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records:     []s2.AppendRecord{{Body: []byte("first")}},
		MatchSeqNum: ptrU64(0),
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	t.Logf("Conditional append succeeded: seq_num=%d", ack.Start.SeqNum)
}

func TestAppend_WithMatchSeqNum_Failure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with match_seq_num (failure)")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-amsnf")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("first")}},
	})
	if err != nil {
		t.Fatalf("First append failed: %v", err)
	}

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records:     []s2.AppendRecord{{Body: []byte("second")}},
		MatchSeqNum: ptrU64(0),
	})

	var seqErr *s2.SeqNumMismatchError
	if !errors.As(err, &seqErr) {
		t.Errorf("Expected SeqNumMismatchError, got: %v", err)
	}
	t.Logf("Got expected seq_num mismatch error: %v", err)
}

func TestAppend_ToNonExistentStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append to non-existent stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	stream := basin.Stream("nonexistent-stream-12345")

	_, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("test")}},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Read Tests ---

func TestRead_FromBeginning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read from beginning")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rdb")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := 0; i < 5; i++ {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) != 5 {
		t.Errorf("Expected 5 records, got %d", len(batch.Records))
	}
	t.Logf("Read %d records from beginning", len(batch.Records))
}

func TestRead_WithCountLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with count limit")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rdcl")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := 0; i < 10; i++ {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
		Count:  ptrU64(3),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) != 3 {
		t.Errorf("Expected 3 records, got %d", len(batch.Records))
	}
	t.Logf("Read %d records with count limit", len(batch.Records))
}

func TestRead_EmptyStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read empty stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rdes")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) != 0 {
		t.Errorf("Expected 0 records, got %d", len(batch.Records))
	}
	t.Log("Read empty stream successfully")
}

func TestRead_NonExistentStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read non-existent stream")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	stream := basin.Stream("nonexistent-stream-12345")

	_, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestRead_WithClampTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with clamp=true")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rdct")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("test")}},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(999999),
		Clamp:  ptrBool(true),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	t.Logf("Read with clamp=true returned %d records", len(batch.Records))
}

func TestRead_VerifyRecordContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read and verify record content")

	client := streamTestClient(t)
	basinName := createTestBasin(ctx, t, client)
	defer deleteTestBasin(ctx, client, basinName)

	basin := client.Basin(string(basinName))
	streamName := uniqueStreamName("test-rdvc")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	expectedBody := "test message content"
	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{
					{Name: []byte("key"), Value: []byte("value")},
				},
				Body: []byte(expectedBody),
			},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(batch.Records))
	}

	rec := batch.Records[0]
	if string(rec.Body) != expectedBody {
		t.Errorf("Expected body %q, got %q", expectedBody, string(rec.Body))
	}
	if len(rec.Headers) != 1 {
		t.Errorf("Expected 1 header, got %d", len(rec.Headers))
	}
	if string(rec.Headers[0].Name) != "key" || string(rec.Headers[0].Value) != "value" {
		t.Error("Header mismatch")
	}
	t.Logf("Verified record content: seq_num=%d, body=%q", rec.SeqNum, string(rec.Body))
}
