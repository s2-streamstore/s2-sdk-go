# Stream API - SDK Test Matrix

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
> - Clean up resources (delete streams) after tests when running against real backends
> - Show progress as each test progresses
> - Set a timeout for each test
> - If the PUT based API for CreateOrReconfigure is not used in the SDK, don't implement the test
> - If idempotency tokens are an internal detail of the SDK, don't implement a specific test
>
> **Protocol-specific guidance:**
> - Some SDKs only support the S2S (binary) protocol for append/read operations, not JSON or SSE
> - If the SDK doesn't expose JSON-based append/read, skip tests for `s2-format` header variations
> - If the SDK doesn't support SSE streaming reads, skip SSE-specific test cases
> - Focus on testing the protocol(s) the SDK actually implements
> - The S2S protocol handles encoding internally, so `s2-format` header tests are only relevant for JSON transport

---

This document enumerates every knob/parameter of the Stream API to ensure SDK test coverage.

## Endpoints Overview

| Method | Endpoint | Operation ID | Description |
|--------|----------|--------------|-------------|
| GET | `/streams` | `list_streams` | List streams in basin |
| POST | `/streams` | `create_stream` | Create a new stream |
| GET | `/streams/{stream}` | `get_stream_config` | Get stream configuration |
| PUT | `/streams/{stream}` | `create_or_reconfigure_stream` | Create or reconfigure stream |
| DELETE | `/streams/{stream}` | `delete_stream` | Delete a stream |
| PATCH | `/streams/{stream}` | `reconfigure_stream` | Reconfigure stream |
| GET | `/streams/{stream}/records/tail` | `check_tail` | Get stream tail position |
| GET | `/streams/{stream}/records` | `read` | Read records from stream |
| POST | `/streams/{stream}/records` | `append` | Append records to stream |

---

## 1. List Streams

**`GET /streams`**

### Query Parameters

| Parameter | Type | Required | Default | Constraints | Description |
|-----------|------|----------|---------|-------------|-------------|
| `prefix` | string | No | `""` | - | Filter to streams whose names begin with this prefix |
| `start_after` | string | No | `""` | Must be >= `prefix` | Filter to streams whose names lexicographically start after this string |
| `limit` | integer | No | `1000` | Clamped to 1-1000 | Number of results (0 defaults to 1000, >1000 clamped to 1000) |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `ListStreamsResponse` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 503 | Basin still creating | `basin_creating` | `ErrorInfo` |

### Response Schema: `ListStreamsResponse`

| Field | Type | Description |
|-------|------|-------------|
| `streams` | `StreamInfo[]` | Matching streams (max 1000) |
| `has_more` | boolean | Indicates more streams match criteria |

### StreamInfo Object

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Stream name |
| `created_at` | string (ISO8601) | Creation timestamp |
| `deleted_at` | string (ISO8601) \| null | Deletion timestamp if deleting |

### Test Cases

| Test | Parameters | Expected |
|------|------------|----------|
| List all streams | none | 200, up to 1000 streams returned |
| List with prefix | `prefix=test-` | 200, only matching streams |
| List with start_after | `start_after=my-stream` | 200, streams after "my-stream" |
| List with limit | `limit=5` | 200, max 5 streams, check `has_more` |
| Pagination | `start_after` + `limit` | 200, correct pagination |
| Empty prefix | `prefix=""` | 200, all streams |
| Limit = 0 | `limit=0` | 200, up to 1000 streams (0 treated as default) |
| Limit > 1000 | `limit=1001` | 200, up to 1000 streams (clamped to max) |
| Invalid start_after < prefix | `prefix=z`, `start_after=a` | 400, validation error |
| Basin not found | invalid basin | 404 (`basin_not_found`) |
| Permission denied | token without `list-streams` op | 403 (`permission_denied`) |

---

## 2. Create Stream

**`POST /streams`**

### Headers

| Header | Type | Required | Description |
|--------|------|----------|-------------|
| `s2-request-token` | string | No | Client-specified idempotency token |

### Request Body: `CreateStreamRequest`

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes, any UTF-8 | Stream name |
| `config` | `StreamConfig` | No | - | Stream configuration |

### StreamConfig Object

