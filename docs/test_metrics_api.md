# Metrics API - SDK Test Matrix

> **LLM Instructions**
>
> This document is a reference for generating SDK test code. When writing tests:
>
> - Write clean, concise, idiomatic code for the target language
> - Do not add comments unless they explain non-obvious business logic
> - Do not add TODO comments, redundant docstrings, or explanatory noise
> - Each test should be self-contained and test exactly one behavior
> - Use descriptive test names that document intent (no need for separate comments)
> - Prefer table-driven/parameterized tests where appropriate
> - Assert on specific expected values, not just "success"
> - Show progress as each test progresses
> - Set a timeout for each test
>
> **Metrics-specific guidance:**
> - Metrics may return empty results if no data exists for the time range - this is NOT an error
> - Time ranges use Unix epoch seconds (not milliseconds)
> - Both `start` and `end` parameters are REQUIRED for all metric sets
> - `start` must be <= `end`
> - `end` must not be more than ~60 seconds in the future
> - Time range must not exceed 30 days
> - Metric values are floating point numbers
> - Timeseries data points are tuples of `[timestamp, value]`
> - The metrics API does NOT return 404 for non-existent basins/streams - it returns permission errors or empty data
> - Different metric sets return different metric types (Label, Gauge, Accumulation)
>
> **Test setup:**
> - Create a basin and stream before running metrics tests (some metrics require data to exist)
> - Perform some operations (appends, reads) to generate metric data
> - Use reasonable time ranges (e.g., last hour, last day) that cover when operations were performed
>
> **Linter and formatter compliance:**
> - Always run the project's linter and formatter after generating code
> - Fix ALL linter warnings and errors - do not leave any unresolved
> - Follow the project's existing code style (check existing test files for patterns)

---

## Endpoints Overview

- `GET /metrics` — `account_metrics` — Get account-level metrics
- `GET /metrics/{basin}` — `basin_metrics` — Get basin-level metrics
- `GET /metrics/{basin}/{stream}` — `stream_metrics` — Get stream-level metrics

---

## 1. Account Metrics

**`GET /metrics`**

### Query Parameters

- `set` (AccountMetricSet, required)
  - Metric set to return
  - Values: `active-basins`, `account-ops`

- `start` (integer, required for most sets)
  - Start timestamp as Unix epoch seconds

- `end` (integer, required for most sets)
  - End timestamp as Unix epoch seconds

- `interval` (TimeseriesInterval, optional)
  - Aggregation interval for timeseries metrics
  - Values: `minute`, `hour`, `day`

### Account Metric Sets

- `active-basins`
  - Returns: Label metric with names of active basins
  - Type: `LabelMetric`
  - Requires: `start`, `end`

- `account-ops`
  - Returns: Accumulation metrics for account operations
  - Type: `AccumulationMetric` (multiple)
  - Requires: `start`, `end`
  - Optional: `interval`

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`

- `422` — Validation error
  - Code: `invalid`
  - Cause: missing required parameters, invalid parameter format

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `account-metrics` permission

- `408` — Timeout
  - Code: `request_timeout`

- `503` — Service unavailable
  - Code: `unavailable`

### Response Schema: MetricSetResponse

- `values` (array of Metric)
  - Metrics comprising the set

### Metric Types

**ScalarMetric:**
- `name` (string) — Metric name
- `unit` (MetricUnit) — `bytes` or `operations`
- `value` (float64) — Metric value

**AccumulationMetric:**
- `name` (string) — Timeseries name
- `unit` (MetricUnit) — `bytes` or `operations`
- `interval` (TimeseriesInterval) — Aggregation interval
- `values` (array of [timestamp, value]) — Timeseries data points

**GaugeMetric:**
- `name` (string) — Timeseries name
- `unit` (MetricUnit) — `bytes` or `operations`
- `values` (array of [timestamp, value]) — Timeseries data points

**LabelMetric:**
- `name` (string) — Label name
- `values` (array of string) — Label values

### Test Cases

- **Get active basins metric**
  - Input: `set=active-basins`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, LabelMetric with name "active_basins", values is array of basin names

- **Get account ops metric**
  - Input: `set=account-ops`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, multiple AccumulationMetrics, unit=operations

- **Get account ops with minute interval**
  - Input: `set=account-ops`, `start=<now-1h>`, `end=<now>`, `interval=minute`
  - Expected: 200, AccumulationMetrics with minute-level data points

- **Get account ops with hour interval**
  - Input: `set=account-ops`, `start=<now-1d>`, `end=<now>`, `interval=hour`
  - Expected: 200, AccumulationMetrics with hourly data points

- **Get account ops with day interval**
  - Input: `set=account-ops`, `start=<now-7d>`, `end=<now>`, `interval=day`
  - Expected: 200, AccumulationMetrics with daily data points

- **Missing start parameter**
  - Input: `set=active-basins`, `end=<now>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing end parameter**
  - Input: `set=active-basins`, `start=<now-1h>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing both start and end**
  - Input: `set=active-basins`
  - Expected: 422 (`invalid`)

- **Missing set parameter**
  - Input: `start=<now-1h>`, `end=<now>`
  - Expected: Error (SDK validation or 400)

- **Invalid set value**
  - Input: `set=invalid-set`, `start=<now-1h>`, `end=<now>`
  - Expected: 422 (`invalid`)

- **Start > end**
  - Input: `set=active-basins`, `start=<now>`, `end=<now-1h>`
  - Expected: 422 (`invalid`)

- **End too far in future**
  - Input: `set=active-basins`, `start=<now-1h>`, `end=<now+10m>`
  - Expected: 422 (`invalid`)

- **Time range > 30 days**
  - Input: `set=active-basins`, `start=<now-40d>`, `end=<now>`
  - Expected: 422 (`invalid`)

- **Empty time range returns empty data**
  - Input: `set=active-basins`, `start=<far-past>`, `end=<far-past+1h>`
  - Expected: 200, LabelMetric with empty values array (not an error)

- **Permission denied**
  - Setup: token without `account-metrics` op
  - Expected: 403 (`permission_denied`)

---

## 2. Basin Metrics

**`GET /metrics/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 characters, lowercase letters, numbers, hyphens

