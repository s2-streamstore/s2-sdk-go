// Documentation examples for Metrics page.
//
// Run with: go run ./examples/docs_metrics
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func main() {
	ctx := context.Background()
	client := s2.NewFromEnvironment(nil)

	// ANCHOR: metrics
	now := time.Now().Unix()
	thirtyDaysAgo := now - 30*24*3600
	sixHoursAgo := now - 6*3600
	hourAgo := now - 3600

	// Account-level: active basins over the last 30 days
	accountMetrics, _ := client.Metrics.Account(ctx, &s2.AccountMetricsArgs{
		Set:   s2.AccountMetricSetActiveBasins,
		Start: &thirtyDaysAgo,
		End:   &now,
	})

	// Basin-level: storage usage with hourly resolution
	hourInterval := s2.TimeseriesIntervalHour
	basinMetrics, _ := client.Metrics.Basin(ctx, &s2.BasinMetricsArgs{
		Basin:    "events",
		Set:      s2.BasinMetricSetStorage,
		Start:    &sixHoursAgo,
		End:      &now,
		Interval: &hourInterval,
	})

	// Stream-level: storage for a specific stream
	minuteInterval := s2.TimeseriesIntervalMinute
	streamMetrics, _ := client.Metrics.Stream(ctx, &s2.StreamMetricsArgs{
		Basin:    "events",
		Stream:   "user-actions",
		Set:      s2.StreamMetricSetStorage,
		Start:    &hourAgo,
		End:      &now,
		Interval: &minuteInterval,
	})
	// ANCHOR_END: metrics

	fmt.Println(accountMetrics, basinMetrics, streamMetrics)
}
