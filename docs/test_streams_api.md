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
>
> **Linter and formatter compliance:**
> - Always run the project's linter and formatter after generating code
> - Fix ALL linter warnings and errors - do not leave any unresolved
> - Follow the project's existing code style (check existing test files for patterns)
> - Common tools by language:
>   - **Go:** `go fmt`, `golangci-lint`, `goimports`
>   - **Rust:** `cargo fmt`, `cargo clippy`
>   - **Python:** `ruff`, `black`, `mypy`
>   - **TypeScript/JavaScript:** `eslint`, `prettier`
> - If the project has a `Makefile`, `justfile`, or CI config, check for lint/format commands
> - Unused imports, unused variables, and dead code should be removed
> - Follow naming conventions specific to the language (e.g., `snake_case` in Python/Rust, `camelCase` in Go/JS)

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

**Basin readiness:**
- After creating a basin, you MUST wait for it to become active before performing stream operations
- A newly created basin returns 503 (`basin_creating`) for stream operations until ready
- Implement a polling loop that calls `GetBasinConfig` until it succeeds (or check state)
- Pattern:
  ```
  createBasin(name)
  while true:
    try:
      getBasinConfig(name)  // or check state != creating
      break
    catch 503 basin_creating:
      sleep(500ms)
  // Basin is now ready for stream operations
  ```

**Common test pattern mistakes to avoid:**

1. **"Read empty stream returns empty array"** - WRONG. Reading `seq_num=0` on an empty stream returns 416, not 200 with empty records. The correct test should verify 416 is returned.

2. **"clamp=true returns existing records"** - WRONG. `clamp=true` clamps to the tail, not to the last record. If the stream has 1 record (tail=1), reading with `seq_num=999999, clamp=true` clamped to position 1, but there's no record at position 1. Test should expect 416 OR use `wait` to long-poll.

3. **"Read from seq_num=0 on non-empty stream"** - CORRECT. If the stream has records, reading from `seq_num=0` returns records starting from the beginning.

4. **"Deleted stream is immediately inaccessible"** - WRONG. After DELETE returns 202, the stream enters "being deleted" state. Operations on it return 409 (`stream_deletion_pending`) for ~60 seconds. It may still appear in list results with `deleted_at` set.

5. **"Trimmed records are immediately unavailable"** - WRONG. Trim is eventually consistent. Records may still be readable for a period after trim succeeds.

---

This document enumerates every knob/parameter of the Stream API to ensure SDK test coverage.

## Endpoints Overview

- `GET /streams` — `list_streams` — List streams in basin
- `POST /streams` — `create_stream` — Create a new stream
- `GET /streams/{stream}` — `get_stream_config` — Get stream configuration
- `PUT /streams/{stream}` — `create_or_reconfigure_stream` — Create or reconfigure stream
- `DELETE /streams/{stream}` — `delete_stream` — Delete a stream
- `PATCH /streams/{stream}` — `reconfigure_stream` — Reconfigure stream
- `GET /streams/{stream}/records/tail` — `check_tail` — Get stream tail position
- `GET /streams/{stream}/records` — `read` — Read records from stream
- `POST /streams/{stream}/records` — `append` — Append records to stream

### SDK-Specific Operations (via append)

- `fence` — Set fencing token via command record
- `trim` — Trim records via command record

> **Note:** SDKs may expose additional helper methods that wrap these operations. See section 10 (Command Records) and section 11 (SDK Client Lifecycle) for SDK-specific tests.

---

## 1. List Streams

**`GET /streams`**

### Query Parameters

- `prefix` (string, optional, default `""`)
  - Filter to streams whose names begin with this prefix

- `start_after` (string, optional, default `""`)
  - Filter to streams whose names lexicographically start after this string
  - Constraint: must be >= `prefix`

- `limit` (integer, optional, default `1000`)
  - Number of results
  - Clamped to 1-1000
  - 0 is treated as default (1000)
  - Values > 1000 are clamped to 1000

### Response Codes

- `200` — Success
  - Body: `ListStreamsResponse`

- `400` — Bad request
  - Code: `invalid_argument`
  - Cause: invalid parameter format/value

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `503` — Basin still creating
  - Code: `basin_creating`

### Response Schema: ListStreamsResponse

- `streams` (array of `StreamInfo`)
  - Matching streams (max 1000)

