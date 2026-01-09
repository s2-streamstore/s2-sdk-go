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
>
> **SDK method coverage:**
> - Review ALL public methods/functions exposed by the SDK before writing tests
> - Test every user-facing SDK method, not just the raw API endpoints
> - SDKs may expose helper methods (e.g., `fence()`, `trim()`, `set_fencing_token()`) that wrap command records
> - SDKs may expose convenience methods for common patterns (e.g., `append_record()` for single record, `append_batch()` for multiple)
> - Test client/session lifecycle methods: initialization, configuration, connection management, cleanup
> - Test any builder patterns, fluent APIs, or configuration options the SDK exposes
> - If the SDK has async/streaming variants of methods, test both
> - If the SDK exposes retry/backoff configuration, verify it works correctly
>
> **Error handling guidance:**
> - SDKs may validate inputs client-side before sending to server (e.g., batch size limits)
> - Client-side validation errors may not be HTTP status codes - just check that an error is returned
> - For tests expecting errors, verify an error occurred; don't require specific HTTP status codes for client-validated constraints
> - Server-side errors will have HTTP status codes and error codes as documented
>
> **Code quality guidance:**
> - Do NOT create redundant helper functions, wrapper types, or utility code that duplicates SDK functionality
> - Use the SDK's provided methods, builders, and types directly - don't wrap them unnecessarily
> - Avoid creating test-only abstractions when the SDK already provides clean APIs
> - Keep test code simple and direct - call SDK methods inline rather than through helper layers
> - If the SDK provides a method for an operation, use it instead of constructing requests manually

---

## Test Setup Guidance

**Critical: Understand stream state before testing reads**

Many read test cases depend on the stream having records. Before testing reads:
1. Create a fresh stream for isolation
2. If testing "read from beginning", first append some records, then read with `seq_num=0`
3. If testing an empty stream, understand that `seq_num=0` returns 416 (not 200 with empty array) because tail=0
4. If testing `clamp=true`, understand it only prevents errors for positions BEYOND the tail; you still get 416 at the tail without `wait`

**Test isolation patterns:**
- Create a unique stream name per test (e.g., `test-<operation>-<timestamp>`)
- Clean up streams after tests complete
- Don't share streams between tests that modify state

**Resource efficiency - Basin reuse:**
- Stream tests do NOT need a new basin per test - stream-level isolation is sufficient
- Create ONE shared basin for all stream tests (e.g., in `TestMain` or a setup function)
- Each test creates its own uniquely-named stream within the shared basin
- This avoids basin limit exhaustion and speeds up tests significantly
- Only basin-specific tests (create/delete/reconfigure basin) need their own basins
- Pattern:
  ```
  // In TestMain or suite setup:
  sharedBasin = createTestBasin("stream-tests-<timestamp>")

  // In each stream test:
  streamName = uniqueStreamName("test-<operation>")
  // ... test using sharedBasin and streamName
  defer deleteStream(streamName)
  ```

**Common test pattern mistakes to avoid:**

1. **"Read empty stream returns empty array"** - WRONG. Reading `seq_num=0` on an empty stream returns 416, not 200 with empty records. The correct test should verify 416 is returned.

2. **"clamp=true returns existing records"** - WRONG. `clamp=true` clamps to the tail, not to the last record. If the stream has 1 record (tail=1), reading with `seq_num=999999, clamp=true` clamped to position 1, but there's no record at position 1. Test should expect 416 OR use `wait` to long-poll.

3. **"Read from seq_num=0 on non-empty stream"** - CORRECT. If the stream has records, reading from `seq_num=0` returns records starting from the beginning.

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

### SDK-Specific Operations (via append)

| Operation | Description |
|-----------|-------------|
| `fence` | Set fencing token via command record |
| `trim` | Trim records via command record |

> **Note:** SDKs may expose additional helper methods that wrap these operations. See section 10 (Command Records) and section 11 (SDK Client Lifecycle) for SDK-specific tests.

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
| Name empty | `{"stream": ""}` | Error (SDK may validate client-side, or 400 from server) |
| Name too long | `{"stream": "a" * 513}` (> 512 bytes) | Error (SDK may validate client-side, or 400 from server) |
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
| `records` | `AppendRecord[]` | **Yes** | 1-1000 records, <= 1 MiB total | Records to append (SDK may validate client-side) |
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
| Append too many records | > 1000 records | Error (SDK validation or 400) |
| Append too large batch | > 1 MiB | Error (SDK validation or 400) |
| Append empty batch | 0 records | Error (SDK validation or 400) |
| Append to non-existent stream | invalid name | 404 (`stream_not_found`) |
| Append header with empty name (command record) | header with name="" | 200 (valid for commands) |
| Append header with empty name (non-command) | header name="" without command value | 400 |
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
| `tail_offset` | integer | No | 0 | Start this many records before the next seq_num (tail) |

> **Note:** Only ONE of `seq_num`, `timestamp`, or `tail_offset` can be specified. Default is `tail_offset=0`.
>
> **Important - Understanding 416 responses:**
> - The tail is the next seq_num that WILL be assigned to a new record. It points to a position where no record exists yet.
> - Reading returns 416 when the starting position is at or beyond the tail AND no `wait` is specified (or `wait=0`).
> - `tail_offset=0` means "start AT the tail" - this always returns 416 without `wait` because there's no record at that position yet.
> - `seq_num=0` on an empty stream returns 416 because the tail is also 0 (no records exist).
> - `clamp=true` brings the starting position TO the tail if beyond it, but you still get 416 because there's no record at the tail position.
> - To read records that exist: use `seq_num=0` on a non-empty stream, or `tail_offset=N` where N>0 to read the last N records.
> - To wait for new records: use `wait=<seconds>` to long-poll for records at/beyond the tail.

