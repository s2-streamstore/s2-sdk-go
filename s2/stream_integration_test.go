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

var (
	sharedTestClient    *s2.Client
	sharedTestBasinName s2.BasinName
	sharedTestBasin     *s2.BasinClient
)

func TestMain(m *testing.M) {
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		os.Exit(0)
	}

	sharedTestClient = s2.NewFromEnvironment(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	sharedTestBasinName = s2.BasinName(fmt.Sprintf("test-strm-%d", time.Now().UnixNano()))
	_, err := sharedTestClient.Basins.Create(ctx, s2.CreateBasinArgs{Basin: sharedTestBasinName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create shared test basin: %v\n", err)
		cancel()
		os.Exit(1)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "Timeout waiting for shared basin to become active\n")
			cancel()
			os.Exit(1)
		default:
		}
		_, err := sharedTestClient.Basins.GetConfig(ctx, sharedTestBasinName)
		if err == nil {
			break
		}
		var s2Err *s2.S2Error
		if errors.As(err, &s2Err) && s2Err.Code == "unavailable" {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		break
	}
	cancel()

	sharedTestBasin = sharedTestClient.Basin(string(sharedTestBasinName))

	code := m.Run()

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	_ = sharedTestClient.Basins.Delete(cleanupCtx, sharedTestBasinName)
	cleanupCancel()

	os.Exit(code)
}

func streamTestClient(t *testing.T) *s2.Client {
	t.Helper()
	if sharedTestClient == nil {
		t.Skip("S2_ACCESS_TOKEN not set")
	}
	return sharedTestClient
}

func getSharedBasin(t *testing.T) *s2.BasinClient {
	t.Helper()
	if sharedTestBasin == nil {
		t.Skip("S2_ACCESS_TOKEN not set")
	}
	return sharedTestBasin
}

func uniqueStreamName(prefix string) s2.StreamName {
	return s2.StreamName(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
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

func ptrI32(v int32) *int32 {
	return &v
}

func isStreamFreeTierLimitation(err error) bool {
	var s2Err *s2.S2Error
	if errors.As(err, &s2Err) && s2Err.Code == "invalid" {
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)

	for i := range 3 {
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

	basin := getSharedBasin(t)
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

func TestListStreams_InvalidStartAfterLessThanPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with start_after < prefix")

	basin := getSharedBasin(t)
	_, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Prefix:     "zzzzzzzz",
		StartAfter: "aaaaaaaa",
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestListStreams_LimitZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with limit=0 (treated as default)")

	basin := getSharedBasin(t)
	limit := 0
	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Streams) > 1000 {
		t.Errorf("Expected at most 1000 streams, got %d", len(resp.Streams))
	}
	t.Logf("Listed %d streams with limit=0 (treated as default 1000)", len(resp.Streams))
}

func TestListStreams_LimitExceeds1000(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: List streams with limit > 1000 (clamped to 1000)")

	basin := getSharedBasin(t)
	limit := 1500
	resp, err := basin.Streams.List(ctx, &s2.ListStreamsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Streams) > 1000 {
		t.Errorf("Expected at most 1000 streams (clamped), got %d", len(resp.Streams))
	}
	t.Logf("Listed %d streams with limit=1500 (clamped to 1000)", len(resp.Streams))
}

// --- Create Stream Tests ---

func TestCreateStream_Minimal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create minimal stream")

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

			basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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
	if s2Err != nil && s2Err.Code != "resource_already_exists" {
		t.Errorf("Expected error code resource_already_exists, got: %s", s2Err.Code)
	}
	t.Logf("Got expected conflict error: %v", err)
}

func TestCreateStream_InvalidRetentionAgeZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with retention_policy.age=0")

	basin := getSharedBasin(t)
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
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Get Stream Config Tests ---

func TestGetStreamConfig_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get existing stream config")

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
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

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdb")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 5 {
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

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdcl")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 10 {
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
	t.Log("Testing: Read empty stream from seq_num=0 (expects 416)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdes")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	_, err = stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 416 {
		t.Errorf("Expected 416 error (tail is 0, no records exist), got: %v", err)
	}
	t.Logf("Got expected 416 error: %v", err)
}