> **Note:** Fields with default values are **omitted** from API responses.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `storage_class` | `StorageClass` | No | `express` (omitted) | Storage class for recent writes |
| `retention_policy` | `RetentionPolicy` | No | `{"age": 604800}` (7 days, omitted) | Retention policy |
| `timestamping` | `TimestampingConfig` | No | (see below, omitted) | Timestamping behavior |
| `delete_on_empty` | `DeleteOnEmptyConfig` | No | disabled (omitted) | Auto-delete empty streams |

### StorageClass Enum

| Value | Description |
|-------|-------------|
| `standard` | Standard storage (append tail latency < 500ms) |
| `express` | Express storage (append tail latency < 50ms) |

### RetentionPolicy (oneOf)

| Variant | Field | Type | Description |
|---------|-------|------|-------------|
| Age-based | `age` | integer (seconds) | Auto-trim records older than this (must be > 0) |
| Infinite | `infinite` | object `{}` | Retain unless explicitly trimmed |

### TimestampingConfig Object

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | `TimestampingMode` | `client-prefer` | How timestamps are handled |
| `uncapped` | boolean | `false` | Allow client timestamps to exceed arrival time |

### TimestampingMode Enum

| Value | Description |
|-------|-------------|
| `client-prefer` | Prefer client timestamp if provided |
| `client-require` | Require client timestamp |
| `arrival` | Use arrival time |

### DeleteOnEmptyConfig Object

> **Note:** When `min_age_secs` is 0 (disabled), the entire `delete_on_empty` field is **omitted** from API responses.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `min_age_secs` | integer | 0 (omitted) | Min age in seconds before empty stream deleted (0 = disabled) |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 201 | Stream created | - | `StreamInfo` |
| 400 | Bad request / invalid config | `invalid_argument`, `invalid_stream_config` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Stream already exists | `stream_exists` | `ErrorInfo` |
| 503 | Basin still creating | `basin_creating` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Create minimal stream | `{"stream": "test-stream"}` | 201 |
| Create with full config | All config fields | 201 |
| **storage_class = standard** | config with storage_class | 201, verify config |
| **storage_class = express** | config with storage_class | 201, verify config |
| **retention_policy.age** | `{"age": 86400}` | 201, verify 1-day retention |
| **retention_policy.infinite** | `{"infinite": {}}` | 201, verify infinite retention |
| **timestamping.mode = client-prefer** | nested config | 201 |
| **timestamping.mode = client-require** | nested config | 201 |
| **timestamping.mode = arrival** | nested config | 201 |
| **timestamping.uncapped = true** | nested config | 201 |
| **timestamping.uncapped = false** | nested config | 201 |
| **delete_on_empty.min_age_secs** | `{"min_age_secs": 3600}` | 201 |
| Idempotent create (same token) | Same request + same `s2-request-token` | 201 |
| Idempotent create (different token) | Same stream + different `s2-request-token` | 409 (`stream_exists`) |
| Name empty | `{"stream": ""}` | 400 |
| Name too long | `{"stream": "a" * 513}` (> 512 bytes) | 400 |
| Name with unicode | `{"stream": "test-stream-"}` | 201 |
| Duplicate name | Create same stream twice | 409 (`stream_exists`) |
| Basin not found | invalid basin | 404 (`basin_not_found`) |
| Invalid retention_policy.age = 0 | `retention_policy: {"age": 0}` | 400 (`invalid_stream_config`) |
| Invalid retention_policy.age < 0 | `retention_policy: {"age": -1}` | 400 (`invalid_argument`, JSON parse error) |
| Permission denied | token without `create-stream` op | 403 (`permission_denied`) |

---

## 3. Get Stream Config

