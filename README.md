<div align="center">
  <p>
    <!-- Light mode logo -->
    <a href="https://s2.dev#gh-light-mode-only">
      <img src="./assets/s2-black.png" height="60">
    </a>
    <!-- Dark mode logo -->
    <a href="https://s2.dev#gh-dark-mode-only">
      <img src="./assets/s2-white.png" height="60">
    </a>
  </p>

  <h1>Go SDK for S2</h1>

  <p>
    <!-- pkg.go.dev -->
    <a href="https://pkg.go.dev/github.com/s2-streamstore/s2-sdk-go/s2"><img src="https://pkg.go.dev/badge/github.com/s2-streamstore/s2-sdk-go/s2.svg" /></a>
    <!-- Discord (chat) -->
    <a href="https://discord.gg/vTCs7kMkAf"><img src="https://img.shields.io/discord/1209937852528599092?logo=discord" /></a>
    <!-- Github Actions (CI) -->
    <a href="https://github.com/s2-streamstore/s2-sdk-go/actions?query=branch%3Amain++"><img src="https://github.com/s2-streamstore/s2-sdk-go/actions/workflows/ci.yml/badge.svg" /></a>
    <!-- LICENSE -->
    <a href="./LICENSE"><img src="https://img.shields.io/github/license/s2-streamstore/s2-sdk-go" /></a>
  </p>
</div>

The Go SDK provides ergonomic wrappers and utilities to interact with the
[S2 API](https://s2.dev/docs).

## Getting started

1. Create a new Go project:
   ```bash
   mkdir s2-example
   cd s2-example
   go mod init example.com/s2-example
   ```

2. Add the SDK dependency:
   ```bash
   go get github.com/s2-streamstore/s2-sdk-go/s2@latest
   ```

3. Generate an authentication token by logging onto the web console at
   [s2.dev](https://s2.dev/dashboard).

4. Set the token as an environment variable:
   ```bash
   export S2_ACCESS_TOKEN="<your auth token>"
   ```

5. Create a simple program:
   ```go
   package main

   import (
       "context"
       "fmt"
       "log"
       "os"

       "github.com/s2-streamstore/s2-sdk-go/s2"
   )

   func main() {
       client := s2.New(os.Getenv("S2_ACCESS_TOKEN"), nil)

       basins := client.Basins.Iter(context.Background(), nil)
       for basins.Next() {
           fmt.Printf("Basin: %s\n", basins.Value().Name)
       }
       if err := basins.Err(); err != nil {
           log.Fatal(err)
       }
   }
   ```

   Run with `go run main.go`.

## Examples

The [`examples/`](./examples) directory contains various examples:

| Example | Description |
|---------|-------------|
| [`unary`](./examples/unary) | Simple append and read operations |
| [`append_session`](./examples/append_session) | High-throughput streaming appends |
| [`batched_append_session`](./examples/batched_append_session) | Batched appends |
| [`read_session`](./examples/read_session) | Streaming reads with backpressure |
| [`list_iter`](./examples/list_iter) | Iterating over basins, streams, and tokens |
| [`access_tokens`](./examples/access_tokens) | Issuing and revoking access tokens |
| [`starwars`](./examples/starwars) | Stream Star Wars ASCII animation through S2 |

Run any example:
```bash
# edit the code to set an appropriate basin and stream
export S2_ACCESS_TOKEN="<your auth token>"
go run ./examples/unary
```

### Starwars

For a fun demo, try streaming the Star Wars ASCII animation through S2:

```bash
# edit the code to set an appropriate basin and stream
export S2_ACCESS_TOKEN="<your auth token>"
go run ./examples/starwars -basin "<basin name>" -stream "<stream name>"
```

## SDK Docs and Reference

Head over to [pkg.go.dev](https://pkg.go.dev/github.com/s2-streamstore/s2-sdk-go/s2)
for detailed documentation and package reference.

## Feedback

We use [Github Issues](https://github.com/s2-streamstore/s2-sdk-go/issues) to
track feature requests and issues with the SDK. If you wish to provide feedback,
report a bug or request a feature, feel free to open a Github issue.

### Contributing

Developers are welcome to submit Pull Requests on the repository. If there is
no tracking issue for the bug or feature request corresponding to the PR, we
encourage you to open one for discussion before submitting the PR.

## Reach out to us

Join our [Discord](https://discord.gg/vTCs7kMkAf) server. We would love to hear
from you.

You can also email us at [hi@s2.dev](mailto:hi@s2.dev).

## License

This project is licensed under the [MIT License](./LICENSE).