- `has_more` (boolean)
  - Indicates more streams match criteria

### StreamInfo Object

- `name` (string)
  - Stream name

- `created_at` (string, ISO8601)
  - Creation timestamp

- `deleted_at` (string, ISO8601, optional)
  - Deletion timestamp if being deleted
  - Stream may still appear in list for ~60s after deletion

### Test Cases

- **List all streams**
  - Parameters: none
  - Expected: 200, up to 1000 streams returned

- **List with prefix**
  - Parameters: `prefix=test-`
  - Expected: 200, only matching streams

- **List with start_after**
  - Parameters: `start_after=my-stream`
  - Expected: 200, streams after "my-stream"

- **List with limit**
  - Parameters: `limit=5`
  - Expected: 200, max 5 streams, check `has_more`

- **Pagination**
  - Parameters: `start_after` + `limit`
  - Expected: 200, correct pagination

- **Empty prefix**
  - Parameters: `prefix=""`
  - Expected: 200, all streams

- **Limit = 0**
  - Parameters: `limit=0`
  - Expected: 200, up to 1000 streams (0 treated as default)

- **Limit > 1000**
  - Parameters: `limit=1001`
  - Expected: 200, up to 1000 streams (clamped to max)

- **Invalid start_after < prefix**
  - Parameters: `prefix=z`, `start_after=a`
  - Expected: 422 (`invalid`)

- **Basin not found**
  - Setup: invalid basin
  - Expected: 404 (`basin_not_found`)

- **Permission denied**
  - Setup: token without `list-streams` op
  - Expected: 403 (`permission_denied`)

---

## 2. Create Stream

**`POST /streams`**

### Headers

- `s2-request-token` (string, optional)
  - Client-specified idempotency token
  - Max 36 bytes

### Request Body: CreateStreamRequest

- `stream` (string, required)
  - Stream name
  - Constraints:
    - 1-512 bytes
    - Any UTF-8 (more permissive than basin names)

- `config` (StreamConfig, optional)
  - Stream configuration

### StreamConfig Object

> **Note:** Fields with default values are **omitted** from API responses.

- `storage_class` (StorageClass, default `express`, omitted)
  - Storage class for recent writes
  - Values: `standard`, `express`

- `retention_policy` (RetentionPolicy, default 7 days, omitted)
  - Retention policy
  - Variants:
    - `{"age": <seconds>}` — Age-based (must be > 0)
    - `{"infinite": {}}` — Retain unless explicitly trimmed

- `timestamping` (TimestampingConfig, omitted when defaults)
  - Timestamping behavior

- `delete_on_empty` (DeleteOnEmptyConfig, omitted when disabled)
  - Auto-delete empty streams

### TimestampingConfig Object

- `mode` (TimestampingMode, default `client-prefer`)
  - Values:
    - `client-prefer` — Prefer client timestamp if provided
    - `client-require` — Require client timestamp
    - `arrival` — Use arrival time

- `uncapped` (boolean, default `false`)
  - Allow client timestamps to exceed arrival time

### DeleteOnEmptyConfig Object

> **Note:** When `min_age_secs` is 0 (disabled), the entire `delete_on_empty` field is **omitted** from API responses.

- `min_age_secs` (integer, default 0, omitted when 0)
  - Min age in seconds before empty stream deleted
  - 0 = disabled

### Response Codes

- `201` — Stream created
  - Body: `StreamInfo`

- `400` — Bad request / invalid config
  - Codes: `invalid_argument`, `invalid_stream_config`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Stream already exists
  - Code: `resource_already_exists`

- `503` — Basin still creating
  - Code: `basin_creating`

### Test Cases

- **Create minimal stream**
  - Input: `{"stream": "test-stream"}`
  - Expected: 201

- **Create with full config**
  - Input: all config fields
  - Expected: 201

- **storage_class = standard**
  - Input: config with storage_class
  - Expected: 201, verify config

- **storage_class = express**
  - Input: config with storage_class
  - Expected: 201, verify config

- **retention_policy.age**
  - Input: `{"age": 86400}`
  - Expected: 201, verify 1-day retention

- **retention_policy.infinite**
  - Input: `{"infinite": {}}`
  - Expected: 201, verify infinite retention

- **timestamping.mode = client-prefer**
  - Input: nested config
  - Expected: 201