**`GET /streams/{stream}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `StreamConfig` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Stream being deleted | `stream_being_deleted` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Get existing stream config | valid stream name | 200, full config |
| Get non-existent stream | name that doesn't exist | 404 (`stream_not_found`) |
| Get deleting stream | stream being deleted | 409 (`stream_being_deleted`) |
| Verify all config fields returned | - | All non-default fields present |
| Verify default fields omitted | stream with all defaults | Minimal response |
| Permission denied | token without `get-stream-config` op | 403 (`permission_denied`) |

---

## 4. Create or Reconfigure Stream

**`PUT /streams/{stream}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Request Body: `CreateOrReconfigureStreamRequest` (optional, can be null)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `config` | `StreamConfig` | No | Stream configuration |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 201 | Stream created | - | `StreamInfo` |
| 204 | No changes | - | - |
| 400 | Bad request / invalid config | `invalid_argument`, `invalid_stream_config` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Conflict | various | `ErrorInfo` |
| 503 | Basin still creating | `basin_creating` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Create new stream via PUT | new name + config | 201 |
| Reconfigure existing stream | existing name + new config | 204 |
| PUT with null body (no-op) | existing stream, null body | 204 |
| PUT with empty object | existing stream, `{}` | 204 |
| Create with defaults | new name, null body | 201 |
| Stream deleted during reconfigure | race condition | 409 (`stream_deleted_during_reconfiguration`) |
| Permission denied | token without appropriate op | 403 (`permission_denied`) |

---

## 5. Delete Stream

**`DELETE /streams/{stream}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 202 | Deletion accepted | - | - |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Delete existing stream | valid stream name | 202 |
| Delete non-existent stream | name that doesn't exist | 404 (`stream_not_found`) |
| Delete already deleting stream | stream being deleted | 202 (idempotent) |
| Verify stream not listable after delete | - | Stream not in list results |
| Permission denied | token without `delete-stream` op | 403 (`permission_denied`) |

---

## 6. Reconfigure Stream

**`PATCH /streams/{stream}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Request Body: `StreamReconfiguration`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `storage_class` | `StorageClass` \| null | No | Change storage class |
| `retention_policy` | `RetentionPolicy` \| null | No | Change retention policy |
| `timestamping` | `TimestampingReconfiguration` \| null | No | Change timestamping |
| `delete_on_empty` | `DeleteOnEmptyReconfiguration` \| null | No | Change delete-on-empty |

### TimestampingReconfiguration Object

| Field | Type | Description |
|-------|------|-------------|
| `mode` | `TimestampingMode` \| null | Change timestamping mode |
| `uncapped` | boolean \| null | Change uncapped setting |

### DeleteOnEmptyReconfiguration Object

| Field | Type | Description |
|-------|------|-------------|
| `min_age_secs` | integer \| null | Change min age (0 = disable) |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `StreamConfig` (updated) |
| 400 | Bad request / invalid config | `invalid_argument`, `bad_config` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Concurrent update | `concurrent_stream_update` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Change storage_class to standard | `{"storage_class": "standard"}` | 200, config updated |
| Change storage_class to express | `{"storage_class": "express"}` | 200, config updated |
| Change retention to age-based | `{"retention_policy": {"age": 3600}}` | 200 |
| Change retention to infinite | `{"retention_policy": {"infinite": {}}}` | 200 |
| Change timestamping mode | all 3 modes | 200 |
| Change timestamping.uncapped | true/false | 200 |
| Change delete_on_empty.min_age_secs | various values | 200 |
| Disable delete_on_empty | `{"delete_on_empty": {"min_age_secs": 0}}` | 200 |
| Reconfigure non-existent stream | name that doesn't exist | 404 (`stream_not_found`) |
| Empty reconfiguration | `{}` | 200, no changes |
| Partial reconfiguration | only some fields | 200, only specified fields changed |
| Invalid retention_policy.age = 0 | `{"retention_policy": {"age": 0}}` | 400 (`bad_config`) |
| Permission denied | token without `reconfigure-stream` op | 403 (`permission_denied`) |

---

## 7. Check Tail

**`GET /streams/{stream}/records/tail`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `TailResponse` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Stream being deleted | `stream_being_deleted` | `ErrorInfo` |

### Response Schema: `TailResponse`

| Field | Type | Description |
|-------|------|-------------|
| `tail.seq_num` | integer | Next sequence number to be assigned |
| `tail.timestamp` | integer | Timestamp of last record (milliseconds since epoch) |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Check tail on empty stream | new stream | 200, seq_num = 0 |
| Check tail after appends | stream with records | 200, seq_num > 0 |
| Check tail on non-existent stream | invalid name | 404 (`stream_not_found`) |
| Permission denied | token without `check-tail` op | 403 (`permission_denied`) |

---

## 8. Append Records

**`POST /streams/{stream}/records`**

