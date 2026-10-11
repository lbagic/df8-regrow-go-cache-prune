package scanner

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func TestScanStaticPaths(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Library", "Caches", "go-build", "obj.o"), 10_000)

	host := engine.Host{OS: "darwin", Version: "15.5", Home: home}
	rules := []engine.Rule{
		{
			ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths: map[string][]engine.PathEntry{"darwin": {{Path: "~/Library/Caches/go-build"}}},
		},
		{
			ID: "absent-rule", Title: "Absent", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths: map[string][]engine.PathEntry{"darwin": {{Path: "~/Library/Caches/never-there"}}},
		},
	}
	findings := New(host).Scan(context.Background(), rules)
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(findings))
	}
	if findings[0].Rule.ID != "go-build-cache" || len(findings[0].Items) != 1 {
		t.Fatalf("first finding wrong: %+v", findings[0])
	}
	if findings[0].Items[0].Bytes < 10_000 {
		t.Errorf("measured %d bytes, want >= 10000", findings[0].Items[0].Bytes)
	}
	if findings[0].Items[0].LastUsed.IsZero() {
		t.Error("LastUsed not set")
	}
	if findings[0].Items[0].Key != "~/Library/Caches/go-build" {
		t.Errorf("path item key = %q, want tilde-abbreviated path", findings[0].Items[0].Key)
	}
	if len(findings[1].Items) != 0 || findings[1].Err != "" {
		t.Errorf("absent path must yield empty finding without error: %+v", findings[1])
	}
}

func TestScanToolQuery(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: t.TempDir()}
	rule := engine.Rule{
		ID: "fake-tool", Title: "Fake", Category: "containers", Risk: engine.RiskSafe,
		ToolQuery: "fake", NativeCommand: engine.Argv{"fake", "rm", "{arg}"},
	}

	s := New(host)
	s.Queries["fake"] = func(ctx context.Context) ([]engine.Item, error) {
		return []engine.Item{{Label: "image:latest", Arg: "image:latest", Bytes: 500}}, nil
	}
	findings := s.Scan(context.Background(), []engine.Rule{rule})
	if len(findings[0].Items) != 1 || findings[0].Items[0].Bytes != 500 {
		t.Fatalf("tool items wrong: %+v", findings[0])
	}
	if findings[0].Items[0].Key != "image:latest" {
		t.Errorf("tool item key = %q, want the arg", findings[0].Items[0].Key)
	}

	s.Queries["fake"] = func(ctx context.Context) ([]engine.Item, error) {
		return nil, errors.New("docker daemon not running")
	}
	findings = s.Scan(context.Background(), []engine.Rule{rule})
	if findings[0].Err == "" || !strings.Contains(findings[0].Err, "daemon") {
		t.Fatalf("tool error not surfaced: %+v", findings[0])
	}
}

func TestScanUnknownToolQuery(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: t.TempDir()}
	rule := engine.Rule{
		ID: "bad-tool", Title: "Bad", Category: "x", Risk: engine.RiskSafe, ToolQuery: "nope",
	}
	findings := New(host).Scan(context.Background(), []engine.Rule{rule})
	if !strings.Contains(findings[0].Err, `unknown tool query "nope"`) {
		t.Fatalf("want unknown-query error, got %+v", findings[0])
	}
}

func TestScanStreamEmitsEveryRuleOnce(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "a", "f"), 100)
	writeFile(t, filepath.Join(home, "b", "f"), 100)
	rules := []engine.Rule{pathRule("a", "~/a"), pathRule("b", "~/b"), pathRule("absent", "~/nope")}

	seen := map[int]string{}
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: testWalker()}
	s.ScanStream(context.Background(), rules, func(i int, f engine.Finding, took time.Duration) {
		if _, dup := seen[i]; dup {
			t.Errorf("rule %d emitted twice", i)
		}
		if took < 0 {
			t.Errorf("rule %d took %v", i, took)
		}
		seen[i] = f.Rule.ID
	})
	if want := map[int]string{0: "a", 1: "b", 2: "absent"}; !maps.Equal(seen, want) {
		t.Fatalf("emitted %v, want %v", seen, want)
	}
}

// A root the scan may not read is an item with Partial set, never a
// rule error: the rule found its target, it just could not size it.
func TestScanUnreadableRootIsAPartialItem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 folders")
	}
	home := t.TempDir()
	locked := filepath.Join(home, "locked")
	writeFile(t, filepath.Join(locked, "hidden.bin"), 50_000)
	chmod(t, locked, 0)

	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: testWalker()}
	f := s.Scan(context.Background(), []engine.Rule{pathRule("locked", "~/locked")})[0]
	if f.Err != "" {
		t.Fatalf("unreadable root must not be a rule error, got %q", f.Err)
	}
	want := engine.Item{Path: locked, Key: "~/locked", Partial: true}
	if len(f.Items) != 1 || f.Items[0].Bytes != 0 || !f.Items[0].Partial || f.Items[0].Path != want.Path || f.Items[0].Key != want.Key {
		t.Fatalf("items = %+v, want one unreadable item %+v", f.Items, want)
	}
}