- **timestamping.mode = client-require**
  - Input: nested config
  - Expected: 201

- **timestamping.mode = arrival**
  - Input: nested config
  - Expected: 201

- **timestamping.uncapped = true**
  - Input: nested config
  - Expected: 201

- **timestamping.uncapped = false**
  - Input: nested config
  - Expected: 201

- **delete_on_empty.min_age_secs**
  - Input: `{"min_age_secs": 3600}`
  - Expected: 201

- **Idempotent create (same token)**
  - Input: same request + same `s2-request-token`
  - Expected: 201

- **Idempotent create (different token)**
  - Input: same stream + different `s2-request-token`
  - Expected: 409 (`resource_already_exists`)

- **Name empty**
  - Input: `{"stream": ""}`
  - Expected: Error (SDK may validate client-side, or 400 from server)

- **Name too long**
  - Input: `{"stream": "a" * 513}` (> 512 bytes)
  - Expected: Error (SDK may validate client-side, or 400 from server)

- **Name with unicode**
  - Input: `{"stream": "test-stream-日本語"}`
  - Expected: 201

- **Duplicate name**
  - Input: create same stream twice
  - Expected: 409 (`resource_already_exists`)

- **Basin not found**
  - Setup: invalid basin
  - Expected: 404 (`basin_not_found`)

- **Invalid retention_policy.age = 0**
  - Input: `retention_policy: {"age": 0}`
  - Expected: 422 (`invalid`)

- **Invalid retention_policy.age < 0**
  - Input: `retention_policy: {"age": -1}`
  - Expected: 400 (`invalid_argument`, JSON parse error)

- **Permission denied**
  - Setup: token without `create-stream` op
  - Expected: 403 (`permission_denied`)

---

## 3. Get Stream Config

**`GET /streams/{stream}`**

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Response Codes

- `200` — Success
  - Body: `StreamConfig`

- `400` — Bad request
  - Code: `invalid_argument`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Stream being deleted
  - Code: `stream_deletion_pending`

### Test Cases

- **Get existing stream config**
  - Input: valid stream name
  - Expected: 200, full config

- **Get non-existent stream**
  - Input: name that doesn't exist
  - Expected: 404 (`stream_not_found`)

- **Get deleting stream**
  - Input: stream being deleted
  - Expected: 409 (`stream_deletion_pending`)

- **Verify all config fields returned**
  - Expected: all non-default fields present

- **Verify default fields omitted**
  - Setup: stream with all defaults
  - Expected: minimal response

- **Permission denied**
  - Setup: token without `get-stream-config` op
  - Expected: 403 (`permission_denied`)

---

## 4. Create or Reconfigure Stream

**`PUT /streams/{stream}`**

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Request Body: CreateOrReconfigureStreamRequest (optional, can be null)

- `config` (StreamConfig, optional)
  - Stream configuration

### Response Codes

- `201` — Stream created
  - Body: `StreamInfo`

- `204` — No changes

- `400` — Bad request / invalid config
  - Codes: `invalid_argument`, `invalid_stream_config`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Conflict
  - Codes: `stream_deletion_pending`, `transaction_conflict`

- `503` — Basin still creating
  - Code: `basin_creating`

### Test Cases

- **Create new stream via PUT**
  - Input: new name + config
  - Expected: 201

- **Reconfigure existing stream**
  - Input: existing name + new config
  - Expected: 204

- **PUT with null body (no-op)**
  - Input: existing stream, null body
  - Expected: 204

- **PUT with empty object**
  - Input: existing stream, `{}`
  - Expected: 204

- **Create with defaults**
  - Input: new name, null body
  - Expected: 201

- **Stream deleted during reconfigure**
  - Setup: race condition
  - Expected: 409 (`stream_deletion_pending`)

- **Permission denied**
  - Setup: token without appropriate op
  - Expected: 403 (`permission_denied`)

---

## 5. Delete Stream

**`DELETE /streams/{stream}`**

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Response Codes

- `202` — Deletion accepted (async operation)

- `400` — Bad request
  - Code: `invalid_argument`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

### Test Cases

- **Delete existing stream**
  - Input: valid stream name
  - Expected: 202

- **Delete non-existent stream**
  - Input: name that doesn't exist
  - Expected: 404 (`stream_not_found`)

