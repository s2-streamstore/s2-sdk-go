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

- Schema to Go type: same name, except the branded scalar renames:
  `AccessTokenIdStr` to `AccessTokenID`, `BasinNameStr` to `BasinName`, `StreamNameStr` to
  `StreamName`.
- Property to Go field: match by the `json:"..."` tag (strip `,omitempty`), not the field
  name. `id` becomes `ID`, `seq_num` becomes `SeqNum`, and so on.
- Query param to Go field: list and metrics operations map to `…Args` structs. Match each
  `in: query` parameter to the field by its JSON tag:
  - `list_streams` to `ListStreamsArgs`
  - `list_basins` to `ListBasinsArgs`
  - `list_access_tokens` to `ListAccessTokensArgs`
- Doc comparison: ignore line-wrapping. Go reflows comments across multiple `//` lines.
  Compare the collapsed text, and only treat a real wording change as drift.

## Intentional divergences: keep these, do not change them

These are deliberate. If the only difference is one of the following, leave the Go doc as is:

- `time.Time` fields drop "in RFC 3339 format". The spec says e.g. "Creation time in RFC
  3339 format.". The SDK uses `time.Time`, so the Go doc is just "Creation time.".
- `*bool` config fields. `BasinConfig.CreateStreamOnAppend` and `CreateStreamOnRead` are
  `*bool` though the spec marks them plain `boolean`. The pointer distinguishes unset from
  false.
- Extra SDK guidance. `StartAfter` docs carry "It must be greater than or equal to the
  `prefix` if specified.", which the spec omits.
- Reshaped or hand-owned types, skip entirely: `Metric` (spec `oneOf`, modeled as a flat
  struct), `Header` (spec `[2]string`, modeled as a struct), `SequencedRecord`,
  `AppendRecord`, `AppendInput` (binary `[]byte` bodies), `GaugeMetric` and
  `AccumulationMetric` tuple values, the four `*Reconfiguration` types (Clear* fields plus
  custom MarshalJSON), and all `…Args`, `…Options`, and `…Response` types that have no
  schema. Their docs are SDK-authored, not spec-derived.

If you are unsure whether a divergence is intentional, ask before changing it.

## Steps

1. Update the spec if asked. Bump the submodule and confirm what changed:
   ```bash
   git -C s2-specs fetch origin --quiet && git -C s2-specs checkout main --quiet && git -C s2-specs pull --quiet
   git diff --submodule=log s2-specs
   ```
   Proto is only regenerated when `s2-specs/s2/v1/s2.proto` changes (`task gen`). An
   `openapi.json`-only change leaves `generated/` untouched.

2. Diff docs. For each non-excluded schema, read the `description` for every property
   (prefer the property description, then a `oneOf` branch description). Do the same for the
   mapped query parameters. Compare against the matching Go field's doc comment, ignoring
   wrapping.

3. Inject the spec wording for every genuine doc drift that is not one of the intentional
   divergences above. Edit the `//` comment lines in `s2/types.go`. Keep the existing field
   order, types, and tags. Re-wrap long comments to match the surrounding style.

4. Flag structural drift (missing or extra fields, type or enum changes) in your summary and
   ask how to handle it. Do not edit struct fields yourself.

5. Verify:
   ```bash
   gofmt -l s2/types.go    # must print nothing
   go build ./... && go test ./s2/ -count=1
   ```

6. Report: list the docs you changed, the intentional divergences you left, and any
   structural drift that needs a decision.
