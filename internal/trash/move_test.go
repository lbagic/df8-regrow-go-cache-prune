package trash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func fakeFinder(to string, err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return to, err }
}

func writeTree(t *testing.T, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMoveFinderSuccess(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	target := filepath.Join(dir, "cache")
	m := &Mover{Home: "/Users/t", RunFinder: fakeFinder("/Users/t/.Trash/cache", nil)}

	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != MethodFinder || r.To != "/Users/t/.Trash/cache" || r.Original != target {
		t.Fatalf("receipt wrong: %+v", r)
	}
}

func TestMoveFallsBackToStaging(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	target := filepath.Join(dir, "cache")
	staging := filepath.Join(t.TempDir(), "staging")
	m := &Mover{Home: "/Users/t", StagingDir: staging, RunFinder: fakeFinder("", errors.New("no Finder"))}

	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != MethodStaging {
		t.Fatalf("want staging fallback, got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(r.To, "a.bin")); err != nil {
		t.Fatalf("tree not moved to staging: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("original must be gone after staging move")
	}
}

func TestMoveStagingNamesNeverCollide(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "staging")
	m := &Mover{Home: "/Users/t", StagingDir: staging, RunFinder: fakeFinder("", errors.New("no Finder"))}

	var tos []string
	for range 3 {
		dir := writeTree(t, "cache/a.bin")
		r, err := m.Move(context.Background(), filepath.Join(dir, "cache"))
		if err != nil {
			t.Fatal(err)
		}
		tos = append(tos, r.To)
	}
	if tos[0] == tos[1] || tos[1] == tos[2] || tos[0] == tos[2] {
		t.Fatalf("staging destinations collided: %v", tos)
	}
}

func TestMoveGuardsPath(t *testing.T) {
	m := &Mover{Home: "/Users/t", RunFinder: fakeFinder("/x", nil)}
	if _, err := m.Move(context.Background(), "/Users/t"); err == nil || !strings.Contains(err.Error(), "guard") {
		t.Fatalf("moving $HOME must hit the guard, got %v", err)
	}
}

func TestMoveMissingPathFails(t *testing.T) {
	m := &Mover{Home: "/Users/t", RunFinder: fakeFinder("/x", nil)}
	target := filepath.Join(t.TempDir(), "vanished", "cache")
	if _, err := m.Move(context.Background(), target); err == nil {
		t.Fatal("path that vanished between scan and execute must error, not fake a receipt")
	}
}

func TestMoveCancelledContextSkipsFallback(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	staging := filepath.Join(t.TempDir(), "staging")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &Mover{Home: "/Users/t", StagingDir: staging,
		RunFinder: func(ctx context.Context, _ string) (string, error) { return "", ctx.Err() }}

	if _, err := m.Move(ctx, filepath.Join(dir, "cache")); err == nil {
		t.Fatal("cancelled move must fail")
	}
	if _, err := os.Lstat(filepath.Join(dir, "cache")); err != nil {
		t.Fatal("cancelled move must leave the target in place")
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	target := filepath.Join(dir, "cache")
	staging := filepath.Join(t.TempDir(), "staging")
	m := &Mover{Home: "/Users/t", StagingDir: staging, RunFinder: fakeFinder("", errors.New("no Finder"))}

	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "a.bin")); err != nil {
		t.Fatalf("restore did not bring the tree back: %v", err)
	}
}

func TestRestoreRefusesToOverwrite(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	target := filepath.Join(dir, "cache")
	staging := filepath.Join(t.TempDir(), "staging")
	m := &Mover{Home: "/Users/t", StagingDir: staging, RunFinder: fakeFinder("", errors.New("no Finder"))}
	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	// The cache regenerated in the meantime.
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Restore(r); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("restore over a regenerated path must refuse, got %v", err)
	}
}

func TestRestoreRefusesExportReceipts(t *testing.T) {
	r := Receipt{Original: "docker volume dune_db", To: "/staging/dune_db.tar", Method: MethodExport}
	if r.Restorable() {
		t.Fatal("export receipts must not be restorable")
	}
	err := Restore(r)
	if err == nil || !strings.Contains(err.Error(), "dune_db.tar") {
		t.Fatalf("restore must refuse and point at the tarball, got %v", err)
	}
}