- **Delete already deleting stream**
  - Input: stream being deleted
  - Expected: 202 (idempotent)

- **Verify stream not listable after delete**
  - Expected: stream not in list results (eventually)

- **Permission denied**
  - Setup: token without `delete-stream` op
  - Expected: 403 (`permission_denied`)

---

## 6. Reconfigure Stream

**`PATCH /streams/{stream}`**

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Request Body: StreamReconfiguration

> **Note:** Fields use partial update semantics — `null` means no change, absent means no change, explicit value means update.

- `storage_class` (StorageClass | null, optional)
  - Change storage class

- `retention_policy` (RetentionPolicy | null, optional)
  - Change retention policy

- `timestamping` (TimestampingReconfiguration | null, optional)
  - Change timestamping config

- `delete_on_empty` (DeleteOnEmptyReconfiguration | null, optional)
  - Change delete-on-empty config

### TimestampingReconfiguration Object

- `mode` (TimestampingMode | null)
  - Change timestamping mode

- `uncapped` (boolean | null)
  - Change uncapped setting

### DeleteOnEmptyReconfiguration Object

- `min_age_secs` (integer | null)
  - Change min age (0 = disable)

### Response Codes

- `200` — Success
  - Body: `StreamConfig` (updated)

- `400` — Bad request / invalid config
  - Codes: `invalid_argument`, `invalid`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Concurrent update
  - Code: `transaction_conflict`

### Test Cases

- **Change storage_class to standard**
  - Input: `{"storage_class": "standard"}`
  - Expected: 200, config updated

- **Change storage_class to express**
  - Input: `{"storage_class": "express"}`
  - Expected: 200, config updated

- **Change retention to age-based**
  - Input: `{"retention_policy": {"age": 3600}}`
  - Expected: 200

- **Change retention to infinite**
  - Input: `{"retention_policy": {"infinite": {}}}`
  - Expected: 200

- **Change timestamping mode**
  - Input: all 3 modes
  - Expected: 200

- **Change timestamping.uncapped**
  - Input: true/false
  - Expected: 200

- **Change delete_on_empty.min_age_secs**
  - Input: various values
  - Expected: 200

- **Disable delete_on_empty**
  - Input: `{"delete_on_empty": {"min_age_secs": 0}}`
  - Expected: 200

- **Reconfigure non-existent stream**
  - Input: name that doesn't exist
  - Expected: 404 (`stream_not_found`)

- **Empty reconfiguration**
  - Input: `{}`
  - Expected: 200, no changes

- **Partial reconfiguration**
  - Input: only some fields
  - Expected: 200, only specified fields changed

- **Invalid retention_policy.age = 0**
  - Input: `{"retention_policy": {"age": 0}}`
  - Expected: 422 (`invalid`)

- **Concurrent update conflict**
  - Setup: race condition
  - Expected: 409 (`transaction_conflict`)

- **Permission denied**
  - Setup: token without `reconfigure-stream` op
  - Expected: 403 (`permission_denied`)

---

## 7. Check Tail

**`GET /streams/{stream}/records/tail`**

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Response Codes

- `200` — Success
  - Body: `TailResponse`

- `400` — Bad request
  - Code: `invalid_argument`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Stream being deleted
  - Code: `stream_deletion_pending`

### Response Schema: TailResponse

- `tail.seq_num` (integer)
  - Next sequence number to be assigned

- `tail.timestamp` (integer)
  - Timestamp of last record (milliseconds since epoch)

### Test Cases

- **Check tail on empty stream**
  - Setup: new stream
  - Expected: 200, seq_num = 0

- **Check tail after appends**
  - Setup: stream with records
  - Expected: 200, seq_num > 0

- **Check tail on non-existent stream**
  - Input: invalid name
  - Expected: 404 (`stream_not_found`)

- **Permission denied**
  - Setup: token without `check-tail` op
  - Expected: 403 (`permission_denied`)

---

## 8. Append Records

**`POST /streams/{stream}/records`**

> **Note:** SDKs may support different transports: JSON (unary), S2S (binary streaming). Test the transport(s) your SDK implements. The `s2-format` header only applies to JSON transport.

