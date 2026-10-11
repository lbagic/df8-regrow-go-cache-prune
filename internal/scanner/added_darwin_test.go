//go:build darwin

package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Date added is when the file arrived in its folder, which an old
// modification time does not move.
func TestAddedTime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app")
	writeFile(t, p, 10)
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	at, err := addedTime(p)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(at) > time.Hour || at.Before(old.Add(time.Hour)) {
		t.Errorf("date added %v, want about now, not the modification time %v", at, old)
	}
	if _, err := addedTime(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file has a date added")
	}
	times := fileTimes(mustLstat(t, p))
	if len(times) != 3 || !times[0].Equal(old) || time.Since(times[1]) > time.Hour {
		t.Errorf("file times %v, want the old modification, a fresh change and the creation", times)
	}
}

func mustLstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
