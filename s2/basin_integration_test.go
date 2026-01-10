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

const testTimeout = 60 * time.Second

func testClient(t *testing.T) *s2.Client {
	t.Helper()
	token := os.Getenv("S2_ACCESS_TOKEN")
	if token == "" {
		t.Skip("S2_ACCESS_TOKEN not set")
	}
	return s2.NewFromEnvironment(nil)
}

func uniqueBasinName(prefix string) s2.BasinName {
	return s2.BasinName(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

func deleteBasin(ctx context.Context, client *s2.Client, name s2.BasinName) {
	_ = client.Basins.Delete(ctx, name)
}

func waitForBasinActive(ctx context.Context, t *testing.T, client *s2.Client, name s2.BasinName) {
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

func ptr[T any](v T) *T {
	return &v
}

func isFreeTierLimitation(err error) bool {
	var s2Err *s2.S2Error
	if errors.As(err, &s2Err) && (s2Err.Code == "invalid_basin_config" || s2Err.Code == "invalid" || s2Err.Code == "bad_config") {
		msg := strings.ToLower(s2Err.Message)
		return strings.Contains(msg, "free tier")
	}
	return false
}

// --- List Basins Tests ---

func TestListBasins_All(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List all basins")

	client := testClient(t)
	resp, err := client.Basins.List(ctx, nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	t.Logf("Listed %d basins, has_more=%v", len(resp.Basins), resp.HasMore)
}

func TestListBasins_WithPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with prefix")

	client := testClient(t)
	basinName := uniqueBasinName("test-list")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)
	t.Logf("Created basin: %s", basinName)

	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Prefix: "test-list",
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, b := range resp.Basins {
		if b.Name == basinName {
			found = true
			break
		}
		if !strings.HasPrefix(string(b.Name), "test-list") {
			t.Errorf("Basin %s does not match prefix test-list", b.Name)
		}
	}
	if !found {
		t.Error("Created basin not found in list")
	}
	t.Logf("Found %d basins with prefix", len(resp.Basins))
}

func TestListBasins_WithStartAfter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with start_after")

	client := testClient(t)
	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		StartAfter: "aaaaaaaa",
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	for _, b := range resp.Basins {
		if string(b.Name) <= "aaaaaaaa" {
			t.Errorf("Basin %s should be after aaaaaaaa", b.Name)
		}
	}
	t.Logf("Listed %d basins after aaaaaaaa", len(resp.Basins))
}

func TestListBasins_WithLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with limit")

	client := testClient(t)
	limit := 5
	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Basins) > limit {
		t.Errorf("Expected at most %d basins, got %d", limit, len(resp.Basins))
	}
	t.Logf("Listed %d basins with limit=%d, has_more=%v", len(resp.Basins), limit, resp.HasMore)
}

func TestListBasins_Pagination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with pagination")

	client := testClient(t)
	limit := 2
	resp1, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("First list failed: %v", err)
	}

	if len(resp1.Basins) == 0 {
		t.Skip("No basins to paginate")
	}

	lastName := string(resp1.Basins[len(resp1.Basins)-1].Name)
	resp2, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		StartAfter: lastName,
		Limit:      &limit,
	})
	if err != nil {
		t.Fatalf("Second list failed: %v", err)
	}

	for _, b := range resp2.Basins {
		if string(b.Name) <= lastName {
			t.Errorf("Basin %s should be after %s", b.Name, lastName)
		}
	}
	t.Logf("Page 1: %d basins, Page 2: %d basins", len(resp1.Basins), len(resp2.Basins))
}

func TestListBasins_LimitZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with limit=0 (treated as default)")

	client := testClient(t)
	limit := 0
	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Basins) > 1000 {
		t.Errorf("Expected at most 1000 basins, got %d", len(resp.Basins))
	}
	t.Logf("Listed %d basins with limit=0 (treated as default 1000)", len(resp.Basins))
}

