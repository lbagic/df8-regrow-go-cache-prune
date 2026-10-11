package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// gitTestEnv isolates git from the machine's config and fixes the
// author and dates, so the throwaway repositories are reproducible. It
// also drops the variables that point git at a repository: under a git
// hook or `rebase --exec` they would send the fixture's commands to the
// enclosing repository.
func gitTestEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, name := range gitRepoEnv {
		if val, ok := os.LookupEnv(name); ok {
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Setenv(name, val) })
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "Fixture")
		t.Setenv("GIT_"+who+"_EMAIL", "fixture@example.com")
		t.Setenv("GIT_"+who+"_DATE", "2026-01-01T00:00:00Z")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "change "+name)
}

// removeWorktreeArgv is the command the rule must plan, written out so
// the test that runs it never takes it from the catalog.
func removeWorktreeArgv(path string) []string {
	return []string{"git", "-C", path, "worktree", "remove", path}
}

// worktreeFixture is a main repository with one linked worktree per
// case, each failing or passing exactly one test of the rule.
type worktreeFixture struct {
	home, app, scratch, scratchLink, solo string
	wt                                    map[string]string // case → worktree path
	short                                 map[string]string // case → HEAD, abbreviated, of a detached case
	now                                   time.Time         // a week and a day after the fixture was built
}