### Query Parameters

- `set` (BasinMetricSet, required)
  - Metric set to return
  - Values: `storage`, `append-ops`, `read-ops`, `read-throughput`, `append-throughput`, `basin-ops`

- `start` (integer, required)
  - Start timestamp as Unix epoch seconds

- `end` (integer, required)
  - End timestamp as Unix epoch seconds

- `interval` (TimeseriesInterval, optional)
  - Aggregation interval for timeseries metrics
  - **Note: Basin `storage` metric only supports `hour` interval. Other basin metrics support all intervals.**

### Basin Metric Sets

- `storage`
  - Returns: Gauge metric with storage bytes
  - Type: `GaugeMetric`
  - Unit: `bytes`

- `append-ops`
  - Returns: Accumulation metrics for append operations (standard, express)
  - Type: `AccumulationMetric` (2 metrics)
  - Unit: `operations`

- `read-ops`
  - Returns: Accumulation metrics for read operations (streaming, unary)
  - Type: `AccumulationMetric` (2 metrics)
  - Unit: `operations`

- `read-throughput`
  - Returns: Accumulation metric for read bytes
  - Type: `AccumulationMetric`
  - Unit: `bytes`

- `append-throughput`
  - Returns: Accumulation metrics for append bytes (standard, express)
  - Type: `AccumulationMetric` (2 metrics)
  - Unit: `bytes`

- `basin-ops`
  - Returns: Accumulation metrics for basin operations
  - Type: `AccumulationMetric` (multiple)
  - Unit: `operations`

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`
  - Note: Returns empty `values` array if no data exists for the time range

- `422` — Validation error
  - Code: `invalid`
  - Cause: missing required parameters (start, end), invalid parameter format

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `basin-metrics` permission or basin not in token scope

- `408` — Timeout
  - Code: `request_timeout`

- `503` — Service unavailable
  - Code: `unavailable`

### Test Cases

- **Get basin storage metric**
  - Input: valid basin, `set=storage`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, GaugeMetric with name containing "storage", unit=bytes

- **Get append ops metric**
  - Input: valid basin, `set=append-ops`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, two AccumulationMetrics (standard, express), unit=operations

- **Get read ops metric**
  - Input: valid basin, `set=read-ops`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, two AccumulationMetrics (streaming, unary), unit=operations

- **Get read throughput metric**
  - Input: valid basin, `set=read-throughput`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, AccumulationMetric, unit=bytes

- **Get append throughput metric**
  - Input: valid basin, `set=append-throughput`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, two AccumulationMetrics (standard, express), unit=bytes

- **Get basin ops metric**
  - Input: valid basin, `set=basin-ops`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, multiple AccumulationMetrics, unit=operations

- **Get storage with hour interval**
  - Input: valid basin, `set=storage`, `start=<now-1d>`, `end=<now>`, `interval=hour`
  - Expected: 200, GaugeMetric with hourly data points
  - Note: Basin `storage` metric only supports `hour` interval

- **Basin not in token scope**
  - Input: basin not in token's basin scope, `set=storage`, `start`, `end`
  - Expected: 403 (`permission_denied`)

- **Missing start parameter**
  - Input: valid basin, `set=storage`, `end=<now>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing end parameter**
  - Input: valid basin, `set=storage`, `start=<now-1h>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing both start and end**
  - Input: valid basin, `set=storage`
  - Expected: 422 (`invalid`)

- **Start > end**
  - Input: valid basin, `set=storage`, `start=<now>`, `end=<now-1h>`
  - Expected: 422 (`invalid`)

- **End too far in future**
  - Input: valid basin, `set=storage`, `start=<now-1h>`, `end=<now+10m>`
  - Expected: 422 (`invalid`)

- **Time range > 30 days**
  - Input: valid basin, `set=storage`, `start=<now-40d>`, `end=<now>`
  - Expected: 422 (`invalid`)

- **Storage interval invalid**
  - Input: valid basin, `set=storage`, `interval=minute`
  - Expected: 422 (`invalid`, only hour supported)

- **Empty time range returns empty data**
  - Input: valid basin, `set=storage`, `start=<far-past>`, `end=<far-past+1h>` (before any data)
  - Expected: 200, empty or zero values (not an error)

- **Permission denied**
  - Setup: token without `basin-metrics` op
  - Expected: 403 (`permission_denied`)

---

## 3. Stream Metrics

**`GET /metrics/{basin}/{stream}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 characters, lowercase letters, numbers, hyphens

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Query Parameters