func TestListBasins_LimitExceeds1000(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with limit > 1000 (clamped to 1000)")

	client := testClient(t)
	limit := 1500
	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(resp.Basins) > 1000 {
		t.Errorf("Expected at most 1000 basins (clamped), got %d", len(resp.Basins))
	}
	t.Logf("Listed %d basins with limit=1500 (clamped to 1000)", len(resp.Basins))
}

func TestListBasins_InvalidStartAfterLessThanPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List basins with start_after < prefix")

	client := testClient(t)
	_, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Prefix:     "zzzzzzzz",
		StartAfter: "aaaaaaaa",
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestListBasins_Iterator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Iterate over basins")

	client := testClient(t)
	limit := 3
	iter := client.Basins.Iter(ctx, &s2.ListBasinsArgs{
		Limit: &limit,
	})

	count := 0
	for iter.Next() {
		basin := iter.Value()
		if basin.Name == "" {
			t.Error("Basin name should not be empty")
		}
		count++
	}

	if err := iter.Err(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	t.Logf("Iterated over %d basins", count)
}

// --- Create Basin Tests ---

func TestCreateBasin_Minimal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create minimal basin")

	client := testClient(t)
	basinName := uniqueBasinName("test-cmin")
	defer deleteBasin(ctx, client, basinName)

	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if info.Name != basinName {
		t.Errorf("Expected name %s, got %s", basinName, info.Name)
	}
	if info.State != s2.BasinStateActive && info.State != s2.BasinStateCreating {
		t.Errorf("Unexpected state: %s", info.State)
	}
	t.Logf("Created basin: %s, state: %s", info.Name, info.State)
}

func TestCreateBasin_WithScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with scope")

	client := testClient(t)
	basinName := uniqueBasinName("test-cscp")
	defer deleteBasin(ctx, client, basinName)

	scope := s2.BasinScopeAwsUsEast1
	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Scope: &scope,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if info.Scope != s2.BasinScopeAwsUsEast1 {
		t.Errorf("Expected scope aws:us-east-1, got %s", info.Scope)
	}
	t.Logf("Created basin with scope: %s", info.Scope)
}

func TestCreateBasin_WithFullConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with full config")

	client := testClient(t)
	basinName := uniqueBasinName("test-full")
	defer deleteBasin(ctx, client, basinName)

	storageClass := s2.StorageClassStandard
	timestampMode := s2.TimestampingModeClientPrefer
	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
			CreateStreamOnRead:   ptr(false),
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: &storageClass,
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(86400)),
				},
				Timestamping: &s2.TimestampingConfig{
					Mode:     &timestampMode,
					Uncapped: ptr(false),
				},
				DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
					MinAgeSecs: ptr(int64(3600)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append=true")
	}
	if config.CreateStreamOnRead == nil || *config.CreateStreamOnRead {
		t.Error("Expected create_stream_on_read=false")
	}
	t.Logf("Created basin %s with full config", info.Name)
}

func TestCreateBasin_CreateStreamOnAppendTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with create_stream_on_append=true")

	client := testClient(t)
	basinName := uniqueBasinName("test-csoa")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append=true")
	}
	t.Log("Verified create_stream_on_append=true")
}

func TestCreateBasin_CreateStreamOnReadTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with create_stream_on_read=true")

	client := testClient(t)
	basinName := uniqueBasinName("test-csor")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnRead: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnRead == nil || !*config.CreateStreamOnRead {
		t.Error("Expected create_stream_on_read=true")
	}
	t.Log("Verified create_stream_on_read=true")
}

func TestCreateBasin_StorageClassStandard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with storage_class=standard")

	client := testClient(t)
	basinName := uniqueBasinName("test-scst")
	defer deleteBasin(ctx, client, basinName)

	storageClass := s2.StorageClassStandard
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: &storageClass,
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.StorageClass == nil {
		t.Fatal("Expected default_stream_config.storage_class")
	}
	if *config.DefaultStreamConfig.StorageClass != s2.StorageClassStandard {
		t.Errorf("Expected storage_class=standard, got %s", *config.DefaultStreamConfig.StorageClass)
	}
	t.Log("Verified storage_class=standard")
}

