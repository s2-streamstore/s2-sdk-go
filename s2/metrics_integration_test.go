package s2_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

const metricsTestTimeout = 60 * time.Second

// --- Account Metrics Tests ---

func TestAccountMetrics_ActiveBasins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - active basins")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &start,
		End:   &end,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values", len(resp.Values))
}

func TestAccountMetrics_AccountOpsMinute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - account ops (minute interval)")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values (minute interval)", len(resp.Values))
}

func TestAccountMetrics_AccountOpsHour(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - account ops (hour interval)")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values (hour interval)", len(resp.Values))
}

func TestAccountMetrics_AccountOpsDay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - account ops (day interval)")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalDay

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values (day interval)", len(resp.Values))
}

func TestAccountMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - nil args")

	client := testClient(t)
	_, err := client.Metrics.Account(ctx, nil)

	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_EmptySet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - empty set")

	client := testClient(t)
	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set: "",
	})

	if err == nil {
		t.Error("Expected error for empty set")
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_MissingStartEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get account metrics - missing start/end")

	client := testClient(t)
	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set: s2.AccountMetricSetActiveBasins,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error for missing start/end, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

// --- Basin Metrics Tests ---

func TestBasinMetrics_Storage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - storage")

	basin := getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
		Start: &start,
		End:   &end,
	})
	_ = basin
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for storage", len(resp.Values))
}

func TestBasinMetrics_AppendOpsMinute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - append ops (minute)")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetAppendOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for append-ops (minute)", len(resp.Values))
}

func TestBasinMetrics_AppendOpsHour(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - append ops (hour)")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetAppendOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for append-ops (hour)", len(resp.Values))
}

func TestBasinMetrics_AppendOpsDay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - append ops (day)")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalDay

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetAppendOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for append-ops (day)", len(resp.Values))
}

func TestBasinMetrics_ReadOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - read ops")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetReadOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for read-ops", len(resp.Values))
}

func TestBasinMetrics_ReadThroughput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - read throughput")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetReadThroughput,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for read-throughput", len(resp.Values))
}

func TestBasinMetrics_AppendThroughput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - append throughput")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetAppendThroughput,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for append-throughput", len(resp.Values))
}

func TestBasinMetrics_BasinOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - basin ops")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetBasinOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for basin-ops", len(resp.Values))
}

func TestBasinMetrics_NonExistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - non-existent basin")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: "nonexistent-basin-12345",
		Set:   s2.BasinMetricSetStorage,
		Start: &start,
		End:   &end,
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	t.Logf("Got %d metric values for non-existent basin (expected empty)", len(resp.Values))
}

func TestBasinMetrics_EmptyBasinName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - empty basin name")

	client := testClient(t)
	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: "",
		Set:   s2.BasinMetricSetStorage,
	})

	if err == nil {
		t.Error("Expected error for empty basin name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin metrics - nil args")

	client := testClient(t)
	_, err := client.Metrics.Basin(ctx, nil)

	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

// --- Stream Metrics Tests ---

func TestStreamMetrics_StorageMinute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - storage (minute)")

	basin := getSharedBasin(t)
	client := streamTestClient(t)
	streamName := uniqueStreamName("test-mstr")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   string(streamName),
		Set:      s2.StreamMetricSetStorage,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Stream metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metric values for storage (minute)", len(resp.Values))
}

func TestStreamMetrics_StorageHourNotSupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - storage (hour) not supported")

	basin := getSharedBasin(t)
	client := streamTestClient(t)
	streamName := uniqueStreamName("test-msth")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	now := time.Now()
	start := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalHour

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   string(streamName),
		Set:      s2.StreamMetricSetStorage,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error for unsupported interval, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_StorageDayNotSupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - storage (day) not supported")

	basin := getSharedBasin(t)
	client := streamTestClient(t)
	streamName := uniqueStreamName("test-mstd")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	now := time.Now()
	start := now.Add(-7 * 24 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalDay

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   string(streamName),
		Set:      s2.StreamMetricSetStorage,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error for unsupported interval, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_NonExistentStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - non-existent stream")

	_ = getSharedBasin(t)
	client := streamTestClient(t)

	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   "nonexistent-stream-12345",
		Set:      s2.StreamMetricSetStorage,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	t.Logf("Got %d metric values for non-existent stream (expected empty)", len(resp.Values))
}

func TestStreamMetrics_NonExistentBasin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - non-existent basin")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    "nonexistent-basin-12345",
		Stream:   "some-stream",
		Set:      s2.StreamMetricSetStorage,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	t.Logf("Got %d metric values for non-existent basin (expected empty)", len(resp.Values))
}

func TestStreamMetrics_EmptyStreamName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - empty stream name")

	client := testClient(t)
	_, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  "some-basin",
		Stream: "",
		Set:    s2.StreamMetricSetStorage,
	})

	if err == nil {
		t.Error("Expected error for empty stream name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_EmptyBasinName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - empty basin name")

	client := testClient(t)
	_, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  "",
		Stream: "some-stream",
		Set:    s2.StreamMetricSetStorage,
	})

	if err == nil {
		t.Error("Expected error for empty basin name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream metrics - nil args")

	client := testClient(t)
	_, err := client.Metrics.Stream(ctx, nil)

	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

// --- Concurrent Metrics Tests ---

func TestMetrics_ConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Concurrent metrics calls")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	errCh := make(chan error, 3)

	go func() {
		_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
			Set:   s2.AccountMetricSetActiveBasins,
			Start: &start,
			End:   &end,
		})
		errCh <- err
	}()

	go func() {
		_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
			Set:      s2.AccountMetricSetAccountOps,
			Start:    &start,
			End:      &end,
			Interval: &interval,
		})
		errCh <- err
	}()

	go func() {
		_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
			Set:   s2.AccountMetricSetActiveBasins,
			Start: &start,
			End:   &end,
		})
		errCh <- err
	}()

	for range 3 {
		if err := <-errCh; err != nil {
			t.Errorf("Concurrent call failed: %v", err)
		}
	}
	t.Log("All concurrent calls succeeded")
}

func TestMetrics_SequentialCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
	defer cancel()
	t.Log("Testing: Sequential metrics calls")

	client := testClient(t)
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	interval := s2.TimeseriesIntervalMinute

	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &start,
		End:   &end,
	})
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	_, err = client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &end,
		Interval: &interval,
	})
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	_, err = client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &start,
		End:   &end,
	})
	if err != nil {
		t.Fatalf("Third call failed: %v", err)
	}

	t.Log("All sequential calls succeeded")
}
