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
> - Show progress as each test progresses
> - Set a timeout for each test
>
> **Important security considerations:**
> - Access tokens are sensitive credentials - never log or expose the actual token value
> - The issued token value is only returned once at creation time
> - Test tokens should be created with minimal scope and short expiration for safety
> - Clean up test tokens by revoking them after tests complete

---

This document enumerates every knob/parameter of the Access Tokens API to ensure SDK test coverage.

## Endpoints Overview

- `GET /access-tokens` — `list_access_tokens` — List access tokens
- `POST /access-tokens` — `issue_access_token` — Issue (create) a new access token
- `DELETE /access-tokens/{id}` — `revoke_access_token` — Revoke an access token

---

## 1. List Access Tokens

**`GET /access-tokens`**

### Query Parameters

- `prefix` (string, optional, default `""`)
  - Filter to access tokens whose ID begins with this prefix

- `start_after` (string, optional, default `""`)
  - Filter to access tokens whose ID lexicographically starts after this string
  - Constraint: must be >= `prefix`

- `limit` (integer, optional, default `1000`)
  - Number of results
  - Clamped to 1-1000
  - 0 is treated as default (1000)
  - Values > 1000 are clamped to 1000

### Response Codes

- `200` — Success
  - Body: `ListAccessTokensResponse`

- `400` — Bad request
  - Code: `invalid`
  - Cause: invalid parameter format/value

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks `list-access-tokens` permission

- `408` — Timeout
  - Code: `timeout`

### Response Schema: ListAccessTokensResponse

- `access_tokens` (array of `AccessTokenInfo`)
  - Matching access tokens (max 1000)

- `has_more` (boolean)
  - Indicates more access tokens match criteria

### AccessTokenInfo Object

- `id` (string)
  - Access token ID (1-96 bytes)

- `expires_at` (string, RFC 3339, optional)
  - Token expiration timestamp
  - If not set, inherits expiration from the requestor's token

- `auto_prefix_streams` (boolean, optional, default `false`)
  - Namespace streams based on the configured stream-level scope prefix

- `scope` (AccessTokenScope)
  - Token permissions and resource access

### Test Cases

- **List all access tokens**
  - Parameters: none
  - Expected: 200, up to 1000 tokens returned

- **List with prefix**
  - Parameters: `prefix=test-`
  - Expected: 200, only tokens with IDs starting with "test-"

- **List with start_after**
  - Parameters: `start_after=my-token`
  - Expected: 200, tokens lexicographically after "my-token"

- **List with limit**
  - Parameters: `limit=5`
  - Expected: 200, max 5 tokens, check `has_more`

- **Pagination**
  - Parameters: `start_after` + `limit`
  - Expected: 200, correct pagination behavior

- **Empty prefix**
  - Parameters: `prefix=""`
  - Expected: 200, all tokens

- **Limit = 0**
  - Parameters: `limit=0`
  - Expected: 200, up to 1000 tokens (0 treated as default)

- **Limit > 1000**
  - Parameters: `limit=1001`
  - Expected: 200, up to 1000 tokens (clamped to max)

- **Invalid start_after < prefix**
  - Parameters: `prefix=z`, `start_after=a`
  - Expected: 422 (`invalid`)

- **Permission denied**
  - Setup: token without `list-access-tokens` op
  - Expected: 403 (`permission_denied`)

---

## 2. Issue Access Token

**`POST /access-tokens`**

### Request Body: IssueAccessTokenArgs

- `id` (string, required)
  - Access token ID
  - Constraints:
    - 1-96 bytes
    - Must be unique within the account

- `scope` (AccessTokenScope, required)
  - Token permissions and resource access

- `auto_prefix_streams` (boolean, optional, default `false`)
  - Namespace streams based on the configured stream-level scope prefix
  - When enabled, stream name arguments are automatically prefixed
  - The prefix is stripped when listing streams

- `expires_at` (string, RFC 3339, optional)
  - Token expiration timestamp
  - If not set, inherits expiration from the requestor's token

