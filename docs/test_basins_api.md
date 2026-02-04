# Basin API - SDK Test Matrix

> **LLM Instructions**
>
> This document is a reference for generating SDK test code. Write tests based on the following instructions:
>
> - Write clean, concise, idiomatic code for the target language
> - Do not add comments unless they explain non-obvious business logic
> - Do not add TODO comments, redundant docstrings, or explanatory noise
> - Each test should be self-contained and test exactly one behavior
> - Use descriptive test names that document intent (no need for separate comments)
> - Prefer table-driven/parameterized tests where appropriate
> - Assert on specific expected values, not just "success"
> - Clean up resources (delete basins) after tests when running against real backends
> - Find the option to include the basin header and enable it in the client
> - Show progress as each test progresses
> - Set a timeout for each test
> - If the PUT based API for CreateOrReconfigure is not used in the SDK, don't implement the test
> - If idempotency tokens are an internal detail of the SDK, don't implement a specific test
> - After a delete, basin MAY appear in list with state=deleting OR not appear at all

---

This document enumerates every knob/parameter of the Basin API to ensure SDK test coverage.

## Endpoints Overview

- `GET /basins` — `list_basins` — List basins
- `POST /basins` — `create_basin` — Create a basin
- `GET /basins/{basin}` — `get_basin_config` — Get basin configuration
- `PUT /basins/{basin}` — `create_or_reconfigure_basin` — Create or reconfigure a basin
- `DELETE /basins/{basin}` — `delete_basin` — Delete a basin
- `PATCH /basins/{basin}` — `reconfigure_basin` — Reconfigure a basin

---

## 1. List Basins

**`GET /basins`**

### Query Parameters

- `prefix` (string, optional, default `""`)
  - Filter to basins whose names begin with this prefix

- `start_after` (string, optional, default `""`)
  - Filter to basins whose names lexicographically start after this string
  - Constraint: must be >= `prefix`

- `limit` (integer, optional, default `1000`)
  - Number of results
  - Clamped to 1-1000
  - 0 is treated as default (1000)
  - Values > 1000 are clamped to 1000

### Response Codes

- `200` — Success
  - Body: `ListBasinsResponse`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `list-basins` permission

- `408` — Timeout
  - Code: `request_timeout`

- `422` — Validation error
  - Code: `invalid`
  - Cause: invalid parameter format/value

### Response Schema: ListBasinsResponse

- `basins` (array of `BasinInfo`)
  - Matching basins (max 1000)
  - Includes basins in "deleting" state with `state: "deleting"`

- `has_more` (boolean)
  - Indicates more basins match criteria

### Test Cases

- **List all basins**
  - Parameters: none
  - Expected: 200, up to 1000 basins returned

- **List with prefix**
  - Parameters: `prefix=test-`
  - Expected: 200, only basins with names starting with "test-"

- **List with start_after**
  - Parameters: `start_after=my-basin`
  - Expected: 200, basins lexicographically after "my-basin"

- **List with limit**
  - Parameters: `limit=5`
  - Expected: 200, max 5 basins, check `has_more`

- **Pagination**
  - Parameters: `start_after` + `limit`
  - Expected: 200, correct pagination behavior

- **Empty prefix**
  - Parameters: `prefix=""`
  - Expected: 200, all basins

- **Limit = 0**
  - Parameters: `limit=0`
  - Expected: 200, up to 1000 basins (0 treated as default)

- **Limit > 1000**
  - Parameters: `limit=1001`
  - Expected: 200, up to 1000 basins (clamped to max)

- **Invalid start_after < prefix**
  - Parameters: `prefix=z`, `start_after=a`
  - Expected: 422 (`invalid`)

- **Permission denied**
  - Setup: token without `list-basins` op
  - Expected: 403 (`permission_denied`)

---

## 2. Create Basin

**`POST /basins`**

### Headers

- `s2-request-token` (string, optional)
  - Client-specified idempotency token
  - Max 36 bytes

### Request Body: CreateBasinRequest

- `basin` (string, required)
  - Basin name (globally unique)
  - Constraints:
    - 8-48 characters
    - Lowercase letters, numbers, and hyphens only
    - Must start with lowercase letter or digit
    - Must end with lowercase letter or digit
    - No leading/trailing hyphen

- `scope` (BasinScope, optional)
  - Basin scope (immutable after creation)
  - Values: `aws:us-east-1`

- `config` (BasinConfig, optional)
  - Basin configuration

### BasinConfig Object

> **Note:** Boolean fields are always present in responses. `default_stream_config` is omitted when all nested fields are defaults.

- `create_stream_on_append` (boolean, default `false`, always present)
  - Auto-create stream on append