func TestRead_NonExistentStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read non-existent stream")

	basin := getSharedBasin(t)
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
	t.Log("Testing: Read with clamp=true beyond tail (expects 416)")

	basin := getSharedBasin(t)
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

	_, err = stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(999999),
		Clamp:  ptrBool(true),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 416 {
		t.Errorf("Expected 416 error (clamped TO tail, but no record exists there), got: %v", err)
	}
	t.Logf("Got expected 416 error: %v", err)
}

func TestRead_WithClampTrueAndWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with clamp=true and wait (long poll at tail)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdctw")
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
		Wait:   ptrI32(1),
	})
	if err != nil {
		t.Fatalf("Read with clamp+wait failed: %v", err)
	}

	t.Logf("Read with clamp=true and wait=1 succeeded, returned %d records", len(batch.Records))
}

func TestRead_EmptyStreamWithWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read empty stream with wait (long poll)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdesw")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
		Wait:   ptrI32(1),
	})
	if err != nil {
		t.Fatalf("Read with wait failed: %v", err)
	}

	t.Logf("Read empty stream with wait=1 succeeded, returned %d records (timeout with empty)", len(batch.Records))
}

func TestRead_VerifyRecordContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read and verify record content")

	basin := getSharedBasin(t)
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

func TestCreateStream_NameEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with empty name")

	basin := getSharedBasin(t)
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: ""})

	if err == nil {
		t.Error("Expected error for empty stream name, got nil")
	}
	t.Logf("Got expected error (SDK validation or server): %v", err)
}

func TestCreateStream_NameTooLong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with name too long (>512 bytes)")

	basin := getSharedBasin(t)
	longName := s2.StreamName(strings.Repeat("a", 513))
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: longName})

	if err == nil {
		t.Error("Expected error for stream name too long, got nil")
	}
	t.Logf("Got expected error (SDK validation or server): %v", err)
}

func TestCreateStream_NameWithUnicode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Create stream with unicode name")

	basin := getSharedBasin(t)
	streamName := s2.StreamName(fmt.Sprintf("test-unicode-日本語-%d", time.Now().UnixNano()))
	defer deleteStream(ctx, basin, streamName)

	info, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if info.Name != streamName {
		t.Errorf("Expected name %s, got %s", streamName, info.Name)
	}
	t.Logf("Created stream with unicode name: %s", info.Name)
}

func TestCreateStream_TimestampingUncapped(t *testing.T) {
	testCases := []struct {
		name     string
		uncapped bool
	}{
		{"uncapped=true", true},
		{"uncapped=false", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Create stream with timestamping.uncapped=%v", tc.uncapped)

			basin := getSharedBasin(t)
			streamName := uniqueStreamName("test-tsu")
			defer deleteStream(ctx, basin, streamName)

			_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
				Stream: streamName,
				Config: &s2.StreamConfig{
					Timestamping: &s2.TimestampingConfig{
						Uncapped: ptrBool(tc.uncapped),
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

			if config.Timestamping != nil && config.Timestamping.Uncapped != nil {
				if *config.Timestamping.Uncapped != tc.uncapped {
					t.Errorf("Expected uncapped=%v, got %v", tc.uncapped, *config.Timestamping.Uncapped)
				}
			}
			t.Logf("Verified timestamping.uncapped=%v", tc.uncapped)
		})
	}
}

func TestAppend_WithTimestamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with client timestamp")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-apts")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	clientTimestamp := uint64(time.Now().UnixMilli())
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Timestamp: &clientTimestamp,
				Body:      []byte("record with timestamp"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	t.Logf("Appended with timestamp: seq_num=%d, start_timestamp=%d", ack.Start.SeqNum, ack.Start.Timestamp)
}

