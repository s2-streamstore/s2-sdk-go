# Access Tokens API - SDK Test Matrix

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
> - Clean up resources (revoke tokens) after tests when running against real backends
> - Show progress as each test progresses
> - Set a timeout for each test
>
> **Access Token-specific guidance:**
> - Access tokens are scoped to an account, not a basin
> - Token IDs must be unique per account (1-96 bytes)
> - Tokens cannot escalate permissions beyond the issuing token
> - Use unique token IDs with random prefixes to avoid conflicts
> - Always revoke test tokens in cleanup, even on test failure
>
> **Linter and formatter compliance:**
> - Always run the project's linter and formatter after generating code
> - Fix ALL linter warnings and errors - do not leave any unresolved
> - Follow the project's existing code style (check existing test files for patterns)

---

## Endpoints Overview

- `GET /access-tokens` — `list_access_tokens` — List access tokens
- `POST /access-tokens` — `issue_access_token` — Issue an access token
- `DELETE /access-tokens/{id}` — `revoke_access_token` — Revoke an access token

---

## 1. List Access Tokens

**`GET /access-tokens`**

### Query Parameters

- `prefix` (string, optional, default `""`)
  - Filter to access tokens whose ID begins with this prefix

- `start_after` (string, optional, default `""`)
  - Filter to access tokens whose ID lexicographically starts after this string

- `limit` (integer, optional, default `1000`)
  - Number of results
  - Clamped to 1-1000

### Response Codes

- `200` — Success
  - Body: `ListAccessTokensResponse`

- `400` — Bad request (malformed query)
  - Code: `bad_query`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `list-access-tokens` permission

- `408` — Timeout
  - Code: `request_timeout`

### Response Schema: ListAccessTokensResponse

- `access_tokens` (array of `AccessTokenInfo`)
  - Matching tokens (max 1000)

- `has_more` (boolean)
  - Indicates more tokens match criteria

### AccessTokenInfo Schema

- `id` (string)
  - Token ID (1-96 bytes)

- `scope` (AccessTokenScope)
  - Token permissions scope

- `auto_prefix_streams` (boolean)
  - Whether stream names are automatically prefixed

- `expires_at` (string, RFC 3339, optional)
  - Token expiration time

### Test Cases

- **List all tokens**
  - Parameters: none
  - Expected: 200, array of tokens returned

- **List with prefix**
  - Setup: create tokens with IDs `test-tok-1`, `test-tok-2`, `other-tok`
  - Parameters: `prefix=test-tok-`
  - Expected: 200, only tokens starting with "test-tok-"

- **List with start_after**
  - Setup: create tokens `aaa-tok`, `bbb-tok`, `ccc-tok`
  - Parameters: `start_after=aaa-tok`
  - Expected: 200, tokens after "aaa-tok" lexicographically

- **List with limit**
  - Setup: create multiple tokens
  - Parameters: `limit=2`
  - Expected: 200, max 2 tokens, check `has_more`

- **Pagination**
  - Setup: create 5 tokens
  - Use `start_after` + `limit=2` to paginate
  - Expected: iterate through all tokens correctly

- **Iterator**
  - Setup: create multiple tokens with common prefix
  - Use iterator with prefix filter
  - Expected: iterate through all matching tokens

- **Permission denied**
  - Setup: token without `list-access-tokens` op
  - Expected: 403 (`permission_denied`)

---

## 2. Issue Access Token

**`POST /access-tokens`**

### Request Body: IssueAccessTokenArgs

- `id` (string, required)
  - Token ID (1-96 bytes, must be unique per account)

- `scope` (AccessTokenScope, required)
  - Token permissions scope

- `auto_prefix_streams` (boolean, optional, default `false`)
  - Automatically prefix stream names with the stream scope prefix
  - Requires stream scope to be a prefix (not exact)

- `expires_at` (string, RFC 3339, optional)
  - Token expiration time
  - Defaults to issuing token's expiration if not specified
  - Cannot exceed issuing token's expiration

### AccessTokenScope Schema

