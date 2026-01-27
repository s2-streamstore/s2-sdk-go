# Changelog

All notable changes to this project will be documented in this file.

## [v0.11.8] - 2026-01-27

### Bug Fixes

- Access token tests ([#161](https://github.com/s2-streamstore/s2-sdk-go/issues/161))
- Negative shift panic in retry logic ([#163](https://github.com/s2-streamstore/s2-sdk-go/issues/163))

### Testing

- Remove redundant basin name validation test cases ([#160](https://github.com/s2-streamstore/s2-sdk-go/issues/160))

### Miscellaneous Tasks

- Update s2-specs to 6f66731 ([#162](https://github.com/s2-streamstore/s2-sdk-go/issues/162))

## [v0.11.7] - 2026-01-21

### Features

- Add SDK documentation examples + alter default timeouts, and pagination behavior ([#155](https://github.com/s2-streamstore/s2-sdk-go/issues/155))

### Bug Fixes

- Drain pending records when error channel closes in ReadSession.Next() ([#157](https://github.com/s2-streamstore/s2-sdk-go/issues/157))
- Data race on inflightEntry.attemptStart in AppendSession ([#158](https://github.com/s2-streamstore/s2-sdk-go/issues/158))

### Miscellaneous Tasks

- Metrics tests ([#154](https://github.com/s2-streamstore/s2-sdk-go/issues/154))
- Access token tests ([#156](https://github.com/s2-streamstore/s2-sdk-go/issues/156))
- *(release)* V0.11.7 ([#159](https://github.com/s2-streamstore/s2-sdk-go/issues/159))

## [v0.11.6] - 2026-01-16

### Features

- Re-enable compression for unary requests ([#138](https://github.com/s2-streamstore/s2-sdk-go/issues/138))

### Bug Fixes

- *(bentobox)* MultiStreamInput shutdown ([#135](https://github.com/s2-streamstore/s2-sdk-go/issues/135))

### Testing

- S2-lite integration ([#149](https://github.com/s2-streamstore/s2-sdk-go/issues/149))
- Bento integration ([#152](https://github.com/s2-streamstore/s2-sdk-go/issues/152))

### Miscellaneous Tasks

- Rm accidentally committed bin ([#136](https://github.com/s2-streamstore/s2-sdk-go/issues/136))
- Update specs ([#150](https://github.com/s2-streamstore/s2-sdk-go/issues/150))
- Rename workflow and enable signed commits ([#151](https://github.com/s2-streamstore/s2-sdk-go/issues/151))
- *(release)* V0.11.6 ([#153](https://github.com/s2-streamstore/s2-sdk-go/issues/153))

## [v0.11.5] - 2026-01-12

### Bug Fixes

- Default scheme for localhost to be http ([#132](https://github.com/s2-streamstore/s2-sdk-go/issues/132))
- Producer doesn't pipeline ([#133](https://github.com/s2-streamstore/s2-sdk-go/issues/133))

### Miscellaneous Tasks

- Update integration tests ([#131](https://github.com/s2-streamstore/s2-sdk-go/issues/131))
- Improve starwars example ([#130](https://github.com/s2-streamstore/s2-sdk-go/issues/130))
- *(release)* V0.11.5 ([#134](https://github.com/s2-streamstore/s2-sdk-go/issues/134))

## [v0.11.4] - 2026-01-09

### Features

- Llm generated tests + encoding bugfix [P1] ([#126](https://github.com/s2-streamstore/s2-sdk-go/issues/126))
- Llm generated tests [P2] ([#127](https://github.com/s2-streamstore/s2-sdk-go/issues/127))

### Miscellaneous Tasks

- *(release)* V0.11.4 ([#129](https://github.com/s2-streamstore/s2-sdk-go/issues/129))

## [v0.11.3] - 2026-01-07

### Bug Fixes

- Incorrect user-agent ([#124](https://github.com/s2-streamstore/s2-sdk-go/issues/124))

### Miscellaneous Tasks

- *(release)* V0.11.3 ([#125](https://github.com/s2-streamstore/s2-sdk-go/issues/125))

## [v0.11.2] - 2026-01-05

### Bug Fixes

- Read session wait elapsed time track for retries ([#119](https://github.com/s2-streamstore/s2-sdk-go/issues/119))
- *(bento)* Cache invalidation ([#120](https://github.com/s2-streamstore/s2-sdk-go/issues/120))
- I/o timeout not getting retried ([#121](https://github.com/s2-streamstore/s2-sdk-go/issues/121))

### Miscellaneous Tasks

- Update protos + docs ([#122](https://github.com/s2-streamstore/s2-sdk-go/issues/122))
- *(release)* V0.11.2 ([#123](https://github.com/s2-streamstore/s2-sdk-go/issues/123))

## [v0.11.1] - 2026-01-04

### Bug Fixes

- Race in StreamClient.getHTTPClient initialization ([#113](https://github.com/s2-streamstore/s2-sdk-go/issues/113))
- Producer close should cancel ctx ([#114](https://github.com/s2-streamstore/s2-sdk-go/issues/114))
- Append session error body ([#115](https://github.com/s2-streamstore/s2-sdk-go/issues/115))
- Batcher close logic ([#116](https://github.com/s2-streamstore/s2-sdk-go/issues/116))
- Connection management + user-agent ([#117](https://github.com/s2-streamstore/s2-sdk-go/issues/117))

### Miscellaneous Tasks

- *(release)* V0.11.1 ([#118](https://github.com/s2-streamstore/s2-sdk-go/issues/118))

## [v0.11.0] - 2025-12-31

### Miscellaneous Tasks

- *(release)* V0.11.0 ([#112](https://github.com/s2-streamstore/s2-sdk-go/issues/112))

## [v0.10.0] - 2025-07-23

### Features

- Clamp ([#99](https://github.com/s2-streamstore/s2-sdk-go/issues/99))

### Miscellaneous Tasks

- *(release)* V0.10.0 ([#100](https://github.com/s2-streamstore/s2-sdk-go/issues/100))

## [v0.9.0] - 2025-06-16

### Bug Fixes

- Revoking access token error ([#95](https://github.com/s2-streamstore/s2-sdk-go/issues/95))

### Miscellaneous Tasks

- *(release)* V0.9.0 ([#96](https://github.com/s2-streamstore/s2-sdk-go/issues/96))

## [v0.8.0] - 2025-05-30

### Features

- *(sdk)* Access token requests ([#88](https://github.com/s2-streamstore/s2-sdk-go/issues/88))

### Miscellaneous Tasks

- Update protos ([#86](https://github.com/s2-streamstore/s2-sdk-go/issues/86))
- *(release)* V0.8.0 ([#92](https://github.com/s2-streamstore/s2-sdk-go/issues/92))

## [v0.7.0] - 2025-03-26

### Features

- Implement request timeout ([#56](https://github.com/s2-streamstore/s2-sdk-go/issues/56))

### Miscellaneous Tasks

- *(sdk)* Proto update ([#60](https://github.com/s2-streamstore/s2-sdk-go/issues/60))
- *(release)* V0.7.0 ([#64](https://github.com/s2-streamstore/s2-sdk-go/issues/64))

## [v0.6.0] - 2025-02-25

### Features

- *(sdk)* Configurable compression parameter ([#52](https://github.com/s2-streamstore/s2-sdk-go/issues/52))
- *(bento)* Configurable retry backoff and input start seq num ([#54](https://github.com/s2-streamstore/s2-sdk-go/issues/54))

### Bug Fixes

- *(sdk)* Retry `CANCELLED` gRPC status code ([#53](https://github.com/s2-streamstore/s2-sdk-go/issues/53))

### Miscellaneous Tasks

- *(release)* V0.6.0 ([#55](https://github.com/s2-streamstore/s2-sdk-go/issues/55))

## [v0.5.2] - 2025-02-06

### Features

- Enable GZIP compression ([#49](https://github.com/s2-streamstore/s2-sdk-go/issues/49))

### Miscellaneous Tasks

- *(release)* V0.5.2 ([#51](https://github.com/s2-streamstore/s2-sdk-go/issues/51))

## [v0.5.1] - 2025-02-04

### Bug Fixes

- *(sdk)* Return empty leftovers when whole batch is valid ([#47](https://github.com/s2-streamstore/s2-sdk-go/issues/47))

### Miscellaneous Tasks

- *(release)* V0.5.1 ([#48](https://github.com/s2-streamstore/s2-sdk-go/issues/48))

## [v0.5.0] - 2025-02-03

### Miscellaneous Tasks

- Update minimum Go version to 1.22 ([#45](https://github.com/s2-streamstore/s2-sdk-go/issues/45))
- *(release)* V0.5.0 ([#46](https://github.com/s2-streamstore/s2-sdk-go/issues/46))

## [v0.4.1] - 2025-01-30

### Bug Fixes

- *(bento)* Only ack continuous batches ([#43](https://github.com/s2-streamstore/s2-sdk-go/issues/43))

### Miscellaneous Tasks

- *(release)* V0.4.1 ([#44](https://github.com/s2-streamstore/s2-sdk-go/issues/44))

## [v0.4.0] - 2025-01-29

### Features

- *(bento)* Simplify input plugin ([#41](https://github.com/s2-streamstore/s2-sdk-go/issues/41))

### Miscellaneous Tasks

- *(release)* V0.4.0 ([#42](https://github.com/s2-streamstore/s2-sdk-go/issues/42))

## [v0.3.0] - 2025-01-28

### Features

- Add `s2-bentobox` package ([#36](https://github.com/s2-streamstore/s2-sdk-go/issues/36))
- *(bento)* Support configurable update list interval ([#38](https://github.com/s2-streamstore/s2-sdk-go/issues/38))

### Bug Fixes

- *(bento)* Handle acks without restarting the read session ([#39](https://github.com/s2-streamstore/s2-sdk-go/issues/39))

### Miscellaneous Tasks

- *(release)* V0.3.0 ([#40](https://github.com/s2-streamstore/s2-sdk-go/issues/40))

## [v0.2.1] - 2025-01-21

### Features

- Use `optr` for optional pointer handling ([#29](https://github.com/s2-streamstore/s2-sdk-go/issues/29))

### Miscellaneous Tasks

- *(release)* V0.2.1 ([#35](https://github.com/s2-streamstore/s2-sdk-go/issues/35))

## [v0.2.0] - 2025-01-16

### Features

- Implement `Sender.CloseSend` to close the request stream ([#24](https://github.com/s2-streamstore/s2-sdk-go/issues/24))

### Miscellaneous Tasks

- Update proto ([#26](https://github.com/s2-streamstore/s2-sdk-go/issues/26))
- *(release)* V0.2.0 ([#27](https://github.com/s2-streamstore/s2-sdk-go/issues/27))

## [v0.1.2] - 2025-01-13

### Miscellaneous Tasks

- Update protos ([#22](https://github.com/s2-streamstore/s2-sdk-go/issues/22))
- *(release)* V0.1.2 ([#23](https://github.com/s2-streamstore/s2-sdk-go/issues/23))

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

### Bug Fixes

- CHANGELOG header ([#18](https://github.com/s2-streamstore/s2-sdk-go/issues/18))

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

<!-- generated by git-cliff -->
