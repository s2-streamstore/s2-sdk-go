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
[S2 API](https://s2.dev/docs/interface/grpc).

## Getting started

1. Create a new Go project:
   ```bash
   mkdir s2-sdk-test
   cd s2-sdk-test
   go mod init example.com/s2-sdk-test
   touch main.go
   ```

1. Add the `s2` dependency to your project:
   ```bash
   go get github.com/s2-streamstore/s2-sdk-go/s2@latest
   ```

1. Generate an authentication token by logging onto the web console at
   [s2.dev](https://s2.dev/dashboard).

1. Make a request using SDK client.
   ```go
   // main.go

   package main

   import (
     "context"
     "fmt"

     "github.com/s2-streamstore/s2-sdk-go/s2"
   )

   func main() {
     client, err := s2.NewClient("<YOUR AUTH TOKEN>")
     if err != nil {
       panic(err)
     }

     basins, err := client.ListBasins(context.TODO(), &s2.ListBasinsRequest{})
     if err != nil {
       panic(err)
     }

     fmt.Println(basins)
   }
   ```

   Run the above program using `go run main.go`.

## Examples

The [`s2/example_test.go`](./s2/example_test.go) contains a variety of example
use cases demonstrating how to use the SDK effectively.

### Starwars

For a more practical example, try out the SDK by running the Starwars example:

```bash
go run ./examples/starwars \
  -basin "<basin name>"    \
  -stream "<stream name>"  \
  -token "<auth token>"
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

This project is licensed under the [Apache-2.0 License](./LICENSE).
