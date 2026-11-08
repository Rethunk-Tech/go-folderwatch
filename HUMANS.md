# HUMANS.md

Run and use `go-folderwatch`. Internals: [AGENTS.md](AGENTS.md).

## Quick start

```bash
go get github.com/Rethunk-Tech/go-folderwatch
```

```go
package main

import (
 "context"
 "fmt"
 "log"
 "os"
 "os/signal"
 "time"

 "github.com/Rethunk-Tech/go-folderwatch"
)

func main() {
 ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
 defer stop()

 err := folderwatch.Run(ctx, folderwatch.Deps{
  Targets: func() []folderwatch.Target {
   return []folderwatch.Target{{Key: "source", Dir: "/path/to/src"}}
  },
  Emit:  func(t folderwatch.Target) { fmt.Println("changed:", t.Key, t.Dir) },
  Quiet: 500 * time.Millisecond,
 })
 if err != nil {
  log.Fatal(err)
 }
}
```

`Run` blocks until `ctx` is cancelled, then closes every watch and returns `nil`.

## Contracts

| Entry point | What it guarantees | What it never does |
| --- | --- | --- |
| `Run(ctx, Deps)` | Watches exactly the `Target`s `Targets` returns, from the first retarget at start until `ctx` ends. Returns `nil` on cancellation, and `nil` too when the underlying watch closes its event channel. | Never calls `Targets` or `Emit` after it returns, and never recovers a panic from either. |
| `Deps.Targets` | Read at start, on every `Retarget` tick, on every `Changed` receive, and after every hold. A target with an empty `Dir` is skipped. | Never called concurrently with itself. `Changed` is a signal, not a queue: a send from a select with several senders may be dropped. |
| `Deps.Emit` | Called once per settled folder, with the `Target` as it stood when the folder went quiet. Called from `Run`'s goroutine. | Never called for a folder that was removed from `Targets`, or that is held, before it settled. A folder whose key or payload changed mid-wait is announced under the newest values. |
| `Target.Key` | Opaque to the package; passed back on `Emit` so the caller can name the change. | Never interpreted, deduplicated or required to be unique. Two targets on one `Dir` are one watch, and the last one in `Targets` wins. |
| `Target.Report` | A caller-supplied folder to report under when it differs from `Dir`. | Never joined to `Dir`, cleaned, or checked for existence. |
| `Target.Data` | Passed back untouched. Shared with the caller's goroutines, not copied. | Never read, compared or serialised by the package. |
| `Target.Settle` | With `Settle`, a folder holding files written in place is announced only once their sizes match across one `Stable` window. | Never delays a folder that only received renames or creates-then-closes: those settle on the first quiet tick. |
| `Deps.Quiet` / `Stable` / `Retarget` | Zero or less means 500ms, 1s and 2s. | Never panics on a negative duration. |
| `Holds.Hold(ctx, root)` | Returns a release func once the watches at and below `root` are closed, or once `ctx` ends. Repeat holds on one root count, and the last release resumes watching. | Never blocks a `Run` that has already stopped, and never removes a watch that is not at or below `root`. |
| `Deps.Touched` | Called with the absolute path of every event the watch sees, before the event is debounced. | Never called after `Run` returns; the caller must do its own work off this goroutine. |

The package watches one level per folder: a change three levels down surfaces as a change to the folder holding it, so list every folder you care about as its own `Target` and use `Report` to say which root the caller should report under.

## Requirements

Go 1.27.3 or newer, plus `lefthook` and `gitleaks` on PATH if you want the git hooks.

```bash
lefthook install
```

That is per clone; git does not commit hooks. Pre-commit scans staged changes for secrets, and
pre-push runs `gate`.

## Verify

```bash
gate
```