func TestAppend_WithoutTimestamp_ClientRequire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append without timestamp on client-require stream (expects 400)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-awtcr")
	defer deleteStream(ctx, basin, streamName)

	mode := s2.TimestampingModeClientRequire
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

	stream := basin.Stream(streamName)
	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("no timestamp")}},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error for missing timestamp on client-require, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestAppend_FutureTimestamp_Uncapped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with future timestamp (uncapped=true preserves it)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aftu")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			Timestamping: &s2.TimestampingConfig{
				Uncapped: ptrBool(true),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	futureTimestamp := uint64(time.Now().Add(1 * time.Hour).UnixMilli())
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Timestamp: &futureTimestamp,
				Body:      []byte("future timestamp"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if ack.Start.Timestamp != futureTimestamp {
		t.Logf("Future timestamp may have been adjusted: sent=%d, got=%d", futureTimestamp, ack.Start.Timestamp)
	}
	t.Logf("Appended with future timestamp: seq_num=%d, timestamp=%d", ack.Start.SeqNum, ack.Start.Timestamp)
}

func TestAppend_FutureTimestamp_Capped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with future timestamp (uncapped=false caps it to arrival time)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aftc")
	defer deleteStream(ctx, basin, streamName)

	// Default uncapped=false
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			Timestamping: &s2.TimestampingConfig{
				Uncapped: ptrBool(false),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	futureTimestamp := uint64(time.Now().Add(1 * time.Hour).UnixMilli())
	arrivalTime := uint64(time.Now().UnixMilli())
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Timestamp: &futureTimestamp,
				Body:      []byte("future timestamp capped"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// With uncapped=false, the timestamp should be capped to approximately arrival time
	if ack.Start.Timestamp >= futureTimestamp {
		t.Errorf("Future timestamp should be capped: sent=%d, got=%d (should be closer to %d)", futureTimestamp, ack.Start.Timestamp, arrivalTime)
	}
	t.Logf("Timestamp capped: sent=%d, got=%d (arrival ~%d)", futureTimestamp, ack.Start.Timestamp, arrivalTime)
}

func TestAppend_PastTimestamp_Monotonicity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with past timestamp (adjusted up for monotonicity)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aptm")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	firstTs := uint64(time.Now().UnixMilli())
	ack1, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Timestamp: &firstTs,
				Body:      []byte("first"),
			},
		},
	})
	if err != nil {
		t.Fatalf("First append failed: %v", err)
	}

	pastTs := uint64(time.Now().Add(-1 * time.Hour).UnixMilli())
	ack2, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Timestamp: &pastTs,
				Body:      []byte("past timestamp"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Second append failed: %v", err)
	}

	if ack2.Start.Timestamp < ack1.End.Timestamp {
		t.Errorf("Monotonicity violated: second timestamp %d < first end %d", ack2.Start.Timestamp, ack1.End.Timestamp)
	}
	t.Logf("Timestamps maintained monotonicity: first_end=%d, second_start=%d", ack1.End.Timestamp, ack2.Start.Timestamp)
}

func TestAppend_FencingTokenTooLong(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with fencing_token too long (>36 bytes)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-afttl")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	longToken := strings.Repeat("a", 37)
	_, err = stream.Append(ctx, &s2.AppendInput{
		Records:      []s2.AppendRecord{{Body: []byte("test")}},
		FencingToken: &longToken,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error for fencing token too long, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestAppend_WithFencingToken_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with correct fencing token")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-afts")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte("my-token"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Set fencing token failed: %v", err)
	}

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records:      []s2.AppendRecord{{Body: []byte("test")}},
		FencingToken: ptrStr("my-token"),
	})
	if err != nil {
		t.Fatalf("Append with fencing token failed: %v", err)
	}

	t.Logf("Append with correct fencing token succeeded: seq_num=%d", ack.Start.SeqNum)
}