func TestRestoreReportsEmptiedTrash(t *testing.T) {
	err := Restore(Receipt{Original: "/Users/t/x", To: filepath.Join(t.TempDir(), "gone"), Method: MethodFinder})
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("want emptied-trash error, got %v", err)
	}
}

// Read-only trees (go mod cache: 0444 files, 0555 dirs) must move and
// restore fine — rename touches only the parents.
func TestMoveAndRestoreReadOnlyTree(t *testing.T) {
	dir := writeTree(t, "mod/pkg/go.mod")
	target := filepath.Join(dir, "mod")
	if err := os.Chmod(filepath.Join(target, "pkg", "go.mod"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(target, "pkg"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(target, "pkg"), 0o755) })

	staging := filepath.Join(t.TempDir(), "staging")
	m := &Mover{Home: "/Users/t", StagingDir: staging, RunFinder: fakeFinder("", errors.New("no Finder"))}
	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatalf("read-only tree must still move: %v", err)
	}
	if err := Restore(r); err != nil {
		t.Fatalf("read-only tree must still restore: %v", err)
	}
	if err := os.Chmod(filepath.Join(target, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptGone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "copy")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for to, want := range map[string]bool{
		file:                          false,
		filepath.Join(dir, "emptied"): true,
		// A component that is a file, not a folder: nothing is there.
		filepath.Join(file, "inside"): true,
	} {
		if got := (Receipt{To: to, Method: MethodFinder}).Gone(); got != want {
			t.Errorf("Gone(%s) = %v, want %v", to, got, want)
		}
	}
	// A refused stat says nothing about the copy, so it is not gone.
	if os.Geteuid() == 0 {
		t.Skip("root reads through a folder's permissions")
	}
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "copy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if (Receipt{To: filepath.Join(locked, "copy"), Method: MethodStaging}).Gone() {
		t.Error("a copy behind a folder that refuses a stat must not read as gone")
	}
}

// Once the copy leaves the Trash, an item the user trashes later can
// take its name. The receipt knows its own item, so that one reads as
// gone and is never restored into the original's place.
func TestRestoreRefusesAnotherItemAtTheCopyPath(t *testing.T) {
	dir := writeTree(t, "cache/a.bin")
	target := filepath.Join(dir, "cache")
	m := &Mover{Home: "/Users/t", StagingDir: filepath.Join(t.TempDir(), "staging"), RunFinder: fakeFinder("", errors.New("no Finder"))}
	r, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ino == 0 || r.Gone() {
		t.Fatalf("receipt %+v: want its item's identity recorded and the copy present", r)
	}
	if err := os.Rename(r.To, filepath.Join(dir, "taken-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.To, "another"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !r.Gone() {
		t.Error("another item at the copy's path must read as gone")
	}
	if err := Restore(r); err == nil || !strings.Contains(err.Error(), "another item") {
		t.Errorf("Restore = %v, want it refused", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Error("another item was restored into the original's place")
	}
}

// macOS gives a volume a new device number when its disk is attached
// again, so a copy still in that volume's Trash is judged by its inode
// alone, however its receipt was journaled.
func TestReceiptKeepsItsCopyWhenTheDiskIsRenumbered(t *testing.T) {
	dir := writeTree(t, "trash/cache/a.bin")
	to := filepath.Join(dir, "trash", "cache")
	fi, err := os.Lstat(to)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	original := filepath.Join(dir, "home", "cache")
	line := fmt.Sprintf(`{"original":%q,"to":%q,"method":"finder","dev":%d,"ino":%d}`, original, to, uint64(st.Dev)+1, st.Ino)
	var r Receipt
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	if r.Gone() {
		t.Error("a copy whose inode matches must read as present on a renumbered disk")
	}
	if err := Restore(r); err != nil {
		t.Fatalf("Restore = %v, want the copy restored", err)
	}
	if _, err := os.Lstat(filepath.Join(original, "a.bin")); err != nil {
		t.Errorf("not restored: %v", err)
	}
}
