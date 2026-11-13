package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A held folder has no watch open below it, so a caller can move it; released, it is watched again.
func TestHoldClosesTheWatchesBelowAFolderUntilReleased(t *testing.T) {
	t.Parallel()
	proj := filepath.Join(t.TempDir(), "Project ²")
	src := filepath.Join(proj, "src")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	holds := NewHolds()
	g := &got{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{
			Targets: func() []Target { return []Target{{Key: "source", Dir: src}} },
			Emit:    g.emit, Quiet: 50 * time.Millisecond, Stable: 100 * time.Millisecond, Retarget: 50 * time.Millisecond, Holds: holds,
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)

	release := holds.Hold(t.Context(), proj)
	moved := proj + " moved"
	if err := os.Rename(proj, moved); err != nil {
		t.Fatalf("moving the held folder: %v", err)
	}
	if err := os.Rename(moved, proj); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600)
	time.Sleep(400 * time.Millisecond)
	if early := g.count(); early != 0 {
		t.Fatalf("a held folder was still watched: %d events", early)
	}

	release()
	time.Sleep(200 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(src, "b.txt"), []byte("x"), 0o600)
	g.wait(t, 1)
}

// Release puts the folder back under watch at once, not on the next Retarget tick.
func TestReleasePutsFolderBackUnderWatchImmediately(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	holds := NewHolds()
	g := &got{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, Deps{
			Targets:  func() []Target { return []Target{{Key: "files", Dir: dir}} },
			Emit:     g.emit,
			Quiet:    50 * time.Millisecond,
			Retarget: time.Hour,
			Holds:    holds,
		})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)

	release := holds.Hold(t.Context(), dir)
	release()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600)
	g.wait(t, 1)
}

// Without a running watch, Hold returns at once.
func TestHoldWithoutAWatchReturns(t *testing.T) {
	t.Parallel()
	NewHolds().Hold(t.Context(), t.TempDir())()
}
