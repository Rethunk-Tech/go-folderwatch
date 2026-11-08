# AGENTS.md

`folderwatch` turns fsnotify events on a fixed set of folders into one debounced callback per burst, and hands the caller back the `Target` it was given so the caller owns the event's name and payload. It is imported by programs that keep a view of a folder tree in sync with the disk. It imports `github.com/fsnotify/fsnotify` and the standard library, and it never imports a program that calls it — the dependency runs one way.

## Gate

`gate` is the command. Each override in `.gate.toml` is there because a runner
cannot resolve that check from PATH. The `windows-latest` test job is the only place
Windows-only behaviour runs.

No test is build-tagged, so both jobs run the same test binary. Two paths only behave differently on Windows, and the Windows job is the only place they are exercised for real:

- `sizeOf`'s `runtime.GOOS == "windows"` branch, reached from `settle` in `TestSlowDirectWriterWaitsForStableSize` and `TestRenamedInFileIsAnnouncedAfterOneSettleWindow`. It reads the size through a handle of its own because Windows reports a stale size by name while a writer holds the file open.
- `TestHoldClosesTheWatchesBelowAFolderUntilReleased`, whose rename of a watched folder fails outright on Windows unless the hold closed the watches below it first — the same test passes on Linux for reasons that have nothing to do with holds.

## Shape

- **`Target` is a value the package carries, not one it reads.** `Key`, `Report` and `Data` are opaque: nothing inside the package inspects them. The moment the package knows what a `Key` means, every caller's vocabulary is baked into the library, and the next caller needs a fork.
- **`Emit` is the only way out.** One call per settled folder, always from `Run`'s goroutine, always with the `Target` as it stood at the quiet tick. No other function calls it, so a caller can serialise its own state without a lock of its own.
- **`fire` is the single settle path.** Every quiet timer runs `fire`; `fire` either emits or hands off to `settle` and returns. A second path to `Emit` would skip the `Settle` size check and the `gen` guard.
- **`settle` re-checks under the lock.** It compares the watch's `gen` and the files' sizes across the `Stable` window, and re-arms the timer when either moved. That re-check is what makes a paused writer safe; dropping it turns `Settle` into a fixed delay.
- **`retarget` keeps the watch when the folder stays.** If a folder is still a target under a new `Key`, its `*watch` is kept so a pending announcement survives the change. Only removal stops the timer and drops the watch.
- **Holds are matched by path prefix, not by target.** `held` uses `filepath.Rel` plus `filepath.IsLocal` so a sibling directory that shares a name prefix (`/a/Mods2` against a held `/a/Mods`) is not held. Never compare with `strings.HasPrefix`.
- **Zero durations are policy; `Settle` is not.** `Quiet`, `Stable` and `Retarget` have defaults because a caller that leaves them out still wants a working watcher. `Settle` has no default because whether a file is written in place is a fact about the caller's folder, and guessing it wrong either delays real announcements or announces half-written files.
- **No exported helper a caller does not need.** `watcher`, `watch`, `fire`, `settle`, `retarget`, `add`, `sizesOf`, `sizeOf` and `isDir` stay unexported. The public surface is `Run`, `Deps`, `Target`, `Holds`, `NewHolds` and `Hold`, and a new export needs a caller that cannot work without it.

## Files

| File | Holds |
| --- | --- |
| `doc.go` | Package comment only. |
| `watch.go` | `Target`, `Deps`, `Run`, and the `watcher`/`watch` internals: event intake, the quiet timer, `fire`, `settle` and the size check, `retarget`, `add`, `Touched`. |
| `hold.go` | `Holds`, `NewHolds`, `Hold`, and the `attach`/`held` pair that lets a running `Run` drop the watches below a held root. |
