# Locations API - SDK Test Matrix

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
> - Show progress as each test progresses
> - Set a timeout for each test
> - The set of available locations is account- and service-dependent; assert on
>   shape and invariants rather than hard-coding specific location names
> - `SetDefault` mutates account-wide state; restore the prior default in cleanup
>   when running against real backends

---

This document enumerates every knob/parameter of the Locations API to ensure SDK test coverage.

Locations describe where basins can be placed. They are an account-level
concept, accessed from the top-level client (not scoped to a basin).

## Endpoints Overview

- `GET /locations` — `list_locations` — List locations available to the account
- `GET /locations/default` — `get_default_location` — Get the account's default location
- `PUT /locations/default` — `set_default_location` — Set the account's default location

---

## 1. List Locations

**`GET /locations`** — SDK `Locations.List`

Lists the locations available to the account.

### Response Codes

- `200` — Success
  - Body: array of `LocationInfo`

- `403` — Forbidden
  - Code: `permission_denied`
  - Cause: token lacks the required permission

- `408` — Timeout
  - Code: `request_timeout`

### Response Schema: LocationInfo

- `name` (LocationName)
  - Location name

- `is_private` (boolean)
  - `true` for account-private placements

### Test Cases

- **List available locations**
  - Input: none
  - Expected: 200, array of `LocationInfo`; each entry has a non-empty `name`

- **Public and private flag present**
  - Input: none
  - Expected: each entry exposes an `is_private` boolean

- **Permission denied**
  - Setup: token lacking the required permission
  - Expected: 403 (`permission_denied`)

---

## 2. Get Default Location

**`GET /locations/default`** — SDK `Locations.GetDefault`

Returns the account's default location, used when a basin is created without an
explicit location.

### Response Codes

- `200` — Success
  - Body: `LocationInfo`

- `403` — Forbidden
  - Code: `permission_denied`

- `404` — No default location set
  - Code: `not_found`

- `408` — Timeout
  - Code: `request_timeout`

### Test Cases

- **Get default location**
  - Setup: account with a default location
  - Input: none
  - Expected: 200, `LocationInfo` whose `name` appears in `List`

- **Default consistency with List**
  - Input: call `GetDefault` then `List`
  - Expected: the default's `name` is one of the listed locations

- **Permission denied**
  - Setup: token lacking the required permission
  - Expected: 403 (`permission_denied`)

---

## 3. Set Default Location

**`PUT /locations/default`** — SDK `Locations.SetDefault`

Sets the account's default location. Body is the `LocationName`.

### Arguments

- `location` (LocationName, required)
  - Location name to set as the account default
  - SDK rejects an empty string client-side (no request sent)

### Response Codes

- `200` — Success
  - Body: `LocationInfo` reflecting the new default

- `403` — Forbidden
  - Code: `permission_denied`

- `408` — Timeout
  - Code: `request_timeout`

- `422` — Validation error
  - Code: `invalid`
  - Cause: unknown or non-selectable location

### Test Cases

- **Set default to a known location**
  - Setup: pick a location returned by `List`
  - Input: that location name
  - Expected: 200, returned `LocationInfo.name` equals the requested name

- **Set default then read back**
  - Input: `SetDefault(loc)` followed by `GetDefault`
  - Expected: `GetDefault` returns the same location

- **Set default to empty string**
  - Input: `""`
  - Expected: client-side validation error (no request sent)

- **Set default to unknown location**
  - Input: a name not present in `List`
  - Expected: 422 (`invalid`)

- **Permission denied**
  - Setup: token lacking the required permission
  - Expected: 403 (`permission_denied`)

---

## Cross-API Notes

- A basin's `location` (see Basin API) must be one of the locations exposed by
  `List`. When a basin is created without a location, the service uses the value
  from `GetDefault`.
- A basin's location cannot be changed once the basin exists; changing the
  account default via `SetDefault` does not relocate existing basins.
