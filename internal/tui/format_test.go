package tui

import (
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestActionLineStatesReversibility(t *testing.T) {
	tests := []struct {
		action engine.Action
		want   string
	}{
		{engine.Action{RuleID: "xcode-derived-data", Kind: engine.ActionTrash, Command: []string{"osascript"}}, "undo restores"},
		{engine.Action{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}}, "no undo"},
		{engine.Action{RuleID: "docker-volumes-named", Kind: engine.ActionNative, Command: []string{"docker", "volume", "rm", "v"},
			PreAction: engine.PreActionVolumeExport}, "no undo"},
	}
	for _, tt := range tests {
		if got := ActionLine(tt.action); !strings.Contains(got, tt.want) {
			t.Errorf("ActionLine(%s) = %q, want it to say %q", tt.action.RuleID, got, tt.want)
		}
	}
}

func TestActionLineNamesWhatGoesWithIt(t *testing.T) {
	a := engine.Action{RuleID: "git-worktrees", Kind: engine.ActionNative, Bytes: 3 << 30,
		Command: []string{"git", "-C", "/w/x", "worktree", "remove", "/w/x"}}
	if got := ActionLine(a); strings.Contains(got, "\n") {
		t.Fatalf("an action with nothing inside it takes one line, got %q", got)
	}
	a.Includes = []engine.Included{
		{ID: "node-modules-dirs//w/x/node_modules", Bytes: 2 << 30},
		{ID: "rust-target-dirs//w/x/target", Bytes: 512 << 20},
	}
	// Surfaces print "  " + ActionLine(a).
	lines := strings.Split("  "+ActionLine(a), "\n")
	want := []string{"node-modules-dirs//w/x/node_modules (2.0 GiB)", "rust-target-dirs//w/x/target (512.0 MiB)"}
	if len(lines) != 1+len(want) {
		t.Fatalf("want the action line plus one line per included item, got %q", lines)
	}
	ruleCol := strings.Index(lines[0], "git-worktrees")
	for i, w := range want {
		if !strings.Contains(lines[i+1], "also removes "+w) {
			t.Errorf("line %d = %q, want it to name %q", i+1, lines[i+1], w)
		}
		if col := strings.Index(lines[i+1], "also"); col != ruleCol {
			t.Errorf("line %d starts at column %d, want %d under the rule id:\n%s", i+1, col, ruleCol, strings.Join(lines, "\n"))
		}
	}
}

func TestTotalsLinesSplitByWhenSpaceReturns(t *testing.T) {
	plan := engine.Plan{Actions: []engine.Action{
		{Kind: engine.ActionNative, Bytes: 3 << 30},
		{Kind: engine.ActionTrash, Bytes: 1 << 30},
	}}
	lines := TotalsLines(plan)
	if len(lines) != 2 {
		t.Fatalf("want two subtotal lines, got %q", lines)
	}
	if !strings.HasPrefix(lines[0], "Frees now") || !strings.Contains(lines[0], "3.0 GiB") {
		t.Errorf("frees-now line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Frees after Trash") || !strings.Contains(lines[1], "1.0 GiB") {
		t.Errorf("after-trash line = %q", lines[1])
	}
}