> **Important - Timestamp behavior:**
> - **Monotonicity guaranteed:** Timestamps are always adjusted UP to maintain monotonicity. If you provide a timestamp lower than the previous record's timestamp, it will be automatically increased.
> - **`timestamping.mode=client-require`:** Appends WITHOUT a timestamp fail with 400 "Record timestamp missing".
> - **`timestamping.mode=client-prefer` (default):** If timestamp is omitted, arrival time is used.
> - **`timestamping.mode=arrival`:** Client timestamps are ignored; arrival time is always used.
> - **`uncapped=false` (default):** Future timestamps (greater than arrival time) are capped to arrival time.
> - **`uncapped=true`:** Future timestamps are preserved (useful for backfilling data).

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Headers

- `s2-format` (string, optional)
  - Record data encoding: `raw` (default) or `base64`
  - Only applies to JSON transport

### Request Body: AppendInput

- `records` (array of AppendRecord, required)
  - Records to append
  - Constraints:
    - 1-1000 records per batch
    - <= 1 MiB total (metered bytes)
  - SDK may validate client-side

- `match_seq_num` (integer, optional)
  - Expected tail seq_num for conditional append

- `fencing_token` (string, optional)
  - Fencing token for coordination
  - Max 36 bytes

### AppendRecord Object

- `timestamp` (integer, optional)
  - Client timestamp (milliseconds since epoch)

- `headers` (array of Header, optional)
  - Record headers

- `body` (string, optional, default empty)
  - Record body

### Header Object

- `name` (string, required)
  - Header name
  - Can be empty for command records

- `value` (string, required)
  - Header value

### Response Codes

- `200` — Success
  - Body: `AppendAck`

- `400` — Bad request
  - Code: `invalid_argument`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Stream being deleted
  - Code: `stream_deletion_pending`

- `412` — Precondition failed
  - Body: `AppendConditionFailed`

- `429` — Rate limited
  - Code: `rate_limited`

### Response Schema: AppendAck

- `start.seq_num` (integer)
  - Sequence number of first appended record

- `start.timestamp` (integer)
  - Timestamp of first appended record

- `end.seq_num` (integer)
  - Sequence number after last appended record

- `end.timestamp` (integer)
  - Timestamp of last appended record

- `tail.seq_num` (integer)
  - Current tail sequence number

- `tail.timestamp` (integer)
  - Current tail timestamp

### AppendConditionFailed (412 Response)

- `fencing_token_mismatch` (string)
  - Expected fencing token (if token mismatch)

- `seq_num_mismatch` (integer)
  - Expected seq_num (if seq_num mismatch)

### Test Cases

- **Append single record**
  - Input: 1 record with body
  - Expected: 200, seq_num incremented

- **Append multiple records**
  - Input: 10 records
  - Expected: 200, all records appended

- **Append max batch size**
  - Input: 1000 records
  - Expected: 200

- **Append with headers**
  - Input: record with headers
  - Expected: 200, headers preserved

- **Append with timestamp**
  - Input: record with client timestamp
  - Expected: 200, timestamp used (client-prefer mode)

- **Append without timestamp (client-require mode)**
  - Setup: stream with `timestamping.mode=client-require`
  - Input: no timestamp
  - Expected: 422 (`invalid`, "Record timestamp missing")

- **Append with future timestamp (uncapped=false)**
  - Setup: stream with `uncapped=false`
  - Input: timestamp > current time
  - Expected: 200, timestamp capped to arrival time

- **Append with future timestamp (uncapped=true)**
  - Setup: stream with `uncapped=true`
  - Input: timestamp > current time
  - Expected: 200, future timestamp preserved

- **Append with past timestamp**
  - Input: timestamp < last record's timestamp
  - Expected: 200, timestamp adjusted up for monotonicity

- **Append empty body**
  - Input: record with empty body
  - Expected: 200

- **Append with match_seq_num (success)**
  - Input: correct seq_num
  - Expected: 200

- **Append with match_seq_num (failure)**
  - Input: wrong seq_num
  - Expected: 412 (`seq_num_mismatch`)

- **Append with fencing_token (success)**
  - Input: correct token
  - Expected: 200

- **Append with fencing_token (failure)**
  - Input: wrong token
  - Expected: 412 (`fencing_token_mismatch`)

- **Append with fencing_token too long**
  - Input: token > 36 bytes
  - Expected: 422 (`invalid`)

- **Append too many records**
  - Input: > 1000 records
  - Expected: Error (SDK validation or 400)