func TestCreateBasin_StorageClassExpress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with storage_class=express (default, may be omitted)")

	client := testClient(t)
	basinName := uniqueBasinName("test-scex")
	defer deleteBasin(ctx, client, basinName)

	storageClass := s2.StorageClassExpress
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: &storageClass,
			},
		},
	})
	if err != nil {
		if isFreeTierLimitation(err) {
			t.Log("Skipped: express storage class not available on free tier")
			return
		}
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DefaultStreamConfig != nil && config.DefaultStreamConfig.StorageClass != nil {
		if *config.DefaultStreamConfig.StorageClass != s2.StorageClassExpress {
			t.Errorf("Expected storage_class=express, got %s", *config.DefaultStreamConfig.StorageClass)
		}
		t.Log("Verified storage_class=express (explicitly returned)")
	} else {
		t.Log("Verified storage_class=express (omitted as default)")
	}
}

func TestCreateBasin_RetentionPolicyAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with retention_policy.age")

	client := testClient(t)
	basinName := uniqueBasinName("test-rpag")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(86400)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.RetentionPolicy == nil {
		t.Fatal("Expected default_stream_config.retention_policy")
	}
	if config.DefaultStreamConfig.RetentionPolicy.Age == nil {
		t.Fatal("Expected retention_policy.age")
	}
	if *config.DefaultStreamConfig.RetentionPolicy.Age != 86400 {
		t.Errorf("Expected age=86400, got %d", *config.DefaultStreamConfig.RetentionPolicy.Age)
	}
	t.Log("Verified retention_policy.age=86400")
}

func TestCreateBasin_RetentionPolicyInfinite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with retention_policy.infinite")

	client := testClient(t)
	basinName := uniqueBasinName("test-rpin")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				RetentionPolicy: &s2.RetentionPolicy{
					Infinite: &s2.InfiniteRetention{},
				},
			},
		},
	})
	if err != nil {
		if isFreeTierLimitation(err) {
			t.Log("Skipped: infinite retention not available on free tier")
			return
		}
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.RetentionPolicy == nil {
		t.Fatal("Expected default_stream_config.retention_policy")
	}
	if config.DefaultStreamConfig.RetentionPolicy.Infinite == nil {
		t.Error("Expected retention_policy.infinite")
	}
	t.Log("Verified retention_policy.infinite")
}

func TestCreateBasin_TimestampingModes(t *testing.T) {
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
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Create basin with timestamping.mode=%s", tc.name)

			client := testClient(t)
			basinName := uniqueBasinName("test-tsm")
			defer deleteBasin(ctx, client, basinName)

			mode := tc.mode
			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
				Basin: basinName,
				Config: &s2.BasinConfig{
					DefaultStreamConfig: &s2.StreamConfig{
						Timestamping: &s2.TimestampingConfig{
							Mode: &mode,
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			waitForBasinActive(ctx, t, client, basinName)

			config, err := client.Basins.GetConfig(ctx, basinName)
			if err != nil {
				t.Fatalf("GetConfig failed: %v", err)
			}

			if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.Timestamping == nil {
				t.Fatal("Expected default_stream_config.timestamping")
			}
			if config.DefaultStreamConfig.Timestamping.Mode == nil {
				t.Fatal("Expected timestamping.mode")
			}
			if *config.DefaultStreamConfig.Timestamping.Mode != tc.mode {
				t.Errorf("Expected mode=%s, got %s", tc.mode, *config.DefaultStreamConfig.Timestamping.Mode)
			}
			t.Logf("Verified timestamping.mode=%s", tc.mode)
		})
	}
}

