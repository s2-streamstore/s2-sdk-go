package s2_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func metricsTimeRange(hoursAgo int64) (*int64, *int64) {
	now := time.Now().Unix()
	start := now - (hoursAgo * 3600)
	return &start, &now
}

// --- Account Metrics Tests ---

func TestAccountMetrics_ActiveBasins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get active basins metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics in response", len(resp.Values))

	for _, m := range resp.Values {
		if m.Label != nil {
			t.Logf("Label metric: name=%s, values=%v", m.Label.Name, m.Label.Values)
		}
	}
}

func TestAccountMetrics_AccountOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get account ops metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetAccountOps,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics in response", len(resp.Values))

	for _, m := range resp.Values {
		if m.Accumulation != nil {
			t.Logf("Accumulation metric: name=%s, unit=%s, interval=%s, points=%d",
				m.Accumulation.Name, m.Accumulation.Unit, m.Accumulation.Interval, len(m.Accumulation.Values))
		}
	}
}

func TestAccountMetrics_AccountOpsWithMinuteInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get account ops with minute interval")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    start,
		End:      end,
		Interval: s2.Ptr(s2.TimeseriesIntervalMinute),
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics with minute interval", len(resp.Values))
}

func TestAccountMetrics_AccountOpsWithHourInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get account ops with hour interval")

	client := streamTestClient(t)
	now := time.Now().Unix()
	start := now - (24 * 3600)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &now,
		Interval: s2.Ptr(s2.TimeseriesIntervalHour),
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics with hour interval", len(resp.Values))
}

func TestAccountMetrics_AccountOpsWithDayInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get account ops with day interval")

	client := streamTestClient(t)
	now := time.Now().Unix()
	start := now - (7 * 24 * 3600)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:      s2.AccountMetricSetAccountOps,
		Start:    &start,
		End:      &now,
		Interval: s2.Ptr(s2.TimeseriesIntervalDay),
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics with day interval", len(resp.Values))
}

func TestAccountMetrics_MissingStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics missing start parameter")

	client := streamTestClient(t)
	now := time.Now().Unix()

	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set: s2.AccountMetricSetActiveBasins,
		End: &now,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_MissingEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics missing end parameter")

	client := streamTestClient(t)
	start := time.Now().Unix() - 3600

	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &start,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_MissingBothStartAndEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics missing both start and end")

	client := streamTestClient(t)

	_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set: s2.AccountMetricSetActiveBasins,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_InvalidTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics invalid time ranges")

	client := streamTestClient(t)
	now := time.Now().Unix()

	testCases := []struct {
		name  string
		start int64
		end   int64
	}{
		{name: "start-after-end", start: now, end: now - 3600},
		{name: "end-too-far-future", start: now - 3600, end: now + 600},
		{name: "range-too-large", start: now - 40*24*3600, end: now},
	}

	for _, tc := range testCases {
		_, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
			Set:   s2.AccountMetricSetActiveBasins,
			Start: &tc.start,
			End:   &tc.end,
		})
		var s2Err *s2.S2Error
		if !errors.As(err, &s2Err) || s2Err.Status != 422 {
			t.Errorf("%s: expected 422 error, got: %v", tc.name, err)
		} else {
			t.Logf("%s: got expected error: %v", tc.name, err)
		}
	}
}

func TestAccountMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics with nil args")

	client := streamTestClient(t)

	_, err := client.Metrics.Account(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

func TestAccountMetrics_EmptyTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Account metrics with empty time range (far past)")

	client := streamTestClient(t)
	start := int64(0)
	end := int64(3600)

	resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &start,
		End:   &end,
	})
	if err != nil {
		t.Fatalf("Account metrics failed: %v", err)
	}

	t.Logf("Got %d metrics for empty time range (expected empty or zero values)", len(resp.Values))
}

// --- Basin Metrics Tests ---

