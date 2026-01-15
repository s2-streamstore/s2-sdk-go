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
> - Metrics may have lag - recently written data may not appear immediately
> - Timeseries data points are aggregated by interval (minute, hour, day)
> - Empty time ranges may return empty values arrays
> - Test with realistic time ranges (avoid requesting future data)

---

This document enumerates every knob/parameter of the Metrics API to ensure SDK test coverage.

## Endpoints Overview

- `GET /metrics` — `account_metrics` — Get account-level metrics
- `GET /metrics/{basin}` — `basin_metrics` — Get basin-level metrics
- `GET /metrics/{basin}/{stream}` — `stream_metrics` — Get stream-level metrics

---

## Common Types

### TimeseriesInterval

Aggregation interval for timeseries metrics:
- `minute` — Per-minute aggregation
- `hour` — Per-hour aggregation
- `day` — Per-day aggregation

### MetricUnit

Unit of measurement:
- `bytes` — Data size in bytes
- `operations` — Count of operations

### Metric Types

Metrics are returned as one of four types:

#### ScalarMetric
Single named value at a point in time.
```json
{
  "scalar": {
    "name": "string",
    "unit": "bytes" | "operations",
    "value": number
  }
}
```

#### AccumulationMetric
Named series of `(timestamp, value)` points representing accumulated values over intervals.
```json
{
  "accumulation": {
    "name": "string",
    "unit": "bytes" | "operations",
    "interval": "minute" | "hour" | "day",
    "values": [[timestamp_unix_epoch, value], ...]
  }
}
```

#### GaugeMetric
Named series of `(timestamp, value)` points each representing an instantaneous value.
```json
{
  "gauge": {
    "name": "string",
    "unit": "bytes" | "operations",
    "values": [[timestamp_unix_epoch, value], ...]
  }
}
```

#### LabelMetric
Set of string labels (e.g., list of basin names).
```json
{
  "label": {
    "name": "string",
    "values": ["string", ...]
  }
}
```

---

## 1. Account Metrics

**`GET /metrics`**

### Query Parameters

- `set` (AccountMetricSet, required)
  - Metric set to return
  - Values:
    - `active-basins` — Set of all basins that had at least one stream during specified period
    - `account-ops` — Count of account-level RPC operations, per interval

- `start` (integer, required)
  - Start timestamp as Unix epoch seconds
  - Required for all metric sets

- `end` (integer, required)
  - End timestamp as Unix epoch seconds
  - Required for all metric sets

- `interval` (TimeseriesInterval, optional)
  - Interval to aggregate over for timeseries metric sets
  - Values: `minute`, `hour`, `day`
  - Required for timeseries metric sets

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`

- `400` — Bad request
  - Code: `invalid`
  - Cause: missing required parameters, invalid set name

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `account-metrics` permission

- `408` — Timeout
  - Code: `timeout`

- `422` — Invalid arguments
  - Code: `invalid`
  - Cause: invalid time range, conflicting parameters

### Response Schema: MetricSetResponse

- `values` (array of `Metric`)
  - Metrics comprising the set

### AccountMetricSet Values

#### `active-basins`
Returns: `LabelMetric`
- `name`: "active_basins"
- `values`: Array of basin names that were active during the period

#### `account-ops`
Returns: `AccumulationMetric`
- `name`: "account_ops"
- `unit`: "operations"
- `interval`: As specified in request
- `values`: Array of `[timestamp, count]` pairs

### Test Cases

- **Get active basins**
  - Parameters: `set=active-basins`, `start=<epoch>`, `end=<epoch>`
  - Expected: 200, label metric with basin names active during range

- **Get account ops (minute interval)**
  - Parameters: `set=account-ops`, `start=<epoch>`, `end=<epoch>`, `interval=minute`
  - Expected: 200, accumulation metric per minute

- **Get account ops (hour interval)**
  - Parameters: `set=account-ops`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric per hour

- **Get account ops (day interval)**
  - Parameters: `set=account-ops`, `start=<epoch>`, `end=<epoch>`, `interval=day`
  - Expected: 200, accumulation metric per day

- **Missing set parameter**
  - Parameters: none
  - Expected: 400 (`invalid`)

- **Missing start/end parameters**
  - Parameters: `set=active-basins` (without start/end)
  - Expected: 422 (`invalid`)

- **Invalid set value**
  - Parameters: `set=invalid-set`
  - Expected: 400 (`invalid`)

- **Timeseries without interval**
  - Parameters: `set=account-ops`, `start=<epoch>`, `end=<epoch>`
  - Expected: 422 (`invalid`) or uses default interval

- **Start > end**
  - Parameters: `set=account-ops`, `start=2000`, `end=1000`
  - Expected: 422 (`invalid`)

- **Future time range**
  - Parameters: `set=account-ops`, `start=<future>`, `end=<future>`
  - Expected: 200, empty values array

- **Permission denied**
  - Setup: token without `account-metrics` op
  - Expected: 403 (`permission_denied`)

---

## 2. Basin Metrics

**`GET /metrics/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

