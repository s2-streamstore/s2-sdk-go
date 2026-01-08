# Basin API - SDK Test Matrix

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
> - Clean up resources (delete basins) after tests when running against real backends
> - Find the option to include the basin header and enable it in the client
> - Show progress as each test progresses
> - Set a timeout for each test
> - If the PUT based API for CreateOrReconfigure is not used in the SDK, don't implement the test
> - If idempotency tokens are an internal detail of the SDK, don't implement a specific test

---

This document enumerates every knob/parameter of the Basin API to ensure SDK test coverage.

## Endpoints Overview

| Method | Endpoint | Operation ID | Description |
|--------|----------|--------------|-------------|
| GET | `/basins` | `list_basins` | List basins |
| POST | `/basins` | `create_basin` | Create a basin |
| GET | `/basins/{basin}` | `get_basin_config` | Get basin configuration |
| PUT | `/basins/{basin}` | `create_or_reconfigure_basin` | Create or reconfigure a basin |
| DELETE | `/basins/{basin}` | `delete_basin` | Delete a basin |
| PATCH | `/basins/{basin}` | `reconfigure_basin` | Reconfigure a basin |

---

## 1. List Basins

**`GET /basins`**

### Query Parameters

| Parameter | Type | Required | Default | Constraints | Description |
|-----------|------|----------|---------|-------------|-------------|
| `prefix` | string | No | `""` | - | Filter to basins whose names begin with this prefix |
| `start_after` | string | No | `""` | Must be >= `prefix` | Filter to basins whose names lexicographically start after this string |
| `limit` | integer | No | `1000` | Clamped to 1-1000 | Number of results (0 defaults to 1000, >1000 clamped to 1000) |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `ListBasinsResponse` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |

### Response Schema: `ListBasinsResponse`

| Field | Type | Description |
|-------|------|-------------|
| `basins` | `BasinInfo[]` | Matching basins (max 1000) |
| `has_more` | boolean | Indicates more basins match criteria |

### Test Cases

| Test | Parameters | Expected |
|------|------------|----------|
| List all basins | none | 200, up to 1000 basins returned |
| List with prefix | `prefix=test-` | 200, only matching basins |
| List with start_after | `start_after=my-basin` | 200, basins after "my-basin" |
| List with limit | `limit=5` | 200, max 5 basins, check `has_more` |
| Pagination | `start_after` + `limit` | 200, correct pagination |
| Empty prefix | `prefix=""` | 200, all basins |
| Limit = 0 | `limit=0` | 200, up to 1000 basins (0 treated as default) |
| Limit > 1000 | `limit=1001` | 200, up to 1000 basins (clamped to max) |
| Invalid start_after < prefix | `prefix=z`, `start_after=a` | 400, validation error |
| Permission denied | token without `list-basins` op | 403 (`permission_denied`) |

---

## 2. Create Basin

**`POST /basins`**

### Headers

| Header | Type | Required | Description |
|--------|------|----------|-------------|
| `s2-request-token` | string | No | Client-specified idempotency token |

### Request Body: `CreateBasinRequest`

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| `basin` | string | **Yes** | 8-48 chars, lowercase + numbers + hyphens, no leading/trailing hyphen | Basin name (globally unique) |
| `scope` | `BasinScope` | No | enum | Basin scope |
| `config` | `BasinConfig` | No | - | Basin configuration |

### BasinScope Enum

| Value | Description |
|-------|-------------|
| `aws:us-east-1` | AWS US East 1 region |

### BasinConfig Object

> **Note:** Boolean fields are always present in responses. `default_stream_config` is omitted when all nested fields are defaults.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `create_stream_on_append` | boolean | No | `false` (always present) | Auto-create stream on append |
| `create_stream_on_read` | boolean | No | `false` (always present) | Auto-create stream on read |
| `default_stream_config` | `StreamConfig` | No | omitted when all defaults | Default config for auto-created streams |

### StreamConfig Object (nested in BasinConfig.default_stream_config)

