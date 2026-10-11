package protocol

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func TestProbeFDA(t *testing.T) {
	mkdir := func(t *testing.T, home, rel string, mode os.FileMode) {
		t.Helper()
		dir := filepath.Join(home, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "entry"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	tests := []struct {
		name  string
		plant func(t *testing.T, home string)
		os    string
		want  FDA
	}{
		{"a readable Trash is granted", func(t *testing.T, home string) { mkdir(t, home, ".Trash", 0o755) }, "darwin", FDAGranted},
		{"an empty readable Trash is granted", func(t *testing.T, home string) {
			if err := os.MkdirAll(filepath.Join(home, ".Trash"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "darwin", FDAGranted},
		{"a refused Trash is denied", func(t *testing.T, home string) {
			mkdir(t, home, ".Trash", 0o000)
			mkdir(t, home, "Library/Safari", 0o755)
		}, "darwin", FDADenied},
		{"no Trash falls back to Safari", func(t *testing.T, home string) { mkdir(t, home, "Library/Safari", 0o755) }, "darwin", FDAGranted},
		{"neither folder is unknown", func(*testing.T, string) {}, "darwin", FDAUnknown},
		{"not macOS is unknown", func(t *testing.T, home string) { mkdir(t, home, ".Trash", 0o755) }, "linux", FDAUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Geteuid() == 0 && tt.want == FDADenied {
				t.Skip("root reads a mode-000 directory")
			}
			home := t.TempDir()
			tt.plant(t, home)
			if got := ProbeFDA(engine.Host{OS: tt.os, Home: home}); got != tt.want {
				t.Fatalf("ProbeFDA = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestProbeFDAIsBoundedWhenTheOpenBlocks(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	blocked := func(string) error {
		<-release
		return nil
	}
	began := time.Now()
	got := probeFDA(t.TempDir(), blocked, 50*time.Millisecond)
	if got != FDAUnknown {
		t.Fatalf("a probe that never answered reported %s", got)
	}
	if waited := time.Since(began); waited > 5*time.Second {
		t.Fatalf("the probe waited %s on a blocked open", waited)
	}
}

func TestProbeFDANeverTouchesAnAppContainer(t *testing.T) {
	var probed []string
	probeFDA("/home", func(path string) error {
		probed = append(probed, path)
		return os.ErrNotExist
	}, time.Second)
	want := []string{"/home/.Trash", "/home/Library/Safari"}
	if len(probed) != len(want) || probed[0] != want[0] || probed[1] != want[1] {
		t.Fatalf("probed %v, want exactly %v", probed, want)
	}
}
