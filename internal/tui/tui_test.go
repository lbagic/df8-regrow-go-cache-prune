package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lbagic/regrow/internal/engine"
)

// fixtureFindings covers every row shape the checklist renders: safe
// (auto-selected), caution, surface-only, scan error, and an empty
// finding that must be filtered out.
func fixtureFindings(home string) []engine.Finding {
	return []engine.Finding{
		{
			Rule: engine.Rule{
				ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches",
				Risk: engine.RiskSafe, NativeCommand: engine.Argv{"go", "clean", "-cache"},
				Regen: engine.Regen{Story: "Repopulated by the next go build.", Cost: "next builds slower once"},
			},
			Items: []engine.Item{{Path: home + "/Library/Caches/go-build", Bytes: 20 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "go-mod-cache", Title: "Go module cache", Category: "dev-caches",
				Risk: engine.RiskCaution, NativeCommand: engine.Argv{"go", "clean", "-modcache"},
				Regen: engine.Regen{Story: "Modules re-download on demand.", Cost: "network"},
			},
			Items: []engine.Item{{Path: home + "/go/pkg/mod", Bytes: 17 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "ios-backups", Title: "iOS device backups", Category: "surface",
				Risk:  engine.RiskSurfaceOnly,
				Regen: engine.Regen{Story: "Finder ▸ Manage Backups.", Cost: "n/a"},
			},
			Items: []engine.Item{{Path: home + "/Library/Application Support/MobileSync/Backup", Bytes: 23 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "docker-cache", Title: "Docker build cache", Category: "dev-caches",
				Risk: engine.RiskSafe, NativeCommand: engine.Argv{"docker", "builder", "prune", "-f"},
			},
			Err: "docker: command not found",
		},
		{
			Rule: engine.Rule{
				ID: "empty-rule", Title: "Nothing here", Category: "dev-caches",
				Risk: engine.RiskSafe,
			},
		},
		{
			Rule: engine.Rule{
				ID: "sim-devices", Title: "iOS simulator devices", Category: "xcode",
				Risk: engine.RiskCaution, ToolQuery: "simctl-devices",
				NativeCommand: engine.Argv{"xcrun", "simctl", "delete", "{arg}"},
				Regen:         engine.Regen{Story: "Recreate via simctl create.", Cost: "sim contents gone"},
			},
			Items: []engine.Item{
				{Label: "iPhone 15 (iOS 17.0)", Arg: "AAA-111", Bytes: 4 << 30},
				{Label: "iPhone 16 (iOS 18.0)", Arg: "CCC-333", Bytes: 3 << 30},
			},
		},
	}
}

// newTestModel builds a model and feeds it the fixture scan result.
func newTestModel(t *testing.T) Model {
	t.Helper()
	host := engine.Host{OS: "darwin", Version: "15.5", Home: "/Users/fixture"}
	m := New(host, "test", func(context.Context) []engine.Finding {
		return fixtureFindings(host.Home)
	})
	next, _ := m.Update(scanDoneMsg{findings: fixtureFindings(host.Home)})
	return next.(Model)
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	if s == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(key(k))
		m = next.(Model)
	}
	return m
}

func TestListGroupsAndRanks(t *testing.T) {
	view := newTestModel(t).View()

	// Categories size-ranked: dev-caches (37 GiB) before surface (23 GiB).
	dev := strings.Index(view, "DEV CACHES")
	surface := strings.Index(view, "SURFACE")
	if dev == -1 || surface == -1 || dev > surface {
		t.Fatalf("want DEV CACHES header before SURFACE, got:\n%s", view)
	}
	// Inside dev-caches, larger finding first.
	build := strings.Index(view, "Go build cache")
	mod := strings.Index(view, "Go module cache")
	if build == -1 || mod == -1 || build > mod {
		t.Fatalf("want Go build cache ranked above Go module cache, got:\n%s", view)
	}
	// Empty findings never render.
	if strings.Contains(view, "Nothing here") {
		t.Fatalf("empty finding should be filtered, got:\n%s", view)
	}
	// Scan errors surface on the row.
	if !strings.Contains(view, "docker: command not found") {
		t.Fatalf("scan error missing, got:\n%s", view)
	}
}

// Selection state is item atoms ("ruleID/key"); the rule checkbox is
// derived. These are the atoms the fixture findings produce.
const (
	atomGoBuild = "go-build-cache/~/Library/Caches/go-build"
	atomGoMod   = "go-mod-cache/~/go/pkg/mod"
)

func TestDefaultSelectionAndToggle(t *testing.T) {
	m := newTestModel(t)

	if !m.selected[atomGoBuild] {
		t.Fatal("safe finding's items should start selected")
	}
	if m.selected[atomGoMod] {
		t.Fatal("caution finding must not start selected")
	}
	if len(m.selected) != 1 {
		t.Fatalf("only the safe item should be selected, got %v", m.selected)
	}

	// Cursor starts on the first finding; move to the caution row, toggle.
	m = press(t, m, "j", " ")
	if !m.selected[atomGoMod] {
		t.Fatal("space should select the caution finding under the cursor")
	}
	m = press(t, m, " ")
	if m.selected[atomGoMod] {
		t.Fatal("space should toggle selection off")
	}
}

func TestUnreadableItemsStartUnticked(t *testing.T) {
	host := engine.Host{OS: "darwin", Version: "15.5", Home: "/Users/fixture"}
	findings := []engine.Finding{{
		Rule: engine.Rule{ID: "app-logs", Title: "App logs", Category: "macos", Risk: engine.RiskSafe},
		Items: []engine.Item{
			{Path: host.Home + "/logs/a", Key: "~/logs/a", Bytes: 1 << 20},
			{Path: host.Home + "/logs/b", Key: "~/logs/b", Partial: true},
		},
	}}
	m := New(host, "test", func(context.Context) []engine.Finding { return findings })
	next, _ := m.Update(scanDoneMsg{findings: findings})
	got := next.(Model).selected
	if !got["app-logs/~/logs/a"] || got["app-logs/~/logs/b"] || len(got) != 1 {
		t.Fatalf("pre-ticked %v, want the read item only", got)
	}
}

func TestSurfaceOnlyNotToggleable(t *testing.T) {
	m := newTestModel(t)
	// Walk to the surface row: build → mod → docker(err) → ios-backups.
	m = press(t, m, "j", "j", "j", " ")
	before := len(m.selected)
	if f := m.currentFinding(); f == nil || f.Rule.ID != "ios-backups" {
		t.Fatalf("cursor not on the surface row, at %+v", m.currentFinding())
	}
	if len(m.selected) != before {
		t.Fatal("surface-only must never be selectable")
	}
	view := m.View()
	if !strings.Contains(view, "regrow never deletes this") {
		t.Fatalf("cursor note should explain surface-only, got:\n%s", view)
	}
}

func TestExpandCollapseAndItemToggle(t *testing.T) {
	m := newTestModel(t)
	// Collapsed by default: no item labels on screen.
	if strings.Contains(m.View(), "iPhone 15") {
		t.Fatalf("items must be collapsed by default, got:\n%s", m.View())
	}

	// Walk to sim-devices (last finding) and expand.
	m = press(t, m, "j", "j", "j", "j", "l")
	view := m.View()
	if !strings.Contains(view, "iPhone 15 (iOS 17.0)") || !strings.Contains(view, "iPhone 16 (iOS 18.0)") {
		t.Fatalf("expand should reveal item rows, got:\n%s", view)
	}

	// First item row (largest first), toggle it.
	m = press(t, m, "j")
	if it := m.currentItem(); it == nil || it.Arg != "AAA-111" {
		t.Fatalf("cursor should sit on the biggest item, got %+v", m.currentItem())
	}
	// Footer shows the copy-pasteable id.
	if !strings.Contains(m.View(), "id: sim-devices/AAA-111") {
		t.Fatalf("item footer must show the item id, got:\n%s", m.View())
	}
	m = press(t, m, " ")
	if !m.selected["sim-devices/AAA-111"] {
		t.Fatal("space on an item row should select that item")
	}
	if m.selected["sim-devices/CCC-333"] {
		t.Fatal("sibling item must stay unselected")
	}
	// Partial selection renders [~] on the rule row.
	if !strings.Contains(m.View(), "[~]") {
		t.Fatalf("partial selection should render [~], got:\n%s", m.View())
	}

	// Plan carries exactly the selected item.
	planned := press(t, m, "enter")
	view = planned.View()
	if !strings.Contains(view, "xcrun simctl delete AAA-111") {
		t.Fatalf("plan must include the selected item's command, got:\n%s", view)
	}
	if strings.Contains(view, "CCC-333") {
		t.Fatalf("unselected item must not be planned, got:\n%s", view)
	}

	// Collapse from the item row: rows fold, cursor lands on the rule.
	m = press(t, m, "h")
	if strings.Contains(m.View(), "iPhone 16") {
		t.Fatalf("collapse should hide item rows, got:\n%s", m.View())
	}
	if f := m.currentFinding(); f == nil || f.Rule.ID != "sim-devices" || m.currentItem() != nil {
		t.Fatal("collapse should park the cursor on the rule row")
	}
}

func TestRuleToggleCascadesToItems(t *testing.T) {
	m := newTestModel(t)
	// sim-devices: select the whole rule from the collapsed row.
	m = press(t, m, "j", "j", "j", "j", " ")
	if !m.selected["sim-devices/AAA-111"] || !m.selected["sim-devices/CCC-333"] {
		t.Fatalf("rule toggle should select every item, got %v", m.selected)
	}
	// From partial, space selects all; from all, clears all.
	m = press(t, m, "l", "j", " ") // expand, drop first item → partial
	if m.selected["sim-devices/AAA-111"] {
		t.Fatal("item toggle should deselect")
	}
	m = press(t, m, "k", " ") // back on rule row: partial → all
	if !m.selected["sim-devices/AAA-111"] || !m.selected["sim-devices/CCC-333"] {
		t.Fatalf("rule toggle from partial should select all, got %v", m.selected)
	}
	m = press(t, m, " ") // all → none
	if m.selected["sim-devices/AAA-111"] || m.selected["sim-devices/CCC-333"] {
		t.Fatalf("rule toggle from all should clear, got %v", m.selected)
	}
}

func TestWholeRuleCommandItemsAreViewOnly(t *testing.T) {
	m := newTestModel(t)
	// go-build-cache runs `go clean -cache` — items expand for
	// inspection but cannot be individually toggled.
	m = press(t, m, "l", "j", " ")
	if it := m.currentItem(); it == nil {
		t.Fatal("cursor should be on the expanded item row")
	}
	if !m.selected[atomGoBuild] {
		t.Fatal("space on a view-only item row must not change selection")
	}
	if !strings.Contains(m.View(), "rule acts as a whole") {
		t.Fatalf("footer should explain why the item is view-only, got:\n%s", m.View())
	}
}

func TestPlanScreen(t *testing.T) {
	m := press(t, newTestModel(t), "enter")
	view := m.View()

	if !strings.Contains(view, "nothing executed") {
		t.Fatalf("plan screen must state dry-run, got:\n%s", view)
	}
	if !strings.Contains(view, "go clean -cache") {
		t.Fatalf("plan must show the exact command, got:\n%s", view)
	}
	if strings.Contains(view, "go clean -modcache") {
		t.Fatalf("unselected caution rule must not be planned, got:\n%s", view)
	}
	if !strings.Contains(view, "Would reclaim: 20.0 GiB") {
		t.Fatalf("plan total wrong, got:\n%s", view)
	}
	for _, want := range [][2]string{
		{"[native] go-build-cache", "no undo"},
		{"Frees now", "20.0 GiB"},
		{"Frees after Trash", " 0 B"},
	} {
		if !lineHas(view, want[0], want[1]) {
			t.Fatalf("plan screen needs a line with %q and %q, got:\n%s", want[0], want[1], view)
		}
	}
	if strings.Contains(view, "Trash first") {
		t.Fatalf("a native-only plan must not claim Trash-first undo, got:\n%s", view)
	}

	// esc returns to the checklist.
	m = press(t, m, "esc")
	if m.state != stateList {
		t.Fatalf("esc should return to list, state=%d", m.state)
	}
}

func TestExecuteConfirmFlow(t *testing.T) {
	m := press(t, newTestModel(t), "enter", "x")
	if m.state != stateConfirm {
		t.Fatalf("x on the plan screen should open the confirm screen, state=%d", m.state)
	}
	view := m.View()
	if !strings.Contains(view, "CONFIRM") || !strings.Contains(view, "go clean -cache") {
		t.Fatalf("confirm screen must restate the actions, got:\n%s", view)
	}

	// esc backs out without confirming; so does n.
	if out := press(t, m, "esc"); out.state != statePlan || out.confirmed {
		t.Fatalf("esc must return to plan unconfirmed, state=%d confirmed=%v", out.state, out.confirmed)
	}
	if out := press(t, m, "n"); out.state != statePlan || out.confirmed {
		t.Fatalf("n must return to plan unconfirmed, state=%d confirmed=%v", out.state, out.confirmed)
	}

	// enter is inert on the confirm screen: plan-screen muscle memory
	// must not execute.
	if out := press(t, m, "enter"); out.state != stateConfirm || out.confirmed {
		t.Fatal("enter must not confirm execution")
	}

	// y confirms and quits; the caller reads plan+confirmed off the model.
	next, cmd := m.Update(key("y"))
	m = next.(Model)
	if !m.confirmed {
		t.Fatal("y should mark the plan confirmed")
	}
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("y should quit so the caller can execute")
	}
}

func TestExecuteNeedsActions(t *testing.T) {
	m := newTestModel(t)
	m.selected = map[string]bool{} // nothing selected → empty plan
	m = press(t, m, "enter", "x")
	if m.state != statePlan {
		t.Fatalf("x must be inert on an empty plan, state=%d", m.state)
	}
	// y outside the confirm screen must never confirm.
	if out := press(t, m, "y"); out.confirmed {
		t.Fatal("y outside the confirm screen must not confirm")
	}
}

func TestSurfaceOnlySkippedInPlan(t *testing.T) {
	// Select everything selectable, then plan: surface-only must show
	// as a skip, never an action (invariant 5 lives in the planner).
	m := newTestModel(t)
	m.selected["ios-backups"] = true // simulate a bug upstream: planner still refuses
	m = press(t, m, "enter")
	view := m.View()
	if !strings.Contains(view, "surface-only: report, never delete") {
		t.Fatalf("plan must skip surface-only with reason, got:\n%s", view)
	}
	if strings.Contains(view, "MobileSync") {
		t.Fatalf("surface-only path must never appear as an action, got:\n%s", view)
	}
}

func TestCursorNoteShowsRegen(t *testing.T) {
	view := newTestModel(t).View()
	if !strings.Contains(view, "regen: Repopulated by the next go build.") {
		t.Fatalf("cursor note should show regen story, got:\n%s", view)
	}
	if !strings.Contains(view, "cost: next builds slower once") {
		t.Fatalf("cursor note should show regen cost, got:\n%s", view)
	}
}

func TestWindowFollowsCursor(t *testing.T) {
	m := newTestModel(t)
	// Height 8 leaves a 2-line body (6 lines of chrome): the list must
	// scroll so the cursor row stays visible.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m = next.(Model)
	m = press(t, m, "j", "j", "j")
	view := m.View()
	if !strings.Contains(view, "iOS device backups") {
		t.Fatalf("cursor row must stay visible in a short window, got:\n%s", view)
	}
	if strings.Contains(view, "Go build cache") {
		t.Fatalf("rows above the window must scroll off, got:\n%s", view)
	}
}

func TestQuitKeys(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should quit")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("q should produce tea.Quit, got %#v", msg)
	}
}

