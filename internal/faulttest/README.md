The harness drives the public SDK through HTTP/2 fault servers. It checks retry
policies, ACK ranges, payloads, read limits, and reconnects against a record model:

```sh
go test -race ./internal/faulttest
go test ./internal/faulttest -run '^$' -fuzz '^FuzzReplay$' -fuzztime=30s -parallel=1
```

Sequential failures print a JSON trace and protocol history. Save the trace to replay:

```sh
S2_FAULT_TRACE=trace.json go test ./internal/faulttest -run '^TestReplay$' -count=1
```

Concurrent tests race unary appends, sessions, producers, reads, and tail checks,
including sequence guards and fencing. A proxy drops requests before commit,
withholds committed ACKs, and resets reads after delivery. Histories are checked
by the same pinned Porcupine model used by S2's simulator:

```sh
git clone https://github.com/s2-streamstore/s2-verification /tmp/s2-verification
git -C /tmp/s2-verification checkout b4af8c8ef4965d9b335101c422eadb33f3169004
(cd /tmp/s2-verification/golang/s2-porcupine && go build -mod=readonly -o /tmp/s2-porcupine .)
export S2_PORCUPINE=/tmp/s2-porcupine
go test -race ./internal/faulttest
go test ./internal/faulttest -run '^$' -fuzz '^FuzzConcurrent$' -fuzztime=30s -parallel=1
```

The durability test starts Lite with disk storage, withholds a committed ACK,
SIGKILLs its process, and restarts it on the same storage. It checks the history,
every acknowledged payload, a second restart, and persisted fencing. Run with
an installed S2 CLI or set `S2_LITE_BIN` to a standalone Lite server:

```sh
S2_LITE_BIN=s2 S2_LITE_ARGS='["lite"]' go test -race ./internal/faulttest -run '^TestLiteDurability$'
```

Concurrent failures retain `trace.json`, `wire.json`, `history.jsonl`, and any
checker HTML visualization in the logged temporary directory. Set
`S2_FAULT_OUTPUT` to choose its parent. Successful runs clean up their artifacts.

```sh
S2_CONCURRENT_TRACE=/path/to/trace.json go test ./internal/faulttest -run '^TestConcurrent$' -count=1
/tmp/s2-porcupine -file=/path/to/history.jsonl
```

Use `TestLiteDurability` with the Lite environment above to replay crash traces.
The trace repeats waves and fault triggers; concurrent scheduling can vary.
The saved JSONL rechecks the exact observed history. Unknown appends remain
pending in that history; concurrent writers use `NoSideEffects` so retries
cannot intentionally duplicate an unconditional append.

Checker and Lite tests skip when their binaries are unset. CI supplies both,
runs the race detector and both fuzzers, and uploads failure artifacts.
`TestConcurrentEndpoint` also accepts `S2_FAULT_ENDPOINT` and
`S2_FAULT_ACCESS_TOKEN` for an existing shared account/basin endpoint; it creates
and deletes a dedicated test basin.
