The harness drives the public Go SDK through an HTTP/2 proxy to real S2 Lite.
Each test owns its basin and stream. Lite uses a temporary disk directory; the
managed process is killed on cleanup, with files retained on failure.

Set `S2_LITE_BIN` to a standalone Lite server or use the S2 CLI:

```sh
export S2_LITE_BIN=s2
export S2_LITE_ARGS='["lite"]'
go test -race ./internal/faulttest
```

Replay tests check exact retry counts, ACK ranges, payloads, and tail positions
against a model derived from the trace. They use fixed client timestamps and
one append batch per session. The proxy rejects a request before forwarding it
or substitutes an error after observing Lite's commit ACK. Negative controls
corrupt ACKs and payloads to verify the assertions reject them.

Failures retain `trace.json` and `wire.json` in the logged artifact directory:

```sh
S2_FAULT_TRACE=/path/to/trace.json go test ./internal/faulttest -run '^TestReplay$' -count=1
```

Streaming reads exercise record/byte limits, compression, fragmented responses,
and resets or errors after record delivery. Fuzz bytes generate the workload
and fault plan; sequential replay compares the logical protocol history.

```sh
go test ./internal/faulttest -run '^$' -fuzz '^FuzzReplay$' -fuzztime=30s -parallel=1
```

The shared Porcupine adapter checks rolling hash vectors and accepts/rejects
known histories, including uncertain appends and lost acknowledged records.

```sh
git clone https://github.com/s2-streamstore/s2-verification /tmp/s2-verification
git -C /tmp/s2-verification checkout b4af8c8ef4965d9b335101c422eadb33f3169004
(cd /tmp/s2-verification/golang/s2-porcupine && go build -mod=readonly -o /tmp/s2-porcupine .)
export S2_PORCUPINE=/tmp/s2-porcupine
go test -race ./internal/faulttest
```

Concurrent tests race unary appends, sessions, producers, reads, and tail checks
against Lite through the proxy, with sequence guards, fencing, and faults.
Each trace gets a fresh basin; fuzz workers share the coordinator-owned Lite process.

```sh
go test ./internal/faulttest -run '^$' -fuzz '^FuzzConcurrent$' -fuzztime=30s -parallel=1
S2_CONCURRENT_TRACE=/path/to/trace.json go test ./internal/faulttest -run '^TestConcurrent$' -count=1
/tmp/s2-porcupine -file=/path/to/history.jsonl
```

Concurrent failures retain `history.jsonl` and any checker visualization.
A trace repeats inputs and fault triggers; Lite timing and goroutine scheduling
can vary. JSONL rechecks the exact observed history. Unknown appends remain
pending; writers use `NoSideEffects` to avoid intentional duplicates.

Tests requiring a binary skip when it is unset. CI supplies the required
binaries and runs the race detector and fuzzers. `S2_FAULT_OUTPUT` chooses the
artifact parent directory; successful runs clean up their files.