func TestAppend_WithFencingToken_Failure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append with wrong fencing token")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aftf")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte("my-token"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Set fencing token failed: %v", err)
	}

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records:      []s2.AppendRecord{{Body: []byte("test")}},
		FencingToken: ptrStr("wrong-token"),
	})

	var fenceErr *s2.FencingTokenMismatchError
	if !errors.As(err, &fenceErr) {
		t.Errorf("Expected FencingTokenMismatchError, got: %v", err)
	}
	t.Logf("Got expected fencing token mismatch error: %v", err)
}

func TestAppend_MaxBatchSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append max batch size (1000 records)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-ambs")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	records := make([]s2.AppendRecord, 1000)
	for i := range records {
		records[i] = s2.AppendRecord{Body: []byte(fmt.Sprintf("r%d", i))}
	}

	ack, err := stream.Append(ctx, &s2.AppendInput{Records: records})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if ack.End.SeqNum-ack.Start.SeqNum != 1000 {
		t.Errorf("Expected 1000 records appended, got %d", ack.End.SeqNum-ack.Start.SeqNum)
	}
	t.Logf("Appended 1000 records: start=%d, end=%d", ack.Start.SeqNum, ack.End.SeqNum)
}

func TestAppend_TooManyRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append too many records (>1000)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-atmr")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	records := make([]s2.AppendRecord, 1001)
	for i := range records {
		records[i] = s2.AppendRecord{Body: []byte("x")}
	}

	_, err = stream.Append(ctx, &s2.AppendInput{Records: records})

	if err == nil {
		t.Error("Expected error for >1000 records, got nil")
	}
	t.Logf("Got expected error (SDK validation or server): %v", err)
}

func TestAppend_EmptyBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append empty batch (0 records)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aeb")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	_, err = stream.Append(ctx, &s2.AppendInput{Records: []s2.AppendRecord{}})

	if err == nil {
		t.Error("Expected error for empty batch, got nil")
	}
	t.Logf("Got expected error (SDK validation or server): %v", err)
}

func TestAppend_HeaderWithEmptyName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append record with header having empty name (command record)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-ahen")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte("token"),
			},
		},
	})

	if err != nil {
		t.Fatalf("Append with empty header name failed unexpectedly: %v", err)
	}
	t.Logf("Empty header name is valid for command records: seq_num=%d", ack.Start.SeqNum)
}

func TestAppend_HeaderWithEmptyName_NonCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append record with empty header name but non-command value (expect 400)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-ahennc")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("not-a-command")}},
				Body:    []byte("test"),
			},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error for empty header name with non-command value, got: %v", err)
	}
	t.Logf("Got expected 400 error: %v", err)
}

func TestRead_FromTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read from tail (tail_offset=0, expects 416)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rft")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 5 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	_, err = stream.Read(ctx, &s2.ReadOptions{
		TailOffset: ptrI64(0),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 416 {
		t.Errorf("Expected 416 error (position at tail with no records), got: %v", err)
	}
	t.Logf("Got expected 416 error: %v", err)
}

func TestRead_WithTailOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with offset from tail (tail_offset=3)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rwto")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 10 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		TailOffset: ptrI64(3),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) != 3 {
		t.Errorf("Expected 3 records, got %d", len(batch.Records))
	}
	t.Logf("Read with tail_offset=3 returned %d records", len(batch.Records))
}

func TestRead_ByTimestamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read by timestamp")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdts")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("first")}},
	})
	if err != nil {
		t.Fatalf("First append failed: %v", err)
	}
	firstTimestamp := ack.Start.Timestamp

	time.Sleep(10 * time.Millisecond)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("second")}},
	})
	if err != nil {
		t.Fatalf("Second append failed: %v", err)
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		Timestamp: ptrU64(firstTimestamp + 1),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if len(batch.Records) == 0 {
		t.Log("No records found after timestamp (timing dependent)")
	} else {
		t.Logf("Read by timestamp returned %d records", len(batch.Records))
	}
}