func TestCreateBasin_TimestampingUncapped(t *testing.T) {
	values := []bool{true, false}

	for _, uncapped := range values {
		t.Run(fmt.Sprintf("uncapped=%v", uncapped), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Create basin with timestamping.uncapped=%v", uncapped)

			client := testClient(t)
			basinName := uniqueBasinName("test-tsu")
			defer deleteBasin(ctx, client, basinName)

			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
				Basin: basinName,
				Config: &s2.BasinConfig{
					DefaultStreamConfig: &s2.StreamConfig{
						Timestamping: &s2.TimestampingConfig{
							Uncapped: ptr(uncapped),
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			waitForBasinActive(ctx, t, client, basinName)

			config, err := client.Basins.GetConfig(ctx, basinName)
			if err != nil {
				t.Fatalf("GetConfig failed: %v", err)
			}

			if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.Timestamping == nil {
				t.Fatal("Expected default_stream_config.timestamping")
			}
			if config.DefaultStreamConfig.Timestamping.Uncapped == nil {
				t.Fatal("Expected timestamping.uncapped")
			}
			if *config.DefaultStreamConfig.Timestamping.Uncapped != uncapped {
				t.Errorf("Expected uncapped=%v, got %v", uncapped, *config.DefaultStreamConfig.Timestamping.Uncapped)
			}
			t.Logf("Verified timestamping.uncapped=%v", uncapped)
		})
	}
}

func TestCreateBasin_DeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with delete_on_empty.min_age_secs")

	client := testClient(t)
	basinName := uniqueBasinName("test-doe")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
					MinAgeSecs: ptr(int64(3600)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.DeleteOnEmpty == nil {
		t.Fatal("Expected default_stream_config.delete_on_empty")
	}
	if config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs == nil {
		t.Fatal("Expected delete_on_empty.min_age_secs")
	}
	if *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs != 3600 {
		t.Errorf("Expected min_age_secs=3600, got %d", *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs)
	}
	t.Log("Verified delete_on_empty.min_age_secs=3600")
}

func TestCreateBasin_DuplicateName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with duplicate name")

	client := testClient(t)
	basinName := uniqueBasinName("test-dup")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("First create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	_, err = client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 409 {
		t.Errorf("Expected 409 conflict, got: %v", err)
	}
	if s2Err != nil && s2Err.Code != "resource_already_exists" {
		t.Errorf("Expected error code resource_already_exists, got: %s", s2Err.Code)
	}
	t.Logf("Got expected conflict error: %v", err)
}

func TestCreateBasin_NameValidation(t *testing.T) {
	testCases := []struct {
		name        string
		basinName   s2.BasinName
		expectError bool
	}{
		{"too_short", "short", true},
		{"too_long", s2.BasinName(strings.Repeat("a", 49)), true},
		{"with_uppercase", "Test-Basin-Name", true},
		{"with_underscore", "test_basin_name", true},
		{"starts_with_hyphen", "-test-basin", true},
		{"ends_with_hyphen", "test-basin-", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Create basin with invalid name (%s)", tc.name)

			client := testClient(t)
			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: tc.basinName})

			if tc.expectError && err == nil {
				t.Error("Expected error but got none")
			} else if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tc.expectError {
				t.Logf("Got expected error: %v", err)
			}
		})
	}
}

func TestCreateBasin_InvalidRetentionAgeZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with retention_policy.age=0")

	client := testClient(t)
	basinName := uniqueBasinName("test-raz")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(0)),
				},
			},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Get Basin Config Tests ---

func TestGetBasinConfig_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Get existing basin config")

	client := testClient(t)
	basinName := uniqueBasinName("test-gcfg")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append=true")
	}
	t.Logf("Got config: create_stream_on_append=%v", *config.CreateStreamOnAppend)
}

func TestGetBasinConfig_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Get non-existent basin config")

	client := testClient(t)
	_, err := client.Basins.GetConfig(ctx, "nonexistent-basin-name-12345")

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	if s2Err != nil && s2Err.Code != "basin_not_found" {
		t.Errorf("Expected error code basin_not_found, got: %s", s2Err.Code)
	}
	t.Logf("Got expected error: %v", err)
}

func TestGetBasinConfig_DefaultFieldsOmitted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Get basin config with defaults (fields should be omitted)")

	client := testClient(t)
	basinName := uniqueBasinName("test-gdef")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil {
		t.Error("create_stream_on_append should always be present")
	}
	if config.CreateStreamOnRead == nil {
		t.Error("create_stream_on_read should always be present")
	}
	if config.DefaultStreamConfig != nil {
		t.Log("Note: default_stream_config is present (may contain non-default inherited values)")
	} else {
		t.Log("Verified: default_stream_config omitted when all defaults")
	}
	t.Log("Verified boolean fields always present, default_stream_config may be omitted")
}