- **Append too large batch**
  - Input: > 1 MiB
  - Expected: Error (SDK validation or 400)

- **Append empty batch**
  - Input: 0 records
  - Expected: Error (SDK validation or 400)

- **Append to non-existent stream**
  - Input: invalid name
  - Expected: 404 (`stream_not_found`)

- **Append header with empty name (command record)**
  - Input: header with name="" and command value
  - Expected: 200 (valid for commands)

- **Append header with empty name (non-command)**
  - Input: header name="" without command value
  - Expected: 422 (`invalid`)

- **Permission denied**
  - Setup: token without `append` op
  - Expected: 403 (`permission_denied`)

---

## 9. Read Records

**`GET /streams/{stream}/records`**

> **Note:** SDKs may support different transports: JSON (unary), SSE (streaming), S2S (binary streaming). Test the transport(s) your SDK implements. The `s2-format` header only applies to JSON/SSE transport.

### Path Parameters

- `stream` (string, required)
  - Stream name
  - Constraints: 1-512 bytes

### Headers

- `s2-format` (string, optional)
  - Record data encoding: `raw` (default) or `base64`
  - Only applies to JSON/SSE transport

### Query Parameters - Start Position (mutually exclusive)

> **Note:** Only ONE of `seq_num`, `timestamp`, or `tail_offset` can be specified. Default is `tail_offset=0`.

- `seq_num` (integer, optional)
  - Start from this sequence number

- `timestamp` (integer, optional)
  - Start from records at/after this timestamp (ms)

- `tail_offset` (integer, optional, default 0)
  - Start this many records before the next seq_num (tail)

> **Important - Understanding 416 responses:**
> - The tail is the next seq_num that WILL be assigned to a new record. It points to a position where no record exists yet.
> - Reading returns 416 when the starting position is at or beyond the tail AND no `wait` is specified (or `wait=0`).
> - `tail_offset=0` means "start AT the tail" - this always returns 416 without `wait` because there's no record at that position yet.
> - `seq_num=0` on an empty stream returns 416 because the tail is also 0 (no records exist).
> - `clamp=true` brings the starting position TO the tail if beyond it, but you still get 416 because there's no record at the tail position.
> - To read records that exist: use `seq_num=0` on a non-empty stream, or `tail_offset=N` where N>0 to read the last N records.
> - To wait for new records: use `wait=<seconds>` to long-poll for records at/beyond the tail.

### Query Parameters - End/Limit

- `count` (integer, optional, default 1000)
  - Max records to return
  - Constraint: <= 1000

- `bytes` (integer, optional, default 1 MiB)
  - Max metered bytes
  - Constraint: <= 1 MiB

- `until` (integer, optional)
  - Exclusive timestamp to read until

- `clamp` (boolean, optional, default `false`)
  - If position > tail, clamp starting position TO the tail (not before it)

- `wait` (integer, optional, default 0)
  - Duration to wait for new records if at/beyond tail
  - Constraint: 0-60 seconds

> **Note on `clamp`:** Setting `clamp=true` prevents a 416 error when requesting a position BEYOND the tail by adjusting the start position to the tail. However, since the tail is where the next record will be written (not an existing record), you will still get 416 unless you also specify `wait` to long-poll for new records.

### Response Codes

- `200` — Success
  - Body: `ReadBatch`

- `400` — Bad request
  - Code: `invalid_argument`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Stream not found
  - Code: `stream_not_found`

- `408` — Timeout
  - Code: `deadline_exceeded`

- `409` — Stream being deleted
  - Code: `stream_deletion_pending`

- `416` — Range not satisfiable
  - Body: `TailResponse`
  - Cause: starting position at/beyond tail with no records available

### Response Schema: ReadBatch

- `records` (array of SequencedRecord)
  - Records read

- `tail.seq_num` (integer)
  - Current tail sequence number

- `tail.timestamp` (integer)
  - Current tail timestamp

### SequencedRecord Object

- `seq_num` (integer)
  - Record sequence number

- `timestamp` (integer)
  - Record timestamp (milliseconds)

- `headers` (array of Header)
  - Record headers

- `body` (string)
  - Record body

### Test Cases

- **Read from beginning (non-empty stream)**
  - Setup: stream with records
  - Input: `seq_num=0`
  - Expected: 200, records from start