func TestRead_WithBytesLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with bytes limit")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdbl")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 10 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(strings.Repeat("x", 100))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
		Bytes:  ptrU64(250),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	t.Logf("Read with bytes=250 returned %d records", len(batch.Records))
}

func TestRead_UntilTimestamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read until timestamp")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdut")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("first")}},
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	batch, err := stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(0),
		Until:  ptrU64(ack.End.Timestamp + 1),
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	t.Logf("Read until timestamp returned %d records", len(batch.Records))
}

func TestRead_ClampFalse_BeyondTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with clamp=false beyond tail (expect 416)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdcf")
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

	_, err = stream.Read(ctx, &s2.ReadOptions{
		SeqNum: ptrU64(999999),
		Clamp:  ptrBool(false),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 416 {
		t.Errorf("Expected 416 error, got: %v", err)
	}
	t.Logf("Got expected 416 error: %v", err)
}

func TestRead_MultipleStartParams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Read with multiple start params (mutually exclusive)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rdmsp")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	// Try to read with both seq_num and timestamp - should be an error
	_, err = stream.Read(ctx, &s2.ReadOptions{
		SeqNum:    ptrU64(0),
		Timestamp: ptrU64(0),
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error for mutually exclusive start params, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestFence_SetToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Set fencing token via command record")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-fst")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte("test-fence-token"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Set fencing token failed: %v", err)
	}

	t.Logf("Fencing token set: seq_num=%d", ack.Start.SeqNum)
}

func TestFence_ClearToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Clear fencing token")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-fct")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte("my-token"),
			},
		},
	})
	if err != nil {
		t.Fatalf("Set fencing token failed: %v", err)
	}

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("fence")}},
				Body:    []byte(""),
			},
		},
	})
	if err != nil {
		t.Fatalf("Clear fencing token failed: %v", err)
	}

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("test")}},
	})
	if err != nil {
		t.Fatalf("Append after clearing token failed: %v", err)
	}

	t.Logf("Fencing token cleared, append succeeded: seq_num=%d", ack.Start.SeqNum)
}

func TestTrim_Stream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Trim stream via command record")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-trim")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 5 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	trimSeqNum := uint64(3)
	trimBody := make([]byte, 8)
	for i := range 8 {
		trimBody[7-i] = byte(trimSeqNum >> (8 * i))
	}

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("trim")}},
				Body:    trimBody,
			},
		},
	})
	if err != nil {
		t.Fatalf("Trim command failed: %v", err)
	}

	t.Log("Trim command succeeded")
}

func TestTrim_CommandAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Trim command accepted (eventually consistent)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-trat")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 5 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	trimSeqNum := uint64(3)
	trimBody := make([]byte, 8)
	for i := range 8 {
		trimBody[7-i] = byte(trimSeqNum >> (8 * i))
	}

	ack, err := stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("trim")}},
				Body:    trimBody,
			},
		},
	})
	if err != nil {
		t.Fatalf("Trim command failed: %v", err)
	}

	t.Logf("Trim command accepted: seq_num=%d (note: actual deletion is eventually consistent)", ack.Start.SeqNum)
}

func TestTrim_ToFutureSeqNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Trim to future seq_num (no-op)")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-trf")
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

	trimSeqNum := uint64(999999)
	trimBody := make([]byte, 8)
	for i := range 8 {
		trimBody[7-i] = byte(trimSeqNum >> (8 * i))
	}

	_, err = stream.Append(ctx, &s2.AppendInput{
		Records: []s2.AppendRecord{
			{
				Headers: []s2.Header{{Name: []byte(""), Value: []byte("trim")}},
				Body:    trimBody,
			},
		},
	})
	if err != nil {
		t.Fatalf("Trim to future failed: %v", err)
	}

	t.Log("Trim to future seq_num succeeded (no-op)")
}