- `create_stream_on_read` (boolean, default `false`, always present)
  - Auto-create stream on read

- `default_stream_config` (StreamConfig, optional, omitted when all defaults)
  - Default config for auto-created streams

### StreamConfig Object (nested in BasinConfig.default_stream_config)

> **Note:** Fields with default values are **omitted** from API responses. If all StreamConfig fields are defaults, the entire `default_stream_config` is omitted.

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

- `200` — Basin already exists (idempotent with same token)
  - Body: `BasinInfo`

- `201` — Basin created
  - Body: `BasinInfo`

- `400` — Bad request
  - Codes: `bad_json`, `bad_query`, `bad_header`, `bad_path`

- `403` — Forbidden / limit exhausted
  - Codes: `permission_denied`, `quota_exhausted`

- `408` — Timeout
  - Code: `request_timeout`

- `422` — Validation error
  - Code: `invalid`

- `409` — Conflict
  - Codes: `resource_already_exists`, `basin_deletion_pending`, `transaction_conflict`

### Response Schema: BasinInfo

- `name` (string) — Basin name
- `scope` (BasinScope) — Basin scope
- `state` (BasinState) — Basin state
  - Values: `active`, `creating`, `deleting`

### Test Cases

- **Create minimal basin**
  - Input: `{"basin": "test-basin-01"}`
  - Expected: 201

- **Create with scope**
  - Input: `{"basin": "...", "scope": "aws:us-east-1"}`
  - Expected: 201

- **Create with full config**
  - Input: all config fields
  - Expected: 201

- **create_stream_on_append = true**
  - Input: config with flag
  - Expected: 201, verify auto-create on append

- **create_stream_on_read = true**
  - Input: config with flag
  - Expected: 201, verify auto-create on read

- **auto-created stream inherits default_stream_config**
  - Setup: create basin with `create_stream_on_append=true` and `default_stream_config` set (e.g., retention age)
  - Input: append to non-existent stream
  - Expected: 200, stream auto-created with matching default stream config

- **create_stream_on_append = false**
  - Input: config with flag
  - Expected: 201, verify no auto-create

- **create_stream_on_read = false**
  - Input: config with flag
  - Expected: 201, verify no auto-create

- **default_stream_config.storage_class = standard**
  - Input: nested config
  - Expected: 201, verify streams use standard

- **default_stream_config.storage_class = express**
  - Input: nested config
  - Expected: 201, verify streams use express

- **default_stream_config.retention_policy.age**
  - Input: `{"age": 86400}`
  - Expected: 201, verify 1-day retention

- **default_stream_config.retention_policy.infinite**
  - Input: `{"infinite": {}}`
  - Expected: 201, verify infinite retention

- **default_stream_config.timestamping.mode = client-prefer**
  - Input: nested config
  - Expected: 201

- **default_stream_config.timestamping.mode = client-require**
  - Input: nested config
  - Expected: 201

- **default_stream_config.timestamping.mode = arrival**
  - Input: nested config
  - Expected: 201

- **default_stream_config.timestamping.uncapped = true**
  - Input: nested config
  - Expected: 201

- **default_stream_config.timestamping.uncapped = false**
  - Input: nested config
  - Expected: 201

- **default_stream_config.delete_on_empty.min_age_secs**
  - Input: `{"min_age_secs": 3600}`
  - Expected: 201

- **Idempotent create (same token)**
  - Input: same request + same `s2-request-token`
  - Expected: 200

- **Idempotent create (different token)**
  - Input: same basin name + different `s2-request-token`
  - Expected: 409 (`resource_already_exists`)

- **Idempotent create (same token, different config)**
  - Input: same `s2-request-token` + different config
  - Expected: 409 (`resource_already_exists`)

- **Name too short**
  - Input: `{"basin": "short"}` (< 8 chars)
  - Expected: 400

- **Name too long**
  - Input: `{"basin": "a" * 49}` (> 48 chars)
  - Expected: 400

- **Name with uppercase**
  - Input: `{"basin": "Test-Basin"}`
  - Expected: 400

- **Name with underscore**
  - Input: `{"basin": "test_basin"}`
  - Expected: 400

- **Name starts with hyphen**
  - Input: `{"basin": "-test-basin"}`
  - Expected: 400

- **Name ends with hyphen**
  - Input: `{"basin": "test-basin-"}`
  - Expected: 400

- **Duplicate name**
  - Input: create same basin twice (no token)
  - Expected: 409 (`resource_already_exists`)

- **Create while same name is deleting**
  - Setup: delete basin, immediately create same name
  - Expected: 409 (`basin_deletion_pending`)