### Query Parameters

- `set` (BasinMetricSet, required)
  - Metric set to return
  - Values:
    - `storage` — Amount of stored data, per hour, aggregated over all streams
    - `append-ops` — Append operations, per interval
    - `read-ops` — Read operations, per interval
    - `read-throughput` — Read bytes, per interval
    - `append-throughput` — Appended bytes, per interval
    - `basin-ops` — Count of basin-level RPC operations, per interval

- `start` (integer, optional)
  - Start timestamp as Unix epoch seconds

- `end` (integer, optional)
  - End timestamp as Unix epoch seconds

- `interval` (TimeseriesInterval, optional)
  - Interval to aggregate over
  - Values: `minute`, `hour`, `day`

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`

- `400` — Bad request
  - Code: `invalid`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `basin-metrics` permission

- `408` — Timeout
  - Code: `timeout`

- `422` — Invalid arguments
  - Code: `invalid`

- `503` — Basin still creating
  - Code: `unavailable`

> **Note:** Non-existent basins return 200 with empty values array, not 404.

### BasinMetricSet Values

#### `storage`
Returns: `GaugeMetric`
- `name`: "storage"
- `unit`: "bytes"
- `values`: Array of `[timestamp, bytes]` pairs (hourly samples)

#### `append-ops`
Returns: `AccumulationMetric`
- `name`: "append_ops"
- `unit`: "operations"
- `values`: Array of `[timestamp, count]` pairs

#### `read-ops`
Returns: `AccumulationMetric`
- `name`: "read_ops"
- `unit`: "operations"
- `values`: Array of `[timestamp, count]` pairs

#### `read-throughput`
Returns: `AccumulationMetric`
- `name`: "read_throughput"
- `unit`: "bytes"
- `values`: Array of `[timestamp, bytes]` pairs

#### `append-throughput`
Returns: `AccumulationMetric`
- `name`: "append_throughput"
- `unit`: "bytes"
- `values`: Array of `[timestamp, bytes]` pairs

#### `basin-ops`
Returns: `AccumulationMetric`
- `name`: "basin_ops"
- `unit`: "operations"
- `values`: Array of `[timestamp, count]` pairs

### Test Cases

- **Get storage metrics**
  - Parameters: `set=storage`, `start=<epoch>`, `end=<epoch>`
  - Expected: 200, gauge metric with storage values

- **Get append-ops (minute)**
  - Parameters: `set=append-ops`, `start=<epoch>`, `end=<epoch>`, `interval=minute`
  - Expected: 200, accumulation metric

- **Get append-ops (hour)**
  - Parameters: `set=append-ops`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric

- **Get append-ops (day)**
  - Parameters: `set=append-ops`, `start=<epoch>`, `end=<epoch>`, `interval=day`
  - Expected: 200, accumulation metric

- **Get read-ops**
  - Parameters: `set=read-ops`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric

- **Get read-throughput**
  - Parameters: `set=read-throughput`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric

- **Get append-throughput**
  - Parameters: `set=append-throughput`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric

- **Get basin-ops**
  - Parameters: `set=basin-ops`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 200, accumulation metric

- **Non-existent basin**
  - Setup: invalid basin name
  - Expected: 200, empty values array

- **Basin still creating**
  - Setup: basin in creating state
  - Expected: 503 (`unavailable`)

- **Missing set parameter**
  - Parameters: none
  - Expected: 400 (`invalid`)

- **Invalid set value**
  - Parameters: `set=invalid-set`
  - Expected: 400 (`invalid`)

- **Permission denied**
  - Setup: token without `basin-metrics` op
  - Expected: 403 (`permission_denied`)

- **Basin outside token scope**
  - Setup: token scoped to different basin
  - Expected: 403 (`permission_denied`)

---

## 3. Stream Metrics

**`GET /metrics/{basin}/{stream}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Query Parameters

- `set` (StreamMetricSet, required)
  - Metric set to return
  - Values:
    - `storage` — Amount of stored data, per minute, for a specific stream

- `start` (integer, optional)
  - Start timestamp as Unix epoch seconds

- `end` (integer, optional)
  - End timestamp as Unix epoch seconds

- `interval` (TimeseriesInterval, optional)
  - Interval to aggregate over
  - Values: `minute` (only minute-level aggregation currently supported)
  - Note: `hour` and `day` intervals return 422 (`invalid`)

### Response Codes

- `200` — Success
  - Body: `MetricSetResponse`

- `400` — Bad request
  - Code: `invalid`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `stream-metrics` permission