func TestScanningState(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: "/Users/fixture"}
	m := New(host, "test", func(context.Context) []engine.Finding { return nil })
	if !strings.Contains(m.View(), "scanning") {
		t.Fatalf("initial view should show scanning, got:\n%s", m.View())
	}
	next, _ := m.Update(scanDoneMsg{findings: nil})
	if view := next.(Model).View(); !strings.Contains(view, "Nothing found") {
		t.Fatalf("empty scan should say nothing found, got:\n%s", view)
	}
}

// lineHas reports whether one line of view contains both fragments.
func lineHas(view, a, b string) bool {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}

// nestedFindings puts a steward-command rule inside a Trash rule,
// reads one folder in part and another not at all.
func nestedFindings(home string) []engine.Finding {
	return []engine.Finding{
		{
			Rule:  engine.Rule{ID: "app-caches", Title: "App caches", Category: "macos", Risk: engine.RiskCaution},
			Items: []engine.Item{{Path: home + "/Library/Caches", Bytes: 70 << 30, Partial: true}},
		},
		{
			Rule: engine.Rule{ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches",
				Risk: engine.RiskSafe, NativeCommand: engine.Argv{"go", "clean", "-cache"}},
			Items: []engine.Item{{Path: home + "/Library/Caches/go-build", Bytes: 55 << 30}},
		},
		{
			Rule:  engine.Rule{ID: "gated-cache", Title: "Gated cache", Category: "macos", Risk: engine.RiskCaution},
			Items: []engine.Item{{Path: home + "/Library/Containers/gated", Partial: true}},
		},
	}
}

