package scanner

// Every embedded rule owns its test data: the `fixture:` block in the
// rule YAML. The harness builds an isolated fake machine per rule,
// scans just that rule, plans, and snapshots the normalized command
// list in testdata/golden/<rule-id>.golden. Coverage is enforced, not
// hoped for: a missing fixture, a scan error, or zero matched items
// fails the rule's subtest — silent path rot turns red instead of
// quietly dropping lines from a shared snapshot. Refresh with:
//
//	go test ./internal/scanner -run Golden -update

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixtureHost builds the rule's isolated fake machine: a temp root
// (Host.Root re-anchors /Library, /Applications) containing a home,
// with every fixture file planted using the same path syntax rules use
// (~ = home, absolute = under root). Default host is darwin/15.5;
// fixture.host overrides for version-gated paths and future linux rules.
func fixtureHost(t *testing.T, r engine.Rule) engine.Host {
	t.Helper()
	root := t.TempDir()
	host := engine.Host{OS: "darwin", Version: "15.5", Home: filepath.Join(root, "Users/dev"), Root: root}
	if fh := r.Fixture.Host; fh != nil {
		if fh.OS != "" {
			host.OS = fh.OS
		}
		if fh.Version != "" {
			host.Version = fh.Version
		}
	}
	for _, p := range r.Fixture.Files {
		writeFile(t, host.ExpandPath(p), 1000)
	}
	return host
}

// fixedItems fakes the rule's tool query: golden runs must never exec
// docker or simctl, and results must be machine-independent.
func fixedItems(items ...engine.Item) ToolQuery {
	return func(context.Context) ([]engine.Item, error) { return items, nil }
}

func fakeQuery(host engine.Host, r engine.Rule) map[string]ToolQuery {
	items := make([]engine.Item, 0, len(r.Fixture.Items))
	for _, it := range r.Fixture.Items {
		item := engine.Item{Label: it.Label, Arg: it.Arg, Bytes: it.Bytes}
		if it.Path != "" {
			item.Path = host.ExpandPath(it.Path)
		}
		items = append(items, item)
	}
	return map[string]ToolQuery{r.ToolQuery: fixedItems(items...)}
}

func renderPlanForGolden(plan engine.Plan, host engine.Host) string {
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, host.Home, "~")
		return strings.ReplaceAll(s, host.Root, "")
	}
	var b strings.Builder
	for _, a := range plan.Actions {
		cmd := make([]string, len(a.Command))
		for i, tok := range a.Command {
			cmd[i] = normalize(tok)
		}
		pre := ""
		if a.PreAction != "" {
			pre = fmt.Sprintf("  (pre: %s)", a.PreAction)
		}
		fmt.Fprintf(&b, "[%s] %s: %s%s\n", a.Kind, a.RuleID, strings.Join(cmd, " "), pre)
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(&b, "[skip] %s: %s\n", s.RuleID, normalize(s.Reason))
	}
	return b.String()
}

func TestGoldenPerRule(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		t.Run(r.ID, func(t *testing.T) {
			if r.Fixture == nil {
				t.Fatal("rule has no fixture: block — every rule gets a golden test (CLAUDE.md)")
			}
			host := fixtureHost(t, r)
			s := New(host)
			s.Queries = nil // never exec real tools in tests
			if r.ToolQuery != "" {
				s.Queries = fakeQuery(host, r)
			}

			findings := s.Scan(context.Background(), []engine.Rule{r})
			f := findings[0]
			if f.Err != "" {
				t.Fatalf("scan error: %s", f.Err)
			}
			if len(f.Items) == 0 {
				t.Fatal("rule matched nothing in its own fixture — path rot or a stale fixture")
			}

			plan := engine.BuildPlan(host, findings, map[string]bool{r.ID: true})
			got := renderPlanForGolden(plan, host)
			if got == "" {
				t.Fatal("rule produced no plan lines")
			}

			goldenPath := filepath.Join("testdata", "golden", r.ID+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("plan drifted from golden.\n--- got ---\n%s--- want ---\n%s", got, want)
			}
		})
	}
}

// TestGoldenNoStrays fails when testdata/golden holds snapshots for
// rules that no longer exist — deleting a rule must delete its golden.
func TestGoldenNoStrays(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(catalog))
	for _, r := range catalog {
		known[r.ID+".golden"] = true
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("stray golden %s: no rule with that id in the embedded catalog", e.Name())
		}
	}
}