func TestBasinMetrics_Storage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin storage metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics in response", len(resp.Values))

	for _, m := range resp.Values {
		if m.Gauge != nil {
			t.Logf("Gauge metric: name=%s, unit=%s, points=%d",
				m.Gauge.Name, m.Gauge.Unit, len(m.Gauge.Values))
		}
	}
}

func TestBasinMetrics_AppendOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin append-ops metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetAppendOps,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics (expected 2: standard, express)", len(resp.Values))

	for _, m := range resp.Values {
		if m.Accumulation != nil {
			t.Logf("Accumulation metric: name=%s, unit=%s", m.Accumulation.Name, m.Accumulation.Unit)
		}
	}
}

func TestBasinMetrics_ReadOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin read-ops metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetReadOps,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics (expected 2: streaming, unary)", len(resp.Values))
}

func TestBasinMetrics_ReadThroughput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin read-throughput metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetReadThroughput,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics", len(resp.Values))
}

func TestBasinMetrics_AppendThroughput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin append-throughput metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetAppendThroughput,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics (expected 2: standard, express)", len(resp.Values))
}

func TestBasinMetrics_BasinOps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin-ops metric")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetBasinOps,
		Start: start,
		End:   end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics", len(resp.Values))
}

func TestBasinMetrics_StorageWithHourInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get basin storage with hour interval")

	client := streamTestClient(t)
	now := time.Now().Unix()
	start := now - (24 * 3600)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetStorage,
		Start:    &start,
		End:      &now,
		Interval: s2.Ptr(s2.TimeseriesIntervalHour),
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics with hour interval", len(resp.Values))
}

func TestBasinMetrics_MissingStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics missing start parameter")

	client := streamTestClient(t)
	now := time.Now().Unix()

	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
		End:   &now,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_MissingEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics missing end parameter")

	client := streamTestClient(t)
	start := time.Now().Unix() - 3600

	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
		Start: &start,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_MissingBothStartAndEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics missing both start and end")

	client := streamTestClient(t)

	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_InvalidTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics invalid time ranges")

	client := streamTestClient(t)
	now := time.Now().Unix()

	testCases := []struct {
		name  string
		start int64
		end   int64
	}{
		{name: "start-after-end", start: now, end: now - 3600},
		{name: "end-too-far-future", start: now - 3600, end: now + 600},
		{name: "range-too-large", start: now - 40*24*3600, end: now},
	}

	for _, tc := range testCases {
		_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
			Basin: string(sharedTestBasinName),
			Set:   s2.BasinMetricSetStorage,
			Start: &tc.start,
			End:   &tc.end,
		})
		var s2Err *s2.S2Error
		if !errors.As(err, &s2Err) || s2Err.Status != 422 {
			t.Errorf("%s: expected 422 error, got: %v", tc.name, err)
		} else {
			t.Logf("%s: got expected error: %v", tc.name, err)
		}
	}
}

func TestBasinMetrics_StorageInvalidInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin storage metric invalid interval (expect error)")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Set:      s2.BasinMetricSetStorage,
		Start:    start,
		End:      end,
		Interval: s2.Ptr(s2.TimeseriesIntervalMinute),
	})
	if err == nil {
		t.Fatal("Expected error for invalid interval")
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_EmptyBasinName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics with empty basin name")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	_, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: "",
		Set:   s2.BasinMetricSetStorage,
		Start: start,
		End:   end,
	})

	if err == nil {
		t.Error("Expected error for empty basin name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics with nil args")

	client := streamTestClient(t)

	_, err := client.Metrics.Basin(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

func TestBasinMetrics_EmptyTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Basin metrics with empty time range (far past)")

	client := streamTestClient(t)
	start := int64(0)
	end := int64(3600)

	resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin: string(sharedTestBasinName),
		Set:   s2.BasinMetricSetStorage,
		Start: &start,
		End:   &end,
	})
	if err != nil {
		t.Fatalf("Basin metrics failed: %v", err)
	}

	t.Logf("Got %d metrics for empty time range (expected empty or zero values)", len(resp.Values))
}

