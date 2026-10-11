package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

type memJournal struct{ entries []oplog.Entry }

func (j *memJournal) Append(e oplog.Entry) error {
	j.entries = append(j.entries, e)
	return nil
}

// noMover fails the test if a plan of native commands reaches the Trash.
type noMover struct{ t *testing.T }

func (m noMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	m.t.Errorf("Trash move of %s: the plan holds no trash action", path)
	return trash.Receipt{}, nil
}

// fixtureGit runs git for a throwaway repository, cut off from the
// machine's config and from the variables a git hook or `rebase --exec`
// sets, which would point it at the enclosing repository.
func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// worktreeRule is the catalog's git-worktrees rule in what a plan and a
// run read of it. Synthetic: these tests never load the catalog.
var worktreeRule = engine.Rule{
	ID: "git-worktrees", Title: "Finished git worktrees", Category: "fixture", Risk: engine.RiskCaution,
	NativeCommand: engine.Argv{"git", "-C", "{path}", "worktree", "remove", "{path}"},
	PreAction:     engine.PreActionWorktreeRecheck,
}

// recheckFixture builds a throwaway repository with one clean linked
// worktree that holds an ignored node_modules, and points the state and
// config dirs of the run executor at temp dirs. The hook's own git
// calls read the process environment, so that is isolated too.
func recheckFixture(t *testing.T) (host engine.Host, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "home", "workspace", "app")
	wt = filepath.Join(repo, ".claude", "worktrees", "done")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", ".gitignore")
	fixtureGit(t, repo, "commit", "-q", "-m", "init")
	fixtureGit(t, repo, "worktree", "add", "-q", "-b", "done", wt)
	if err := os.MkdirAll(filepath.Join(wt, "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "node_modules", "dep", "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return engine.Host{OS: "darwin", Version: "15.5", Home: filepath.Join(root, "home")}, wt
}

func addIgnoredEnv(t *testing.T, wt string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeRecheckStopsTheRemoval drives the hooks of the run
// executor `regrow clean` builds, with the command and the journal
// injected: the removal runs for a worktree unchanged since planning,
// and never runs once an ignored file appeared in it.
func TestWorktreeRecheckStopsTheRemoval(t *testing.T) {
	host, wt := recheckFixture(t)
	findings := []engine.Finding{{Rule: worktreeRule, Items: []engine.Item{{Path: wt, Bytes: 10}}}}
	plan := engine.BuildPlan(host, findings, map[string]bool{worktreeRule.ID: true})
	if len(plan.Actions) != 1 || plan.Actions[0].PreAction != engine.PreActionWorktreeRecheck {
		t.Fatalf("plan = %+v, want one action carrying the recheck", plan)
	}

	var ran [][]string
	journal := &memJournal{}
	exe, release, err := newRunExecutor(host, "fixture-run", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe.Trash = noMover{t}
	exe.Log = journal
	exe.RunNative = func(_ context.Context, argv []string) error {
		ran = append(ran, argv)
		return nil
	}

	res, err := exe.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"git", "-C", wt, "worktree", "remove", wt}}
	if res.Done != 1 || res.Failed != 0 || !reflect.DeepEqual(ran, want) {
		t.Fatalf("unchanged worktree: result %+v, ran %v, want the removal to run once", res, ran)
	}

	addIgnoredEnv(t, wt)
	journal.entries = nil
	res, err = exe.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 0 || res.Failed != 1 || len(ran) != 1 {
		t.Fatalf("after an ignored .env appeared: result %+v, ran %v, want one failure and no second removal", res, ran)
	}
	if len(journal.entries) != 2 || journal.entries[1].Event != oplog.EventFail {
		t.Fatalf("journal = %+v, want a start line and a fail line", journal.entries)
	}
	if msg := journal.entries[1].Error; !strings.Contains(msg, ".env") || !strings.Contains(msg, wt) {
		t.Fatalf("journaled error = %q, want it to name the file and the worktree", msg)
	}
}

// TestEngineRechecksAWorktreeBeforeRemovingIt runs scan, plan and
// execute over the engine protocol. The scan and the steward command
// are injected; the executor is the engine's own, hooks included. The
// plan event names the unselected node_modules the removal takes along,
// the removal runs for an unchanged worktree, and once an ignored file
// appeared the recheck journals a fail and no command runs.
func TestEngineRechecksAWorktreeBeforeRemovingIt(t *testing.T) {
	host, wt := recheckFixture(t)
	modules := engine.Rule{ID: "fixture-modules", Title: "Fixture node_modules", Category: "fixture", Risk: engine.RiskCaution}
	catalog := []engine.Rule{worktreeRule, modules}

	var ran [][]string
	srv := newEngineServer(host, catalog, nil)
	srv.Headroom = fixtureSample
	srv.Scan = func(_ context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		emit(0, engine.Finding{Rule: rules[0], Items: []engine.Item{{Path: wt, Bytes: 100}}}, 0)
		emit(1, engine.Finding{Rule: rules[1], Items: []engine.Item{{Path: filepath.Join(wt, "node_modules"), Bytes: 60}}}, 0)
	}
	build := srv.NewExecutor
	srv.NewExecutor = func(runID string) (*executor.Executor, func(), error) {
		ex, release, err := build(runID)
		if err != nil {
			return nil, nil, err
		}
		ex.Trash = noMover{t}
		ex.RunNative = func(_ context.Context, argv []string) error {
			ran = append(ran, argv)
			return nil
		}
		return ex, release, nil
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(context.Background(), inR, outW)
		_ = outW.Close()
	}()
	lines := bufio.NewScanner(outR)
	type action struct {
		Command   []string `json:"command"`
		PreAction string   `json:"pre_action"`
		Bytes     int64    `json:"bytes"`
		Includes  []struct {
			ID    string `json:"id"`
			Bytes int64  `json:"bytes"`
		} `json:"includes"`
	}
	type event struct {
		Event  string `json:"event"`
		Re     string `json:"re"`
		ScanID string `json:"scan_id"`
		PlanID string `json:"plan_id"`
		Plan   *struct {
			Actions []action `json:"actions"`
		} `json:"plan"`
		Entry  *oplog.Entry     `json:"entry"`
		Result *executor.Result `json:"result"`
	}
	// answer sends one request and returns its events through done.
	answer := func(id, line string) []event {
		t.Helper()
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatal(err)
		}
		var got []event
		for lines.Scan() {
			var e event
			if err := json.Unmarshal(lines.Bytes(), &e); err != nil {
				t.Fatalf("not an event line: %s", lines.Text())
			}
			if e.Event == "hello" {
				continue
			}
			if e.Event == "error" || e.Re != id {
				t.Fatalf("request %s got %s", id, lines.Text())
			}
			got = append(got, e)
			if e.Event == "done" {
				return got
			}
		}
		t.Fatalf("the engine closed its output before request %s was done", id)
		return nil
	}
	// run scans, plans the worktree alone and executes the plan.
	run := func(n string) (action, []oplog.Entry, executor.Result) {
		t.Helper()
		var scanID string
		for _, e := range answer("s"+n, `{"type":"scan","id":"s`+n+`"}`) {
			if e.Event == "start" {
				scanID = e.ScanID
			}
		}
		planned := answer("p"+n, `{"type":"plan","id":"p`+n+`","scan_id":"`+scanID+`","select":["git-worktrees"]}`)[0]
		if planned.Plan == nil || len(planned.Plan.Actions) != 1 {
			t.Fatalf("plan event = %+v, want one action", planned)
		}
		var journal []oplog.Entry
		var result executor.Result
		for _, e := range answer("x"+n, `{"type":"execute","id":"x`+n+`","plan_id":"`+planned.PlanID+`"}`) {
			if e.Entry != nil {
				journal = append(journal, *e.Entry)
			}
			if e.Result != nil {
				result = *e.Result
			}
		}
		return planned.Plan.Actions[0], journal, result
	}

	removal := []string{"git", "-C", wt, "worktree", "remove", wt}
	act, journal, res := run("1")
	if !reflect.DeepEqual(act.Command, removal) || act.PreAction != engine.PreActionWorktreeRecheck || act.Bytes != 100 {
		t.Fatalf("planned action = %+v, want the removal with its recheck and the worktree's bytes", act)
	}
	if len(act.Includes) != 1 || !strings.HasPrefix(act.Includes[0].ID, "fixture-modules/") ||
		!strings.HasSuffix(act.Includes[0].ID, "/done/node_modules") || act.Includes[0].Bytes != 60 {
		t.Fatalf("includes = %+v, want the unselected node_modules inside the worktree with its bytes", act.Includes)
	}
	if res.Done != 1 || res.Failed != 0 || !reflect.DeepEqual(ran, [][]string{removal}) {
		t.Fatalf("unchanged worktree: result %+v, ran %v, want the removal to run once", res, ran)
	}
	if len(journal) != 2 || journal[1].Event != oplog.EventDone {
		t.Fatalf("journal = %+v, want a start line and a done line", journal)
	}

	addIgnoredEnv(t, wt)
	_, journal, res = run("2")
	if res.Done != 0 || res.Failed != 1 || len(ran) != 1 {
		t.Fatalf("after an ignored .env appeared: result %+v, ran %v, want one failure and no second removal", res, ran)
	}
	if len(journal) != 2 || journal[1].Event != oplog.EventFail {
		t.Fatalf("journal = %+v, want a start line and a fail line", journal)
	}
	if msg := journal[1].Error; !strings.Contains(msg, ".env") || !strings.Contains(msg, wt) {
		t.Fatalf("journaled error = %q, want it to name the file and the worktree", msg)
	}

	_ = inW.Close()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "node_modules", "dep", "index.js")); err != nil {
		t.Fatalf("no command ran for real, the worktree must be intact: %v", err)
	}
}