func TestReconfigureStream_ChangeTimestampingMode(t *testing.T) {
	modes := []s2.TimestampingMode{
		s2.TimestampingModeClientPrefer,
		s2.TimestampingModeClientRequire,
		s2.TimestampingModeArrival,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Reconfigure stream timestamping.mode=%s", mode)

			basin := getSharedBasin(t)
			streamName := uniqueStreamName("test-rctm")
			defer deleteStream(ctx, basin, streamName)

			_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			newMode := mode
			config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
				Stream: streamName,
				Config: s2.StreamReconfiguration{
					Timestamping: &s2.TimestampingReconfiguration{
						Mode: &newMode,
					},
				},
			})
			if err != nil {
				t.Fatalf("Reconfigure failed: %v", err)
			}

			if config.Timestamping != nil && config.Timestamping.Mode != nil {
				if *config.Timestamping.Mode != mode {
					t.Errorf("Expected mode=%s, got %s", mode, *config.Timestamping.Mode)
				}
			}
			t.Logf("Verified timestamping.mode=%s", mode)
		})
	}
}

func TestReconfigureStream_ChangeTimestampingUncapped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream timestamping.uncapped")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rctu")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			Timestamping: &s2.TimestampingReconfiguration{
				Uncapped: ptrBool(true),
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.Timestamping != nil && config.Timestamping.Uncapped != nil {
		if !*config.Timestamping.Uncapped {
			t.Error("Expected uncapped=true")
		}
	}
	t.Log("Verified timestamping.uncapped=true")
}

func TestReconfigureStream_ChangeDeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure stream delete_on_empty.min_age_secs")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rcdoe")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	config, err := basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			DeleteOnEmpty: &s2.DeleteOnEmptyReconfiguration{
				MinAgeSecs: ptrI64(3600),
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.DeleteOnEmpty == nil || config.DeleteOnEmpty.MinAgeSecs == nil {
		t.Fatal("Expected delete_on_empty.min_age_secs")
	}
	if *config.DeleteOnEmpty.MinAgeSecs != 3600 {
		t.Errorf("Expected min_age_secs=3600, got %d", *config.DeleteOnEmpty.MinAgeSecs)
	}
	t.Log("Verified delete_on_empty.min_age_secs=3600")
}

func TestReconfigureStream_DisableDeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Disable delete_on_empty")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rddoe")
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

	_, err = basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			DeleteOnEmpty: &s2.DeleteOnEmptyReconfiguration{
				MinAgeSecs: ptrI64(0),
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	t.Log("Disabled delete_on_empty (min_age_secs=0)")
}

