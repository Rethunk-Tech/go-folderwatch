package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type got struct {
	mu  sync.Mutex
	evs []Target
}

func (g *got) emit(t Target) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evs = append(g.evs, t)
}

func (g *got) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.evs)
}

// keysLocked reports the keys announced so far; the caller holds mu.
func (g *got) keysLocked() []string {
	out := make([]string, len(g.evs))
	for i, t := range g.evs {
		out[i] = t.Key
	}
	return out
}

func (g *got) keys() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.keysLocked()
}

func (g *got) wait(t *testing.T, n int) []string {
	t.Helper()
	for range 100 {
		g.mu.Lock()
		out := g.keysLocked()
		g.mu.Unlock()
		if len(out) >= n {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("want %d events, got %v", n, g.keys())
	return nil
}

func start(t *testing.T, targets func() []Target) *got {
	t.Helper()
	g := &got{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{Targets: targets, Emit: g.emit, Quiet: 50 * time.Millisecond, Stable: 300 * time.Millisecond, Retarget: 50 * time.Millisecond})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)
	return g
}

func TestUncleanDirStillReceivesEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := start(t, func() []Target { return []Target{{Key: "k", Dir: dir + string(filepath.Separator) + "."}} })
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.wait(t, 1)
}

func TestRemovedSettlingFolderIsStillAnnounced(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "watched")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	g := start(t, func() []Target { return []Target{{Key: "files", Dir: dir, Settle: true}} })
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".gone"); err != nil {
		t.Fatal(err)
	}
	g.wait(t, 1)
}

func TestNonSettleEmitDropsWritingSoALaterSettleIsNotBlocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.zip")
	var mu sync.Mutex
	settle := false
	g := &got{}
	changed := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{
			Targets: func() []Target {
				mu.Lock()
				defer mu.Unlock()
				return []Target{{Key: "files", Dir: dir, Settle: settle}}
			},
			Emit: g.emit, Quiet: 50 * time.Millisecond, Stable: 200 * time.Millisecond, Retarget: time.Hour, Changed: changed,
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(oldPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.wait(t, 1)

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Link(oldPath, alias); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	settle = true
	mu.Unlock()
	changed <- struct{}{}

	if err := os.WriteFile(filepath.Join(dir, "new.zip"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopGrow := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		buf := []byte("z")
		for {
			select {
			case <-stopGrow:
				return
			default:
				buf = append(buf, 'z')
				_ = os.WriteFile(alias, buf, 0o600)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	t.Cleanup(func() { close(stopGrow) })
	g.wait(t, 2)
}

func TestEmitDoesNotRunConcurrently(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	inCall := 0
	overlapped := false
	finished := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{
			Targets: func() []Target {
				return []Target{{Key: "a", Dir: a}, {Key: "b", Dir: b}}
			},
			Quiet: 50 * time.Millisecond,
			Emit: func(Target) {
				mu.Lock()
				if inCall > 0 {
					overlapped = true
				}
				inCall++
				mu.Unlock()
				time.Sleep(100 * time.Millisecond)
				mu.Lock()
				inCall--
				mu.Unlock()
				finished <- struct{}{}
			},
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(a, "x"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(b, "x"), []byte("x"), 0o600)
	for range 2 {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Emit")
		}
	}
	if overlapped {
		t.Fatal("Emit ran concurrently")
	}
}

func TestBurstFiresOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := start(t, func() []Target { return []Target{{Key: "files", Dir: dir}} })
	for i := range 5 {
		_ = os.WriteFile(filepath.Join(dir, string(rune('a'+i))+".zip"), nil, 0o600)
	}
	time.Sleep(400 * time.Millisecond)
	if evs := g.wait(t, 1); len(evs) != 1 || evs[0] != "files" {
		t.Fatalf("events = %v", evs)
	}
}

// Emit receives the Target itself, so the caller reads Report and Data from it rather than from a fixed payload.
func TestEmitCarriesTheTargetBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := struct{ Name string }{"alpha"}
	want := Target{Key: "source", Dir: dir, Report: filepath.Dir(dir), Data: data}
	g := start(t, func() []Target { return []Target{want} })
	_ = os.WriteFile(filepath.Join(dir, "MyMod.dll"), []byte("x"), 0o600)
	g.wait(t, 1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.evs[0] != want {
		t.Fatalf("event = %+v, want %+v", g.evs[0], want)
	}
}

func TestMissingFolderFiresWhenItAppears(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "watched")
	g := start(t, func() []Target { return []Target{{Key: "source", Dir: dir}} })
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	g.wait(t, 1)
	_ = os.WriteFile(filepath.Join(dir, "x"), nil, 0o600)
	g.wait(t, 2)
}

func TestRetargetStopsOldFolder(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	cur := a
	g := start(t, func() []Target {
		mu.Lock()
		defer mu.Unlock()
		return []Target{{Key: "source", Dir: cur}}
	})
	mu.Lock()
	cur = b
	mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(a, "x"), nil, 0o600)
	time.Sleep(300 * time.Millisecond)
	if n := g.count(); n != 0 {
		t.Fatalf("old folder still watched: %d events", n)
	}
	_ = os.WriteFile(filepath.Join(b, "x"), nil, 0o600)
	g.wait(t, 1)
}

func TestSlowDirectWriterWaitsForStableSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := start(t, func() []Target { return []Target{{Key: "files", Dir: dir, Settle: true}} })
	f, err := os.Create(filepath.Clean(filepath.Join(dir, "big.zip")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	// Keep growing the file for twice the stable window, writing right before the check: a slow runner delays the
	// sleeps, but a file that changed since the last look is never announced.
	for range 30 {
		_, _ = f.WriteString("a")
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = f.WriteString("b")
	if early := g.count(); early != 0 {
		t.Fatalf("announced while the writer was still going: %d events", early)
	}
	if evs := g.wait(t, 1); len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}
}

// A target without Settle announces as soon as the folder is quiet, so a caller watching a folder it does not write
// into is not delayed by the stable window.
func TestATargetWithoutSettleAnnouncesOnTheQuietTick(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := start(t, func() []Target { return []Target{{Key: "source", Dir: dir}} })
	f, err := os.Create(filepath.Clean(filepath.Join(dir, "big.zip")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString("a")
	began := time.Now()
	g.wait(t, 1)
	if waited := time.Since(began); waited > 250*time.Millisecond {
		t.Fatalf("waited for the stable window: %v", waited)
	}
}

func TestRenamedInFileIsAnnouncedAfterOneSettleWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := start(t, func() []Target { return []Target{{Key: "files", Dir: dir, Settle: true}} })
	part := filepath.Join(t.TempDir(), "x.part")
	if err := os.WriteFile(part, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(part, filepath.Join(dir, "x.zip")); err != nil {
		t.Fatal(err)
	}
	if evs := g.wait(t, 1); len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}
}

// The key a folder reports under can change on a retarget tick; a change that landed just before it must still be
// announced, under the new key, whether the folder was still quiet-waiting or already waiting for a stable size.
func TestRetargetKeepsPendingEvent(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ quiet, flipAfter time.Duration }{
		"quiet wait":  {200 * time.Millisecond, 0},
		"stable wait": {50 * time.Millisecond, 100 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var mu sync.Mutex
			key := "beta"
			g := &got{}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				_ = Run(ctx, Deps{Targets: func() []Target {
					mu.Lock()
					defer mu.Unlock()
					return []Target{{Key: key, Dir: dir, Settle: true}}
				}, Emit: g.emit, Quiet: c.quiet, Stable: 250 * time.Millisecond, Retarget: 50 * time.Millisecond})
				close(done)
			}()
			t.Cleanup(func() { cancel(); <-done })
			time.Sleep(200 * time.Millisecond)
			if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(c.flipAfter)
			mu.Lock()
			key = "alpha"
			mu.Unlock()
			g.wait(t, 1)
			time.Sleep(350 * time.Millisecond)
			if evs := g.wait(t, 1); len(evs) != 1 || evs[0] != "alpha" {
				t.Fatalf("events = %v", evs)
			}
		})
	}
}

// A changed signal re-reads the targets at once: the new folder is watched long before the next tick.
func TestChangedSignalRetargetsAtOnce(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	cur := a
	g := &got{}
	changed := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{Targets: func() []Target {
			mu.Lock()
			defer mu.Unlock()
			return []Target{{Key: "source", Dir: cur}}
		}, Emit: g.emit, Quiet: 50 * time.Millisecond, Retarget: time.Hour, Changed: changed})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	cur = b
	mu.Unlock()
	changed <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(b, "x"), nil, 0o600)
	g.wait(t, 1)
}

