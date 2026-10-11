package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// npxFixture is a home whose ~/.npm/_npx holds the named folders, each
// with a file in it, with no process running and a clock 30 days past
// the moment the fixture was made.
func npxFixture(t *testing.T, names ...string) (*npxCache, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(30 * 24 * time.Hour)
	n := &npxCache{
		w:     testWalker(),
		dir:   filepath.Join(home, npxCacheDir),
		quiet: npxQuiet,
		procs: func(context.Context) (processes, error) { return processes{}, nil },
		now:   func() time.Time { return now },
	}
	for _, name := range names {
		writeFile(t, filepath.Join(n.dir, name, "node_modules", "pkg", "index.js"), 5000)
	}
	return n, home
}

func TestNpxCacheOffersFoldersNoProcessUses(t *testing.T) {
	n, _ := npxFixture(t, "aaa", "bbb", "ccc", "ddd", "eee", "fff")
	// npx's lock folder stamps the folder on every run.
	ran := n.now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(n.dir, "fff"), ran, ran); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.dir, "aaa", "package.json"), []byte(`{"_npx":{"packages":["create-vite@latest"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.dir, "bbb", "package.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file beside the folders is no npx folder.
	writeFile(t, filepath.Join(n.dir, ".DS_Store"), 10)
	ccc, ddd := filepath.Join(n.dir, "ccc"), filepath.Join(n.dir, "ddd")
	n.procs = func(context.Context) (processes, error) {
		return processes{
			cwds: []string{"/", ddd + "/node_modules"},
			// A folder whose hash starts another's is not named by it.
			args: []string{"node " + ccc + "/node_modules/.bin/server --stdio", "node " + filepath.Join(n.dir, "eeee") + "/x"},
		}, nil
	}

	items, kept, err := n.scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var paths, labels []string
	for _, it := range items {
		if it.Bytes <= 0 || it.LastUsed.IsZero() || it.Partial {
			t.Errorf("%s: want a complete measurement, got %+v", it.Path, it)
		}
		paths = append(paths, it.Path)
		labels = append(labels, it.Label)
	}
	wantPaths := []string{filepath.Join(n.dir, "aaa"), filepath.Join(n.dir, "bbb"), filepath.Join(n.dir, "eee")}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("offered %v, want %v", paths, wantPaths)
	}
	wantLabels := []string{"create-vite@latest · npx aaa", "npx bbb", "npx eee"}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("labels %q, want %q", labels, wantLabels)
	}
	wantKept := []keptNpx{{ccc, "a running process uses it"}, {ddd, "a running process uses it"}, {filepath.Join(n.dir, "fff"), "npx ran it in the last 7 days"}}
	if !reflect.DeepEqual(kept, wantKept) {
		t.Errorf("kept %+v, want %+v", kept, wantKept)
	}
}

func TestNpxCacheUnknownProcessesAreAnError(t *testing.T) {
	n, _ := npxFixture(t, "aaa")
	n.procs = func(context.Context) (processes, error) { return processes{}, errors.New("lsof: exit status 1") }
	items, _, err := n.scan(context.Background())
	if err == nil || len(items) != 0 {
		t.Fatalf("items %+v, err %v; want no items and the error", items, err)
	}
}

func TestNpxCacheAbsentAsksNoProcesses(t *testing.T) {
	n, _ := npxFixture(t)
	n.procs = func(context.Context) (processes, error) {
		t.Error("processes listed with no npx folder to judge")
		return processes{}, nil
	}
	items, kept, err := n.scan(context.Background())
	if err != nil || len(items) != 0 || len(kept) != 0 {
		t.Fatalf("items %+v, kept %+v, err %v; want nothing", items, kept, err)
	}
}

func TestNpxCacheUnreadableFolderIsARow(t *testing.T) {
	n, _ := npxFixture(t, "aaa")
	if err := os.Chmod(n.dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(n.dir, 0o755) })
	items, _, err := n.scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "" || !items[0].Partial {
		t.Fatalf("items %+v, want one unreadable row with no path", items)
	}
}

func TestNpxCacheKeepsAFolderNotAllRead(t *testing.T) {
	n, _ := npxFixture(t, "aaa", "bbb")
	locked := filepath.Join(n.dir, "aaa", "node_modules")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	items, kept, err := n.scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantKept := []keptNpx{{filepath.Join(n.dir, "aaa"), "it cannot all be read, so when npx last ran it is unknown"}}
	if len(items) != 1 || items[0].Path != filepath.Join(n.dir, "bbb") || !reflect.DeepEqual(kept, wantKept) {
		t.Fatalf("items %+v, kept %+v; want bbb offered and aaa kept", items, kept)
	}
}

func TestNpxRecheck(t *testing.T) {
	n, home := npxFixture(t, "aaa", "bbb")
	aaa, bbb := filepath.Join(n.dir, "aaa"), filepath.Join(n.dir, "bbb")
	n.procs = func(context.Context) (processes, error) {
		return processes{args: []string{"node " + bbb + "/node_modules/.bin/server"}}, nil
	}
	ctx := context.Background()
	if err := n.recheck(ctx, aaa); err != nil {
		t.Errorf("idle folder: %v, want it allowed", err)
	}
	if err := n.recheck(ctx, bbb); err == nil || !strings.Contains(err.Error(), "a running process uses it") {
		t.Errorf("folder in use: %v, want a refusal naming why", err)
	}
	if err := n.recheck(ctx, filepath.Join(home, ".npm", "_cacache")); err == nil {
		t.Error("a folder outside the npx cache was allowed")
	}
	ran := n.now().Add(-time.Hour)
	if err := os.Chtimes(aaa, ran, ran); err != nil {
		t.Fatal(err)
	}
	if err := n.recheck(ctx, aaa); err == nil || !strings.Contains(err.Error(), "npx ran it") {
		t.Errorf("folder run since the scan: %v, want a refusal naming why", err)
	}
	if err := n.recheck(ctx, filepath.Join(n.dir, "gone")); err == nil {
		t.Error("a folder that is gone was allowed")
	}
	n.procs = func(context.Context) (processes, error) { return processes{}, errors.New("ps: exit status 1") }
	if err := n.recheck(ctx, aaa); err == nil {
		t.Error("with the processes unknown the folder was allowed")
	}
}

func TestNpxCacheRule(t *testing.T) {
	r := catalogRule(t, "npx-cache")
	if r.Risk != engine.RiskSafe || len(r.NativeCommand) != 0 {
		t.Errorf("risk %q, native_command %v; want a safe rule moving folders to the Trash", r.Risk, r.NativeCommand)
	}
	if r.PreAction != engine.PreActionNpxRecheck {
		t.Errorf("pre_action = %q, want %q", r.PreAction, engine.PreActionNpxRecheck)
	}
	if !r.Beta {
		t.Error("npx-cache is not beta")
	}
	if _, ok := DefaultQueries()[r.ToolQuery]; !ok || r.ToolQuery != "npx-cache" {
		t.Errorf("tool_query %q is not the registered npx-cache query", r.ToolQuery)
	}
}

// npm cache clean --force deletes ~/.npm/_cacache alone. An npm-cache
// item over all of ~/.npm would hold npx's folders in the containment
// forest, which would then skip their moves as covered by a command
// that leaves them in place.
func TestNpmCacheLeavesNpxFolders(t *testing.T) {
	npm := catalogRule(t, "npm-cache")
	for osName, entries := range npm.Paths {
		if len(entries) != 1 || entries[0].Path != "~/.npm/_cacache" {
			t.Errorf("npm-cache paths on %s = %+v, want ~/.npm/_cacache alone", osName, entries)
		}
	}
	npx := catalogRule(t, "npx-cache")
	root := t.TempDir()
	host := engine.Host{OS: "darwin", Version: "15.5", Home: filepath.Join(root, "home"), Root: root}
	writeFile(t, host.ExpandPath("~/.npm/_cacache/content-v2/blob"), 8192)
	npxDir := host.ExpandPath("~/.npm/_npx/aaa")
	writeFile(t, filepath.Join(npxDir, "node_modules", "pkg", "index.js"), 65536)

	s := New(host)
	s.Queries = map[string]ToolQuery{"npx-cache": fixedItems(engine.Item{Path: npxDir, Label: "npx aaa", Bytes: 65536})}
	findings := s.Scan(context.Background(), []engine.Rule{npm, npx})
	if f := findings[0]; f.Err != "" || len(f.Items) != 1 || f.Items[0].Path != host.ExpandPath("~/.npm/_cacache") || f.Items[0].Bytes >= 65536 {
		t.Fatalf("npm-cache finding = %+v, want one item, ~/.npm/_cacache, without the npx folder's bytes", f)
	}
	plan := engine.BuildPlan(host, findings, map[string]bool{"npm-cache": true, "npx-cache": true})
	var moved bool
	for _, a := range plan.Actions {
		moved = moved || (a.RuleID == "npx-cache" && a.Kind == engine.ActionTrash && a.Path == npxDir)
	}
	if !moved || len(plan.Actions) != 2 || len(plan.Skipped) != 0 {
		t.Fatalf("plan = %+v, want npm cache clean and the npx folder's move, nothing skipped", plan)
	}
}