// Two actionable rules, one inside the other, measured from real
// folders: the ledger counts the inner folder once, in its own bucket.
func TestScanLedgerCountsNestedRulesOnce(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Caches", "app", "data.bin"), 300_000)
	writeFile(t, filepath.Join(home, "Caches", "go-build", "obj.o"), 700_000)

	outer := pathRule("caches", "~/Caches")
	inner := pathRule("go-build", "~/Caches/go-build")
	inner.NativeCommand = engine.Argv{"go", "clean", "-cache"}
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: testWalker()}
	findings := s.Scan(context.Background(), []engine.Rule{outer, inner})

	whole, nested := findings[0].Items[0].Bytes, findings[1].Items[0].Bytes
	if nested < 700_000 || whole < nested+300_000 {
		t.Fatalf("measured outer %d, inner %d: outer must contain inner", whole, nested)
	}
	led := engine.Account(findings)
	want := engine.Totals{FreesNow: nested, AfterTrash: whole - nested}
	if led.Totals != want {
		t.Fatalf("totals = %+v, want %+v (inner folder counted once)", led.Totals, want)
	}
	if got := led.Exclusive["caches/~/Caches"]; got != whole-nested {
		t.Errorf("outer exclusive = %d, want %d", got, whole-nested)
	}
}

// A cancelled scan's findings are incomplete and must not look clean.
func TestScanCancelledMarksFindings(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "a", "f"), 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: testWalker()}
	f := s.Scan(ctx, []engine.Rule{pathRule("a", "~/a")})[0]
	if !strings.Contains(f.Err, context.Canceled.Error()) {
		t.Fatalf("cancelled finding Err = %q, want the context error", f.Err)
	}
	if len(engine.DefaultSelection([]engine.Finding{f})) != 0 {
		t.Fatal("a cancelled finding must stay out of the default selection")
	}
}

// A tool query that never answers ends at the query deadline with the
// deadline in Finding.Err, whether it honours its ctx or ignores it
// (parked on a lock another query holds).
func TestToolQueryDeadline(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	queries := map[string]ToolQuery{
		"honours-ctx": func(ctx context.Context) ([]engine.Item, error) {
			<-ctx.Done()
			return nil, nil // a killed CLI often reads as "not available"
		},
		"ignores-ctx": func(context.Context) ([]engine.Item, error) {
			<-stuck
			return []engine.Item{{Label: "late", Bytes: 1}}, nil
		},
	}
	for name := range queries {
		t.Run(name, func(t *testing.T) {
			s := &Scanner{Host: engine.Host{OS: "darwin", Home: t.TempDir()}, Queries: queries, fs: testWalker(), queryTimeout: 50 * time.Millisecond}
			rule := engine.Rule{ID: "tool", Title: "tool", Category: "test", Risk: engine.RiskSafe, ToolQuery: name, NativeCommand: engine.Argv{"tool", "rm", "{arg}"}}
			done := make(chan engine.Finding, 1)
			go func() { done <- s.Scan(context.Background(), []engine.Rule{rule})[0] }()
			select {
			case f := <-done:
				if !strings.Contains(f.Err, "tool gave no answer within 50ms") || len(f.Items) != 0 {
					t.Fatalf("finding = %+v, want the deadline in Err and no items", f)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("scan did not return past the query deadline")
			}
		})
	}
}

func TestToolQueryAnswerInTimeKeepsItems(t *testing.T) {
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: t.TempDir()}, fs: testWalker(), queryTimeout: time.Second,
		Queries: map[string]ToolQuery{"q": fixedItems(engine.Item{Label: "img", Arg: "img", Bytes: 7})}}
	rule := engine.Rule{ID: "tool", Title: "tool", Category: "test", Risk: engine.RiskSafe, ToolQuery: "q", NativeCommand: engine.Argv{"tool", "rm", "{arg}"}}
	f := s.Scan(context.Background(), []engine.Rule{rule})[0]
	if f.Err != "" || len(f.Items) != 1 || f.Items[0].Bytes != 7 {
		t.Fatalf("finding = %+v, want the query's item", f)
	}
}

// A path under a regular file (ENOTDIR) is absent, not unreadable: no
// item, so nothing to plan.
func TestScanPathUnderAFileIsAbsent(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "file"), 10)
	s := &Scanner{Host: engine.Host{OS: "darwin", Home: home}, fs: testWalker()}
	f := s.Scan(context.Background(), []engine.Rule{pathRule("under-file", "~/file/cache", "~/file/*.bin")})[0]
	if f.Err != "" || len(f.Items) != 0 {
		t.Fatalf("finding = %+v, want no items and no error", f)
	}
}