> **Note:** SDKs may support different transports: JSON (unary), S2S (binary streaming). Test the transport(s) your SDK implements. The `s2-format` header only applies to JSON transport.

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Headers

| Header | Type | Required | Description |
|--------|------|----------|-------------|
| `s2-format` | string | No | Record data encoding: `raw` (default) or `base64` |

### Request Body: `AppendInput`

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `records` | `AppendRecord[]` | **Yes** | 1-1000 records, <= 1 MiB total | Records to append |
| `match_seq_num` | integer | No | - | Expected tail seq_num for conditional append |
| `fencing_token` | string | No | max 36 chars | Fencing token for coordination |

### AppendRecord Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `timestamp` | integer | No | Client timestamp (milliseconds since epoch) |
| `headers` | `Header[]` | No | Record headers |
| `body` | string | No | Record body (default empty) |

### Header Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | **Yes** | Header name (cannot be empty) |
| `value` | string | **Yes** | Header value |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `AppendAck` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Stream being deleted | `stream_being_deleted` | `ErrorInfo` |
| 412 | Precondition failed | - | `AppendConditionFailed` |
| 429 | Rate limited | `rate_limited` | `ErrorInfo` |

### Response Schema: `AppendAck`

| Field | Type | Description |
|-------|------|-------------|
| `start.seq_num` | integer | Sequence number of first appended record |
| `start.timestamp` | integer | Timestamp of first appended record |
| `end.seq_num` | integer | Sequence number after last appended record |
| `end.timestamp` | integer | Timestamp of last appended record |
| `tail.seq_num` | integer | Current tail sequence number |
| `tail.timestamp` | integer | Current tail timestamp |

### AppendConditionFailed (412 Response)

| Field | Type | Description |
|-------|------|-------------|
| `fencing_token_mismatch` | string | Expected fencing token (if token mismatch) |
| `seq_num_mismatch` | integer | Expected seq_num (if seq_num mismatch) |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Append single record | 1 record with body | 200, seq_num incremented |
| Append multiple records | 10 records | 200, all records appended |
| Append max batch size | 1000 records | 200 |
| Append with headers | record with headers | 200, headers preserved |
| Append with timestamp | record with client timestamp | 200, timestamp used (client-prefer mode) |
| Append empty body | record with empty body | 200 |
| Append with match_seq_num (success) | correct seq_num | 200 |
| Append with match_seq_num (failure) | wrong seq_num | 412 (`seq_num_mismatch`) |
| Append with fencing_token (success) | correct token | 200 |
| Append with fencing_token (failure) | wrong token | 412 (`fencing_token_mismatch`) |
| Append too many records | > 1000 records | 400 |
| Append too large batch | > 1 MiB | 400 |
| Append empty batch | 0 records | 400 |
| Append to non-existent stream | invalid name | 404 (`stream_not_found`) |
| Append header with empty name | header with name="" | 400 |
| Permission denied | token without `append` op | 403 (`permission_denied`) |

---

## 9. Read Records

**`GET /streams/{stream}/records`**

> **Note:** SDKs may support different transports: JSON (unary), SSE (streaming), S2S (binary streaming). Test the transport(s) your SDK implements. The `s2-format` header only applies to JSON/SSE transport.

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `stream` | string | **Yes** | 1-512 bytes | Stream name |

### Headers

| Header | Type | Required | Description |
|--------|------|----------|-------------|
| `s2-format` | string | No | Record data encoding: `raw` (default) or `base64` |

### Query Parameters - Start Position (mutually exclusive)

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `seq_num` | integer | No | - | Start from this sequence number |
| `timestamp` | integer | No | - | Start from records at/after this timestamp (ms) |
| `tail_offset` | integer | No | 0 | Start this many records before tail |

> **Note:** Only ONE of `seq_num`, `timestamp`, or `tail_offset` can be specified. Default is `tail_offset=0` (start from tail).

### Query Parameters - End/Limit