### AccessTokenScope Object

**Constraint:** The scope must have at least one permission granted via `ops` or `op_groups`. A token with empty permissions (no `ops` and no `op_groups` with `read` or `write` set to `true`) will be rejected with a 422 error.

- `basins` (ResourceSet, optional)
  - Basin names allowed
  - If null/omitted, no basin access

- `streams` (ResourceSet, optional)
  - Stream names allowed
  - If null/omitted, no stream access

- `access_tokens` (ResourceSet, optional)
  - Access token IDs allowed for management
  - If null/omitted, no token management access

- `op_groups` (PermittedOperationGroups, optional)
  - Access permissions at operation group level

- `ops` (array of string, optional)
  - Specific operations allowed
  - Union of `ops` and `op_groups` determines effective permissions

### ResourceSet Object

Exactly one of the following must be set:

- `exact` (string)
  - Match only the resource with this exact name
  - Use empty string `""` to match no resources

- `prefix` (string)
  - Match all resources that start with this prefix
  - Use empty string `""` to match all resources

### PermittedOperationGroups Object

- `account` (ReadWritePermissions, optional)
  - Account-level access permissions

- `basin` (ReadWritePermissions, optional)
  - Basin-level access permissions

- `stream` (ReadWritePermissions, optional)
  - Stream-level access permissions

### ReadWritePermissions Object

- `read` (boolean, default `false`)
  - Read permission

- `write` (boolean, default `false`)
  - Write permission

### Supported Operations

Account-level operations:
- `list-basins`
- `create-basin`
- `delete-basin`
- `reconfigure-basin`
- `get-basin-config`
- `account-metrics`

Basin-level operations:
- `list-streams`
- `create-stream`
- `delete-stream`
- `get-stream-config`
- `reconfigure-stream`
- `basin-metrics`

Stream-level operations:
- `check-tail`
- `append`
- `read`
- `trim`
- `fence`
- `stream-metrics`

Token management operations:
- `issue-access-token`
- `revoke-access-token`
- `list-access-tokens`

### Response Codes

- `201` — Token created
  - Body: `IssueAccessTokenResponse`

- `400` — Bad request / invalid parameters
  - Code: `invalid`

- `403` — Forbidden
  - Codes: `permission_denied`, `quota_exhausted`

- `408` — Timeout
  - Code: `timeout`

- `409` — Conflict (ID already exists)
  - Code: `resource_already_exists`

### Response Schema: IssueAccessTokenResponse

- `access_token` (string)
  - The actual token value (only returned at creation time)

### Test Cases

- **Issue token with empty scope (fails)**
  - Input: `{"id": "test-token-01", "scope": {}}`
  - Expected: 422 (`invalid`) - permissions cannot be empty

- **Issue token with full scope**
  - Input: all scope fields populated
  - Expected: 201

- **Issue token with op_groups.account.read**
  - Input: `{"id": "...", "scope": {"op_groups": {"account": {"read": true}}}}`
  - Expected: 201, token has account read access

- **Issue token with op_groups.account.write**
  - Input: `{"id": "...", "scope": {"op_groups": {"account": {"write": true}}}}`
  - Expected: 201, token has account write access

- **Issue token with op_groups.basin.read**
  - Input: `{"id": "...", "scope": {"op_groups": {"basin": {"read": true}}}}`
  - Expected: 201, token has basin read access

- **Issue token with op_groups.basin.write**
  - Input: `{"id": "...", "scope": {"op_groups": {"basin": {"write": true}}}}`
  - Expected: 201, token has basin write access

- **Issue token with op_groups.stream.read**
  - Input: `{"id": "...", "scope": {"op_groups": {"stream": {"read": true}}}}`
  - Expected: 201, token has stream read access

- **Issue token with op_groups.stream.write**
  - Input: `{"id": "...", "scope": {"op_groups": {"stream": {"write": true}}}}`
  - Expected: 201, token has stream write access

