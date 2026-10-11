package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// movingMover stands in for the Trash: it renames the folder out of
// its project, as a Trash move does, which stamps the folder it leaves.
type movingMover struct {
	to    string
	moved []string
}

func (m *movingMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	dst := filepath.Join(m.to, strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-"))
	if err := os.Rename(path, dst); err != nil {
		return trash.Receipt{}, err
	}
	m.moved = append(m.moved, path)
	return trash.Receipt{Original: path, To: dst, Method: trash.MethodStaging}, nil
}

// buildOutputRule is the catalog's build-output rule in what a plan and
// a run read of it. Synthetic: these tests never load the catalog.
var buildOutputRule = engine.Rule{
	ID: "build-output", Title: "Build output in idle projects", Category: "fixture", Risk: engine.RiskCaution,
	PreAction: engine.PreActionBuildOutputRecheck,
}

// buildOutputFixture builds a throwaway home whose ~/workspace/app is a
// repository with three ignored dist folders, each beside a tracked
// file, every path of it dated 60 days back so the project reads as
// idle today. The hook reads the home, git's config and the state dir
// from the process environment, so those point at temp dirs.
func buildOutputFixture(t *testing.T) (host engine.Host, project string, dists []string) {
	t.Helper()
	for _, tool := range []string{"git", "lsof", "ps"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project = filepath.Join(home, "workspace", "app")
	files := map[string]string{".gitignore": "dist/\n", "main.js": "source\n"}
	for _, at := range []string{"", "web", "api"} {
		dists = append(dists, filepath.Join(project, at, "dist"))
		files[filepath.Join(at, "dist", "bundle.js")] = "built\n"
		files[filepath.Join(at, "index.js")] = "source\n"
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, project, "init", "-q", "-b", "main")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(project, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, project, "add", ".")
	fixtureGit(t, project, "commit", "-q", "-m", "init")
	old := time.Now().Add(-60 * 24 * time.Hour)
	err = filepath.WalkDir(project, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine.Host{OS: "darwin", Version: "15.5", Home: home}, project, dists
}

// TestBuildOutputRecheckStopsTheTrashMove drives the hooks of the run
// executor `regrow clean` and the engine build, with the Trash and the
// journal injected. Two build folders of one idle project both go: the
// first move stamps the folder it leaves, which must not keep the
// second. Once a file in the project was edited, the third stays.
func TestBuildOutputRecheckStopsTheTrashMove(t *testing.T) {
	host, project, dists := buildOutputFixture(t)
	planFor := func(paths ...string) engine.Plan {
		t.Helper()
		var items []engine.Item
		for _, p := range paths {
			items = append(items, engine.Item{Path: p, Bytes: 10})
		}
		plan := engine.BuildPlan(host, []engine.Finding{{Rule: buildOutputRule, Items: items}}, map[string]bool{buildOutputRule.ID: true})
		if len(plan.Actions) != len(paths) {
			t.Fatalf("plan = %+v, want %d actions", plan, len(paths))
		}
		for _, a := range plan.Actions {
			if a.Kind != engine.ActionTrash || a.PreAction != engine.PreActionBuildOutputRecheck {
				t.Fatalf("action = %+v, want a Trash move carrying the recheck", a)
			}
		}
		return plan
	}

	mover := &movingMover{to: t.TempDir()}
	journal := &memJournal{}
	exe, release, err := newRunExecutor(host, "fixture-run", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe.Trash = mover
	exe.Log = journal
	exe.RunNative = func(_ context.Context, argv []string) error {
		t.Errorf("command %v ran: the plan holds only Trash moves", argv)
		return nil
	}

	// The subfolder's first: its move stamps app/web, which the recheck
	// of app/dist then walks past.
	res, err := exe.Execute(context.Background(), planFor(dists[1], dists[0]))
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Failed != 0 || !reflect.DeepEqual(mover.moved, []string{dists[1], dists[0]}) {
		t.Fatalf("idle project: result %+v, moved %v, journal %+v; want both folders moved", res, mover.moved, journal.entries)
	}

	if err := os.WriteFile(filepath.Join(project, "main.js"), []byte("edited today\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal.entries = nil
	res, err = exe.Execute(context.Background(), planFor(dists[2]))
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 0 || res.Failed != 1 || len(mover.moved) != 2 {
		t.Fatalf("after an edit in the project: result %+v, moved %v, want one failure and no third move", res, mover.moved)
	}
	if len(journal.entries) != 2 || journal.entries[1].Event != oplog.EventFail {
		t.Fatalf("journal = %+v, want a start line and a fail line", journal.entries)
	}
	if msg := journal.entries[1].Error; !strings.Contains(msg, "its project changed") || !strings.Contains(msg, dists[2]) {
		t.Fatalf("journaled error = %q, want it to name the folder and the reason", msg)
	}
}