- `basins` (ResourceSet, optional)
  - Basin names allowed
  - `{"exact": "basin-name"}` — match only this basin
  - `{"prefix": "prefix-"}` — match basins starting with prefix
  - `{"prefix": ""}` — match all basins

- `streams` (ResourceSet, optional)
  - Stream names allowed
  - Same format as basins

- `access_tokens` (ResourceSet, optional)
  - Token IDs allowed for management
  - Same format as basins

- `op_groups` (PermittedOperationGroups, optional)
  - Operation group permissions

- `ops` (array of string, optional)
  - Specific operations allowed

### PermittedOperationGroups Schema

- `account` (ReadWritePermissions, optional)
  - `read`: list-basins, list-access-tokens, account-metrics
  - `write`: create-basin, delete-basin, reconfigure-basin, issue-access-token, revoke-access-token

- `basin` (ReadWritePermissions, optional)
  - `read`: get-basin-config, list-streams, basin-metrics
  - `write`: create-stream, delete-stream, reconfigure-stream

- `stream` (ReadWritePermissions, optional)
  - `read`: get-stream-config, check-tail, read, stream-metrics
  - `write`: append, trim, fence

### Operations List

- Account: `list-basins`, `create-basin`, `delete-basin`, `reconfigure-basin`, `get-basin-config`
- Access tokens: `issue-access-token`, `revoke-access-token`, `list-access-tokens`
- Streams: `list-streams`, `create-stream`, `delete-stream`, `get-stream-config`, `reconfigure-stream`
- Data: `check-tail`, `append`, `read`, `trim`, `fence`
- Metrics: `account-metrics`, `basin-metrics`, `stream-metrics`

### Response Codes

- `201` — Created
  - Body: `IssueAccessTokenResponse`

- `400` — Bad request (malformed JSON)
  - Code: `bad_json`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `issue-access-token` permission, or attempting to escalate permissions

- `408` — Timeout
  - Code: `request_timeout`

- `409` — Conflict
  - Code: `resource_already_exists`
  - Cause: token ID already exists

- `422` — Validation error
  - Code: `invalid`
  - Cause: invalid token ID format, invalid scope

### Response Schema: IssueAccessTokenResponse

- `access_token` (string)
  - Base64-encoded access token

### Test Cases

- **Issue token with specific ops**
  - Input: `id=test-tok-1`, `ops=["list-basins", "list-streams"]`
  - Expected: 201, token returned

- **Issue token with op_groups (read only)**
  - Input: `id=test-tok-2`, `op_groups={account: {read: true}}`
  - Expected: 201, token with account read permissions

- **Issue token with op_groups (read and write)**
  - Input: `id=test-tok-3`, `op_groups={stream: {read: true, write: true}}`
  - Expected: 201, token with stream read/write permissions

- **Issue token with basin exact scope**
  - Input: `id=test-tok-4`, `basins={exact: "my-basin"}`, ops
  - Expected: 201, token scoped to single basin

- **Issue token with basin prefix scope**
  - Input: `id=test-tok-5`, `basins={prefix: "test-"}`, ops
  - Expected: 201, token scoped to basins starting with "test-"

- **Issue token with stream exact scope**
  - Input: `id=test-tok-6`, `streams={exact: "my-stream"}`, ops
  - Expected: 201, token scoped to single stream

- **Issue token with stream prefix scope**
  - Input: `id=test-tok-7`, `streams={prefix: "logs-"}`, ops
  - Expected: 201, token scoped to streams starting with "logs-"

- **Issue token with auto_prefix_streams**
  - Input: `id=test-tok-8`, `streams={prefix: "tenant/"}`, `auto_prefix_streams=true`, ops
  - Expected: 201, token with auto-prefixing enabled

- **Issue token with auto_prefix_streams + exact stream scope (invalid)**
  - Input: `id=test-tok-8b`, `streams={exact: "tenant/stream"}`, `auto_prefix_streams=true`
  - Expected: 422 (`invalid`, auto-prefix requires stream prefix scope)