func nestedModel(t *testing.T) Model {
	t.Helper()
	host := engine.Host{OS: "darwin", Version: "15.5", Home: "/Users/fixture"}
	m := New(host, "test", nil)
	next, _ := m.Update(scanDoneMsg{findings: nestedFindings(host.Home)})
	return next.(Model)
}

func TestListShowsBucketsOnceAndPartialSizes(t *testing.T) {
	view := nestedModel(t).View()
	for _, want := range []string{
		// go-build's 55 GiB count under its steward command only.
		"frees now 55.0 GiB · after Trash 15.0 GiB",
		"shown only 0 B · macOS-managed 0 B · 2 unreadable in part",
		"≥ 70.0 GiB",
		"unreadable",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("list is missing %q:\n%s", want, view)
		}
	}
	// Category headers carry no size: a category mixes buckets.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "MACOS") && strings.Contains(line, "GiB") {
			t.Errorf("category header must not carry a total: %q", line)
		}
	}
}

func TestSelectedEstimateCountsNestedSelectionOnce(t *testing.T) {
	m := nestedModel(t)
	// go-build-cache is pre-selected (safe); add the container rule.
	for i, r := range m.rows {
		if r.kind == rowFinding && m.findings[r.finding].Rule.ID == "app-caches" {
			m.cursor = i
		}
	}
	m = press(t, m, " ")
	view := m.View()
	// go-build is skipped as "inside app-caches": both selected, the
	// outer size counted once, and the skip counted with it.
	if !strings.Contains(view, "selected 2 · plan ~70.0 GiB · 1 skipped") {
		t.Fatalf("selecting a folder and a folder inside it must estimate the outer size once, got:\n%s", view)
	}

	// The summary follows the selection: untick the container again.
	m = press(t, m, " ")
	if view := m.View(); !strings.Contains(view, "selected 1 · plan ~55.0 GiB   ") {
		t.Fatalf("after unticking the container, got:\n%s", view)
	}
}

func TestCursorNoteExplainsPartialSizes(t *testing.T) {
	m := nestedModel(t)
	for i, r := range m.rows {
		if r.kind == rowFinding && m.findings[r.finding].Rule.ID == "gated-cache" {
			m.cursor = i
		}
	}
	m.width = 200
	if view := m.View(); !strings.Contains(view, "unreadable: "+engine.UnreadableNote) {
		t.Fatalf("footer must explain an unreadable row, got:\n%s", view)
	}
}
