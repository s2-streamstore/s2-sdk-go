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

Tests requiring a binary skip when it is unset. CI supplies the required
binaries and runs the race detector. `S2_FAULT_OUTPUT` chooses the
artifact parent directory; successful runs clean up their files.