- `408` — Timeout
  - Code: `timeout`

- `409` — Stream being deleted
  - Code: `stream_deletion_pending`

- `422` — Invalid arguments
  - Code: `invalid`

- `503` — Unavailable
  - Code: `unavailable`

> **Note:** Non-existent basins/streams return 200 with empty values array, not 404.

### StreamMetricSet Values

#### `storage`
Returns: `GaugeMetric`
- `name`: "storage"
- `unit`: "bytes"
- `values`: Array of `[timestamp, bytes]` pairs (per-minute samples)

### Test Cases

- **Get stream storage (minute)**
  - Parameters: `set=storage`, `start=<epoch>`, `end=<epoch>`, `interval=minute`
  - Expected: 200, gauge metric with storage values

- **Get stream storage (hour) - not supported**
  - Parameters: `set=storage`, `start=<epoch>`, `end=<epoch>`, `interval=hour`
  - Expected: 422 (`invalid`)

- **Get stream storage (day) - not supported**
  - Parameters: `set=storage`, `start=<epoch>`, `end=<epoch>`, `interval=day`
  - Expected: 422 (`invalid`)

- **Non-existent stream**
  - Setup: invalid stream name
  - Expected: 200, empty values array

- **Non-existent basin**
  - Setup: invalid basin name
  - Expected: 200, empty values array

- **Stream being deleted**
  - Setup: stream in deleting state
  - Expected: 409 (`stream_deletion_pending`)

- **Missing set parameter**
  - Parameters: none
  - Expected: 400 (`invalid`)

- **Invalid set value**
  - Parameters: `set=invalid-set`
  - Expected: 400 (`invalid`)

- **Permission denied**
  - Setup: token without `stream-metrics` op
  - Expected: 403 (`permission_denied`)

- **Stream outside token scope**
  - Setup: token scoped to different stream
  - Expected: 403 (`permission_denied`)

- **Stream with unicode name**
  - Setup: stream with unicode characters
  - Parameters: `set=storage`
  - Expected: 200

---

## Complete Metric Set Matrix

### Account-Level Metric Sets

| Set | Metric Type | Unit | Description |
|-----|-------------|------|-------------|
| `active-basins` | Label | - | Basin names active during period |
| `account-ops` | Accumulation | operations | Account-level API operations count |

### Basin-Level Metric Sets

| Set | Metric Type | Unit | Description |
|-----|-------------|------|-------------|
| `storage` | Gauge | bytes | Total storage used by all streams |
| `append-ops` | Accumulation | operations | Append operation count |
| `read-ops` | Accumulation | operations | Read operation count |
| `read-throughput` | Accumulation | bytes | Bytes read |
| `append-throughput` | Accumulation | bytes | Bytes appended |
| `basin-ops` | Accumulation | operations | Basin-level API operations count |

### Stream-Level Metric Sets

| Set | Metric Type | Unit | Description |
|-----|-------------|------|-------------|
| `storage` | Gauge | bytes | Storage used by the stream |

---

## Time Range Behavior

### Default Behavior
- If `start` is omitted, defaults to earliest available data
- If `end` is omitted, defaults to current time
- Empty time ranges return empty `values` arrays

### Interval Alignment
- Data points are aligned to interval boundaries
- `minute`: Aligned to minute start (seconds = 0)
- `hour`: Aligned to hour start (minutes = seconds = 0)
- `day`: Aligned to day start (UTC midnight)

### Data Lag
- Recent data may not appear immediately
- Expect up to several minutes of lag for real-time metrics
- Historical data is more reliable for testing

---

## SDK Client Lifecycle Tests

### Test Cases

- **Metrics client initialization**
  - Description: create metrics client from main client
  - Expected: client ready for metrics operations

- **Sequential metrics calls**
  - Description: call multiple metrics endpoints in sequence
  - Expected: all calls succeed

- **Concurrent metrics calls**
  - Description: call multiple metrics endpoints concurrently
  - Expected: all calls succeed, no interference

- **Metrics after data write**
  - Description: write data, then query metrics (with delay)
  - Expected: metrics reflect written data eventually

---

## Error Codes Reference

- `400` `bad_query`
  - Invalid query parameters (missing set, invalid interval)

- `400` `bad_path`
  - Invalid path parameter (basin or stream name)

- `422` `invalid`
  - Validation errors (invalid time range, unknown set)

- `403` `permission_denied`
  - Token lacks required metrics permission

- `404` `basin_not_found`
  - Basin does not exist

- `404` `stream_not_found`
  - Stream does not exist

- `408` `timeout`
  - Request timeout

- `409` `stream_deletion_pending`
  - Stream is being deleted

- `503` `unavailable`
  - Basin still creating or service unavailable

- `500` `other`
  - Internal server error