> **Note:** Fields with default values are **omitted** from API responses. If all StreamConfig fields are defaults, the entire `default_stream_config` is omitted.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `storage_class` | `StorageClass` | No | `express` (omitted) | Storage class for recent writes |
| `retention_policy` | `RetentionPolicy` | No | `{"age": 604800}` (7 days, omitted) | Retention policy |
| `timestamping` | `TimestampingConfig` | No | (see below, omitted) | Timestamping behavior |
| `delete_on_empty` | `DeleteOnEmptyConfig` | No | disabled (omitted) | Auto-delete empty streams |

### StorageClass Enum

| Value | Description |
|-------|-------------|
| `standard` | Standard storage |
| `express` | Express storage |

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
| `min_age_secs` | integer | 0 (omitted) | Min age in seconds before empty stream deleted (0 = disabled, field omitted) |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Basin already exists (idempotent) | - | `BasinInfo` |
| 201 | Basin created | - | `BasinInfo` |
| 400 | Bad request / invalid config | `invalid_argument`, `invalid_basin_config`, `basin_scope_mismatch` | `ErrorInfo` |
| 403 | Forbidden / limit exhausted | `permission_denied`, `basins_limit` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Conflict (name taken) | `basin_exists` | `ErrorInfo` |
| 429 | Retryable conflict | `too_many_basin_creations` | `ErrorInfo` |

### Response Schema: `BasinInfo`

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Basin name |
| `scope` | `BasinScope` | Basin scope |
| `state` | `BasinState` | Basin state |

### BasinState Enum

| Value | Description |
|-------|-------------|
| `active` | Basin is active |
| `creating` | Basin is being created |
| `deleting` | Basin is being deleted |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Create minimal basin | `{"basin": "test-basin-01"}` | 201 |
| Create with scope | `{"basin": "...", "scope": "aws:us-east-1"}` | 201 |
| Create with full config | All config fields | 201 |
| **create_stream_on_append = true** | config with flag | 201, verify auto-create on append |
| **create_stream_on_read = true** | config with flag | 201, verify auto-create on read |
| **create_stream_on_append = false** | config with flag | 201, verify no auto-create |
| **create_stream_on_read = false** | config with flag | 201, verify no auto-create |
| **default_stream_config.storage_class = standard** | nested config | 201, verify streams use standard |
| **default_stream_config.storage_class = express** | nested config | 201, verify streams use express |
| **default_stream_config.retention_policy.age** | `{"age": 86400}` | 201, verify 1-day retention |
| **default_stream_config.retention_policy.infinite** | `{"infinite": {}}` | 201, verify infinite retention |
| **default_stream_config.timestamping.mode = client-prefer** | nested | 201 |
| **default_stream_config.timestamping.mode = client-require** | nested | 201 |
| **default_stream_config.timestamping.mode = arrival** | nested | 201 |
| **default_stream_config.timestamping.uncapped = true** | nested | 201 |
| **default_stream_config.timestamping.uncapped = false** | nested | 201 |
| **default_stream_config.delete_on_empty.min_age_secs** | `{"min_age_secs": 3600}` | 201 |
| Idempotent create (same token) | Same request + same `s2-request-token` | 200 |
| Idempotent create (different token) | Same basin name + different `s2-request-token` | 409 (`basin_exists`) |
| Name too short | `{"basin": "short"}` (< 8 chars) | 400 |
| Name too long | `{"basin": "a" * 49}` (> 48 chars) | 400 |
| Name with uppercase | `{"basin": "Test-Basin"}` | 400 |
| Name with underscore | `{"basin": "test_basin"}` | 400 |
| Name starts with hyphen | `{"basin": "-test-basin"}` | 400 |
| Name ends with hyphen | `{"basin": "test-basin-"}` | 400 |
| Duplicate name | Create same basin twice (no token) | 409 (`basin_exists`) |
| Basin limit exhausted | Create beyond account limit | 403 (`basins_limit`) |
| Retryable conflict | Concurrent creation race | 429 (`too_many_basin_creations`) |
| Account frozen | Frozen account | 403 (`permission_denied`) |
| Invalid retention_policy.age = 0 | `retention_policy: {"age": 0}` | 400 (`invalid_basin_config`) |
| Invalid retention_policy.age < 0 | `retention_policy: {"age": -1}` | 400 (`invalid_argument`, JSON parse error) |