- **Basin limit exhausted**
  - Setup: create beyond account limit
  - Expected: 403 (`quota_exhausted`)

- **Account frozen**
  - Setup: frozen account
  - Expected: 403 (`permission_denied`)

- **Invalid retention_policy.age = 0**
  - Input: `retention_policy: {"age": 0}`
  - Expected: 422 (`invalid`)

- **Invalid retention_policy.age < 0**
  - Input: `retention_policy: {"age": -1}`
  - Expected: 400 (`invalid`, JSON parse error)

---

## 3. Get Basin Config

**`GET /basins/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

### Response Codes

- `200` — Success
  - Body: `BasinConfig`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `request_timeout`

- `503` — Basin still creating
  - Code: `unavailable`

### Test Cases

- **Get existing basin config**
  - Input: valid basin name
  - Expected: 200, full config

- **Get non-existent basin**
  - Input: name that doesn't exist
  - Expected: 404 (`basin_not_found`)

- **Get deleted basin**
  - Input: deleted basin name
  - Expected: 404 (`basin_not_found`)

- **Get creating basin**
  - Input: basin in creating state
  - Expected: 503 (`unavailable`)

- **Verify all config fields returned**
  - Expected: all nested fields present with non-default values

- **Permission denied**
  - Setup: token without `get-basin-config` op
  - Expected: 403 (`permission_denied`)

---

## 4. Create or Reconfigure Basin

**`PUT /basins/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

### Request Body: CreateOrReconfigureBasinRequest (optional, can be null)

- `scope` (BasinScope, optional)
  - Basin scope
  - Cannot be changed on reconfiguration

- `config` (BasinConfig, optional)
  - Basin configuration

### Response Codes

- `200` — Basin reconfigured
  - Body: `BasinInfo`

- `201` — Basin created
  - Body: `BasinInfo`

- `400` — Bad request
  - Codes: `bad_json`

- `403` — Forbidden / limit exhausted
  - Codes: `permission_denied`, `quota_exhausted`

- `408` — Timeout
  - Code: `request_timeout`

- `409` — Conflict
  - Codes: `basin_deletion_pending`, `transaction_conflict`

- `422` — Validation error / scope mismatch
  - Code: `invalid`

### Test Cases

- **Create new basin via PUT**
  - Input: new name + config
  - Expected: 201

- **Reconfigure existing basin**
  - Input: existing name + new config
  - Expected: 200

- **PUT with null body (no-op)**
  - Input: existing basin, null body
  - Expected: 200

- **PUT with empty object**
  - Input: existing basin, `{}`
  - Expected: 200 (reconfigured with defaults)

- **Attempt to change scope**
  - Input: different scope
  - Expected: 422 (`invalid`)

- **Create with defaults**
  - Input: new name, null body
  - Expected: 201

- **Basin deleted during reconfigure**
  - Setup: race condition
  - Expected: 409 (`basin_deletion_pending`)

- **Account frozen**
  - Setup: frozen account
  - Expected: 403 (`permission_denied`)

---

## 5. Delete Basin

