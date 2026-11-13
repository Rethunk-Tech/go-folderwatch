package folderwatch

import (
	"context"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Target is one folder to watch. The watcher hands the Target back unchanged when the folder settles, so Key names
// the change and Data is whatever the caller needs to make sense of it.
type Target struct {
	// Key names this folder in the caller's vocabulary; the watcher never interprets it.
	Key string
	// Dir is the folder to watch. Only its top level is watched, so a change deeper in the tree surfaces as a
	// change to the folder holding it.
	Dir string
	// Report is the folder the change is reported under when it is not Dir, such as the root of a project whose
	// subfolder changed. The watcher passes it back untouched; empty, the caller reports under Dir.
	Report string
	// Data is the caller's payload, passed back untouched.
	Data any
	// Settle announces a file written in place only once its size holds for Deps.Stable, so a writer that pauses
	// mid-file is not mistaken for one that finished. A file renamed into the folder is announced on the first
	// quiet tick either way.
	Settle bool
}

// Deps wires the watcher to the caller's targets and callback.
type Deps struct {
	// Targets is read at start, on every Retarget tick and on every Changed signal; the folders it returns are
	// exactly the ones watched. A target with an empty Dir is skipped.
	Targets func() []Target
	// Emit is called once per settled folder, from the watcher's own goroutine; it must not block.
	Emit func(Target)
	// Quiet is how long a folder stays unchanged before it settles. Zero or less means 500ms.
	Quiet time.Duration
	// Stable is how long a file written in place must keep its size before a settling target is announced. Zero
	// or less means 1s.
	Stable time.Duration
	// Retarget is how often Targets is re-read, which also retries folders that did not exist. Zero or less means
	// 2s.
	Retarget time.Duration
	// Changed re-reads Targets at once, so a folder is watched from the moment it becomes a target and a file that
	// lands right after is not missed.
	Changed <-chan struct{}
	// Holds takes folders out of the watch while a caller moves them; nil holds none.
	Holds *Holds
	// Touched is called at once with the path of every change the watch sees, so a caller that caches what Targets
	// found can drop it; nil ignores them.
	Touched func(path string)
}

type watch struct {
	t     Target
	timer *time.Timer
	// writing holds files that received write events since the last announcement; gen counts every event.
	writing map[string]struct{}
	// created holds files that appeared since the last announcement; they join writing when the folder goes quiet.
	created map[string]struct{}
	gen     int
}

// watcher is one Run: the fsnotify watcher, the folders watched or awaited, and the lock guarding them.
type watcher struct {
	done    <-chan struct{}
	d       Deps
	w       *fsnotify.Watcher
	quiet   time.Duration
	stable  time.Duration
	mu      sync.Mutex
	active  map[string]*watch // by Dir
	missing map[string]bool   // folders seen absent, so their appearing fires once
	due     chan *watch
}

// Run watches until ctx ends. fsnotify watches each folder's top level only; a folder that is missing is retried at
// every retarget tick and fires once when it appears.
func Run(ctx context.Context, d Deps) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = fw.Close() }()
	holdRequests, detach := d.Holds.attach()
	defer detach()
	quiet, every, stable := d.Quiet, d.Retarget, d.Stable
	if quiet <= 0 {
		quiet = 500 * time.Millisecond
	}
	if every <= 0 {
		every = 2 * time.Second
	}
	if stable <= 0 {
		stable = time.Second
	}
	r := &watcher{done: ctx.Done(), d: d, w: fw, quiet: quiet, stable: stable, active: map[string]*watch{}, missing: map[string]bool{}, due: make(chan *watch)}
	tick := time.NewTicker(every)
	defer tick.Stop()
	r.retarget()
	for {
		select {
		case <-ctx.Done():
			r.stopAll()
			return nil
		case <-tick.C:
			r.retarget()
		case <-d.Changed:
			r.retarget()
		case done := <-holdRequests:
			r.retarget()
			close(done)
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			r.onEvent(ev)
		case a := <-r.due:
			r.fire(a)
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			log.Printf("folderwatch: %v", err)
		}
	}
}

func (r *watcher) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.active {
		a.timer.Stop()
	}
}