---

## 3. Get Basin Config

**`GET /basins/{basin}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `basin` | string | **Yes** | 8-48 chars | Basin name |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Success | - | `BasinConfig` |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 503 | Basin still creating | `basin_creating` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Get existing basin config | valid basin name | 200, full config |
| Get non-existent basin | name that doesn't exist | 404 (`basin_not_found`) |
| Get deleted basin | deleted basin name | 404 (`basin_not_found`) |
| Get creating basin | basin in creating state | 503 (`basin_creating`) |
| Verify all config fields returned | - | All nested fields present |
| Permission denied | token without `get-basin-config` op | 403 (`permission_denied`) |

---

## 4. Create or Reconfigure Basin

**`PUT /basins/{basin}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `basin` | string | **Yes** | 8-48 chars | Basin name |

### Request Body: `CreateOrReconfigureBasinRequest` (optional, can be null)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `scope` | `BasinScope` | No | Basin scope (cannot be reconfigured) |
| `config` | `BasinConfig` | No | Basin configuration |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 200 | Basin reconfigured | - | `BasinInfo` |
| 201 | Basin created | - | `BasinInfo` |
| 204 | No changes (null body) | - | - |
| 400 | Bad request / scope mismatch | `invalid_argument`, `basin_scope_mismatch`, `invalid_basin_config` | `ErrorInfo` |
| 403 | Forbidden / limit exhausted | `permission_denied`, `basins_limit` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 409 | Basin deleted during reconfigure | `basin_deleted` | `ErrorInfo` |
| 429 | Retryable conflict | `too_many_basin_creations` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Create new basin via PUT | new name + config | 201 |
| Reconfigure existing basin | existing name + new config | 200 |
| PUT with null body (no-op) | existing basin, null body | 204 |
| PUT with empty object | existing basin, `{}` | 200 (reconfigured with defaults) |
| Attempt to change scope | different scope | 400 (`basin_scope_mismatch`) |
| Create with defaults | new name, null body | 201 |
| Basin deleted during reconfigure | race condition | 409 (`basin_deleted`) |
| Account frozen | frozen account | 403 (`permission_denied`) |

---

## 5. Delete Basin