- **Issue token with specific ops**
  - Input: `{"id": "...", "scope": {"ops": ["list-basins", "read"]}}`
  - Expected: 201, token limited to specified ops

- **Issue token with basins.exact**
  - Input: `{"id": "...", "scope": {"basins": {"exact": "my-basin"}, "ops": ["list-basins"]}}`
  - Expected: 201, token limited to exact basin

- **Issue token with basins.prefix**
  - Input: `{"id": "...", "scope": {"basins": {"prefix": "test-"}, "ops": ["list-basins"]}}`
  - Expected: 201, token limited to basins with prefix

- **Issue token with streams.exact**
  - Input: `{"id": "...", "scope": {"streams": {"exact": "my-stream"}, "ops": ["read"]}}`
  - Expected: 201, token limited to exact stream

- **Issue token with streams.prefix**
  - Input: `{"id": "...", "scope": {"streams": {"prefix": "logs/"}, "ops": ["read"]}}`
  - Expected: 201, token limited to streams with prefix

- **Issue token with access_tokens.exact**
  - Input: `{"id": "...", "scope": {"access_tokens": {"exact": "child-token"}, "ops": ["list-access-tokens"]}}`
  - Expected: 201, token can only manage exact token ID

- **Issue token with access_tokens.prefix**
  - Input: `{"id": "...", "scope": {"access_tokens": {"prefix": "child-"}, "ops": ["list-access-tokens"]}}`
  - Expected: 201, token can manage tokens with prefix

- **Issue token with basins.exact = "" (deny all basins)**
  - Input: `{"id": "...", "scope": {"basins": {"exact": ""}, "ops": ["list-basins"]}}`
  - Expected: 201, token cannot access any basins
  - Verify: any basin operation returns 403

- **Issue token with basins.prefix = "" (allow all basins)**
  - Input: `{"id": "...", "scope": {"basins": {"prefix": ""}, "ops": ["list-basins"]}}`
  - Expected: 201, token can access all basins
  - Verify: basin operations succeed on any basin

- **Issue token with streams.exact = "" (deny all streams)**
  - Input: `{"id": "...", "scope": {"streams": {"exact": ""}, "ops": ["read"]}}`
  - Expected: 201, token cannot access any streams
  - Verify: any stream operation returns 403

- **Issue token with streams.prefix = "" (allow all streams)**
  - Input: `{"id": "...", "scope": {"streams": {"prefix": ""}, "ops": ["read"]}}`
  - Expected: 201, token can access all streams
  - Verify: stream operations succeed on any stream

- **Issue token with auto_prefix_streams = true**
  - Input: `{"id": "...", "scope": {"streams": {"prefix": "ns/"}, "ops": ["read"]}, "auto_prefix_streams": true}`
  - Expected: 201, stream names will be auto-prefixed

- **Issue token with expires_at**
  - Input: `{"id": "...", "scope": {"ops": ["list-basins"]}, "expires_at": "<future_timestamp>"}`
  - Expected: 201, token has custom expiration

- **Issue token with past expires_at**
  - Input: `{"id": "...", "scope": {"ops": ["list-basins"]}, "expires_at": "<past_timestamp>"}`
  - Expected: 422 (`invalid`)

- **ID empty (0 bytes)**
  - Input: `{"id": "", "scope": {"ops": ["list-basins"]}}`
  - Expected: Error (SDK may validate client-side, or 400 from server)

- **ID minimum length (1 byte)**
  - Input: `{"id": "a", "scope": {"ops": ["list-basins"]}}`
  - Expected: 201, token created successfully

- **ID maximum length (96 bytes)**
  - Input: `{"id": "a" * 96, "scope": {"ops": ["list-basins"]}}` (exactly 96 bytes)
  - Expected: 201, token created successfully

- **ID too long (97 bytes)**
  - Input: `{"id": "a" * 97, "scope": {"ops": ["list-basins"]}}` (> 96 bytes)
  - Expected: Error (SDK may validate client-side, or 400 from server)