| Parameter | Type | Required | Default | Constraints | Description |
|-----------|------|----------|---------|-------------|-------------|
| `count` | integer | No | 1000 | <= 1000 | Max records to return |
| `bytes` | integer | No | 1 MiB | <= 1 MiB | Max metered bytes |
| `until` | integer | No | - | - | Exclusive timestamp to read until |
| `clamp` | boolean | No | `false` | - | Clamp to tail if position exceeds it |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `ReadBatch` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Stream not found | `stream_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Stream being deleted | `stream_being_deleted` | `ErrorInfo` |
| 416 | Range not satisfiable | - | `TailResponse` |

### Response Schema: `ReadBatch`

| Field | Type | Description |
|-------|------|-------------|
| `records` | `SequencedRecord[]` | Records read |
| `tail.seq_num` | integer | Current tail sequence number |
| `tail.timestamp` | integer | Current tail timestamp |

### SequencedRecord Object

| Field | Type | Description |
|-------|------|-------------|
| `seq_num` | integer | Record sequence number |
| `timestamp` | integer | Record timestamp (milliseconds) |
| `headers` | `Header[]` | Record headers |
| `body` | string | Record body |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Read from beginning | `seq_num=0` | 200, records from start |
| Read from tail | `tail_offset=0` | 200, empty or latest records |
| Read with offset from tail | `tail_offset=10` | 200, last 10 records |
| Read by timestamp | `timestamp=<ms>` | 200, records at/after timestamp |
| Read with count limit | `count=5` | 200, max 5 records |
| Read with bytes limit | `bytes=1024` | 200, max 1KB of records |
| Read until timestamp | `until=<ms>` | 200, records before timestamp |
| Read with clamp=true | `seq_num=999999`, `clamp=true` | 200, clamped to tail |
| Read with clamp=false (beyond tail) | `seq_num=999999`, `clamp=false` | 416 (`position_out_of_range`) |
| Read empty stream | new stream, `seq_num=0` | 200, empty records array |
| Read non-existent stream | invalid name | 404 (`stream_not_found`) |
| Multiple start params | `seq_num=0`, `timestamp=0` | 400 (mutually exclusive) |
| Permission denied | token without `read` op | 403 (`permission_denied`) |

---

## Complete Configuration Matrix

### StreamConfig Fields

> **Response serialization:** Fields with default values are omitted from responses.

| Field Path | Type | Default | Values to Test |
|------------|------|---------|----------------|
| `storage_class` | enum | `express` | `standard`, `express` (default, omitted) |
| `retention_policy` | oneOf | 7 days | `{"age": N}`, `{"infinite": {}}` (default omitted) |
| `retention_policy.age` | integer | 604800 | 1, 86400, 604800 (default), max value |
| `timestamping.mode` | enum | `client-prefer` | `client-prefer` (default), `client-require`, `arrival` |
| `timestamping.uncapped` | boolean | `false` | `true`, `false` (default) |
| `delete_on_empty.min_age_secs` | integer | 0 (omitted) | 0 (disabled, omitted), 60, 3600, max value |

---

## Free Tier Limitations

When running against an account on the Free tier, certain configurations will be rejected with `invalid_stream_config`. Tests should accept these failures as valid outcomes:

| Limitation | Example Message |
|------------|-----------------|
| Retention > 28 days | "Retention is currently limited to 28 days for free tier" |
| Infinite retention | "Retention is currently limited to 28 days for free tier" |
| Express storage class | "Express storage class is not available on free tier" |

---

## Error Codes Reference

| HTTP Code | Error Code | Scenario |
|-----------|------------|----------|
| 400 | `invalid_argument` | Invalid parameter format/value |
| 400 | `invalid_stream_config` | Stream config validation failed (including tier limits) |
| 400 | `bad_config` | Reconfiguration validation failed |
| 403 | `permission_denied` | Token lacks required permissions |
| 404 | `stream_not_found` | Stream does not exist |
| 404 | `basin_not_found` | Parent basin does not exist |
| 408 | `deadline_exceeded` | Request timeout |
| 409 | `stream_exists` | Stream name already taken |
| 409 | `stream_being_deleted` | Stream is being deleted |
| 409 | `stream_deleted_during_reconfiguration` | Stream deleted during PUT |
| 409 | `concurrent_stream_update` | Concurrent reconfiguration conflict |
| 412 | - | Append precondition failed (fencing/seq_num) |
| 416 | - | Read position beyond tail (returns `TailResponse`) |
| 429 | `rate_limited` | Rate limit exceeded |
| 500 | `internal` | Internal server error |
| 503 | `basin_creating` | Basin still initializing |