func (r *watcher) onEvent(ev fsnotify.Event) {
	if r.d.Touched != nil {
		r.d.Touched(ev.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.active[ev.Name]; a != nil && ev.Has(fsnotify.Remove|fsnotify.Rename) {
		// The watched folder itself went away: drop it so a later tick re-adds it when it returns.
		a.timer.Reset(r.quiet)
		_ = r.w.Remove(ev.Name)
		delete(r.active, ev.Name)
		r.missing[ev.Name] = true
		return
	}
	a := r.active[filepath.Dir(ev.Name)]
	if a == nil {
		return
	}
	a.gen++
	if ev.Has(fsnotify.Write) {
		a.writing[ev.Name] = struct{}{}
	}
	if ev.Has(fsnotify.Create) {
		a.created[ev.Name] = struct{}{}
	}
	a.timer.Reset(r.quiet)
}

// settle announces a quiet folder. Files written in place may only be paused, so their sizes must match across
// a second look; any event or size change in between keeps the folder pending.
func (r *watcher) settle(a *watch) {
	r.mu.Lock()
	names, gen := slices.Collect(maps.Keys(a.writing)), a.gen
	r.mu.Unlock()
	before := sizesOf(names)
	select {
	case <-time.After(r.stable):
	case <-r.done:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[a.t.Dir] != a {
		return
	}
	if a.gen != gen || !maps.Equal(before, sizesOf(names)) {
		a.timer.Reset(r.quiet)
		return
	}
	clear(a.writing)
	r.d.Emit(a.t)
}

func (r *watcher) retarget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]Target{}
	for _, t := range r.d.Targets() {
		if t.Dir != "" && !r.d.Holds.held(t.Dir) {
			want[t.Dir] = t
		}
	}
	for dir, a := range r.active {
		if t, ok := want[dir]; ok {
			// The same folder under another key keeps its watch, so a change that is still waiting to be
			// announced is not lost.
			a.t = t
			continue
		}
		a.timer.Stop()
		_ = r.w.Remove(dir)
		delete(r.active, dir)
		log.Printf("folderwatch: stopped watching %s", dir)
	}
	for dir, t := range want {
		if r.active[dir] == nil {
			r.add(dir, t)
		}
	}
}

// add starts watching dir, or notes it as awaited when it does not exist yet. The caller holds mu.
func (r *watcher) add(dir string, t Target) {
	if !isDir(dir) {
		if !r.missing[dir] {
			log.Printf("folderwatch: waiting for %s to appear", dir)
		}
		r.missing[dir] = true
		return
	}
	if err := r.w.Add(dir); err != nil {
		log.Printf("folderwatch: %s: %v", dir, err)
		return
	}
	a := &watch{t: t, writing: map[string]struct{}{}, created: map[string]struct{}{}}
	a.timer = time.AfterFunc(r.quiet, func() {
		select {
		case r.due <- a:
		case <-r.done:
		}
	})
	if !r.missing[dir] {
		a.timer.Stop()
	}
	delete(r.missing, dir)
	r.active[dir] = a
	log.Printf("folderwatch: watching %s (%s)", dir, t.Key)
}

// fire announces a folder that went quiet, or hands it to settle while files in it may still be growing.
func (r *watcher) fire(a *watch) {
	r.mu.Lock()
	// A file created in place may still be growing before its write events arrive (Windows delays them), so
	// the size check decides whether it is done.
	for n := range a.created {
		a.writing[n] = struct{}{}
	}
	clear(a.created)
	now := a.t
	gone := r.active[now.Dir] != a
	waits := !gone && now.Settle && len(a.writing) > 0
	if !waits {
		clear(a.writing)
	}
	r.mu.Unlock()
	if waits {
		r.settle(a)
		return
	}
	r.d.Emit(now)
}

// isDir reports whether dir is an existing folder.
func isDir(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

// sizesOf maps each file to its size, or -1 when it is gone.
func sizesOf(names []string) map[string]int64 {
	out := make(map[string]int64, len(names))
	for _, n := range names {
		out[n] = -1
		if size, ok := sizeOf(n); ok {
			out[n] = size
		}
	}
	return out
}

// sizeOf reads a file's size. On Windows the size by name lags behind a writer that holds the file open, so it is
// read through a handle of our own (Go opens it shared with writers).
func sizeOf(name string) (int64, bool) {
	if runtime.GOOS == "windows" {
		f, err := os.Open(name) //nolint:gosec // name is a path fsnotify reported inside a folder already being watched; reading its size is the point.
		if err != nil {
			return 0, false
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil {
			return 0, false
		}
		return st.Size(), true
	}
	st, err := os.Stat(name)
	if err != nil {
		return 0, false
	}
	return st.Size(), true
}