### Query Parameters - End/Limit

| Parameter | Type | Required | Default | Constraints | Description |
|-----------|------|----------|---------|-------------|-------------|
| `count` | integer | No | 1000 | <= 1000 | Max records to return |
| `bytes` | integer | No | 1 MiB | <= 1 MiB | Max metered bytes |
| `until` | integer | No | - | - | Exclusive timestamp to read until |
| `clamp` | boolean | No | `false` | - | If position > tail, clamp starting position TO the tail (not before it) |
| `wait` | integer | No | 0 | 0-60 seconds | Duration to wait for new records if at/beyond tail |

> **Note on `clamp`:** Setting `clamp=true` prevents a 416 error when requesting a position BEYOND the tail by adjusting the start position to the tail. However, since the tail is where the next record will be written (not an existing record), you will still get 416 unless you also specify `wait` to long-poll for new records.

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
| Read from beginning (non-empty stream) | `seq_num=0` on stream with records | 200, records from start |
| Read from tail (no wait) | `tail_offset=0` | 416 (tail position has no record yet) |
| Read from tail (with wait) | `tail_offset=0`, `wait=5` | 200 with records if appended, or 200 empty if timeout |
| Read last N records | `tail_offset=10` on stream with >=10 records | 200, last 10 records |
| Read by timestamp | `timestamp=<ms>` | 200, records at/after timestamp |
| Read with count limit | `count=5` | 200, max 5 records |
| Read with bytes limit | `bytes=1024` | 200, max 1KB of records |
| Read until timestamp | `until=<ms>` | 200, records before timestamp |
| Read with clamp=true (beyond tail) | `seq_num=999999`, `clamp=true` | 416 (clamped TO tail, but no record exists there) |
| Read with clamp=true + wait | `seq_num=999999`, `clamp=true`, `wait=5` | 200 (waits for records at tail) |
| Read with clamp=false (beyond tail) | `seq_num=999999`, `clamp=false` | 416 (position beyond tail) |
| Read empty stream from seq_num=0 | new stream, `seq_num=0` | 416 (tail is 0, no records exist) |
| Read empty stream with wait | new stream, `seq_num=0`, `wait=5` | 200 with records if appended, or 200 empty if timeout |
| Read non-existent stream | invalid name | 404 (`stream_not_found`) |
| Multiple start params | `seq_num=0`, `timestamp=0` | 400 (mutually exclusive) |
| Permission denied | token without `read` op | 403 (`permission_denied`) |

---

## 10. Command Records

Command records are special records that control stream behavior. SDKs typically expose these as helper methods.

### Fence Command

Sets or updates the fencing token for a stream. Subsequent appends with `fencing_token` must match.

| Field | Value |
|-------|-------|
| Header name | `""` (empty string) |
| Header value | `fence` |
| Body | New fencing token (max 36 chars) |

### Trim Command

Trims (deletes) all records before the specified sequence number.

| Field | Value |
|-------|-------|
| Header name | `""` (empty string) |
| Header value | `trim` |
| Body | 8-byte big-endian sequence number |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Set fencing token | Append fence command with token "my-token" | 200, token set |
| Append with correct fencing token | `fencing_token="my-token"` | 200 |
| Append with wrong fencing token | `fencing_token="wrong"` | 412 (`fencing_token_mismatch`) |
| Clear fencing token | Append fence command with empty body | 200, token cleared |
| Trim stream | Append trim command with seq_num | 200, records trimmed |
| Read after trim | Read from seq_num < trim point | Records not returned |
| Trim to future seq_num | trim_point > tail | 200 (no-op, nothing to trim) |

> **Note:** If the SDK exposes `fence()` or `trim()` helper methods, test those instead of manually constructing command records.

---

## 11. SDK Client Lifecycle

Test client initialization, configuration, and cleanup patterns exposed by the SDK.

### Test Cases

| Test | Description | Expected |
|------|-------------|----------|
| Client initialization | Create client with valid credentials | Client ready |
| Client with invalid token | Create client with bad auth token | Auth error on first operation |
| Client with custom endpoint | Configure non-default endpoint | Connects to specified endpoint |
| Client cleanup/close | Properly close/dispose client | Resources released, no leaks |
| Multiple concurrent clients | Create multiple client instances | All operate independently |
| Client reuse | Use same client for multiple operations | Works correctly |
| Connection recovery | Client reconnects after transient failure | Operations succeed after recovery |

### Streaming Session Tests (if SDK supports streaming)

| Test | Description | Expected |
|------|-------------|----------|
| Open append session | Create streaming append session | Session ready |
| Append multiple batches | Send multiple batches on same session | All batches acknowledged |
| Close append session | Gracefully close session | Final acks received, session closed |
| Open read session | Create streaming read session | Session ready, records flow |
| Cancel read session | Cancel/close read mid-stream | Session terminates cleanly |
| Session timeout | Session idle beyond timeout | Session closed, reconnect works |

> **Note:** Test all client/session creation patterns the SDK exposes (builders, factory methods, config objects, etc.)

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
| 416 | - | Read position at/beyond tail with no records available (returns `TailResponse`, not `ErrorInfo`) |
| 429 | `rate_limited` | Rate limit exceeded |
| 500 | `internal` | Internal server error |
| 503 | `basin_creating` | Basin still initializing |