// --- Stream Metrics Tests ---

func TestStreamMetrics_Storage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream storage metric")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: string(streamName),
		Set:    s2.StreamMetricSetStorage,
		Start:  start,
		End:    end,
	})
	if err != nil {
		t.Fatalf("Stream metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics in response", len(resp.Values))

	for _, m := range resp.Values {
		if m.Gauge != nil {
			t.Logf("Gauge metric: name=%s, unit=%s, points=%d",
				m.Gauge.Name, m.Gauge.Unit, len(m.Gauge.Values))
		}
	}
}

func TestStreamMetrics_StorageWithMinuteInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Get stream storage with minute interval")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-min")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	start, end := metricsTimeRange(1)

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   string(streamName),
		Set:      s2.StreamMetricSetStorage,
		Start:    start,
		End:      end,
		Interval: s2.Ptr(s2.TimeseriesIntervalMinute),
	})
	if err != nil {
		t.Fatalf("Stream metrics failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	t.Logf("Got %d metrics with minute interval", len(resp.Values))
}

func TestStreamMetrics_MissingStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics missing start parameter")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-ms")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	now := time.Now().Unix()

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: string(streamName),
		Set:    s2.StreamMetricSetStorage,
		End:    &now,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_MissingEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics missing end parameter")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-me")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	start := time.Now().Unix() - 3600

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: string(streamName),
		Set:    s2.StreamMetricSetStorage,
		Start:  &start,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_MissingBothStartAndEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics missing both start and end")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-mb")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: string(streamName),
		Set:    s2.StreamMetricSetStorage,
	})

	var s2Err *s2.S2Error
	if !errors.As(err, &s2Err) || s2Err.Status != 422 {
		t.Errorf("Expected 422 error, got: %v", err)
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_EmptyBasinName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics with empty basin name")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	_, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  "",
		Stream: "test-stream",
		Set:    s2.StreamMetricSetStorage,
		Start:  start,
		End:    end,
	})

	if err == nil {
		t.Error("Expected error for empty basin name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_EmptyStreamName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics with empty stream name")

	client := streamTestClient(t)
	start, end := metricsTimeRange(1)

	_, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: "",
		Set:    s2.StreamMetricSetStorage,
		Start:  start,
		End:    end,
	})

	if err == nil {
		t.Error("Expected error for empty stream name")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_NilArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics with nil args")

	client := streamTestClient(t)

	_, err := client.Metrics.Stream(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil args")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_InvalidTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics invalid time ranges")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-it")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	now := time.Now().Unix()
	testCases := []struct {
		name  string
		start int64
		end   int64
	}{
		{name: "start-after-end", start: now, end: now - 3600},
		{name: "end-too-far-future", start: now - 3600, end: now + 600},
		{name: "range-too-large", start: now - 40*24*3600, end: now},
	}

	for _, tc := range testCases {
		_, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
			Basin:  string(sharedTestBasinName),
			Stream: string(streamName),
			Set:    s2.StreamMetricSetStorage,
			Start:  &tc.start,
			End:    &tc.end,
		})
		var s2Err *s2.S2Error
		if !errors.As(err, &s2Err) || s2Err.Status != 422 {
			t.Errorf("%s: expected 422 error, got: %v", tc.name, err)
		} else {
			t.Logf("%s: got expected error: %v", tc.name, err)
		}
	}
}

func TestStreamMetrics_StorageInvalidInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream storage metric invalid interval (expect error)")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-ii")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	start, end := metricsTimeRange(1)

	_, err = client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    string(sharedTestBasinName),
		Stream:   string(streamName),
		Set:      s2.StreamMetricSetStorage,
		Start:    start,
		End:      end,
		Interval: s2.Ptr(s2.TimeseriesIntervalHour),
	})
	if err == nil {
		t.Fatal("Expected error for invalid interval")
	}
	t.Logf("Got expected error: %v", err)
}

