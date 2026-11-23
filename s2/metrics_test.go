package s2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	internalframing "github.com/s2-streamstore/s2-sdk-go/internal/framing"
)

func TestAccountMetricsEstimatedBillableUsage(t *testing.T) {
	hour, day := TimeseriesIntervalHour, TimeseriesIntervalDay
	for _, tc := range []struct {
		name     string
		interval *TimeseriesInterval
	}{
		{name: "default"},
		{name: "hour", interval: &hour},
		{name: "day", interval: &day},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end := int64(1_700_000_000), int64(1_700_003_600)
			wantInterval := TimeseriesIntervalHour
			wantQuery := url.Values{
				"set":   {"estimated-billable-usage"},
				"start": {"1700000000"},
				"end":   {"1700003600"},
			}
			if tc.interval != nil {
				wantInterval = *tc.interval
				wantQuery.Set("interval", string(*tc.interval))
			}
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("expected GET, got %s", req.Method)
				}
				if req.URL.Scheme != "http" || req.URL.Host != "account.test" || req.URL.Path != "/v1/metrics" {
					t.Fatalf("unexpected URL: %s", req.URL)
				}
				if req.URL.RawQuery != wantQuery.Encode() {
					t.Fatalf("unexpected query: %s", req.URL.RawQuery)
				}
				body := fmt.Sprintf(`{"values":[
					{"accumulation":{"name":"transfer.writes_standard","unit":"usd","interval":%q,"values":[[1700000000,0.012345]]}},
					{"accumulation":{"name":"total","unit":"usd","interval":%q,"values":[[1700000000,0.012345]]}}
				]}`, wantInterval, wantInterval)
				return jsonResponse(http.StatusOK, "", body), nil
			})
			client := New("token", &ClientOptions{
				BaseURL:          "http://account.test",
				MakeBasinBaseURL: testBasinBaseURL,
				HTTPClient:       &http.Client{Transport: rt},
			})
			response, err := client.Metrics.Account(context.Background(), &AccountMetricsArgs{
				Set:      AccountMetricSetEstimatedBillableUsage,
				Start:    &start,
				End:      &end,
				Interval: tc.interval,
			})
			if err != nil {
				t.Fatalf("account metrics: %v", err)
			}
			if len(response.Values) != 2 {
				t.Fatalf("expected 2 metrics, got %#v", response.Values)
			}
			for i, name := range []string{"transfer.writes_standard", "total"} {
				metric := response.Values[i].Accumulation
				if metric == nil {
					t.Fatalf("expected accumulation metric, got %#v", response.Values[i])
				}
				if metric.Name != name || metric.Unit != MetricUnitUSD || metric.Interval != wantInterval {
					t.Fatalf("unexpected metric: %#v", metric)
				}
				if len(metric.Values) != 1 || metric.Values[0] != (MetricSample{float64(start), 0.012345}) {
					t.Fatalf("unexpected samples: %#v", metric.Values)
				}
			}
		})
	}
}

// Session frames have a decompressed size limit; unary responses do not, and
// minute-interval metrics over the maximum 90-day range exceed it.
func TestMetricsDecompressesResponsesPastFrameLimit(t *testing.T) {
	const samples = 90 * 24 * 60
	var body strings.Builder
	body.WriteString(`{"values":[{"accumulation":{"name":"reads","unit":"bytes","interval":"minute","values":[`)
	for i := range samples {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, "[%d,%d]", 1_700_000_000+60*i, 1_000_000+i)
	}
	body.WriteString(`]}}]}`)
	if body.Len() <= internalframing.MaxDecompressedSize {
		t.Fatalf("response body of %d bytes does not exceed the frame limit", body.Len())
	}

	for _, compression := range []CompressionType{CompressionGzip, CompressionZstd} {
		t.Run(compression.ContentEncoding(), func(t *testing.T) {
			compressed, err := internalframing.Compress([]byte(body.String()), compression)
			if err != nil {
				t.Fatal(err)
			}
			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := jsonResponse(http.StatusOK, "", "")
				resp.Header.Set("Content-Encoding", compression.ContentEncoding())
				resp.Body = io.NopCloser(bytes.NewReader(compressed))
				return resp, nil
			})
			client := New("token", &ClientOptions{
				BaseURL:          "http://account.test",
				MakeBasinBaseURL: testBasinBaseURL,
				HTTPClient:       &http.Client{Transport: rt},
				Compression:      compression,
			})
			response, err := client.Metrics.Account(context.Background(), &AccountMetricsArgs{Set: AccountMetricSetAccountOps})
			if err != nil {
				t.Fatalf("account metrics: %v", err)
			}
			if len(response.Values) != 1 || response.Values[0].Accumulation == nil || len(response.Values[0].Accumulation.Values) != samples {
				t.Fatalf("expected %d samples, got %#v", samples, response.Values)
			}
		})
	}
}