- `set` (StreamMetricSet, required)
  - Metric set to return
  - Values: `storage`

- `start` (integer, required)
  - Start timestamp as Unix epoch seconds

- `end` (integer, required)
  - End timestamp as Unix epoch seconds

- `interval` (TimeseriesInterval, optional)
  - Aggregation interval for timeseries metrics
  - **Note: Stream `storage` metric only supports `minute` interval**

### Stream Metric Sets

- `storage`
  - Returns: Gauge metric with stream storage bytes
  - Type: `GaugeMetric`
  - Unit: `bytes`

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`
  - Note: Returns empty `values` array if no data exists for the time range

- `422` — Validation error
  - Code: `invalid`
  - Cause: missing required parameters (start, end), invalid parameter format

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `stream-metrics` permission, basin not in scope, or stream not in scope

- `408` — Timeout
  - Code: `request_timeout`

- `503` — Service unavailable
  - Code: `unavailable`

### Test Cases

- **Get stream storage metric**
  - Input: valid basin/stream, `set=storage`, `start=<now-1h>`, `end=<now>`
  - Expected: 200, GaugeMetric with name containing "storage", unit=bytes

- **Get stream storage with minute interval**
  - Input: valid basin/stream, `set=storage`, `start=<now-1h>`, `end=<now>`, `interval=minute`
  - Expected: 200, GaugeMetric with minute-level data points
  - Note: Stream `storage` metric only supports `minute` interval

- **Stream not in token scope**
  - Input: valid basin, stream not in token's stream scope, `set=storage`, `start`, `end`
  - Expected: 403 (`permission_denied`)

- **Basin not in token scope**
  - Input: basin not in token scope, valid stream, `set=storage`, `start`, `end`
  - Expected: 403 (`permission_denied`)

- **Missing start parameter**
  - Input: valid basin/stream, `set=storage`, `end=<now>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing end parameter**
  - Input: valid basin/stream, `set=storage`, `start=<now-1h>`
  - Expected: 422 (`invalid`) with message "start and end params required"

- **Missing both start and end**
  - Input: valid basin/stream, `set=storage`
  - Expected: 422 (`invalid`)

- **Start > end**
  - Input: valid basin/stream, `set=storage`, `start=<now>`, `end=<now-1h>`
  - Expected: 422 (`invalid`)

- **End too far in future**
  - Input: valid basin/stream, `set=storage`, `start=<now-1h>`, `end=<now+10m>`
  - Expected: 422 (`invalid`)

- **Time range > 30 days**
  - Input: valid basin/stream, `set=storage`, `start=<now-40d>`, `end=<now>`
  - Expected: 422 (`invalid`)

- **Storage interval invalid**
  - Input: valid basin/stream, `set=storage`, `interval=hour`
  - Expected: 422 (`invalid`, only minute supported)

- **Empty time range returns empty data**
  - Input: valid basin/stream, `set=storage`, `start=<far-past>`, `end=<far-past+1h>`
  - Expected: 200, empty or zero values (not an error)

- **Permission denied**
  - Setup: token without `stream-metrics` op
  - Expected: 403 (`permission_denied`)

---

## Configuration Matrix

### TimeseriesInterval Values

- `minute` — Aggregate by minute
- `hour` — Aggregate by hour
- `day` — Aggregate by day

### MetricUnit Values

- `bytes` — Data size in bytes
- `operations` — Operation count

### AccountMetricSet Values

- `active-basins` — List of active basin names
- `account-ops` — Account operation counts

### BasinMetricSet Values

- `storage` — Basin storage in bytes
- `append-ops` — Append operation counts
- `read-ops` — Read operation counts
- `read-throughput` — Read bytes throughput
- `append-throughput` — Append bytes throughput
- `basin-ops` — Basin operation counts

### StreamMetricSet Values

- `storage` — Stream storage in bytes

---

## Error Codes Reference

- `422` `invalid`
  - Missing required parameters (start, end)
  - Invalid parameter format or value
  - Invalid metric set value
  - Validation message: "start and end params required for [MetricSet]"

- `403` `permission_denied`
  - Token lacks required metrics permission (`account-metrics`, `basin-metrics`, `stream-metrics`)
  - Basin name not in token's basin scope
  - Stream name not in token's stream scope

- `408` `request_timeout`
  - Request timeout

- `500` `other`
  - Internal server error

- `503` `unavailable`
  - Metrics service unavailable

> **Note:** The metrics API does NOT return 404 errors for non-existent basins or streams.
> Instead, it returns 403 if the resource is not in the token's scope, or 200 with empty data
> if the resource exists but has no metrics in the requested time range.