- **Read from tail (no wait)**
  - Input: `tail_offset=0`
  - Expected: 416 (tail position has no record yet)

- **Read from tail (with wait)**
  - Input: `tail_offset=0`, `wait=5`
  - Expected: 200 with records if appended, or 200 empty if timeout

- **Read last N records**
  - Setup: stream with >=10 records
  - Input: `tail_offset=10`
  - Expected: 200, last 10 records

- **Read by timestamp**
  - Input: `timestamp=<ms>`
  - Expected: 200, records at/after timestamp

- **Read with count limit**
  - Input: `count=5`
  - Expected: 200, max 5 records

- **Read with bytes limit**
  - Input: `bytes=1024`
  - Expected: 200, max 1KB of records

- **Read until timestamp**
  - Input: `until=<ms>`
  - Expected: 200, records before timestamp

- **Read with clamp=true (beyond tail)**
  - Input: `seq_num=999999`, `clamp=true`
  - Expected: 416 (clamped TO tail, but no record exists there)

- **Read with clamp=true + wait**
  - Input: `seq_num=999999`, `clamp=true`, `wait=5`
  - Expected: 200 (waits for records at tail)

- **Read with clamp=false (beyond tail)**
  - Input: `seq_num=999999`, `clamp=false`
  - Expected: 416 (position beyond tail)

- **Read empty stream from seq_num=0**
  - Setup: new stream
  - Input: `seq_num=0`
  - Expected: 416 (tail is 0, no records exist)

- **Read empty stream with wait**
  - Setup: new stream
  - Input: `seq_num=0`, `wait=5`
  - Expected: 200 with records if appended, or 200 empty if timeout

- **Read non-existent stream**
  - Input: invalid name
  - Expected: 404 (`stream_not_found`)

- **Multiple start params**
  - Input: `seq_num=0`, `timestamp=0`
  - Expected: 400 (mutually exclusive)

- **Permission denied**
  - Setup: token without `read` op
  - Expected: 403 (`permission_denied`)

---

## 10. Command Records

Command records are special records that control stream behavior. SDKs typically expose these as helper methods.

### Fence Command

Sets or updates the fencing token for a stream. Subsequent appends with `fencing_token` must match.

- Header name: `""` (empty string)
- Header value: `fence`
- Body: New fencing token (max 36 bytes, empty to clear)

### Trim Command

Trims (deletes) all records before the specified sequence number.

- Header name: `""` (empty string)
- Header value: `trim`
- Body: 8-byte big-endian sequence number

### Test Cases

- **Set fencing token**
  - Input: append fence command with token "my-token"
  - Expected: 200, token set

- **Append with correct fencing token**
  - Setup: fencing token set
  - Input: `fencing_token="my-token"`
  - Expected: 200

- **Append with wrong fencing token**
  - Setup: fencing token set
  - Input: `fencing_token="wrong"`
  - Expected: 412 (`fencing_token_mismatch`)

- **Clear fencing token**
  - Input: append fence command with empty body
  - Expected: 200, token cleared

- **Trim stream**
  - Input: append trim command with seq_num
  - Expected: 200, trim accepted

- **Trim to future seq_num**
  - Input: trim_point > tail
  - Expected: 200 (no-op, nothing to trim)

> **Note:** If the SDK exposes `fence()` or `trim()` helper methods, test those instead of manually constructing command records.

> **Important - Trim is eventually consistent:**
> - The trim command returns 200 immediately when accepted, but actual record deletion is asynchronous
> - Do NOT write tests that assert records are immediately unavailable after trim
> - Trimmed records may still be readable for a short period after the trim command succeeds
> - If you must test trim behavior, either:
>   - Only verify the trim command succeeds (200 response)
>   - Add a reasonable delay and accept that records *may* still exist
>   - Skip "read after trim" verification entirely - it's testing infrastructure behavior, not SDK correctness

---

## 11. SDK Client Lifecycle

Test client initialization, configuration, and cleanup patterns exposed by the SDK.

### Test Cases

- **Client initialization**
  - Description: create client with valid credentials
  - Expected: client ready

- **Client with invalid token**
  - Description: create client with bad auth token
  - Expected: auth error on first operation

- **Client with custom endpoint**
  - Description: configure non-default endpoint
  - Expected: connects to specified endpoint

