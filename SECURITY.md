# Security

## Supported versions

The latest tagged release. There are no long-lived release branches, so a fix ships in the next tag
rather than being backported.

## Reporting a vulnerability

Use GitHub's **private vulnerability reporting** on this repository (Security → Report a
vulnerability). It stays private until a fix ships.

Please do not open a public issue for a suspected vulnerability first.

## What is in scope

The package reads no network input and parses no file content. What it does take from outside its own control is the paths in `Target.Dir`, the paths the operating system reports in `fsnotify.Event.Name`, and the root passed to `Holds.Hold`:

- `Holds.Hold` cleans its root before matching and compares with `filepath.Rel` + `filepath.IsLocal`. A root such as `..` or a symlinked ancestor must not make `held` match folders outside that root, or a caller would silently stop watching a folder it never held.
- `Holds.Hold` must return even when `Run` has stopped, and must not leave a goroutine blocked on the hold channel after `Run` returns. A crafted sequence of hold/release around cancellation must not deadlock the caller.
- `Deps.Targets` and `Deps.Emit` are called from `Run`'s goroutine with no lock held. A slow or panicking callback must not corrupt the watcher's state or leak the underlying fsnotify watcher; `Run`'s deferred close must still run.
- `sizesOf` allocates one entry per file named since the last announcement. Repeated writes to the same paths must not grow that map without bound, and a file removed between looks must read as gone rather than panic.
- A folder that disappears mid-watch must be dropped and re-added when it returns, without a stale entry in `active` or an unbounded `missing` map.

## What is out of scope

- **The caller owns `Dir`, `Report` and `Data`.** The package does not clean, resolve, validate or copy them, and it never opens a path it was not given as a folder to watch. A caller that passes a path from an untrusted source has already made that decision.
- **`Data` is shared, not copied.** It is handed back on the same goroutine that received it, and the caller is trusted not to mutate it concurrently with itself.
- **Events named in `Touched` are paths, not content.** The package reports that a path changed, never what it now holds; treating that path as safe to act on is the caller's call.
- **Running out of inotify watches, or a watch that the OS refuses to add, is logged and skipped**, not an error. A caller that must notice needs to check the folder itself.
- **A burst that never goes quiet is never announced.** That is the `Quiet` contract, not a denial of service: a caller that wants a bound sets `Settle` and a `Stable` it can live with.
- **Files inside a folder are watched one level deep only.** A change below a watched folder surfaces as a change to the folder above it; it is not a path escape.
