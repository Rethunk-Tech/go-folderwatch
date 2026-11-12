package folderwatch

import (
	"path/filepath"
	"testing"
)

func FuzzHeld(f *testing.F) {
	// A held folder covers itself and children; a sibling that only shares a name prefix must not match.
	f.Add("/a/sub", "/a/sub")
	f.Add("/a/sub", "/a/sub/file")
	f.Add("/a/sub", "/a/sub2")
	f.Add("/a/sub", "/a")
	f.Add("/a/sub", "/b/other")
	f.Add(".", "foo")
	f.Add("proj", "proj/nested/x")
	f.Add(`C:\a\sub`, `C:\a\sub\file`)
	f.Add(`C:\a\sub`, `C:\a\sub2`)

	f.Fuzz(func(t *testing.T, root, dir string) {
		h := NewHolds()
		if h.held(dir) {
			t.Fatalf("held(%q) = true with no holds", dir)
		}
		release := h.Hold(t.Context(), root)
		defer release()

		got := h.held(dir)
		rel, err := filepath.Rel(filepath.Clean(root), dir)
		want := err == nil && filepath.IsLocal(rel)
		if got != want {
			t.Fatalf("held(%q) under %q = %v, want %v (rel=%q err=%v)", dir, root, got, want, rel, err)
		}
	})
}

func FuzzHoldRefcount(f *testing.F) {
	f.Add("/a/sub", "/a/sub/file")
	f.Add("/a/sub", "/a/sub2")
	f.Add(".", "foo")
	f.Add("proj", "proj/nested/x")

	f.Fuzz(func(t *testing.T, root, dir string) {
		h := NewHolds()
		first := h.Hold(t.Context(), root)
		second := h.Hold(t.Context(), root)
		rel, err := filepath.Rel(filepath.Clean(root), dir)
		matches := err == nil && filepath.IsLocal(rel)

		first()
		if got := h.held(dir); got != matches {
			t.Fatalf("after one of two Hold releases, held(%q) under %q = %v, want %v", dir, root, got, matches)
		}
		second()
		if h.held(dir) {
			t.Fatalf("held(%q) = true after both holds of %q released", dir, root)
		}
	})
}