func TestOverflowReArmsEveryWatchedFolder(t *testing.T) {
	t.Parallel()
	a := &watch{timer: time.NewTimer(time.Hour)}
	r := &watcher{quiet: time.Hour, active: map[string]*watch{"d": a}}
	r.overflowed()
	if a.gen != 1 {
		t.Fatalf("gen %d", a.gen)
	}
}

func TestAddedFolderDoesNotAnnounceBeforeAnyChange(t *testing.T) {
	var targets []Target
	for i := range 300 {
		targets = append(targets, Target{Key: "k", Dir: t.TempDir(), Data: i})
	}
	g := &got{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{Targets: func() []Target { return targets }, Emit: g.emit, Quiet: time.Nanosecond, Retarget: time.Hour})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(300 * time.Millisecond)
	if n := g.count(); n != 0 {
		t.Fatalf("%d spurious announcements for untouched folders", n)
	}
}

func TestStoppedWatchLeavesNoTimerGoroutineWhenFsnotifyCloses(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "later")
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = run(ctx, Deps{Targets: func() []Target { return []Target{{Key: "k", Dir: missing}} }, Emit: func(Target) {}, Quiet: 20 * time.Millisecond, Retarget: 10 * time.Millisecond}, fw)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := os.Mkdir(missing, 0o750); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	_ = fw.Close()
	<-done
	time.Sleep(200 * time.Millisecond)
	buf := make([]byte, 1<<20)
	if stacks := string(buf[:runtime.Stack(buf, true)]); strings.Contains(stacks, "folderwatch.(*watcher).add.func") {
		t.Fatalf("timer goroutine still blocked:\n%s", stacks)
	}
}

func TestRetargetForgetsFoldersNoLongerTargeted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	var mu sync.Mutex
	var targets []Target
	set := func(ts ...Target) { mu.Lock(); targets = ts; mu.Unlock() }
	set(Target{Key: "k", Dir: dir})
	g := start(t, func() []Target { mu.Lock(); defer mu.Unlock(); return targets })
	set()
	time.Sleep(200 * time.Millisecond)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	set(Target{Key: "k", Dir: dir})
	time.Sleep(400 * time.Millisecond)
	if n := g.count(); n != 0 {
		t.Fatalf("%d spurious announcements for a folder that was dropped while missing", n)
	}
}

// Renaming a parent moves the watched folder without an event of its own; a new folder at the path must be watched.
func TestFolderReplacedThroughItsParentIsWatchedAgain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := filepath.Join(root, "Mods")
	dir := filepath.Join(parent, "Pack")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	g := start(t, func() []Target { return []Target{{Key: "pack", Dir: dir}} })
	if err := os.Rename(parent, filepath.Join(root, "Mods.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	n := g.count()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	g.wait(t, n+1)
}
