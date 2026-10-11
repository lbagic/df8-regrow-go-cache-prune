package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testWalker is a walker with its own blocked set, so no test sees
// another's written-off paths.
func testWalker() *walker {
	return &walker{open: openDir, stall: 2 * time.Second, workers: walkWorkers, blocked: &blockedSet{}}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10_000)
	writeFile(t, filepath.Join(dir, "sub", "b.bin"), 20_000)

	got, err := DirSize(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Physical usage is block-rounded, so assert a sane envelope
	// rather than an exact byte count.
	if got.Bytes < 30_000 || got.Bytes > 1_000_000 {
		t.Errorf("DirSize = %d, want between 30000 and 1000000", got.Bytes)
	}
	if got.Partial {
		t.Error("a fully readable tree must not be partial")
	}
}

func TestDirSizeSingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "one.bin")
	writeFile(t, f, 5_000)
	got, err := DirSize(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes < 5_000 {
		t.Errorf("DirSize(file) = %d, want >= 5000", got.Bytes)
	}
}

func TestDirSizeDoesNotFollowSymlinks(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "big.bin"), 100_000)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "small.bin"), 1_000)
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := DirSize(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes >= 100_000 {
		t.Errorf("DirSize = %d, followed a symlink", got.Bytes)
	}
}

func TestDirSizeMissingPathIsZero(t *testing.T) {
	got, err := DirSize(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != (Usage{}) {
		t.Fatalf("missing path = (%+v, %v), want the zero Usage and no error", got, err)
	}
}

func TestDirSizeCancelled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10_000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := DirSize(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled walk must report ctx error, got %v", err)
	}
	if !got.Partial {
		t.Error("a cancelled walk's usage is a lower bound and must say so")
	}
}

// Package managers stamp old fixed times on files (npm writes 1985),
// so recency must come from directories too.
func TestDirSizeNewestCountsDirectories(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "node_modules", "left-pad")
	file := filepath.Join(pkg, "index.js")
	writeFile(t, file, 100)

	stamped := time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC)
	installed := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for path, mt := range map[string]time.Time{
		file: stamped, pkg: installed,
		filepath.Join(root, "node_modules"): older, root: older,
	} {
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	got, err := DirSize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Newest.Equal(installed) {
		t.Fatalf("Newest = %v, want the package directory's install time %v", got.Newest, installed)
	}
}

// A folder the scan may not read leaves the rest of the tree measured
// and marks the item as a lower bound; a root it may not read is an
// item with nothing measured.
func TestDirSizeUnreadableFolders(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 folders")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "visible.bin"), 40_000)
	locked := filepath.Join(root, "deep", "locked")
	writeFile(t, filepath.Join(locked, "hidden.bin"), 400_000)
	chmod(t, locked, 0)

	got, err := DirSize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Partial || got.Bytes < 40_000 || got.Bytes >= 400_000 {
		t.Fatalf("tree with a mode-000 folder = %+v, want Partial with the visible bytes only", got)
	}

	chmod(t, root, 0)
	got, err = DirSize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Partial || got.Bytes != 0 {
		t.Fatalf("mode-000 root = %+v, want Partial with 0 bytes", got)
	}
}

// chmod sets mode now and restores a walkable mode at cleanup, so
// t.TempDir can remove the tree.
func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}
