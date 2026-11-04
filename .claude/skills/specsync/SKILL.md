---
name: specsync
description: Sync the hand-written doc comments in s2/types.go with the s2-specs OpenAPI spec. Use after bumping the s2-specs submodule, or when asked to "sync types with the spec", "update type docs", or "check types.go against the spec".
---

# /specsync

Keep `s2/types.go` in sync with the OpenAPI spec in the `s2-specs` submodule.

The types are hand-written on purpose: curated field order, reshaped unions, branded
scalars, and SDK-specific ergonomics that no generator should flatten. The doc comments are
copied from the spec by hand, so they go stale when the spec changes. This skill updates
those doc comments and reports structural drift. It does not regenerate the file.

## Sources of truth

- Spec: `s2-specs/s2/v1/openapi.json`
  - `components.schemas.<Name>`: the data types (fields, enums, descriptions).
  - `paths.<path>.<method>.parameters[]`: query parameters for list and metrics operations.
- Code: `s2/types.go`

## What to edit vs. what to flag

Edit the file for doc-comment text that has drifted from the spec, where the spec wording
is the intended source. That is the main job.

Flag for the human (do not edit), then ask, for structural drift: a spec field with no Go
field, a Go field whose JSON tag is no longer in the spec, an enum value added or removed,
or a Go type that changed. The Go type, pointer-ness, and field order are judgment calls.
Report them, do not guess.

## How things map

- `internal/specsync/config.json` is the source of truth for schema renames, operation-to-Args
  mappings, intentional skips, and accepted doc differences.
- Properties and query parameters map to Go fields by the `json:"..."` tag, with
  `,omitempty` ignored. The checker applies generic whitespace and Markdown normalization.

## Intentional divergences

`internal/specsync/config.json` is the source of truth for deliberate differences. Record a
wording difference as an `accepted` entry with the exact current spec and Go text and a reason.
Record SDK-owned schemas and operations in the corresponding `skipTypes`, `ignoreSchemas`, or
`ignoreOperations` map. Review those entries when the spec or SDK model changes.

## Steps

1. Update the spec if asked. Bump the submodule and confirm what changed:
   ```bash
   git -C s2-specs fetch origin --quiet && git -C s2-specs checkout main --quiet && git -C s2-specs pull --quiet
   git diff --submodule=log s2-specs
   ```
   Proto is only regenerated when `s2-specs/s2/v1/s2.proto` changes (`task gen`). An
   `openapi.json`-only change leaves `generated/` untouched.

2. Run `task specsync:check`. It compares docs, fields, query parameters, enums, mappings,
   and configured exceptions. Resolve genuine doc drift and investigate structural findings.

3. Inject the spec wording for every genuine doc drift that is not an accepted divergence.
   Edit the relevant `//` comment lines. Keep field order, types, and tags unchanged.

4. Do not edit structural drift automatically. Flag any unexplained field or enum changes
   for a human decision.

5. Verify:
   ```bash
   gofmt -l s2/types.go    # must print nothing
   go build ./... && go test ./s2/ -count=1
   ```

6. Report: list the docs you changed, the intentional divergences you left, and any
   structural drift that needs a decision.
