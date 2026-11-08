# Changelog

Notable changes to `go-folderwatch`. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-08

### Added

- `Run` watches the `Target`s `Deps.Targets` returns and calls `Deps.Emit` once per folder that goes quiet, collapsing each burst into one call.
- `Target` carries `Key`, `Dir`, `Report` and `Data`, and `Emit` receives the `Target` itself, so the caller names the change and its payload.
- `Target.Settle` announces a file written in place only after its size holds for `Deps.Stable`, while a file renamed into the folder is announced on the first quiet tick.
- `Deps.Retarget` polls `Targets` and `Deps.Changed` re-reads it at once, so the watched set follows the caller without a restart, and a folder that does not exist yet fires once when it appears.
- `Deps.Touched` reports every path the watch sees as it happens, before the debounce.
- `Holds` with `NewHolds` and `Hold` closes the watches at and below a root until released, so a caller can move a folder a watch would otherwise pin open.

[Unreleased]: https://github.com/Rethunk-Tech/go-folderwatch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Rethunk-Tech/go-folderwatch/releases/tag/v0.1.0