- **Client cleanup/close**
  - Description: properly close/dispose client
  - Expected: resources released, no leaks

- **Multiple concurrent clients**
  - Description: create multiple client instances
  - Expected: all operate independently

- **Client reuse**
  - Description: use same client for multiple operations
  - Expected: works correctly

- **Connection recovery**
  - Description: client reconnects after transient failure
  - Expected: operations succeed after recovery

### Streaming Session Tests (if SDK supports streaming)

- **Open append session**
  - Description: create streaming append session
  - Expected: session ready

- **Append multiple batches**
  - Description: send multiple batches on same session
  - Expected: all batches acknowledged

- **Close append session**
  - Description: gracefully close session
  - Expected: final acks received, session closed

- **Open read session**
  - Description: create streaming read session
  - Expected: session ready, records flow

- **Cancel read session**
  - Description: cancel/close read mid-stream
  - Expected: session terminates cleanly

- **Session timeout**
  - Description: session idle beyond timeout
  - Expected: session closed, reconnect works

> **Note:** Test all client/session creation patterns the SDK exposes (builders, factory methods, config objects, etc.)

---

## Complete Configuration Matrix

### StreamConfig Fields

> **Response serialization:** Fields with default values are omitted from responses.

- `storage_class` (enum, default `express`)
  - Values to test: `standard`, `express`

- `retention_policy` (oneOf, default 7 days)
  - Values to test: `{"age": 1}`, `{"age": 86400}`, `{"age": 604800}`, `{"infinite": {}}`

- `timestamping.mode` (enum, default `client-prefer`)
  - Values to test: `client-prefer`, `client-require`, `arrival`

- `timestamping.uncapped` (boolean, default `false`)
  - Values to test: `true`, `false`

- `delete_on_empty.min_age_secs` (integer, default 0/omitted)
  - Values to test: 0 (disabled), 60, 3600

---

## Free Tier Limitations

When running against an account on the Free tier, certain configurations will be rejected with `invalid_stream_config`. Tests should accept these failures as valid outcomes:

- Retention > 28 days
  - Example: "Retention is currently limited to 28 days for free tier"

- Infinite retention
  - Example: "Retention is currently limited to 28 days for free tier"

- Express storage class
  - Example: "Express storage class is not available on free tier"

---

## Eventually Consistent Operations

Several operations are eventually consistent. Tests should NOT assert on immediate effects:

- **Delete Stream**
  - Behavior: returns 202 immediately, actual deletion is asynchronous (60s+ delay)
  - What to test: verify 202 returned
  - Do NOT verify: stream is immediately inaccessible

- **Trim**
  - Behavior: returns 200 immediately, record deletion is asynchronous
  - What to test: verify 200 returned
  - Do NOT verify: records are immediately unavailable

- **Delete Basin**
  - Behavior: returns 202 immediately, actual deletion is asynchronous
  - What to test: verify 202 returned

> **Important:** If you need to verify deletion effects, you must either:
> - Wait a significant delay (60+ seconds) and accept that timing may vary
> - Skip verification of deletion effects entirely (recommended for SDK tests)
> - Use separate tests that don't assert on timing-dependent behavior

---

## Error Codes Reference

- `400` `invalid_argument`
  - Invalid parameter format/value

- `400` `invalid_stream_config`
  - Stream config validation failed (including tier limits)

- `422` `invalid`
  - Validation errors (config, arguments, etc.)

- `403` `permission_denied`
  - Token lacks required permissions

- `404` `stream_not_found`
  - Stream does not exist

- `404` `basin_not_found`
  - Parent basin does not exist

- `408` `deadline_exceeded`
  - Request timeout

- `409` `resource_already_exists`
  - Stream name already taken

- `409` `stream_deletion_pending`
  - Stream is being deleted

- `409` `transaction_conflict`
  - Concurrent reconfiguration conflict

- `412` (no code)
  - Append precondition failed (fencing/seq_num)
  - Body contains `fencing_token_mismatch` or `seq_num_mismatch`

- `416` (no code)
  - Read position at/beyond tail with no records available
  - Returns `TailResponse`, not `ErrorInfo`

- `429` `rate_limited`
  - Rate limit exceeded

- `500` `internal`
  - Internal server error

- `503` `basin_creating`
  - Basin still initializing