func TestStreamMetrics_EmptyTimeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
	defer cancel()
	t.Log("Testing: Stream metrics with empty time range (far past)")

	client := streamTestClient(t)
	basin := getSharedBasin(t)

	streamName := uniqueStreamName("test-metrics-et")
	defer deleteStream(ctx, basin, streamName)

	_, err := basin.Streams.Create(ctx, s2.CreateStreamArgs{Stream: streamName})
	if err != nil {
		t.Fatalf("Create stream failed: %v", err)
	}

	start := int64(0)
	end := int64(3600)

	resp, err := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:  string(sharedTestBasinName),
		Stream: string(streamName),
		Set:    s2.StreamMetricSetStorage,
		Start:  &start,
		End:    &end,
	})
	if err != nil {
		t.Fatalf("Stream metrics failed: %v", err)
	}

	t.Logf("Got %d metrics for empty time range (expected empty or zero values)", len(resp.Values))
}

// --- All Metric Sets Table Tests ---

func TestBasinMetrics_AllSets(t *testing.T) {
	sets := []struct {
		set  s2.BasinMetricSet
		name string
	}{
		{s2.BasinMetricSetStorage, "storage"},
		{s2.BasinMetricSetAppendOps, "append-ops"},
		{s2.BasinMetricSetReadOps, "read-ops"},
		{s2.BasinMetricSetReadThroughput, "read-throughput"},
		{s2.BasinMetricSetAppendThroughput, "append-throughput"},
		{s2.BasinMetricSetBasinOps, "basin-ops"},
	}

	for _, tc := range sets {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Basin metric set %s", tc.name)

			client := streamTestClient(t)
			start, end := metricsTimeRange(1)

			resp, err := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
				Basin: string(sharedTestBasinName),
				Set:   tc.set,
				Start: start,
				End:   end,
			})
			if err != nil {
				t.Fatalf("Basin metrics failed: %v", err)
			}

			if resp == nil {
				t.Fatal("Expected non-nil response")
			}
			t.Logf("Got %d metrics for set %s", len(resp.Values), tc.name)
		})
	}
}

func TestAccountMetrics_AllSets(t *testing.T) {
	sets := []struct {
		set  s2.AccountMetricSet
		name string
	}{
		{s2.AccountMetricSetActiveBasins, "active-basins"},
		{s2.AccountMetricSetAccountOps, "account-ops"},
	}

	for _, tc := range sets {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Account metric set %s", tc.name)

			client := streamTestClient(t)
			start, end := metricsTimeRange(1)

			resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
				Set:   tc.set,
				Start: start,
				End:   end,
			})
			if err != nil {
				t.Fatalf("Account metrics failed: %v", err)
			}

			if resp == nil {
				t.Fatal("Expected non-nil response")
			}
			t.Logf("Got %d metrics for set %s", len(resp.Values), tc.name)
		})
	}
}

func TestAccountMetrics_AllIntervals(t *testing.T) {
	intervals := []struct {
		interval s2.TimeseriesInterval
		name     string
	}{
		{s2.TimeseriesIntervalMinute, "minute"},
		{s2.TimeseriesIntervalHour, "hour"},
		{s2.TimeseriesIntervalDay, "day"},
	}

	for _, tc := range intervals {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamTestTimeout)
			defer cancel()
			t.Logf("Testing: Interval %s", tc.name)

			client := streamTestClient(t)
			now := time.Now().Unix()
			start := now - (7 * 24 * 3600)

			resp, err := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
				Set:      s2.AccountMetricSetAccountOps,
				Start:    &start,
				End:      &now,
				Interval: s2.Ptr(tc.interval),
			})
			if err != nil {
				t.Fatalf("Account metrics failed: %v", err)
			}

			if resp == nil {
				t.Fatal("Expected non-nil response")
			}
			t.Logf("Got %d metrics with interval %s", len(resp.Values), tc.name)
		})
	}
}