func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	gitTestEnv(t)
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real // git reports real paths
	}
	f := worktreeFixture{
		home:        filepath.Join(root, "home"),
		scratch:     filepath.Join(root, "scratch"),
		scratchLink: filepath.Join(root, "scratch-link"),
		wt:          map[string]string{},
		short:       map[string]string{},
		now:         time.Now().Add(worktreeIdle + 24*time.Hour),
	}
	f.app = filepath.Join(f.home, "workspace", "app")
	if err := os.MkdirAll(f.app, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.app, "init", "-q", "-b", "main")
	runGit(t, f.app, "remote", "add", "origin", filepath.Join(root, "origin.git"))
	commitFile(t, f.app, ".gitignore", "node_modules/\n.env\n.idea/\n")
	commitFile(t, f.app, "README", "app\n")

	add := func(name string, args ...string) string {
		p := filepath.Join(f.app, ".claude", "worktrees", name)
		runGit(t, f.app, append([]string{"worktree", "add", "-q", p}, args...)...)
		f.wt[name] = p
		return p
	}
	branch := func(name string) string { return add(name, "-b", name) }
	detached := func(name string) string {
		p := add(name, "--detach", "main")
		f.short[name] = runGit(t, p, "rev-parse", "--short=7", "HEAD")
		return p
	}

	// Qualifies: merged by ancestry, holding ignored node_modules in a
	// folder that holds nothing else.
	p := branch("merged")
	commitFile(t, p, "feature", "done\n")
	touch(t, filepath.Join(p, "web", "node_modules", "dep", "index.js"))
	runGit(t, f.app, "merge", "-q", "--ff-only", "merged")

	// Qualifies: squash-merged elsewhere, its upstream branch deleted.
	p = branch("squashed")
	commitFile(t, p, "fix", "done\n")
	runGit(t, f.app, "config", "branch.squashed.remote", "origin")
	runGit(t, f.app, "config", "branch.squashed.merge", "refs/heads/squashed")

	// Qualifies: merged on the remote, whose default branch is not
	// called main, while the local main is stale.
	p = branch("merged-upstream")
	commitFile(t, p, "upstream", "done\n")
	runGit(t, f.app, "update-ref", "refs/remotes/origin/trunk", "refs/heads/merged-upstream")
	runGit(t, f.app, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")

	// Qualifies: detached at a commit main contains.
	detached("detached-merged")

	// Qualifies: a sparse checkout's skip-worktree entry is not on disk,
	// so nothing of it can be lost.
	p = branch("sparse")
	runGit(t, p, "update-index", "--skip-worktree", "README")
	if err := os.Remove(filepath.Join(p, "README")); err != nil {
		t.Fatal(err)
	}

	// Qualifies: another tool's plain `git status` stamped the admin
	// dir and its index, which is not work in the worktree.
	branch("status-refreshed")

	// Each of these fails exactly one test.
	p = branch("unmerged")
	commitFile(t, p, "wip", "half\n")

	p = detached("detached-unmerged")
	commitFile(t, p, "experiment", "x\n")

	// An unborn branch lists an all-zero HEAD; it must not take the
	// repository's other worktrees out with it.
	p = detached("unborn")
	runGit(t, p, "checkout", "-q", "--orphan", "unborn")

	p = branch("untracked")
	touch(t, filepath.Join(p, "notes.txt"))

	p = branch("modified")
	if err := os.WriteFile(filepath.Join(p, "README"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p = branch("ignored-env")
	touch(t, filepath.Join(p, ".env"))

	// An edit to a tracked file that a flag hides from git status.
	for _, flag := range []string{"skip-worktree", "assume-unchanged"} {
		p = branch(flag)
		runGit(t, p, "update-index", "--"+flag, "README")
		if err := os.WriteFile(filepath.Join(p, "README"), []byte("local edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A base branch in a linked worktree is the main checkout of a
	// bare-repository layout: master by name, trunk as the local side
	// of the remote's default branch.
	branch("master")
	branch("trunk")

	// A bisect in progress leaves HEAD on a merged commit and status
	// empty; its state lives in the admin dir.
	p = detached("bisecting")
	runGit(t, p, "bisect", "start")

	// An ignored directory that is not build output.
	p = branch("ignored-idea")
	touch(t, filepath.Join(p, ".idea", "workspace.xml"))

	// dist/ is not ignored; its only file is, under the .env pattern.
	p = branch("env-in-dist")
	touch(t, filepath.Join(p, "dist", ".env"))

	p = branch("active")
	touch(t, filepath.Join(p, "node_modules", "dep", "index.js"))
	recent := f.now.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(p, "node_modules", "dep", "index.js"), recent, recent); err != nil {
		t.Fatal(err)
	}

	// A commit records itself in the admin dir; the files can be older.
	branch("committed-recently")
	stamp := func(wt string, names ...string) {
		admin := runGit(t, wt, "rev-parse", "--absolute-git-dir")
		for _, name := range names {
			if err := os.Chtimes(filepath.Join(admin, name), recent, recent); err != nil {
				t.Fatal(err)
			}
		}
	}
	stamp(f.wt["committed-recently"], filepath.Join("logs", "HEAD"))
	stamp(f.wt["status-refreshed"], "", "index")

	branch("locked")
	runGit(t, f.app, "worktree", "lock", f.wt["locked"])

	gone := branch("prunable")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	// A worktree of app in an agent's scratch dir, which is also a root
	// (given through a symlink), and a scratch clone with a worktree of
	// its own.
	p = filepath.Join(f.scratch, "session", "wt")
	runGit(t, f.app, "worktree", "add", "-q", "-b", "scratch", p, "main")
	f.wt["scratch"] = p
	f.solo = filepath.Join(f.scratch, "session", "solo")
	if err := os.MkdirAll(f.solo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.solo, "init", "-q", "-b", "main")
	commitFile(t, f.solo, "README", "solo\n")
	p = filepath.Join(f.scratch, "session", "solo-wt")
	runGit(t, f.solo, "worktree", "add", "-q", "--detach", p, "main")
	f.wt["solo"] = p
	f.short["solo"] = runGit(t, p, "rev-parse", "--short=7", "HEAD")
	if err := os.Symlink(f.scratch, f.scratchLink); err != nil {
		t.Fatal(err)
	}

	// Merged and clean, but a checked-out submodule makes git refuse
	// the removal. Last, since merging it moves main.
	lib := filepath.Join(root, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, lib, "init", "-q", "-b", "main")
	commitFile(t, lib, "README", "lib\n")
	p = branch("submodule")
	runGit(t, p, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "lib")
	runGit(t, p, "commit", "-q", "-m", "add lib")
	runGit(t, f.app, "merge", "-q", "--ff-only", "submodule")

	// A deinit'd submodule leaves a modules folder in the admin dir,
	// which git refuses as well.
	p = branch("submodule-deinit")
	runGit(t, p, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "lib")
	runGit(t, p, "submodule", "deinit", "-q", "-f", "lib")
	return f
}

func (f worktreeFixture) scan(t *testing.T) []engine.Item {
	t.Helper()
	host := engine.Host{OS: "darwin", Home: f.home}
	items, err := scanWorktrees(context.Background(), testWalker(), host, []string{"~/workspace", f.scratchLink}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestScanWorktrees(t *testing.T) {
	f := newWorktreeFixture(t)
	items := f.scan(t)

	t.Run("offers only finished ones", func(t *testing.T) {
		got := map[string]string{}
		for _, it := range items {
			got[it.Path] = it.Label
			if it.Bytes <= 0 || it.LastUsed.IsZero() || it.Partial {
				t.Errorf("%s: want a complete measurement, got %+v", it.Label, it)
			}
		}
		want := map[string]string{
			f.wt["merged"]:           "merged (merged)",
			f.wt["squashed"]:         "squashed (upstream gone)",
			f.wt["merged-upstream"]:  "merged-upstream (merged)",
			f.wt["detached-merged"]:  "detached " + f.short["detached-merged"] + " (merged)",
			f.wt["sparse"]:           "sparse (merged)",
			f.wt["status-refreshed"]: "status-refreshed (merged)",
			f.wt["scratch"]:          "scratch (merged)",
			f.wt["solo"]:             "detached " + f.short["solo"] + " (merged)",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("items = %v\nwant %v", got, want)
		}
	})

	t.Run("one action per worktree", func(t *testing.T) {
		finding := engine.Finding{Rule: worktreeRule(t), Items: items}
		finding.FillItemKeys(f.home)
		plan := engine.BuildPlan(engine.Host{OS: "darwin", Home: f.home}, []engine.Finding{finding}, map[string]bool{"git-worktrees": true})
		if len(plan.Actions) != len(finding.Items) || len(plan.Skipped) != 0 {
			t.Fatalf("want one action per worktree, got %d actions for %d items (skips %+v)", len(plan.Actions), len(finding.Items), plan.Skipped)
		}
		for i, a := range plan.Actions {
			if want := removeWorktreeArgv(finding.Items[i].Path); !reflect.DeepEqual(a.Command, want) {
				t.Errorf("action %d = %v, want %v", i, a.Command, want)
			}
			if a.ItemKey != finding.Items[i].Key {
				t.Errorf("action %d targets key %q, want the item's own %q", i, a.ItemKey, finding.Items[i].Key)
			}
		}
	})
}

// TestWorktreeRuleCommand pins the catalog's command to the one
// TestWorktreeRemoveAndRecheck runs. It executes nothing.
func TestWorktreeRuleCommand(t *testing.T) {
	r := worktreeRule(t)
	got, err := r.NativeCommand.ExpandItem(engine.Item{Path: "/w/x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := removeWorktreeArgv("/w/x"); !reflect.DeepEqual(got, want) {
		t.Fatalf("rule command = %v, want %v", got, want)
	}
	if r.PreAction != engine.PreActionWorktreeRecheck {
		t.Fatalf("pre_action = %q, want %q", r.PreAction, engine.PreActionWorktreeRecheck)
	}
}

// TestWorktreeRemoveAndRecheck runs the command on the throwaway
// repository: the worktree goes with its ignored build output and the
// branch stays. git refuses untracked files but not a new ignored one,
// which the pre-action's recheck catches.
func TestWorktreeRemoveAndRecheck(t *testing.T) {
	f := newWorktreeFixture(t)
	ctx := context.Background()
	run := func(path string) error {
		argv := removeWorktreeArgv(path)
		return exec.Command(argv[0], argv[1:]...).Run()
	}

	if err := CheckWorktreeClean(ctx, f.wt["merged"]); err != nil {
		t.Fatalf("recheck of an unchanged worktree: %v", err)
	}
	if err := run(f.wt["merged"]); err != nil {
		t.Fatalf("remove merged worktree: %v", err)
	}
	if _, err := os.Lstat(f.wt["merged"]); !os.IsNotExist(err) {
		t.Fatalf("worktree folder still there (err %v)", err)
	}
	if runGit(t, f.app, "branch", "--list", "merged") == "" {
		t.Fatal("the branch must survive the worktree")
	}
	for _, wt := range parseWorktreeList([]byte(runGit(t, f.app, "worktree", "list", "--porcelain", "-z"))) {
		if wt.path == f.wt["merged"] {
			t.Fatal("git still lists the removed worktree")
		}
	}

	if err := run(f.wt["untracked"]); err == nil {
		t.Fatal("git removed a worktree with untracked files")
	}
	if _, err := os.Lstat(filepath.Join(f.wt["untracked"], "notes.txt")); err != nil {
		t.Fatalf("the untracked file must survive: %v", err)
	}

	touch(t, filepath.Join(f.wt["squashed"], ".env"))
	err := CheckWorktreeClean(ctx, f.wt["squashed"])
	if err == nil || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), f.wt["squashed"]) {
		t.Fatalf("recheck after an ignored .env appeared = %v, want a refusal naming the file and the worktree", err)
	}

	// A commit on a detached HEAD leaves status empty, and no ref would
	// hold it once the worktree is gone.
	detached := f.wt["detached-merged"]
	if err := CheckWorktreeClean(ctx, detached); err != nil {
		t.Fatalf("recheck of a detached worktree at a merged commit: %v", err)
	}
	commitFile(t, detached, "late", "after the scan\n")
	err = CheckWorktreeClean(ctx, detached)
	if err == nil || !strings.Contains(err.Error(), "no branch contains") || !strings.Contains(err.Error(), detached) {
		t.Fatalf("recheck after a commit on a detached HEAD = %v, want a refusal naming the worktree", err)
	}
	// On a branch the same commit stays reachable through the branch.
	commitFile(t, f.wt["status-refreshed"], "late", "after the scan\n")
	if err := CheckWorktreeClean(ctx, f.wt["status-refreshed"]); err != nil {
		t.Fatalf("recheck after a commit on a branch: %v", err)
	}
}

// TestGitIgnoresRepositoryVariables: under a git hook or `rebase
// --exec`, GIT_DIR and its kin name the enclosing repository and would
// override -C in every call the query and the recheck make.
func TestGitIgnoresRepositoryVariables(t *testing.T) {
	f := newWorktreeFixture(t)
	want := f.scan(t)
	if len(want) == 0 {
		t.Fatal("fixture offers nothing")
	}
	elsewhere := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, "other.git"))
	t.Setenv("GIT_WORK_TREE", elsewhere)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(elsewhere, "index"))
	if got := f.scan(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan with GIT_DIR set = %d items, want the same %d as without", len(got), len(want))
	}
	if err := CheckWorktreeClean(context.Background(), f.wt["merged"]); err != nil {
		t.Fatalf("recheck with GIT_DIR set: %v", err)
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /r\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /r/w1\x00HEAD bbb\x00detached\x00locked\x00\x00" +
		"worktree /r/w2\x00HEAD ccc\x00branch refs/heads/x\x00prunable gitdir file points to non-existent location\x00\x00"
	got := parseWorktreeList([]byte(out))
	want := []worktree{
		{path: "/r", head: "aaa", branch: "refs/heads/main"},
		{path: "/r/w1", head: "bbb", locked: true},
		{path: "/r/w2", head: "ccc", branch: "refs/heads/x", prunable: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %+v\nwant %+v", got, want)
	}
}

func worktreeRule(t *testing.T) engine.Rule {
	t.Helper()
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.ID == "git-worktrees" {
			return r
		}
	}
	t.Fatal("git-worktrees rule missing from the catalog")
	return engine.Rule{}
}