- **Auto-prefix create + list behavior**
  - Setup: issue token with `streams={prefix: "tenant/"}`, `auto_prefix_streams=true`, `ops=[create-stream, list-streams]`
  - Input: create stream named `my-stream` using limited token; list streams with prefix `my-`
  - Expected: create succeeds; limited list returns `my-stream` (prefix stripped); admin list shows `tenant/my-stream`

- **Issue token with expires_at**
  - Input: `id=test-tok-9`, `expires_at=<future timestamp>`, ops
  - Expected: 201, token with custom expiration

- **Issue token with all scopes combined**
  - Input: basins prefix + streams prefix + specific ops
  - Expected: 201, token with combined restrictions

- **Duplicate token ID**
  - Setup: issue token with ID `dup-tok`
  - Input: issue another token with same ID `dup-tok`
  - Expected: 409 (`resource_already_exists`)

- **Invalid token ID (empty)**
  - Input: `id=""`
  - Expected: SDK validation error or 422 (`invalid`)

- **Invalid token ID (too long)**
  - Input: `id=<97+ characters>`
  - Expected: SDK validation error or 422 (`invalid`)

- **Permission escalation attempt (ops)**
  - Setup: limited token with only `list-basins`
  - Attempt: issue token with `create-basin`
  - Expected: 403 (`permission_denied`)

- **Permission escalation attempt (scope)**
  - Setup: limited token with `basins={exact: "basin-a"}`
  - Attempt: issue token with `basins={prefix: ""}` (all basins)
  - Expected: 403 (`permission_denied`)

- **Permission denied (no issue permission)**
  - Setup: token without `issue-access-token` op
  - Expected: 403 (`permission_denied`)

---

## 3. Revoke Access Token

**`DELETE /access-tokens/{id}`**

### Path Parameters

- `id` (string, required)
  - Access token ID to revoke (1-96 bytes)

### Response Codes

- `204` — Success (no content)

- `400` — Bad request (invalid token ID in path)
  - Code: `bad_path`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `revoke-access-token` permission, or token ID not in scope

- `404` — Not found
  - Code: `access_token_not_found`
  - Cause: token does not exist

- `408` — Timeout
  - Code: `request_timeout`

### Test Cases

- **Revoke existing token**
  - Setup: issue token with ID `revoke-test`
  - Input: revoke token `revoke-test`
  - Expected: 204

- **Revoke non-existent token**
  - Input: revoke token `does-not-exist`
  - Expected: 404 (`access_token_not_found`)

- **Revoke already revoked token**
  - Setup: issue and revoke token
  - Input: revoke same token again
  - Expected: 404 (`access_token_not_found`)

- **Invalid token ID (empty)**
  - Input: `id=""`
  - Expected: SDK validation error or 400 (`bad_path`)

- **Permission denied (no revoke permission)**
  - Setup: token without `revoke-access-token` op
  - Expected: 403 (`permission_denied`)

- **Token ID out of scope**
  - Setup: limited token with `access_tokens={prefix: "my-"}`
  - Input: revoke token `other-tok`
  - Expected: 403 (`permission_denied`)

---

## 4. Authorization & Scope Tests

These tests verify that issued tokens respect their configured scope.

### Basin Scope Tests

- **Basin exact scope - allowed**
  - Setup: issue token with `basins={exact: "allowed-basin"}` + `create-stream`
  - Create stream in `allowed-basin`
  - Expected: 201

- **Basin exact scope - denied**
  - Setup: issue token with `basins={exact: "allowed-basin"}` + `create-stream`
  - Create stream in `other-basin`
  - Expected: 403 (`permission_denied`)

- **Basin prefix scope - allowed**
  - Setup: issue token with `basins={prefix: "test-"}` + `create-basin`
  - Create basin `test-mybasin`
  - Expected: 201

- **Basin prefix scope - denied**
  - Setup: issue token with `basins={prefix: "test-"}` + `create-basin`
  - Create basin `prod-mybasin`
  - Expected: 403 (`permission_denied`)