func TestReconfigureStream_PartialConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Partial reconfiguration")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rcpc")
	defer deleteStream(ctx, basin, streamName)

	storageClass := s2.StorageClassStandard
	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{
		Stream: streamName,
		Config: &s2.StreamConfig{
			StorageClass: &storageClass,
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
				Age: ptrI64(3600),
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.StorageClass == nil || *config.StorageClass != s2.StorageClassStandard {
		t.Error("storage_class should remain standard")
	}
	if config.RetentionPolicy == nil || config.RetentionPolicy.Age == nil || *config.RetentionPolicy.Age != 3600 {
		t.Error("retention_policy.age should be 3600")
	}
	t.Log("Verified partial reconfiguration: only retention changed")
}

func TestReconfigureStream_InvalidRetentionAgeZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure with invalid retention_policy.age=0")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rcira")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = basin.Streams.Reconfigure(ctx, s2.ReconfigureStreamArgs{
		Stream: streamName,
		Config: s2.StreamReconfiguration{
			RetentionPolicy: &s2.RetentionPolicy{
				Age: ptrI64(0),
			},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 400 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestClient_Initialization(t *testing.T) {
	t.Log("Testing: Client initialization")

	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set")
	}

	client := s2.NewFromEnvironment(nil)

	if client == nil {
		t.Fatal("Client should not be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()

	_, err := client.Basins.List(ctx, nil)
	if err != nil {
		t.Fatalf("First operation failed: %v", err)
	}

	t.Log("Client initialization successful")
}

func TestClient_InvalidToken(t *testing.T) {
	t.Log("Testing: Client with invalid token")

	client := s2.New("invalid-token", nil)

	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()

	_, err := client.Basins.List(ctx, nil)

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || (s2Err.Status != 401 && s2Err.Status != 403) {
		t.Errorf("Expected 401 or 403 error, got: %v", err)
	}
	t.Logf("Got expected auth error: %v", err)
}

func TestClient_MultipleInstances(t *testing.T) {
	t.Log("Testing: Multiple concurrent clients")

	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set")
	}

	client1 := s2.NewFromEnvironment(nil)
	client2 := s2.NewFromEnvironment(nil)

	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()

	_, err1 := client1.Basins.List(ctx, nil)
	_, err2 := client2.Basins.List(ctx, nil)

	if err1 != nil {
		t.Errorf("Client 1 failed: %v", err1)
	}
	if err2 != nil {
		t.Errorf("Client 2 failed: %v", err2)
	}

	t.Log("Multiple clients operate independently")
}

func TestClient_Reuse(t *testing.T) {
	t.Log("Testing: Client reuse for multiple operations")

	client := streamTestClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()

	for i := range 3 {
		_, err := client.Basins.List(ctx, nil)
		if err != nil {
			t.Fatalf("Operation %d failed: %v", i, err)
		}
	}

	t.Log("Client reused successfully for multiple operations")
}

func TestAppendSession_Open(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Open append session")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-aso")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("AppendSession failed: %v", err)
	}
	defer session.Close()

	t.Log("Append session opened successfully")
}

func TestAppendSession_MultipleBatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Append multiple batches via session")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-asmb")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("AppendSession failed: %v", err)
	}
	defer session.Close()

	for i := range 3 {
		future, err := session.Submit(&s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("batch-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Submit %d failed: %v", i, err)
		}

		ticket, err := future.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait %d failed: %v", i, err)
		}

		ack, err := ticket.Ack(ctx)
		if err != nil {
			t.Fatalf("Ack %d failed: %v", i, err)
		}
		t.Logf("Batch %d acked: seq_num=%d", i, ack.Start.SeqNum)
	}

	t.Log("Multiple batches submitted via session")
}

func TestAppendSession_Close(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Close append session")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-asc")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)
	session, err := stream.AppendSession(ctx, nil)
	if err != nil {
		t.Fatalf("AppendSession failed: %v", err)
	}

	future, err := session.Submit(&s2.AppendInput{
		Records: []s2.AppendRecord{{Body: []byte("test")}},
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	ticket, err := future.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	_, err = ticket.Ack(ctx)
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}

	err = session.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	t.Log("Append session closed successfully")
}

func TestReadSession_Open(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Open read session")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rso")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 5 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	session, err := stream.ReadSession(ctx, &s2.ReadOptions{SeqNum: ptrU64(0)})
	if err != nil {
		t.Fatalf("ReadSession failed: %v", err)
	}
	defer session.Close()

	count := 0
	for session.Next() {
		_ = session.Record()
		count++
		if count >= 5 {
			break
		}
	}

	if err := session.Err(); err != nil {
		t.Fatalf("Session error: %v", err)
	}

	t.Logf("Read session read %d records", count)
}

func TestReadSession_Cancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Cancel read session")

	basin := getSharedBasin(t)
	streamName := uniqueStreamName("test-rsc")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	stream := basin.Stream(streamName)

	for i := range 10 {
		_, err := stream.Append(ctx, &s2.AppendInput{
			Records: []s2.AppendRecord{{Body: []byte(fmt.Sprintf("record-%d", i))}},
		})
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	session, err := stream.ReadSession(ctx, &s2.ReadOptions{SeqNum: ptrU64(0)})
	if err != nil {
		t.Fatalf("ReadSession failed: %v", err)
	}

	if session.Next() {
		_ = session.Record()
	}

	err = session.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	t.Log("Read session cancelled successfully")
}
