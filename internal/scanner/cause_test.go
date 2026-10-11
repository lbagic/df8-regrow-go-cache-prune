package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

var causeNow = time.Date(2026, 10, 4, 15, 30, 0, 0, time.UTC)

// causeFixture is a fake machine for the cause checks: a root holding
// a home, a clock, a 16 GiB memory and a go command whose GOFLAGS is
// empty.
func causeFixture(t *testing.T) *causeChecks {
	t.Helper()
	root := t.TempDir()
	return &causeChecks{
		host:   engine.Host{OS: "darwin", Version: "15.7", Home: filepath.Join(root, "Users/dev"), Root: root},
		w:      testWalker(),
		now:    func() time.Time { return causeNow },
		goEnv:  func(context.Context, string) (string, error) { return "", nil },
		memory: func() int64 { return 16 << 30 },
	}
}

func embeddedRule(t *testing.T, id string) engine.Rule {
	t.Helper()
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no rule %s in the embedded catalog", id)
	return engine.Rule{}
}

// wantCause asserts a check's verdict and that its detail holds every
// fragment.
func wantCause(t *testing.T, verdict engine.Verdict, detail string, want engine.Verdict, fragments ...string) {
	t.Helper()
	if verdict != want {
		t.Errorf("verdict = %q (%s), want %q", verdict, detail, want)
	}
	for _, f := range fragments {
		if !strings.Contains(detail, f) {
			t.Errorf("detail %q lacks %q", detail, f)
		}
	}
}