**`DELETE /basins/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

### Response Codes

- `202` — Deletion accepted (async operation)

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `request_timeout`

- `503` — Basin still creating
  - Code: `unavailable`

### Test Cases

- **Delete existing basin**
  - Input: valid basin name
  - Expected: 202

- **Delete non-existent basin**
  - Input: name that doesn't exist
  - Expected: 404 (`basin_not_found`)

- **Delete already deleting basin**
  - Input: basin in deleting state
  - Expected: 202 (idempotent)

- **Delete basin with streams**
  - Input: basin containing streams
  - Expected: 202

- **Delete creating basin**
  - Input: basin in creating state
  - Expected: 503 (`unavailable`)

- **Verify basin state after delete**
  - Expected: state = "deleting"

- **Account frozen**
  - Setup: frozen account
  - Expected: 403 (`permission_denied`)

- **Permission denied**
  - Setup: token without `delete-basin` op
  - Expected: 403 (`permission_denied`)

---

## 6. Reconfigure Basin

**`PATCH /basins/{basin}`**

### Path Parameters

- `basin` (string, required)
  - Basin name
  - Constraints: 8-48 chars

### Request Body: BasinReconfiguration

> **Note:** Fields use partial update semantics — `null` means no change, absent means no change, explicit value means update.

- `create_stream_on_append` (boolean | null, optional)
  - Set auto-create on append
  - null = no change

- `create_stream_on_read` (boolean | null, optional)
  - Set auto-create on read
  - null = no change

- `default_stream_config` (StreamReconfiguration, optional)
  - Reconfigure default stream config

### StreamReconfiguration Object

- `storage_class` (StorageClass | null)
  - Change storage class

- `retention_policy` (RetentionPolicy | null)
  - Change retention policy

- `timestamping` (TimestampingReconfiguration | null)
  - Change timestamping config

- `delete_on_empty` (DeleteOnEmptyReconfiguration | null)
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
  - Body: `BasinConfig` (updated)

- `400` — Bad request
  - Codes: `bad_json`, `bad_query`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Basin not found
  - Code: `basin_not_found`

- `408` — Timeout
  - Code: `request_timeout`

- `409` — Conflict
  - Codes: `transaction_conflict`, `basin_deletion_pending`

- `422` — Validation error
  - Code: `invalid`

### Test Cases

- **Reconfigure deleting basin**
  - Setup: delete basin, then PATCH
  - Expected: 409 (`basin_deletion_pending`)

- **Enable create_stream_on_append**
  - Input: `{"create_stream_on_append": true}`
  - Expected: 200, config updated

- **Disable create_stream_on_append**
  - Input: `{"create_stream_on_append": false}`
  - Expected: 200, config updated

- **Enable create_stream_on_read**
  - Input: `{"create_stream_on_read": true}`
  - Expected: 200, config updated

- **Disable create_stream_on_read**
  - Input: `{"create_stream_on_read": false}`
  - Expected: 200, config updated

- **Null field (no change)**
  - Input: `{"create_stream_on_append": null}`
  - Expected: 200, field unchanged

- **Change storage_class to express**
  - Input: nested config
  - Expected: 200

- **Change storage_class to standard**
  - Input: nested config
  - Expected: 200

- **Change retention to age-based**
  - Input: `{"default_stream_config": {"retention_policy": {"age": 3600}}}`
  - Expected: 200

- **Change retention to infinite**
  - Input: `{"default_stream_config": {"retention_policy": {"infinite": {}}}}`
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
  - Input: `{"default_stream_config": {"delete_on_empty": {"min_age_secs": 0}}}`
  - Expected: 200

- **Reconfigure non-existent basin**
  - Input: name that doesn't exist
  - Expected: 404 (`basin_not_found`)

- **Empty reconfiguration**
  - Input: `{}`
  - Expected: 200, no changes

- **Partial reconfiguration**
  - Input: only some fields
  - Expected: 200, only specified fields changed

- **Invalid retention_policy.age = 0**
  - Input: `{"default_stream_config": {"retention_policy": {"age": 0}}}`
  - Expected: 422 (`invalid`)

- **Concurrent update conflict**
  - Setup: race condition with another update
  - Expected: 409 (`transaction_conflict`)

- **Account frozen**
  - Setup: frozen account
  - Expected: 403 (`permission_denied`)

- **Permission denied**
  - Setup: token without `reconfigure-basin` op
  - Expected: 403 (`permission_denied`)

---

## Complete Configuration Matrix

### BasinConfig Fields

- `create_stream_on_append` (boolean)
  - Values to test: `true`, `false`

- `create_stream_on_read` (boolean)
  - Values to test: `true`, `false`

- `default_stream_config` (object/null)
  - Values to test: present, absent

### StreamConfig Fields (under default_stream_config)

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

When running against an account on the Free tier, certain configurations will be rejected with `invalid`. Tests should accept these failures as valid outcomes:

- Retention > 28 days
  - Example: "Retention is currently limited to 28 days for free tier"

- Infinite retention
  - Example: "Retention is currently limited to 28 days for free tier"

- Express storage class
  - Example: "Express storage class is not available on free tier"

---

## Error Codes Reference

- `400` `bad_json`
  - Malformed JSON in request body

- `400` `bad_query`
  - Invalid query parameters

- `400` `bad_header`
  - Invalid header value

- `400` `bad_path`
  - Invalid path parameter

- `400` `bad_proto`
  - Invalid protobuf message

- `400` `bad_frame`
  - Invalid frame format

- `422` `invalid`
  - Validation errors (config, arguments, scope mismatch, tier limits)

- `403` `permission_denied`
  - Token lacks required permissions or account frozen

- `403` `quota_exhausted`
  - Account basin limit reached

- `404` `basin_not_found`
  - Basin does not exist

- `408` `request_timeout`
  - Request timeout

- `409` `resource_already_exists`
  - Basin name already taken

- `409` `basin_deletion_pending`
  - Basin deleted during PUT reconfigure

- `409` `transaction_conflict`
  - Concurrent update conflict

- `500` `storage`
  - Storage layer error

- `500` `other`
  - Internal server error

- `502` `hot_server`
  - Hot server unavailable

- `503` `unavailable`
  - Basin still in creating state or service unavailable

- `504` `upstream_timeout`
  - Upstream service timeout
