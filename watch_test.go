package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