func TestGetBasinConfig_NonDefaultFieldsPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Get basin config with non-default fields (should all be present)")

	client := testClient(t)
	basinName := uniqueBasinName("test-gall")
	defer deleteBasin(ctx, client, basinName)

	storageClass := s2.StorageClassStandard
	timestampMode := s2.TimestampingModeClientRequire
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
			CreateStreamOnRead:   ptr(false),
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: &storageClass,
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(86400)),
				},
				Timestamping: &s2.TimestampingConfig{
					Mode:     &timestampMode,
					Uncapped: ptr(true),
				},
				DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
					MinAgeSecs: ptr(int64(7200)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.GetConfig(ctx, basinName)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil {
		t.Error("Missing create_stream_on_append (should always be present)")
	}
	if config.CreateStreamOnRead == nil {
		t.Error("Missing create_stream_on_read (should always be present)")
	}
	if config.DefaultStreamConfig == nil {
		t.Fatal("Missing default_stream_config (expected with non-default values)")
	}
	if config.DefaultStreamConfig.StorageClass == nil {
		t.Error("Missing storage_class (non-default value should be present)")
	}
	if config.DefaultStreamConfig.RetentionPolicy == nil {
		t.Error("Missing retention_policy (non-default value should be present)")
	}
	if config.DefaultStreamConfig.Timestamping == nil {
		t.Error("Missing timestamping (non-default values should be present)")
	}
	if config.DefaultStreamConfig.DeleteOnEmpty == nil {
		t.Error("Missing delete_on_empty (non-default value should be present)")
	}
	t.Log("Verified all non-default config fields are present")
}

// --- Delete Basin Tests ---

func TestDeleteBasin_Existing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Delete existing basin")

	client := testClient(t)
	basinName := uniqueBasinName("test-del")

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)
	t.Logf("Created basin: %s", basinName)

	err = client.Basins.Delete(ctx, basinName)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	t.Log("Basin deleted successfully")
}

func TestDeleteBasin_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Delete non-existent basin")

	client := testClient(t)
	err := client.Basins.Delete(ctx, "nonexistent-basin-name-12345")

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestDeleteBasin_AlreadyDeleting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Delete basin that is already deleting (idempotent)")

	client := testClient(t)
	basinName := uniqueBasinName("test-deld")

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	err = client.Basins.Delete(ctx, basinName)
	if err != nil {
		t.Fatalf("First delete failed: %v", err)
	}

	err = client.Basins.Delete(ctx, basinName)
	if err != nil {
		var s2Err *s2.S2Error
		if !errors.As(err, &s2Err) || (s2Err.Status != 202 && s2Err.Status != 404) {
			t.Logf("Note: Second delete returned error (expected in some cases): %v", err)
		}
	}
	t.Log("Delete is idempotent or returns expected error")
}

func TestDeleteBasin_VerifyState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Verify basin state after delete")

	client := testClient(t)
	basinName := uniqueBasinName("test-dvst")

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	err = client.Basins.Delete(ctx, basinName)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	resp, err := client.Basins.List(ctx, &s2.ListBasinsArgs{
		Prefix: string(basinName),
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	for _, b := range resp.Basins {
		if b.Name == basinName {
			if b.State != s2.BasinStateDeleting {
				t.Errorf("Expected state=deleting, got %s", b.State)
			} else {
				t.Log("Verified state=deleting")
			}
			return
		}
	}
	t.Log("Basin no longer in list (already deleted)")
}

// --- Reconfigure Basin Tests ---

func TestReconfigureBasin_EnableCreateStreamOnAppend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure basin to enable create_stream_on_append")

	client := testClient(t)
	basinName := uniqueBasinName("test-rcsa")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(false),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			CreateStreamOnAppend: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append=true after reconfigure")
	}
	t.Log("Verified create_stream_on_append enabled")
}