// treeState lists every entry under root with its mode, size and
// mtime, so a test can show a check changed nothing.
func treeState(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %s %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEmbeddedCatalogCauseChecksRegistered(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	queries := DefaultCauseQueries(engine.Host{})
	owner := map[string]string{}
	for _, r := range catalog {
		for _, c := range r.Causes {
			if _, ok := queries[c.Check]; !ok {
				t.Errorf("rule %s references unknown cause check %q", r.ID, c.Check)
			}
			if prev, dup := owner[c.Check]; dup {
				t.Errorf("cause check %q is declared by both %s and %s: doctor would print the row twice", c.Check, prev, r.ID)
			}
			owner[c.Check] = r.ID
		}
	}
	for name := range queries {
		if owner[name] == "" {
			t.Errorf("cause check %q has no rule declaring it: no story, no fix, never run", name)
		}
	}
}

func TestCausesRowsFollowTheCatalog(t *testing.T) {
	cause := func(check, osMax string) engine.Cause {
		return engine.Cause{Check: check, Title: check, Story: "why", Fix: []string{"do"}, OSMax: osMax}
	}
	rules := []engine.Rule{
		{ID: "first", Causes: []engine.Cause{cause("in-effect", ""), cause("older-macos-only", "14")}},
		{ID: "second", Causes: []engine.Cause{cause("typo", ""), cause("fine", "")}},
	}
	var asked []string
	s := &Scanner{Host: engine.Host{OS: "darwin", Version: "15.7"}, CauseQueries: map[string]CauseQuery{
		"in-effect": func(_ context.Context, r engine.Rule) (engine.Verdict, string) {
			asked = append(asked, r.ID)
			return engine.VerdictFlagged, "seen"
		},
		"older-macos-only": func(context.Context, engine.Rule) (engine.Verdict, string) {
			t.Error("a cause outside the host's version must not be checked")
			return engine.VerdictFlagged, ""
		},
		"fine": func(context.Context, engine.Rule) (engine.Verdict, string) { return engine.VerdictNormal, "set" },
	}}
	rows := s.Causes(context.Background(), rules)

	var got []string
	for _, row := range rows {
		got = append(got, fmt.Sprintf("%s/%s %s %s", row.RuleID, row.Cause.Check, row.Verdict, row.Detail))
	}
	want := []string{
		"first/in-effect flagged seen",
		`second/typo unknown unknown cause check "typo"`,
		"second/fine normal set",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(asked) != 1 || asked[0] != "first" {
		t.Errorf("the check was handed %v, want its own rule once", asked)
	}
}

// New hands out a scanner that can run every cause check the shipped
// catalog declares; without them doctor prints only unknown rows.
func TestNewWiresTheCauseChecks(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(engine.Host{OS: "darwin", Version: "15.7", Home: t.TempDir()})
	declared := 0
	for _, r := range catalog {
		for _, c := range r.Causes {
			declared++
			if s.CauseQueries[c.Check] == nil {
				t.Errorf("New's scanner has no check %q, declared by %s", c.Check, r.ID)
			}
		}
	}
	if declared == 0 {
		t.Fatal("the embedded catalog declares no causes")
	}
}

// The checks run at once: each of these waits until all three have
// started, which checks run one after another never reach.
func TestCauseChecksRunConcurrently(t *testing.T) {
	const n = 3
	var started sync.WaitGroup
	started.Add(n)
	all := make(chan struct{})
	go func() { started.Wait(); close(all) }()

	s := &Scanner{queryTimeout: 5 * time.Second, CauseQueries: map[string]CauseQuery{}}
	var rule engine.Rule
	rule.ID = "r"
	for i := range n {
		name := fmt.Sprintf("check-%d", i)
		rule.Causes = append(rule.Causes, engine.Cause{Check: name})
		s.CauseQueries[name] = func(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
			started.Done()
			select {
			case <-all:
				return engine.VerdictFlagged, name
			case <-ctx.Done():
				return engine.VerdictUnknown, "ran alone"
			}
		}
	}
	rows := s.Causes(context.Background(), []engine.Rule{rule})
	if len(rows) != n {
		t.Fatalf("got %d rows, want %d", len(rows), n)
	}
	for i, row := range rows {
		if want := fmt.Sprintf("check-%d", i); row.Verdict != engine.VerdictFlagged || row.Detail != want || row.Cause.Check != want {
			t.Errorf("row %d = %s %s (%s), want flagged %s in catalog order", i, row.Cause.Check, row.Verdict, row.Detail, want)
		}
	}
}

func TestCauseCheckDeadlineIsUnknown(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	s := &Scanner{queryTimeout: 50 * time.Millisecond, CauseQueries: map[string]CauseQuery{
		"stuck": func(context.Context, engine.Rule) (engine.Verdict, string) {
			<-stuck
			return engine.VerdictFlagged, "late"
		},
	}}
	rules := []engine.Rule{{ID: "r", Causes: []engine.Cause{{Check: "stuck"}}}}
	done := make(chan []engine.CauseCheck, 1)
	go func() { done <- s.Causes(context.Background(), rules) }()
	select {
	case rows := <-done:
		if len(rows) != 1 || rows[0].Verdict != engine.VerdictUnknown || rows[0].Detail != "the check gave no answer within 50ms" {
			t.Fatalf("rows = %+v, want one unknown row naming the check and the deadline, not a tool", rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Causes did not return past the query deadline")
	}
}

// The shipped rows against one fixture machine where every cause is in
// effect: each is flagged through its own rule, the aerial row is gone
// on a macOS version it was not seen on, and no check changed a byte.
func TestEmbeddedCausesOnAFixtureMachine(t *testing.T) {
	c := causeFixture(t)
	home, root := c.host.Home, c.host.Root

	video := filepath.Join(root, "Library/Application Support/com.apple.idleassetsd/Customer/4KSDR240FPS/a.mov")
	writeFile(t, video, 4096)
	at := causeNow.Add(-5 * time.Minute)
	if err := os.Chtimes(video, at, at); err != nil {
		t.Fatal(err)
	}
	plantRepo(t, filepath.Join(home, "workspace/org/svc"), "go.mod", 3)
	writeText(t, filepath.Join(home, "Library/Group Containers/group.com.docker/settings-store.json"), `{"AutoStart": false}`)
	writeText(t, filepath.Join(home, ".docker/daemon.json"), `{"builder": {"gc": {"enabled": false}}}`)

	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	before := treeState(t, root)
	s := &Scanner{Host: c.host, fs: c.w, CauseQueries: c.queries()}
	rows := s.Causes(context.Background(), catalog)
	if after := treeState(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("the checks changed the machine:\nbefore:\n%s\nafter:\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}

	want := map[string]string{
		"aerial-downloads":         "aerial-wallpapers",
		"docker-build-cache-limit": "docker-build-cache",
		"docker-vm-memory":         "docker-vm-disk",
		"go-worktrees-trimpath":    "go-build-cache",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for _, row := range rows {
		if want[row.Cause.Check] != row.RuleID || row.Verdict != engine.VerdictFlagged {
			t.Errorf("row %s on rule %s = %s (%s), want flagged on rule %s", row.Cause.Check, row.RuleID, row.Verdict, row.Detail, want[row.Cause.Check])
		}
	}

	s.Host.Version = "26.0"
	for _, row := range s.Causes(context.Background(), catalog) {
		if row.Cause.Check == "aerial-downloads" {
			t.Errorf("the aerial row names idleassetsd, seen on macOS 15 only; on 26 it must not print: %+v", row)
		}
	}
}

func TestAgoText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second: "under a minute ago",
		3 * time.Minute:  "3 min ago",
		5 * time.Hour:    "5 h ago",
		72 * time.Hour:   "3 d ago",
	} {
		if got := agoText(d); got != want {
			t.Errorf("agoText(%s) = %q, want %q", d, got, want)
		}
	}
}
