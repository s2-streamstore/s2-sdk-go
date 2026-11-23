package s2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
)

type AccountMetricsArgs struct {
	// Metric set to return.
	Set AccountMetricSet `json:"set"`
	// Start timestamp as Unix epoch seconds, if applicable for the metric set.
	// Time ranges may span at most 90 days.
	Start *int64 `json:"start,omitempty"`
	// End timestamp as Unix epoch seconds, if applicable for the metric set.
	End *int64 `json:"end,omitempty"`
	// Interval to aggregate over for timeseries metric sets.
	Interval *TimeseriesInterval `json:"interval,omitempty"`
}

type BasinMetricsArgs struct {
	//  Basin name.
	Basin string `json:"basin"`
	// Metric set to return.
	Set BasinMetricSet `json:"set"`
	// Start timestamp as Unix epoch seconds, if applicable for the metric set.
	// Time ranges may span at most 90 days.
	Start *int64 `json:"start,omitempty"`
	// End timestamp as Unix epoch seconds, if applicable for the metric set.
	End *int64 `json:"end,omitempty"`
	// Interval to aggregate over for timeseries metric sets.
	Interval *TimeseriesInterval `json:"interval,omitempty"`
}

type StreamMetricsArgs struct {
	// Basin name.
	Basin string `json:"basin"`
	// Stream name.
	Stream string `json:"stream"`
	// Metric set to return.
	Set StreamMetricSet `json:"set"`
	// Start timestamp as Unix epoch seconds, if applicable for the metric set.
	// Time ranges may span at most 90 days.
	Start *int64 `json:"start,omitempty"`
	// End timestamp as Unix epoch seconds, if applicable for metric set.
	End *int64 `json:"end,omitempty"`
	// Interval to aggregate over for timeseries metric sets.
	Interval *TimeseriesInterval `json:"interval,omitempty"`
}

// Metrics client.
type MetricsClient struct {
	client *Client
}

func (m *MetricsClient) Account(ctx context.Context, args *AccountMetricsArgs) (*MetricSetResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		return nil, fmt.Errorf("account metrics args cannot be nil")
	}
	if err := validateMetricsInterval(args.Interval); err != nil {
		return nil, err
	}
	return m.fetchMetrics(ctx, fmt.Sprintf("%s/metrics", m.client.baseURL), string(args.Set), args.Start, args.End, args.Interval)
}

func (m *MetricsClient) Basin(ctx context.Context, args *BasinMetricsArgs) (*MetricSetResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		return nil, fmt.Errorf("basin metrics args cannot be nil")
	}
	if err := validateBasinName(BasinName(args.Basin)); err != nil {
		return nil, err
	}
	if err := validateMetricsInterval(args.Interval); err != nil {
		return nil, err
	}
	if args.Set == BasinMetricSetStorage && args.Interval != nil && *args.Interval != "" && *args.Interval != TimeseriesIntervalHour {
		return nil, fmt.Errorf("basin storage metrics only support hour interval")
	}
	endpoint := fmt.Sprintf("%s/metrics/%s", m.client.baseURL, url.PathEscape(args.Basin))
	return m.fetchMetrics(ctx, endpoint, string(args.Set), args.Start, args.End, args.Interval)
}

func (m *MetricsClient) Stream(ctx context.Context, args *StreamMetricsArgs) (*MetricSetResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		return nil, fmt.Errorf("stream metrics args cannot be nil")
	}
	if err := validateBasinName(BasinName(args.Basin)); err != nil {
		return nil, err
	}
	if err := validateStreamName(StreamName(args.Stream)); err != nil {
		return nil, err
	}
	if err := validateMetricsInterval(args.Interval); err != nil {
		return nil, err
	}
	if args.Set == StreamMetricSetStorage && args.Interval != nil && *args.Interval != "" && *args.Interval != TimeseriesIntervalMinute {
		return nil, fmt.Errorf("stream storage metrics only support minute interval")
	}
	endpoint := fmt.Sprintf("%s/metrics/%s/%s",
		m.client.baseURL,
		url.PathEscape(args.Basin),
		url.PathEscape(args.Stream),
	)
	return m.fetchMetrics(ctx, endpoint, string(args.Set), args.Start, args.End, args.Interval)
}

func (m *MetricsClient) fetchMetrics(ctx context.Context, endpoint string, set string, start, end *int64, interval *TimeseriesInterval) (*MetricSetResponse, error) {
	if set == "" {
		return nil, newValidationError("metrics set must be provided")
	}
	if err := validateMetricsTimestamp("start", start); err != nil {
		return nil, err
	}
	if err := validateMetricsTimestamp("end", end); err != nil {
		return nil, err
	}

	// Snapshot query parameters before retries so caller mutations have no effect (spec C10).
	q := url.Values{}
	q.Set("set", set)
	if start != nil {
		q.Set("start", strconv.FormatInt(*start, 10))
	}
	if end != nil {
		q.Set("end", strconv.FormatInt(*end, 10))
	}
	if interval != nil && *interval != "" {
		q.Set("interval", string(*interval))
	}
	rawQuery := q.Encode()

	return withRetries(ctx, m.client.retryConfig, m.client.logger, func() (*MetricSetResponse, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+m.client.accessToken)
		req.Header.Set("Content-Type", "application/json")
		if encoding := m.client.compression.AcceptEncoding(); encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		req.URL.RawQuery = rawQuery

		resp, err := m.client.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("making request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, parseHTTPError(resp)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, makeConnectionLostError(err, "response body read")
		}
		body, err = decompressResponseBody(resp, body)
		if err != nil {
			return nil, err
		}

		var result MetricSetResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, makeProtocolError(err, "metrics response body decode")
		}

		return &result, nil
	})
}

func validateMetricsTimestamp(name string, ts *int64) error {
	if ts == nil {
		return nil
	}
	if *ts < 0 || *ts > math.MaxUint32 {
		return newValidationError(fmt.Sprintf("metrics %s timestamp must be between 0 and %d", name, uint64(math.MaxUint32)))
	}
	return nil
}

func validateMetricsInterval(interval *TimeseriesInterval) error {
	if interval == nil || *interval == "" {
		return nil
	}
	switch *interval {
	case TimeseriesIntervalMinute, TimeseriesIntervalHour, TimeseriesIntervalDay:
		return nil
	default:
		return newValidationError(fmt.Sprintf("invalid metrics interval: %q", *interval))
	}
}
