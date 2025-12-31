# Changelog

All notable changes to this project will be documented in this file.

## [v0.11.0] - 2025-12-31

### Added

- AppendSession for streaming appends with automatic retries
- ReadSession for streaming reads with automatic retries
- Producer API with batching support
- Batcher with configurable size, count, and time thresholds
- Iterator support for basins, streams, and access tokens
- Metrics API support
- Configurable retry policies

### Changed

- Migrated from gRPC to REST
- Simplified API surface

## [v0.10.0] - 2025-07-23

### Features

- Clamp ([#99](https://github.com/s2-streamstore/s2-sdk-go/issues/99))

## [v0.9.0] - 2025-06-16

### Bug Fixes

- Revoking access token error ([#95](https://github.com/s2-streamstore/s2-sdk-go/issues/95))

## [v0.8.0] - 2025-05-30

### Features

- *(sdk)* Access token requests ([#88](https://github.com/s2-streamstore/s2-sdk-go/issues/88))

### Miscellaneous Tasks

- Update protos ([#86](https://github.com/s2-streamstore/s2-sdk-go/issues/86))

## [v0.7.0] - 2025-03-26

### Features

- Implement request timeout ([#56](https://github.com/s2-streamstore/s2-sdk-go/issues/56))

### Miscellaneous Tasks

- *(sdk)* Proto update ([#60](https://github.com/s2-streamstore/s2-sdk-go/issues/60))

## [v0.6.0] - 2025-02-25

### Features

- *(sdk)* Configurable compression parameter ([#52](https://github.com/s2-streamstore/s2-sdk-go/issues/52))
- *(bento)* Configurable retry backoff and input start seq num ([#54](https://github.com/s2-streamstore/s2-sdk-go/issues/54))

### Bug Fixes

- *(sdk)* Retry `CANCELLED` gRPC status code ([#53](https://github.com/s2-streamstore/s2-sdk-go/issues/53))

## [v0.5.2] - 2025-02-06

### Features

- Enable GZIP compression ([#49](https://github.com/s2-streamstore/s2-sdk-go/issues/49))

## [v0.5.1] - 2025-02-04

### Bug Fixes

- *(sdk)* Return empty leftovers when whole batch is valid ([#47](https://github.com/s2-streamstore/s2-sdk-go/issues/47))

## [v0.5.0] - 2025-02-03

### Miscellaneous Tasks

- Update minimum Go version to 1.22 ([#45](https://github.com/s2-streamstore/s2-sdk-go/issues/45))

## [v0.4.1] - 2025-01-30

### Bug Fixes

- *(bento)* Only ack continuous batches ([#43](https://github.com/s2-streamstore/s2-sdk-go/issues/43))

## [v0.4.0] - 2025-01-29

### Features

- *(bento)* Simplify input plugin ([#41](https://github.com/s2-streamstore/s2-sdk-go/issues/41))

## [v0.3.0] - 2025-01-28

### Features

- Add `s2-bentobox` package ([#36](https://github.com/s2-streamstore/s2-sdk-go/issues/36))
- *(bento)* Support configurable update list interval ([#38](https://github.com/s2-streamstore/s2-sdk-go/issues/38))

### Bug Fixes

- *(bento)* Handle acks without restarting the read session ([#39](https://github.com/s2-streamstore/s2-sdk-go/issues/39))

## [v0.2.1] - 2025-01-21

### Features

- Use `optr` for optional pointer handling ([#29](https://github.com/s2-streamstore/s2-sdk-go/issues/29))

## [v0.2.0] - 2025-01-16

### Features

- Implement `Sender.CloseSend` to close the request stream ([#24](https://github.com/s2-streamstore/s2-sdk-go/issues/24))

### Miscellaneous Tasks

- Update proto ([#26](https://github.com/s2-streamstore/s2-sdk-go/issues/26))

## [v0.1.2] - 2025-01-13

### Miscellaneous Tasks

- Update protos ([#22](https://github.com/s2-streamstore/s2-sdk-go/issues/22))

## [v0.1.1] - 2025-01-13

### Documentation

- Update README with detailed steps ([#20](https://github.com/s2-streamstore/s2-sdk-go/issues/20))

### Miscellaneous Tasks

- Add script for valid semver tag parsing ([#19](https://github.com/s2-streamstore/s2-sdk-go/issues/19))

## [v0.1.0] - 2025-01-10

### Features

- Add retry support
- Add more connection params ([#5](https://github.com/s2-streamstore/s2-sdk-go/issues/5))
- Support read session resumption ([#7](https://github.com/s2-streamstore/s2-sdk-go/issues/7))
- Append record batching ([#8](https://github.com/s2-streamstore/s2-sdk-go/issues/8))

### Documentation

- Add documentation comments for exported functions/types ([#11](https://github.com/s2-streamstore/s2-sdk-go/issues/11))
- Package level documentation ([#12](https://github.com/s2-streamstore/s2-sdk-go/issues/12))
- Add examples ([#13](https://github.com/s2-streamstore/s2-sdk-go/issues/13))
- README ([#16](https://github.com/s2-streamstore/s2-sdk-go/issues/16))

### Miscellaneous Tasks

- Update starwars example with flags ([#4](https://github.com/s2-streamstore/s2-sdk-go/issues/4))
- Add github actions for CI ([#6](https://github.com/s2-streamstore/s2-sdk-go/issues/6))
- *(refactor)* Rearrange files ([#15](https://github.com/s2-streamstore/s2-sdk-go/issues/15))
- *(release)* Create release v0.1.0 ([#17](https://github.com/s2-streamstore/s2-sdk-go/issues/17))