**`DELETE /basins/{basin}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `basin` | string | **Yes** | 8-48 chars | Basin name |

### Response Codes

| Code | Description | Error Code | Body |
|------|-------------|------------|------|
| 202 | Deletion accepted | - | - |
| 400 | Bad request | `invalid_argument` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |
| 503 | Basin still creating | `basin_creating` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Delete existing basin | valid basin name | 202 |
| Delete non-existent basin | name that doesn't exist | 404 (`basin_not_found`) |
| Delete already deleting basin | basin in deleting state | 202 (idempotent) |
| Delete basin with streams | basin containing streams | 202 |
| Delete creating basin | basin in creating state | 503 (`basin_creating`) |
| Verify basin state after delete | - | state = "deleting" |
| Account frozen | frozen account | 403 (`permission_denied`) |
| Permission denied | token without `delete-basin` op | 403 (`permission_denied`) |

---

## 6. Reconfigure Basin

**`PATCH /basins/{basin}`**

### Path Parameters

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `basin` | string | **Yes** | 8-48 chars | Basin name |

### Request Body: `BasinReconfiguration`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `create_stream_on_append` | boolean \| null | No | Set auto-create on append (null = no change) |
| `create_stream_on_read` | boolean \| null | No | Set auto-create on read (null = no change) |
| `default_stream_config` | `StreamReconfiguration` | No | Reconfigure default stream config |

### StreamReconfiguration Object

| Field | Type | Description |
|-------|------|-------------|
| `storage_class` | `StorageClass` \| null | Change storage class |
| `retention_policy` | `RetentionPolicy` \| null | Change retention policy |
| `timestamping` | `TimestampingReconfiguration` \| null | Change timestamping |
| `delete_on_empty` | `DeleteOnEmptyReconfiguration` \| null | Change delete-on-empty |

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
| 200 | Success | - | `BasinConfig` (updated) |
| 400 | Bad request / invalid config | `invalid_argument`, `bad_config` | `ErrorInfo` |
| 403 | Forbidden | `permission_denied` | `ErrorInfo` |
| 404 | Basin not found | `basin_not_found` | `ErrorInfo` |
| 408 | Timeout | `deadline_exceeded` | `ErrorInfo` |

### Test Cases

| Test | Input | Expected |
|------|-------|----------|
| Enable create_stream_on_append | `{"create_stream_on_append": true}` | 200, config updated |
| Disable create_stream_on_append | `{"create_stream_on_append": false}` | 200, config updated |
| Enable create_stream_on_read | `{"create_stream_on_read": true}` | 200, config updated |
| Disable create_stream_on_read | `{"create_stream_on_read": false}` | 200, config updated |
| Null field (no change) | `{"create_stream_on_append": null}` | 200, field unchanged |
| Change storage_class to express | nested config | 200 |
| Change storage_class to standard | nested config | 200 |
| Change retention to age-based | `{"default_stream_config": {"retention_policy": {"age": 3600}}}` | 200 |
| Change retention to infinite | `{"default_stream_config": {"retention_policy": {"infinite": {}}}}` | 200 |
| Change timestamping mode | all 3 modes | 200 |
| Change timestamping.uncapped | true/false | 200 |
| Change delete_on_empty.min_age_secs | various values | 200 |
| Disable delete_on_empty | `{"default_stream_config": {"delete_on_empty": {"min_age_secs": 0}}}` | 200 |
| Reconfigure non-existent basin | name that doesn't exist | 404 (`basin_not_found`) |
| Empty reconfiguration | `{}` | 200, no changes |
| Partial reconfiguration | only some fields | 200, only specified fields changed |
| Invalid retention_policy.age = 0 | `{"default_stream_config": {"retention_policy": {"age": 0}}}` | 400 (`bad_config`) |
| Account frozen | frozen account | 403 (`permission_denied`) |
| Permission denied | token without `reconfigure-basin` op | 403 (`permission_denied`) |

---

## Complete Configuration Matrix

### BasinConfig Fields

| Field Path | Type | Values to Test |
|------------|------|----------------|
| `create_stream_on_append` | boolean | `true`, `false` |
| `create_stream_on_read` | boolean | `true`, `false` |
| `default_stream_config` | object/null | present, absent |

### StreamConfig Fields (under default_stream_config)

> **Response serialization:** Fields with default values are omitted from responses. `null` in the table below means "omitted in response when default".

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

When running against an account on the Free tier, certain configurations will be rejected with `invalid_basin_config`. Tests should accept these failures as valid outcomes:

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
| 400 | `invalid_basin_config` | Basin config validation failed (including tier limits) |
| 400 | `basin_scope_mismatch` | Attempted to change basin scope |
| 400 | `bad_config` | Reconfiguration validation failed |
| 403 | `permission_denied` | Token lacks required permissions or account frozen |
| 403 | `basins_limit` | Account basin limit reached |
| 404 | `basin_not_found` | Basin does not exist |
| 408 | `deadline_exceeded` | Request timeout |
| 409 | `basin_exists` | Basin name already taken |
| 409 | `basin_deleted` | Basin deleted during PUT reconfigure |
| 429 | `too_many_basin_creations` | Concurrent creation conflict, retry |
| 503 | `basin_creating` | Basin still in creating state |
| 500 | `internal` | Internal server error |