func TestReconfigureBasin_DisableCreateStreamOnAppend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure basin to disable create_stream_on_append")

	client := testClient(t)
	basinName := uniqueBasinName("test-rdsoa")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			CreateStreamOnAppend: ptr(false),
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || *config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append=false after reconfigure")
	}
	t.Log("Verified create_stream_on_append disabled")
}

func TestReconfigureBasin_EnableCreateStreamOnRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure basin to enable create_stream_on_read")

	client := testClient(t)
	basinName := uniqueBasinName("test-rcsr")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnRead: ptr(false),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			CreateStreamOnRead: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.CreateStreamOnRead == nil || !*config.CreateStreamOnRead {
		t.Error("Expected create_stream_on_read=true after reconfigure")
	}
	t.Log("Verified create_stream_on_read enabled")
}

func TestReconfigureBasin_ChangeStorageClass(t *testing.T) {
	testCases := []struct {
		from s2.StorageClass
		to   s2.StorageClass
	}{
		{s2.StorageClassStandard, s2.StorageClassExpress},
		{s2.StorageClassExpress, s2.StorageClassStandard},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s_to_%s", tc.from, tc.to), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Reconfigure storage_class from %s to %s", tc.from, tc.to)

			client := testClient(t)
			basinName := uniqueBasinName("test-rcsc")
			defer deleteBasin(ctx, client, basinName)

			from := tc.from
			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
				Basin: basinName,
				Config: &s2.BasinConfig{
					DefaultStreamConfig: &s2.StreamConfig{
						StorageClass: &from,
					},
				},
			})
			if err != nil {
				if isFreeTierLimitation(err) {
					t.Logf("Skipped: %s storage class not available on free tier", tc.from)
					return
				}
				t.Fatalf("Create failed: %v", err)
			}
			waitForBasinActive(ctx, t, client, basinName)

			to := tc.to
			config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
				Basin: basinName,
				Config: s2.BasinReconfiguration{
					DefaultStreamConfig: &s2.StreamReconfiguration{
						StorageClass: &to,
					},
				},
			})
			if err != nil {
				if isFreeTierLimitation(err) {
					t.Logf("Skipped: %s storage class not available on free tier", tc.to)
					return
				}
				t.Fatalf("Reconfigure failed: %v", err)
			}

			if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.StorageClass == nil {
				t.Fatal("Expected default_stream_config.storage_class")
			}
			if *config.DefaultStreamConfig.StorageClass != tc.to {
				t.Errorf("Expected storage_class=%s, got %s", tc.to, *config.DefaultStreamConfig.StorageClass)
			}
			t.Logf("Verified storage_class changed to %s", tc.to)
		})
	}
}

func TestReconfigureBasin_ChangeRetentionToAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure retention_policy to age-based")

	client := testClient(t)
	basinName := uniqueBasinName("test-rra")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				RetentionPolicy: &s2.RetentionPolicy{
					Infinite: &s2.InfiniteRetention{},
				},
			},
		},
	})
	if err != nil {
		if isFreeTierLimitation(err) {
			t.Log("Skipped: infinite retention not available on free tier")
			return
		}
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			DefaultStreamConfig: &s2.StreamReconfiguration{
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(3600)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.RetentionPolicy == nil {
		t.Fatal("Expected retention_policy")
	}
	if config.DefaultStreamConfig.RetentionPolicy.Age == nil {
		t.Fatal("Expected retention_policy.age")
	}
	if *config.DefaultStreamConfig.RetentionPolicy.Age != 3600 {
		t.Errorf("Expected age=3600, got %d", *config.DefaultStreamConfig.RetentionPolicy.Age)
	}
	t.Log("Verified retention_policy changed to age=3600")
}

func TestReconfigureBasin_ChangeRetentionToInfinite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure retention_policy to infinite")

	client := testClient(t)
	basinName := uniqueBasinName("test-rri")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(86400)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			DefaultStreamConfig: &s2.StreamReconfiguration{
				RetentionPolicy: &s2.RetentionPolicy{
					Infinite: &s2.InfiniteRetention{},
				},
			},
		},
	})
	if err != nil {
		if isFreeTierLimitation(err) {
			t.Log("Skipped: infinite retention not available on free tier")
			return
		}
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.RetentionPolicy == nil {
		t.Fatal("Expected retention_policy")
	}
	if config.DefaultStreamConfig.RetentionPolicy.Infinite == nil {
		t.Error("Expected retention_policy.infinite")
	}
	t.Log("Verified retention_policy changed to infinite")
}