- **Duplicate ID**
  - Input: create same ID twice with `{"scope": {"ops": ["list-basins"]}}`
  - Expected: 409 (`resource_already_exists`)

- **Permission denied**
  - Setup: token without `issue-access-token` op
  - Expected: 403 (`permission_denied`)

- **Scope escalation attempt**
  - Setup: try to issue token with broader scope than requestor
  - Expected: 422 (`invalid`)

---

## 3. Revoke Access Token

**`DELETE /access-tokens/{id}`**

### Path Parameters

- `id` (string, required)
  - Access token ID to revoke
  - Constraints: 1-96 bytes

### Response Codes

- `204` — Token revoked (no body)

- `400` — Bad request
  - Code: `invalid`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — Token not found
  - Code: `access_token_not_found`

- `408` — Timeout
  - Code: `timeout`

### Test Cases

- **Revoke existing token**
  - Input: valid token ID
  - Expected: 204

- **Revoke non-existent token**
  - Input: ID that doesn't exist
  - Expected: 404 (`access_token_not_found`)

- **Revoke already revoked token**
  - Input: previously revoked token ID
  - Expected: 404 (`access_token_not_found`)

- **Revoke self**
  - Input: revoke the token being used for authentication
  - Expected: 204 (allowed, subsequent requests will fail)

- **Verify token unusable after revoke**
  - Setup: issue token, revoke it, try to use it
  - Expected: revoked token returns 403 on use

- **Permission denied**
  - Setup: token without `revoke-access-token` op
  - Expected: 403 (`permission_denied`)

- **Token ID outside scope**
  - Setup: token scoped to manage `child-*` tokens, try to revoke `other-token`
  - Expected: 403 (`permission_denied`)

---

## Complete Scope Matrix

### ResourceSet Values

- `null` / omitted
  - No access to this resource type

- `{"exact": ""}`
  - Match no resources

- `{"exact": "name"}`
  - Match only the exact resource name

- `{"prefix": ""}`
  - Match all resources

- `{"prefix": "test-"}`
  - Match resources starting with prefix

### Operation Group Permissions

#### Account Level (`op_groups.account`)
- `read`: `list-basins`, `get-basin-config`, `list-access-tokens`, `account-metrics`
- `write`: `create-basin`, `delete-basin`, `reconfigure-basin`, `issue-access-token`, `revoke-access-token`

#### Basin Level (`op_groups.basin`)
- `read`: `list-streams`, `get-stream-config`, `basin-metrics`
- `write`: `create-stream`, `delete-stream`, `reconfigure-stream`

#### Stream Level (`op_groups.stream`)
- `read`: `check-tail`, `read`, `stream-metrics`
- `write`: `append`, `trim`, `fence`

---

## SDK Client Lifecycle Tests

### Test Cases

- **Client with issued token**
  - Description: issue a token and create a new client with it
  - Expected: client works with correct permissions

- **Client with revoked token**
  - Description: use a client after its token has been revoked
  - Expected: 403 on all operations

- **Client with expired token**
  - Description: use a client after its token has expired
  - Expected: 403 on all operations

- **Token scope enforcement**
  - Description: try operations outside token scope
  - Expected: 403 for unauthorized operations

- **Token hierarchy**
  - Description: issue child token, verify it cannot escalate privileges
  - Expected: child token is constrained by parent's scope

---

## Error Codes Reference

- `400` `bad_json`
  - Malformed JSON in request body

- `400` `bad_query`
  - Invalid query parameters

- `400` `bad_path`
  - Invalid path parameter

- `422` `invalid`
  - Validation errors (ID length, scope issues, expiration)

- `403` `permission_denied`
  - Token lacks required permissions

- `403` `quota_exhausted`
  - Account token limit reached

- `404` `access_token_not_found`
  - Access token does not exist

- `408` `timeout`
  - Request timeout

- `409` `resource_already_exists`
  - Token ID already taken

- `500` `other`
  - Internal server error
