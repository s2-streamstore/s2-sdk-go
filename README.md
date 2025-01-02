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
    <!-- TODO: Badges for pkg.go.dev and CI -->
    <!-- Discord (chat) -->
    <a href="https://discord.gg/vTCs7kMkAf"><img src="https://img.shields.io/discord/1209937852528599092?logo=discord" /></a>
    <!-- LICENSE -->
    <a href="./LICENSE"><img src="https://img.shields.io/github/license/s2-streamstore/s2-sdk-go" /></a>
  </p>
</div>

The Go SDK provides ergonomic wrappers and utilities to interact with the
[S2 API](https://s2.dev/docs/interface/grpc).

---

This project is a **WORK IN PROGRESS**

## Starwars

Try out the SDK by running the Starwars example:

```bash
go run ./examples/starwars \
  -basin "<basin name>"    \
  -stream "<stream name>"  \
  -token "<auth token>"
```