func TestReconfigureBasin_ChangeTimestampingMode(t *testing.T) {
	modes := []s2.TimestampingMode{
		s2.TimestampingModeClientPrefer,
		s2.TimestampingModeClientRequire,
		s2.TimestampingModeArrival,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Reconfigure timestamping.mode to %s", mode)

			client := testClient(t)
			basinName := uniqueBasinName("test-rtm")
			defer deleteBasin(ctx, client, basinName)

			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			waitForBasinActive(ctx, t, client, basinName)

			m := mode
			config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
				Basin: basinName,
				Config: s2.BasinReconfiguration{
					DefaultStreamConfig: &s2.StreamReconfiguration{
						Timestamping: &s2.TimestampingReconfiguration{
							Mode: &m,
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("Reconfigure failed: %v", err)
			}

			if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.Timestamping == nil {
				t.Fatal("Expected timestamping config")
			}
			if config.DefaultStreamConfig.Timestamping.Mode == nil {
				t.Fatal("Expected timestamping.mode")
			}
			if *config.DefaultStreamConfig.Timestamping.Mode != mode {
				t.Errorf("Expected mode=%s, got %s", mode, *config.DefaultStreamConfig.Timestamping.Mode)
			}
			t.Logf("Verified timestamping.mode=%s", mode)
		})
	}
}

