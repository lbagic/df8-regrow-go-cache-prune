package main

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

// recheckRun is the run executor `regrow clean` and the engine build,
// with the Trash and the journal injected, over a throwaway home.
func recheckRun(t *testing.T) (host engine.Host, exe func(engine.Plan) (int, int, []string, string)) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	host = engine.Host{OS: "darwin", Version: "15.5", Home: home}
	return host, func(plan engine.Plan) (done, failed int, moved []string, lastErr string) {
		t.Helper()
		mover := &movingMover{to: t.TempDir()}
		journal := &memJournal{}
		ex, release, err := newRunExecutor(host, "fixture-run", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ex.Trash = mover
		ex.Log = journal
		ex.RunNative = func(_ context.Context, argv []string) error {
			t.Errorf("command %v ran: the plan holds only Trash moves", argv)
			return nil
		}
		res, err := ex.Execute(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range journal.entries {
			if e.Error != "" {
				lastErr = e.Error
			}
		}
		return res.Done, res.Failed, mover.moved, lastErr
	}
}

func trashPlan(t *testing.T, host engine.Host, rule engine.Rule, paths ...string) engine.Plan {
	t.Helper()
	var items []engine.Item
	for _, p := range paths {
		items = append(items, engine.Item{Path: p, Bytes: 10})
	}
	plan := engine.BuildPlan(host, []engine.Finding{{Rule: rule, Items: items}}, map[string]bool{rule.ID: true})
	if len(plan.Actions) != len(paths) {
		t.Fatalf("plan = %+v, want %d actions", plan, len(paths))
	}
	for _, a := range plan.Actions {
		if a.Kind != engine.ActionTrash || a.PreAction != rule.PreAction {
			t.Fatalf("action = %+v, want a Trash move carrying %s", a, rule.PreAction)
		}
	}
	return plan
}

// An npx folder a running process names stays; an idle one goes.
func TestNpxRecheckStopsTheTrashMove(t *testing.T) {
	for _, tool := range []string{"lsof", "ps", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	host, run := recheckRun(t)
	rule := engine.Rule{ID: "npx-cache", Title: "npx package folders", Category: "fixture", Risk: engine.RiskSafe, PreAction: engine.PreActionNpxRecheck}
	used, idle := filepath.Join(host.Home, ".npm", "_npx", "used"), filepath.Join(host.Home, ".npm", "_npx", "idle")
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, d := range []string{used, idle} {
		if err := os.MkdirAll(filepath.Join(d, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{filepath.Join(d, "node_modules"), d} {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The shell waits in a builtin, so it starts no child to outlive it.
	server := exec.Command("sh", "-c", "read line", used+"/node_modules/.bin/server")
	stdin, err := server.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = server.Process.Kill()
		_ = server.Wait()
	})

	done, failed, moved, msg := run(trashPlan(t, host, rule, used, idle))
	if done != 1 || failed != 1 || !reflect.DeepEqual(moved, []string{idle}) {
		t.Fatalf("done %d, failed %d, moved %v; want the idle folder moved and the used one kept", done, failed, moved)
	}
	if !strings.Contains(msg, "a running process uses it") || !strings.Contains(msg, used) {
		t.Fatalf("journaled error = %q, want it to name the folder and the reason", msg)
	}
}

// A download touched today stays, whatever the apps on this machine.
func TestDownloadedInstallerRecheckStopsTheTrashMove(t *testing.T) {
	host, run := recheckRun(t)
	rule := engine.Rule{ID: "downloaded-installers", Title: "Installers of installed apps", Category: "fixture", Risk: engine.RiskCaution, PreAction: engine.PreActionDownloadedInstallerRecheck}
	dmg := filepath.Join(host.Home, "Downloads", "Docker.dmg")
	if err := os.MkdirAll(filepath.Dir(dmg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dmg, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	done, failed, moved, msg := run(trashPlan(t, host, rule, dmg))
	if done != 0 || failed != 1 || len(moved) != 0 {
		t.Fatalf("done %d, failed %d, moved %v; want the download kept", done, failed, moved)
	}
	if !strings.Contains(msg, "is kept now") || !strings.Contains(msg, dmg) {
		t.Fatalf("journaled error = %q, want it to name the download and say it is kept", msg)
	}
}
