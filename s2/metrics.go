package s2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type AccountMetricsArgs struct {
	// Metric set to return.
	Set AccountMetricSet `json:"set"`
	// Start timestamp as Unix epoch seconds, if applicable for the metric set.
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
	endpoint := fmt.Sprintf("%s/metrics/%s/%s",
		m.client.baseURL,
		url.PathEscape(args.Basin),
		url.PathEscape(args.Stream),
	)
	return m.fetchMetrics(ctx, endpoint, string(args.Set), args.Start, args.End, args.Interval)
}

func (m *MetricsClient) fetchMetrics(ctx context.Context, endpoint string, set string, start, end *int64, interval *TimeseriesInterval) (*MetricSetResponse, error) {
	if set == "" {
		return nil, fmt.Errorf("metrics set must be provided")
	}

	return withRetries(ctx, m.client.retryConfig, m.client.logger, func() (*MetricSetResponse, error) {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+m.client.accessToken)
		req.Header.Set("Content-Type", "application/json")

		q := req.URL.Query()
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
		req.URL.RawQuery = q.Encode()

		resp, err := m.client.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("making request: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode >= 400 {
			return nil, decodeAPIError(resp.StatusCode, body)
		}

		var result MetricSetResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}

		return &result, nil
	})
}
