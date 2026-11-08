<h1 align="center">go-folderwatch</h1>

<div align="center">

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27.3+-00ADD8?logo=go&logoColor=white)](go.mod)

</div>

---

Watch a folder tree for changes, debounced, with holds while the caller writes into it. Package `folderwatch`, module `github.com/Rethunk-Tech/go-folderwatch`.

`folderwatch` turns a stream of filesystem events into one callback per burst. You describe the folders you care about as `Target`s, it watches exactly those, and when one goes quiet it hands that `Target` back through `Emit` — so the caller, not the library, decides what the change means. Folders that do not exist yet are waited for, a caller can take a folder out of the watch while it moves it, and an optional size-stability window keeps a file that is still being written from being announced early.

## Quick start

```bash
go get github.com/Rethunk-Tech/go-folderwatch
```

A worked example and the contracts: [HUMANS.md](HUMANS.md).

## Features

- **One callback per burst.** Changes inside `Quiet` of each other collapse into a single `Emit` for the folder.
- **Opt-in size stability.** A `Target` marked `Settle` is announced only once its written files keep their size for `Stable`, so a slow writer is not mistaken for a finished one; a file renamed in is announced on the first quiet tick.
- **Targets are re-read, not captured.** `Targets` is polled every `Retarget` and immediately on a `Changed` signal, so the watched set follows the caller's state without a restart.
- **Folders that are not there yet.** A missing folder is retried on every tick and fires once when it appears.
- **Holds.** `Holds.Hold` closes the watches at and below a root and returns only when they are closed, so a caller can move or delete a folder that a watch would otherwise pin open.
- **Every change, not just the settled ones.** `Touched` sees each path as it happens, for callers that cache a directory listing and need to drop it promptly.

## Documentation

| Doc | Audience |
| --- | --- |
| [HUMANS.md](HUMANS.md) | Running and using the package |
| [AGENTS.md](AGENTS.md) | Internals, invariants, and why |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Process — commits, tests, docs |
| [SECURITY.md](SECURITY.md) | Vulnerability disclosure |
| [CHANGELOG.md](CHANGELOG.md) | Release notes |

## License

MIT — see [LICENSE](LICENSE). © 2026 Rethunk.Tech, LLC.
