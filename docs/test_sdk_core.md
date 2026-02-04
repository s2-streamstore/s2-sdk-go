# SDK Core Behaviors - Test Matrix

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
> - Only implement tests for features the SDK actually exposes
> - Prefer unit tests for helpers; use integration tests only when required
>
> **Error handling guidance:**
> - Client-side validation errors may not include HTTP status codes
> - For tests expecting errors, verify an error occurred and check fields the SDK exposes
>
> **Linter and formatter compliance:**
> - Always run the project's linter and formatter after generating code
> - Fix ALL linter warnings and errors - do not leave any unresolved
> - Follow the project's existing code style (check existing test files for patterns)

---

This document captures SDK-specific behaviors not tied to a single API endpoint
(client configuration, retry semantics, batching helpers, producer abstractions,
and error mapping).

## 1. Endpoint Configuration (if SDK exposes endpoint builder or config)

### Test Cases

- **Default endpoints**
  - Input: no endpoint configuration provided
  - Expected: SDK uses default service endpoints and appends `/v1` as appropriate

- **Implicit scheme**
  - Input: endpoint without scheme (e.g., `example.com:8443`)
  - Expected: SDK assumes `https` and preserves host/port

- **Explicit path**
  - Input: endpoint containing a path or trailing slash
  - Expected: SDK uses the path verbatim and does not append `/v1`

- **Trailing slash treated as explicit path**
  - Input: endpoint ending with `/` (e.g., `https://example.com/`)
  - Expected: SDK preserves trailing slash and does not append `/v1`

- **Basin placeholder**
  - Input: basin endpoint template containing `{basin}` in host or path
  - Expected: placeholder replaced and basin name is URL-encoded

- **Basin placeholder in path with encoding**
  - Input: basin endpoint template with `{basin}` in path and basin name containing `/`
  - Expected: basin name is URL-encoded in the path (e.g., `a/b` -> `a%2Fb`)

- **Shared basin endpoint uses basin header (if supported)**
  - Input: basin endpoint without `{basin}`
  - Expected: SDK includes basin header (or equivalent) on basin-scoped requests

- **Reject empty endpoint**
  - Input: endpoint string is empty or whitespace
  - Expected: SDK validation error

---

## 2. Error Mapping (if SDK exposes typed errors)

### Test Cases

- **Server error mapping**
  - Setup: server responds with error payload containing `message` and `code`
  - Expected: SDK error exposes message, code, and HTTP status

- **Fallback error message**
  - Setup: server error response without `message`
  - Expected: SDK error message falls back to HTTP status text (or equivalent)

- **Wrapped thrown errors**
  - Setup: transport throws a generic error
  - Expected: SDK error preserves message and marks status as 0 or "unknown"

---

## 3. Retry and Backoff (if SDK exposes retry config)

### Test Cases

- **Default retry config**
  - Expected: default maxAttempts/min/max base delay match SDK defaults

- **Retryable vs non-retryable**
  - Setup: operation fails with 5xx or 408 then succeeds
  - Expected: SDK retries
  - Setup: operation fails with 4xx
  - Expected: SDK does not retry

- **MaxAttempts respected**
  - Setup: retryable error always returned
  - Expected: SDK attempts exactly MaxAttempts times

- **Append retry policy (noSideEffects)**
  - Setup: appendRetryPolicy=noSideEffects
  - Input: append with match_seq_num explicitly set (including 0)
  - Expected: retries on retryable errors
  - Input: append without match_seq_num
  - Expected: no retry

- **Read session retry adjusts limits (if SDK auto-retries streaming reads)**
  - Setup: streaming read returns some records then transient error
  - Expected: retry uses remaining count/bytes (no duplicate records)

- **Read session retry adjusts wait/seq_num (if SDK auto-retries streaming reads)**
  - Setup: streaming read returns some records then transient error
  - Expected: retry decrements remaining wait time and resumes from next seq_num

- **Read session retry keeps absolute until unchanged**
  - Setup: streaming read with `until` set; transient error after some records
  - Expected: retry preserves `until` value

- **Read session retry combines adjustments**
  - Setup: streaming read with count/bytes/wait/seq_num/until set
  - Expected: retry adjusts count/bytes/wait/seq_num consistently; until unchanged

- **Read session retry max attempts**
  - Setup: transient error persists across all attempts
  - Expected: iteration fails after maxAttempts

- **Read session retry does not double-subtract**
  - Setup: multiple retries after partial reads
  - Expected: remaining count reflects total records read across attempts (no over-subtraction)

- **Append session retry behavior (if SDK exposes)**
  - Setup: send-phase transient error then success
  - Expected: session recovers and acks resolve
  - Setup: retries disabled (maxAttempts=1)
  - Expected: ack fails immediately with error
  - Setup: appendRetryPolicy=noSideEffects with non-idempotent inflight
  - Expected: no retry; failure cause exposed (if SDK exposes failureCause)

---

## 4. Batching Helpers (if SDK exposes Batcher/BatchTransform)

### Test Cases

- **Flush on max records**
  - Setup: maxBatchRecords=2, write 3 records
  - Expected: first batch has 2 records, second has 1

- **Flush on max bytes**
  - Setup: maxBatchBytes small, write records exceeding limit
  - Expected: batch boundary splits to honor max bytes

- **Linger flush**
  - Setup: linger>0, write records then wait
  - Expected: single batch emitted after linger

- **Close flushes remaining**
  - Setup: write records then close
  - Expected: final batch emitted, then stream ends

- **Oversized record rejected**
  - Input: single record > maxBatchBytes
  - Expected: error on write or read (SDK validation)

- **Fencing token propagation**
  - Setup: configure fencing token
  - Expected: batches include fencing token

- **MatchSeqNum auto-increments**
  - Setup: matchSeqNum set on batcher
  - Expected: first batch uses matchSeqNum, subsequent batches increment by batch size

- **Batcher -> append session pipe (if SDK exposes)**
  - Setup: pipe batcher output into append session
  - Expected: linger-based batching yields expected number of submissions
  - Expected: matchSeqNum increments across flushes
  - Expected: acks stream yields one ack per batch

- **Invalid configuration handling**
  - Input: maxBatchRecords>1000 or maxBatchBytes>1 MiB
  - Expected: SDK either rejects config or clamps to limits; tests should accept documented behavior

---

## 5. Producer (if SDK exposes a Producer abstraction)

### Test Cases

- **Per-record ack ordering**
  - Setup: submit multiple records
  - Expected: per-record acks resolve in submit order with increasing seq_nums

- **Preserves record order across batching**
  - Setup: submit many records to a producer with batching enabled
  - Expected: underlying append order matches input order

- **Waits for appendSession submit to preserve ordering**
  - Setup: appendSession submit resolves out-of-order if not awaited
  - Expected: producer serializes submits to preserve match_seq_num ordering

- **Concurrent submits preserve order**
  - Setup: submit many records concurrently
  - Expected: producer serializes batches; no seq_num mismatch

- **Producer close drains**
  - Setup: submit records then close
  - Expected: close waits for pending acks; no lost records

- **Error propagation**
  - Setup: underlying append/session fails
  - Expected: pending record acks reject with error

- **Producer bytes round-trip (integration)**
  - Setup: submit bytes records via producer; unary read back as bytes
  - Expected: record bodies match exactly

- **Oversized record rejection**
  - Setup: submit record exceeding max batch bytes
  - Expected: producer or writable stream rejects with size error

- **Integration gapless read**
  - Setup: producer writes N records while read session reads concurrently
  - Expected: read sees a contiguous seq_num range without gaps