### Stream Scope Tests

- **Stream exact scope - allowed**
  - Setup: issue token with `streams={exact: "allowed-stream"}` + `create-stream`
  - Create stream `allowed-stream`
  - Expected: 201

- **Stream exact scope - denied**
  - Setup: issue token with `streams={exact: "allowed-stream"}` + `create-stream`
  - Create stream `other-stream`
  - Expected: 403 (`permission_denied`)

- **Stream prefix scope - allowed**
  - Setup: issue token with `streams={prefix: "logs-"}` + `create-stream`
  - Create stream `logs-app`
  - Expected: 201

- **Stream prefix scope - denied**
  - Setup: issue token with `streams={prefix: "logs-"}` + `create-stream`
  - Create stream `events-app`
  - Expected: 403 (`permission_denied`)

### Operation Tests

- **Operation allowed**
  - Setup: issue token with `ops=["list-basins"]`
  - List basins
  - Expected: 200

- **Operation denied**
  - Setup: issue token with `ops=["list-basins"]`
  - Create basin
  - Expected: 403 (`permission_denied`)

### Op Groups Tests

- **Account read only**
  - Setup: issue token with `op_groups={account: {read: true}}`
  - List basins: Expected 200
  - Create basin: Expected 403

- **Stream read/write**
  - Setup: issue token with `op_groups={stream: {read: true, write: true}}` + basin/stream scope
  - Append records: Expected 200
  - Read records: Expected 200

### Auto-Prefix Streams Tests

- **Auto-prefix on create**
  - Setup: issue token with `streams={prefix: "tenant/"}`, `auto_prefix_streams=true`, `create-stream`
  - Create stream `mystream` (without prefix)
  - Verify: stream created as `tenant/mystream` on server

- **Auto-prefix on list**
  - Setup: create `tenant/stream1`, `tenant/stream2` with admin token
  - Issue token with `streams={prefix: "tenant/"}`, `auto_prefix_streams=true`, `list-streams`
  - List streams
  - Verify: returned names are `stream1`, `stream2` (prefix stripped)

- **Auto-prefix requires prefix scope**
  - Setup: issue token with `streams={exact: "tenant/stream"}`, `auto_prefix_streams=true`
  - Expected: 422 (`invalid`)

### Cross-Basin Access Tests

- **Cross-basin denied**
  - Setup: create basins `basin-a`, `basin-b`
  - Issue token with `basins={exact: "basin-a"}` + `create-stream`
  - Create stream in `basin-b`
  - Expected: 403 (`permission_denied`)

---

## Error Codes Reference

- `400` `bad_json`
  - Malformed JSON in request body

- `400` `bad_query`
  - Invalid query parameters

- `400` `bad_path`
  - Invalid path parameters (e.g., token ID)

- `403` `permission_denied`
  - Token lacks required permission
  - Token attempting to escalate permissions
  - Resource not in token's scope

- `404` `access_token_not_found`
  - Token does not exist (on revoke)

- `408` `request_timeout`
  - Request timeout

- `409` `resource_already_exists`
  - Token ID already exists

- `422` `invalid`
  - Invalid token ID format
  - Invalid scope configuration

---

## Configuration Matrix

### ResourceSet Variants

- `{"exact": "name"}` — Match only this exact resource
- `{"prefix": "prefix-"}` — Match resources starting with this prefix
- `{"prefix": ""}` — Match all resources (empty prefix)
- `null` / omitted — Match no resources (deny all)

### Operation Groups Mapping

**Account level:**
- Read: `list-basins`, `list-access-tokens`, `account-metrics`
- Write: `create-basin`, `delete-basin`, `reconfigure-basin`, `get-basin-config`, `issue-access-token`, `revoke-access-token`

**Basin level:**
- Read: `list-streams`, `basin-metrics`
- Write: `create-stream`, `delete-stream`, `reconfigure-stream`, `get-stream-config`

**Stream level:**
- Read: `check-tail`, `read`, `stream-metrics`
- Write: `append`, `trim`, `fence`