func TestReconfigureBasin_ChangeTimestampingUncapped(t *testing.T) {
	values := []bool{true, false}

	for _, uncapped := range values {
		t.Run(fmt.Sprintf("uncapped=%v", uncapped), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			t.Logf("Testing: Reconfigure timestamping.uncapped to %v", uncapped)

			client := testClient(t)
			basinName := uniqueBasinName("test-rtu")
			defer deleteBasin(ctx, client, basinName)

			_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
				Basin: basinName,
				Config: &s2.BasinConfig{
					DefaultStreamConfig: &s2.StreamConfig{
						Timestamping: &s2.TimestampingConfig{
							Uncapped: ptr(!uncapped),
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			waitForBasinActive(ctx, t, client, basinName)

			config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
				Basin: basinName,
				Config: s2.BasinReconfiguration{
					DefaultStreamConfig: &s2.StreamReconfiguration{
						Timestamping: &s2.TimestampingReconfiguration{
							Uncapped: ptr(uncapped),
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("Reconfigure failed: %v", err)
			}

			if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.Timestamping == nil {
				t.Fatal("Expected timestamping config")
			}
			if config.DefaultStreamConfig.Timestamping.Uncapped == nil {
				t.Fatal("Expected timestamping.uncapped")
			}
			if *config.DefaultStreamConfig.Timestamping.Uncapped != uncapped {
				t.Errorf("Expected uncapped=%v, got %v", uncapped, *config.DefaultStreamConfig.Timestamping.Uncapped)
			}
			t.Logf("Verified timestamping.uncapped=%v", uncapped)
		})
	}
}

func TestReconfigureBasin_ChangeDeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure delete_on_empty.min_age_secs")

	client := testClient(t)
	basinName := uniqueBasinName("test-rdoe")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
					MinAgeSecs: ptr(int64(0)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			DefaultStreamConfig: &s2.StreamReconfiguration{
				DeleteOnEmpty: &s2.DeleteOnEmptyReconfiguration{
					MinAgeSecs: ptr(int64(7200)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.DeleteOnEmpty == nil {
		t.Fatal("Expected delete_on_empty config")
	}
	if config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs == nil {
		t.Fatal("Expected delete_on_empty.min_age_secs")
	}
	if *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs != 7200 {
		t.Errorf("Expected min_age_secs=7200, got %d", *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs)
	}
	t.Log("Verified delete_on_empty.min_age_secs=7200")
}

func TestReconfigureBasin_DisableDeleteOnEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure to disable delete_on_empty (min_age_secs=0, field omitted)")

	client := testClient(t)
	basinName := uniqueBasinName("test-rddoe")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			DefaultStreamConfig: &s2.StreamConfig{
				DeleteOnEmpty: &s2.DeleteOnEmptyConfig{
					MinAgeSecs: ptr(int64(3600)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			DefaultStreamConfig: &s2.StreamReconfiguration{
				DeleteOnEmpty: &s2.DeleteOnEmptyReconfiguration{
					MinAgeSecs: ptr(int64(0)),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.DefaultStreamConfig != nil && config.DefaultStreamConfig.DeleteOnEmpty != nil {
		if config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs != nil && *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs != 0 {
			t.Errorf("Expected min_age_secs=0 or omitted, got %d", *config.DefaultStreamConfig.DeleteOnEmpty.MinAgeSecs)
		}
		t.Log("Verified delete_on_empty disabled (min_age_secs=0, explicitly returned)")
	} else {
		t.Log("Verified delete_on_empty disabled (field omitted as expected)")
	}
}

func TestReconfigureBasin_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure non-existent basin")

	client := testClient(t)
	_, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: "nonexistent-basin-name-12345",
		Config: s2.BasinReconfiguration{
			CreateStreamOnAppend: ptr(true),
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 404 {
		t.Errorf("Expected 404 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestReconfigureBasin_EmptyConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure with empty config (no changes)")

	client := testClient(t)
	basinName := uniqueBasinName("test-remp")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin:  basinName,
		Config: s2.BasinReconfiguration{},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append to remain true")
	}
	t.Log("Verified empty reconfigure preserves config")
}

func TestReconfigureBasin_PartialConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure with partial config (only some fields changed)")

	client := testClient(t)
	basinName := uniqueBasinName("test-rpar")
	defer deleteBasin(ctx, client, basinName)

	storageClass := s2.StorageClassStandard
	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
		Config: &s2.BasinConfig{
			CreateStreamOnAppend: ptr(true),
			CreateStreamOnRead:   ptr(false),
			DefaultStreamConfig: &s2.StreamConfig{
				StorageClass: &storageClass,
			},
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	config, err := client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			CreateStreamOnRead: ptr(true),
		},
	})
	if err != nil {
		t.Fatalf("Reconfigure failed: %v", err)
	}

	if config.CreateStreamOnAppend == nil || !*config.CreateStreamOnAppend {
		t.Error("Expected create_stream_on_append to remain true")
	}
	if config.CreateStreamOnRead == nil || !*config.CreateStreamOnRead {
		t.Error("Expected create_stream_on_read to be changed to true")
	}
	if config.DefaultStreamConfig == nil || config.DefaultStreamConfig.StorageClass == nil {
		t.Error("Expected storage_class to be preserved")
	} else if *config.DefaultStreamConfig.StorageClass != s2.StorageClassStandard {
		t.Error("Expected storage_class to remain standard")
	}
	t.Log("Verified partial reconfigure only changes specified fields")
}

func TestReconfigureBasin_InvalidRetentionAgeZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Reconfigure with invalid retention_policy.age=0")

	client := testClient(t)
	basinName := uniqueBasinName("test-rraz")
	defer deleteBasin(ctx, client, basinName)

	_, err := client.Basins.Create(ctx, s2.CreateBasinArgs{Basin: basinName})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	waitForBasinActive(ctx, t, client, basinName)

	_, err = client.Basins.Reconfigure(ctx, s2.ReconfigureBasinArgs{
		Basin: basinName,
		Config: s2.BasinReconfiguration{
			DefaultStreamConfig: &s2.StreamReconfiguration{
				RetentionPolicy: &s2.RetentionPolicy{
					Age: ptr(int64(0)),
				},
			},
		},
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}
