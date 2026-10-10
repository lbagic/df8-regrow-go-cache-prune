package engine

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var testHost = Host{OS: "darwin", Version: "15.5", Home: "/Users/t"}

// selectRules selects every given finding's rule as a whole.
func selectRules(findings ...Finding) map[string]bool {
	sel := map[string]bool{}
	for _, f := range findings {
		sel[f.Rule.ID] = true
	}
	return sel
}

func TestBuildPlanNativeWholeRule(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
		Items: []Item{
			{Path: "/Users/t/Library/Caches/go-build", Bytes: 100},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 {
		t.Fatalf("want 1 action, got %+v", plan.Actions)
	}
	a := plan.Actions[0]
	if !reflect.DeepEqual(a.Command, []string{"go", "clean", "-cache"}) || a.Kind != ActionNative || a.Bytes != 100 {
		t.Errorf("action wrong: %+v", a)
	}
}

func TestBuildPlanNativePerItemPlaceholders(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-runtimes", Risk: RiskCaution, NativeCommand: Argv{"xcrun", "simctl", "runtime", "delete", "{arg}"}, Sudo: true},
		Items: []Item{
			{Label: "iOS 17.5", Arg: "8A2C", Bytes: 10},
			{Label: "iOS 18.0", Arg: "9B3D", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 2 {
		t.Fatalf("want 2 actions, got %+v", plan.Actions)
	}
	want := []string{"sudo", "xcrun", "simctl", "runtime", "delete", "8A2C"}
	if !reflect.DeepEqual(plan.Actions[0].Command, want) {
		t.Errorf("command = %v, want %v", plan.Actions[0].Command, want)
	}
}

func TestBuildPlanCarriesPreAction(t *testing.T) {
	f := Finding{
		Rule: Rule{
			ID: "docker-volumes-named", Risk: RiskCaution,
			NativeCommand: Argv{"docker", "volume", "rm", "{arg}"},
			PreAction:     PreActionVolumeExport,
		},
		Items: []Item{{Label: "dune_db", Arg: "dune_db", Bytes: 10}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].PreAction != PreActionVolumeExport {
		t.Fatalf("action must carry the rule's pre-action, got %+v", plan.Actions)
	}
}

func TestBuildPlanTrashCarriesPreAction(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "agent-scratch", Risk: RiskCaution, PreAction: PreActionAgentScratchRecheck},
		Items: []Item{{Path: "/private/tmp/claude-1000/-Users-t-proj/11111111-1111-4111-8111-111111111111", Bytes: 10}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionTrash || plan.Actions[0].PreAction != PreActionAgentScratchRecheck {
		t.Fatalf("a Trash move must carry the rule's pre-action, got %+v", plan.Actions)
	}
}

func TestBuildPlanTrashFallback(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "xcode-derived-data", Risk: RiskSafe},
		Items: []Item{
			{Path: "/Users/t/Library/Developer/Xcode/DerivedData", Bytes: 42},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 {
		t.Fatalf("want 1 action, got %+v", plan.Actions)
	}
	a := plan.Actions[0]
	if a.Kind != ActionTrash || a.Command[0] != "osascript" {
		t.Errorf("trash action wrong: %+v", a)
	}
	if !strings.Contains(a.Command[2], "/Users/t/Library/Developer/Xcode/DerivedData") {
		t.Errorf("command misses path: %v", a.Command)
	}
}

func TestBuildPlanSurfaceOnlyNeverActs(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "ios-backups", Risk: RiskSurfaceOnly},
		Items: []Item{{Path: "/Users/t/Library/Application Support/MobileSync/Backup/x", Bytes: 1 << 30}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 {
		t.Fatalf("surface-only produced actions: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "surface-only") {
		t.Fatalf("want surface-only skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanGuardRejectsDangerousPaths(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "broken-rule", Risk: RiskSafe},
		Items: []Item{{Path: "/Users/t", Bytes: 1}}, // home itself
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 {
		t.Fatalf("guard let home through: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "path guard") {
		t.Fatalf("want guard skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanSelection(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "wanted", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/a/b", Bytes: 1}}},
		{Rule: Rule{ID: "unwanted", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/c/d", Bytes: 2}}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{"wanted": true})
	if len(plan.Actions) != 1 || plan.Actions[0].RuleID != "wanted" {
		t.Fatalf("selection ignored: %+v", plan.Actions)
	}
}

func TestBuildPlanPathlessItemWithoutNativeCommand(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "odd", Risk: RiskSafe},
		Items: []Item{{Label: "ghost", Bytes: 5}},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("pathless item mishandled: %+v", plan)
	}
}

func TestPlanTotalBytes(t *testing.T) {
	p := Plan{Actions: []Action{{Bytes: 3}, {Bytes: 4}}}
	if p.TotalBytes() != 7 {
		t.Fatalf("TotalBytes = %d", p.TotalBytes())
	}
}

func TestBuildPlanItemAtomSubset(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-devices", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "delete", "{arg}"}},
		Items: []Item{
			{Label: "iPhone 15", Arg: "AAA-111", Key: "AAA-111", Bytes: 10},
			{Label: "iPhone 16", Arg: "CCC-333", Key: "CCC-333", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"sim-devices/CCC-333": true})
	if len(plan.Actions) != 1 || plan.Actions[0].ItemKey != "CCC-333" {
		t.Fatalf("item atom should plan exactly one item, got %+v", plan.Actions)
	}
	if plan.Actions[0].Command[len(plan.Actions[0].Command)-1] != "CCC-333" {
		t.Errorf("command targets wrong item: %v", plan.Actions[0].Command)
	}
	if len(plan.Unmatched) != 0 {
		t.Errorf("matched atom reported unmatched: %v", plan.Unmatched)
	}
}

func TestBuildPlanItemAtomDerivesMissingKeys(t *testing.T) {
	// Hand-built findings without keys must still match item atoms.
	f := Finding{
		Rule:  Rule{ID: "xcode-archives", Risk: RiskCaution},
		Items: []Item{{Path: "/Users/t/Library/Archives/A.xcarchive", Bytes: 1}, {Path: "/Users/t/Library/Archives/B.xcarchive", Bytes: 2}},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"xcode-archives/~/Library/Archives/B.xcarchive": true})
	if len(plan.Actions) != 1 || plan.Actions[0].Path != "/Users/t/Library/Archives/B.xcarchive" {
		t.Fatalf("tilde-key atom should match, got %+v", plan.Actions)
	}
	if plan.Actions[0].ItemKey != "~/Library/Archives/B.xcarchive" {
		t.Errorf("action must carry the item key, got %q", plan.Actions[0].ItemKey)
	}
}

func TestBuildPlanWholeRuleCommandRefusesPartialSelection(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
		Items: []Item{
			{Path: "/Users/t/Library/Caches/go-build", Key: "~/Library/Caches/go-build", Bytes: 100},
			{Path: "/Users/t/other/go-build", Key: "~/other/go-build", Bytes: 50},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"go-build-cache/~/other/go-build": true})
	if len(plan.Actions) != 0 {
		t.Fatalf("partial selection on a whole-rule command must never act: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "whole-rule command") {
		t.Fatalf("want whole-rule skip with reason, got %+v", plan.Skipped)
	}

	// Selecting every item extensionally equals the whole rule.
	full := BuildPlan(testHost, []Finding{f}, map[string]bool{
		"go-build-cache/~/Library/Caches/go-build": true,
		"go-build-cache/~/other/go-build":          true,
	})
	if len(full.Actions) != 1 || full.Actions[0].Bytes != 150 || full.Actions[0].ItemKey != "" {
		t.Fatalf("all-item atoms should plan the whole command once, got %+v", full.Actions)
	}
}

func TestBuildPlanUnmatchedSelectors(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "sim-devices", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "delete", "{arg}"}},
			Items: []Item{{Label: "iPhone", Arg: "AAA-111", Key: "AAA-111", Bytes: 1}}},
		{Rule: Rule{ID: "empty-rule", Risk: RiskSafe}},
	}
	plan := BuildPlan(testHost, findings, map[string]bool{
		"sim-devices/AAA-111": true,
		"sim-devices/TYPO":    true,
		"no-such-rule":        true,
		"empty-rule":          true, // rule exists, found nothing: not a typo
	})
	want := []string{"no-such-rule", "sim-devices/TYPO"}
	if !reflect.DeepEqual(plan.Unmatched, want) {
		t.Fatalf("Unmatched = %v, want %v", plan.Unmatched, want)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("valid atom must still plan, got %+v", plan.Actions)
	}
}

func TestBuildPlanSurfaceOnlyItemAtomStillSkips(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "ios-backups", Risk: RiskSurfaceOnly},
		Items: []Item{{Path: "/Users/t/Library/Backup/x", Key: "~/Library/Backup/x", Bytes: 1}},
	}
	plan := BuildPlan(testHost, []Finding{f}, map[string]bool{"ios-backups/~/Library/Backup/x": true})
	if len(plan.Actions) != 0 {
		t.Fatalf("surface-only item atom produced actions: %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "surface-only") {
		t.Fatalf("want surface-only skip, got %+v", plan.Skipped)
	}
}

func TestBuildPlanSkipsItemWithEmptyPlaceholderValue(t *testing.T) {
	f := Finding{
		Rule: Rule{ID: "sim-runtimes", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"xcrun", "simctl", "runtime", "delete", "{arg}"}},
		Items: []Item{
			{Label: "good", Arg: "8A2C", Bytes: 10},
			{Label: "bad-no-arg", Bytes: 20},
		},
	}
	plan := BuildPlan(testHost, []Finding{f}, selectRules(f))
	if len(plan.Actions) != 1 || plan.Actions[0].Command[len(plan.Actions[0].Command)-1] != "8A2C" {
		t.Fatalf("want 1 action for the good item, got %+v", plan.Actions)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "{arg}") {
		t.Fatalf("empty placeholder value must be a skip with reason, got %+v", plan.Skipped)
	}
}

func TestBuildPlanEmptySelectionPlansNothing(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "safe-cache", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/cache", Bytes: 10}}},
		{Rule: Rule{ID: "go-build-cache", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}}, Items: []Item{{Path: "/Users/t/gb", Bytes: 20}}},
	}
	for name, sel := range map[string]map[string]bool{
		"nil":       nil,
		"empty":     {},
		"all false": {"safe-cache": false, "go-build-cache/~/gb": false},
	} {
		t.Run(name, func(t *testing.T) {
			plan := BuildPlan(testHost, findings, sel)
			if len(plan.Actions) != 0 || len(plan.Skipped) != 0 || len(plan.Unmatched) != 0 {
				t.Fatalf("an empty selection must plan nothing, got %+v", plan)
			}
		})
	}
}

func TestDefaultSelection(t *testing.T) {
	read := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: key, Bytes: 1} }
	unreadable := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: key, Partial: true} }
	lowerBound := Item{Path: "/Users/t/lb", Key: "lb", Bytes: 7, Partial: true}
	marker := Item{Label: "folders under ~/src", Key: "folders under ~/src", Partial: true}
	wipe := Argv{"wipe-cache"}
	findings := []Finding{
		{Rule: Rule{ID: "safe-found", Risk: RiskSafe}, Items: []Item{read("a")}},
		{Rule: Rule{ID: "safe-empty", Risk: RiskSafe}},
		{Rule: Rule{ID: "safe-errored", Risk: RiskSafe}, Items: []Item{read("b")}, Err: "tool gave no answer"},
		{Rule: Rule{ID: "caution-found", Risk: RiskCaution}, Items: []Item{read("c")}},
		{Rule: Rule{ID: "surface-found", Risk: RiskSurfaceOnly}, Items: []Item{read("d")}},
		// Partly unreadable: a lower bound was measured, so it stays.
		{Rule: Rule{ID: "safe-lower-bound", Risk: RiskSafe}, Items: []Item{lowerBound}},
		// Nothing measured: never pre-selected.
		{Rule: Rule{ID: "safe-unreadable", Risk: RiskSafe}, Items: []Item{unreadable("e")}},
		{Rule: Rule{ID: "safe-mixed-trash", Risk: RiskSafe}, Items: []Item{read("f"), unreadable("g"), marker}},
		{Rule: Rule{ID: "safe-mixed-whole", Risk: RiskSafe, NativeCommand: wipe}, Items: []Item{read("h"), unreadable("i")}},
		// Keyless, as a caller that skipped FillItemKeys hands it over:
		// no "rule/" atom may form.
		{Rule: Rule{ID: "safe-mixed-keyless", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/j", Bytes: 1}, {Path: "/Users/t/k", Partial: true}}},
	}
	got := DefaultSelection(findings)
	want := map[string]bool{"safe-found": true, "safe-lower-bound": true, "safe-mixed-trash/f": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultSelection = %v, want %v", got, want)
	}

	// The plan of the default selection holds no unreadable target and
	// no skip for the marker that cannot be planned.
	plan := BuildPlan(testHost, findings, got)
	var targets []string
	for _, a := range plan.Actions {
		targets = append(targets, a.Path)
	}
	if want := []string{"/Users/t/a", "/Users/t/lb", "/Users/t/f"}; !reflect.DeepEqual(targets, want) || len(plan.Skipped) != 0 {
		t.Fatalf("default plan targets %v with skips %+v, want %v and none", targets, plan.Skipped, want)
	}
}

func TestBuildPlanEmptiesTrashFirst(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "cache-a", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/a", Bytes: 1}}},
		{Rule: Rule{ID: "empty-the-trash", Risk: RiskCaution, NativeCommand: Argv{"empty-trash"}, EmptiesTrash: true},
			Items: []Item{{Path: "/Users/t/.Trash", Bytes: 5}}},
		{Rule: Rule{ID: "cache-b", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/b", Bytes: 1}}},
	}
	plan := BuildPlan(testHost, findings, selectRules(findings...))
	var order []string
	for _, a := range plan.Actions {
		order = append(order, a.RuleID)
	}
	if want := []string{"empty-the-trash", "cache-a", "cache-b"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("action order = %v, want %v (Trash emptied before this run's moves, the rest in catalog order)", order, want)
	}
}

// The embedded catalog's trash-empty must carry empties_trash: a plan
// over every actionable rule runs it before anything else.
func TestCatalogPlanRunsTrashEmptyFirst(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var findings []Finding
	sel := map[string]bool{}
	for _, r := range catalog {
		if !r.Risk.Actionable() {
			continue
		}
		findings = append(findings, Finding{Rule: r, Items: []Item{{
			Path:  "/Users/t/fixture/" + r.ID,
			Arg:   "arg-" + r.ID,
			Label: r.ID,
			Bytes: 1,
		}}})
		sel[r.ID] = true
	}
	plan := BuildPlan(testHost, findings, sel)
	if len(plan.Actions) < 2 {
		t.Fatalf("want a multi-rule plan, got %+v", plan)
	}
	if plan.Actions[0].RuleID != "trash-empty" {
		t.Fatalf("first action = %s, want trash-empty", plan.Actions[0].RuleID)
	}
	for _, a := range plan.Actions[1:] {
		if a.RuleID == "trash-empty" {
			t.Fatal("trash-empty planned twice")
		}
	}
}

func TestPlanTotalsSplitByKind(t *testing.T) {
	p := Plan{Actions: []Action{
		{Kind: ActionTrash, Bytes: 100},
		{Kind: ActionNative, Bytes: 20},
		{Kind: ActionTrash, Bytes: 3},
	}}
	got := p.Totals()
	if got.FreesNow != 20 || got.AfterTrash != 103 {
		t.Fatalf("Totals = %+v, want FreesNow 20, AfterTrash 103", got)
	}
}

func TestBuildPlanMarksSudoActions(t *testing.T) {
	findings := []Finding{
		{Rule: Rule{ID: "whole-sudo", Risk: RiskCaution, NativeCommand: Argv{"mdutil", "-E", "/"}, Sudo: true},
			Items: []Item{{Path: "/Users/t/Library/Metadata/index", Bytes: 1}}},
		{Rule: Rule{ID: "item-sudo", Risk: RiskSafe, NativeCommand: Argv{"find", "{path}", "-delete"}, Sudo: true},
			Items: []Item{{Path: "/Users/t/Library/videos", Bytes: 2}}},
		{Rule: Rule{ID: "plain", Risk: RiskSafe, NativeCommand: Argv{"go", "clean", "-cache"}},
			Items: []Item{{Path: "/Users/t/Library/Caches/go-build", Bytes: 3}}},
		{Rule: Rule{ID: "trashed", Risk: RiskSafe, Sudo: true},
			Items: []Item{{Path: "/Users/t/Library/trashed", Bytes: 4}}},
	}
	plan := BuildPlan(testHost, findings, selectRules(findings...))
	want := map[string]bool{"whole-sudo": true, "item-sudo": true, "plain": false, "trashed": false}
	if len(plan.Actions) != len(want) {
		t.Fatalf("want %d actions, got %+v", len(want), plan.Actions)
	}
	for _, a := range plan.Actions {
		if a.Sudo != want[a.RuleID] || a.Sudo != (a.Command[0] == "sudo") {
			t.Errorf("%s: Sudo = %v with command %v", a.RuleID, a.Sudo, a.Command)
		}
	}
}

func TestBuildPlanWithNoSudo(t *testing.T) {
	const reason = "needs administrator rights — run `regrow clean %s` in Terminal"
	whole := Finding{
		Rule: Rule{ID: "whole-sudo", Risk: RiskCaution, NativeCommand: Argv{"mdutil", "-E", "/"}, Sudo: true},
		Items: []Item{
			{Path: "/Users/t/Library/Metadata/index", Bytes: 1},
			{Path: "/Users/t/Library/Metadata/other", Bytes: 1},
		},
	}
	perItem := Finding{
		Rule: Rule{ID: "item-sudo", Risk: RiskSafe, NativeCommand: Argv{"find", "{path}", "-delete"}, Sudo: true},
		Items: []Item{
			{Path: "/Users/t/Library/Application Support/videos", Bytes: 2},
			{Path: "/Users/t/Library/more-videos", Bytes: 3},
		},
	}
	// Lives inside perItem's second item.
	nested := Finding{
		Rule:  Rule{ID: "nested", Risk: RiskSafe},
		Items: []Item{{Path: "/Users/t/Library/more-videos/cache", Bytes: 1}},
	}
	// A trash rule never runs sudo, whatever its rule says.
	trashed := Finding{
		Rule:  Rule{ID: "trashed", Risk: RiskSafe, Sudo: true},
		Items: []Item{{Path: "/Users/t/Library/trashed", Bytes: 4}},
	}
	findings := []Finding{whole, perItem, nested, trashed}

	tests := []struct {
		name        string
		selected    map[string]bool
		flags       []string
		wantActions []string
		wantSkips   []Skip
	}{
		{
			name:        "whole rules name the rule",
			selected:    selectRules(findings...),
			wantActions: []string{"nested", "trashed"},
			wantSkips: []Skip{
				{RuleID: "whole-sudo", Reason: fmt.Sprintf(reason, "whole-sudo")},
				{RuleID: "item-sudo", Reason: fmt.Sprintf(reason, "item-sudo")},
			},
		},
		{
			name:     "a partial selection names the item, quoted for the shell",
			selected: map[string]bool{"item-sudo/~/Library/Application Support/videos": true},
			wantSkips: []Skip{{RuleID: "item-sudo", ItemKey: "~/Library/Application Support/videos",
				Reason: fmt.Sprintf(reason, "'item-sudo/~/Library/Application Support/videos'")}},
		},
		{
			name:      "the command carries the flags that load the same catalog",
			selected:  selectRules(whole),
			flags:     []string{"--rules-dir", "/opt/my rules", "--beta-rules"},
			wantSkips: []Skip{{RuleID: "whole-sudo", Reason: fmt.Sprintf(reason, "--rules-dir '/opt/my rules' --beta-rules whole-sudo")}},
		},
		{
			name:      "a partial pick of a whole-rule command keeps its own refusal",
			selected:  map[string]bool{"whole-sudo/~/Library/Metadata/index": true},
			wantSkips: []Skip{{RuleID: "whole-sudo", Reason: "whole-rule command cannot target individual items — select the whole rule"}},
		},
		{
			name:     "an item id without shell characters stays bare",
			selected: map[string]bool{"item-sudo/~/Library/more-videos": true},
			wantSkips: []Skip{{RuleID: "item-sudo", ItemKey: "~/Library/more-videos",
				Reason: fmt.Sprintf(reason, "item-sudo/~/Library/more-videos")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := BuildPlanWith(testHost, findings, tt.selected, PlanOptions{NoSudo: true, CleanFlags: tt.flags})
			var got []string
			for _, a := range plan.Actions {
				if a.Sudo || a.Command[0] == "sudo" {
					t.Errorf("a sudo action was planned: %+v", a)
				}
				got = append(got, a.RuleID)
			}
			if !reflect.DeepEqual(got, tt.wantActions) {
				t.Errorf("actions = %v, want %v", got, tt.wantActions)
			}
			if !reflect.DeepEqual(plan.Skipped, tt.wantSkips) {
				t.Errorf("skipped = %+v, want %+v", plan.Skipped, tt.wantSkips)
			}
			if len(plan.Unmatched) != 0 {
				t.Errorf("a refused selector is not a typo: unmatched = %v", plan.Unmatched)
			}
		})
	}

	// With sudo allowed the outer action deletes the nested item; once
	// it is refused, nothing covers that item and it is planned itself.
	covered := BuildPlan(testHost, findings, selectRules(perItem, nested))
	for _, a := range covered.Actions {
		if a.RuleID == "nested" {
			t.Fatalf("the nested item must be covered by its sudo parent when sudo is allowed: %+v", covered.Actions)
		}
	}
}

func TestSelectable(t *testing.T) {
	read := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: key, Bytes: 1} }
	unreadable := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: key, Partial: true} }
	lowerBound := Item{Path: "/Users/t/lb", Key: "lb", Bytes: 7, Partial: true}
	marker := Item{Label: "folders under ~/src", Key: "folders under ~/src", Partial: true}
	wipe := Argv{"wipe-cache"}
	perItem := Argv{"tool", "rm", "{arg}"}
	tests := []struct {
		name string
		f    Finding
		want []string
	}{
		{"surface-only is never ticked", Finding{Rule: Rule{ID: "r", Risk: RiskSurfaceOnly}, Items: []Item{read("a")}}, nil},
		{"a rule that found nothing", Finding{Rule: Rule{ID: "r", Risk: RiskSafe}}, nil},
		{"a rule that failed", Finding{Rule: Rule{ID: "r", Risk: RiskSafe}, Items: []Item{read("a")}, Err: "tool gave no answer"}, nil},
		{"Trash items are picked one by one", Finding{Rule: Rule{ID: "r", Risk: RiskCaution}, Items: []Item{read("a"), read("b")}},
			[]string{"r/a", "r/b"}},
		{"a per-item command's items too", Finding{Rule: Rule{ID: "r", Risk: RiskCaution, ToolQuery: "q", NativeCommand: perItem},
			Items: []Item{{Label: "one", Arg: "x1", Key: "x1", Bytes: 1}, {Label: "two", Arg: "x2", Key: "x2", Bytes: 2}}},
			[]string{"r/x1", "r/x2"}},
		{"unreadable items are left out, a lower bound stays", Finding{Rule: Rule{ID: "r", Risk: RiskSafe},
			Items: []Item{read("a"), unreadable("b"), lowerBound, marker}}, []string{"r/a", "r/lb"}},
		{"nothing read, nothing to tick", Finding{Rule: Rule{ID: "r", Risk: RiskSafe}, Items: []Item{unreadable("a"), marker}}, nil},
		{"items that share an id are one atom", Finding{Rule: Rule{ID: "r", Risk: RiskSafe}, Items: []Item{read("a"), read("a"), read("b")}},
			[]string{"r/a", "r/b"}},
		{"a whole-rule command read in full is the rule", Finding{Rule: Rule{ID: "r", Risk: RiskSafe, NativeCommand: wipe},
			Items: []Item{read("a"), lowerBound}}, []string{"r"}},
		{"a whole-rule command cannot leave an unreadable item alone", Finding{Rule: Rule{ID: "r", Risk: RiskSafe, NativeCommand: wipe},
			Items: []Item{read("a"), unreadable("b")}}, nil},
		{"keyless items read in full are the rule", Finding{Rule: Rule{ID: "r", Risk: RiskSafe},
			Items: []Item{{Path: "/Users/t/j", Bytes: 1}, {Path: "/Users/t/k", Bytes: 2}}}, []string{"r"}},
		{"keyless items with one unreadable form no atom", Finding{Rule: Rule{ID: "r", Risk: RiskSafe},
			Items: []Item{{Path: "/Users/t/j", Bytes: 1}, {Path: "/Users/t/k", Partial: true}}}, nil},
	}
	for _, tt := range tests {
		if got := Selectable(tt.f); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Selectable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// What a face pre-ticks from Selectable is what DefaultSelection plans:
// over safe findings the two give the same plan.
func TestSelectableOverSafeFindingsPlansTheDefaultSelection(t *testing.T) {
	read := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: "~/" + key, Bytes: 1} }
	unreadable := func(key string) Item { return Item{Path: "/Users/t/" + key, Key: "~/" + key, Partial: true} }
	wipe := Argv{"wipe-cache"}
	findings := []Finding{
		{Rule: Rule{ID: "trash-read", Risk: RiskSafe}, Items: []Item{read("a"), read("b")}},
		{Rule: Rule{ID: "whole-read", Risk: RiskSafe, NativeCommand: wipe}, Items: []Item{read("c"), read("d")}},
		{Rule: Rule{ID: "item-read", Risk: RiskSafe, NativeCommand: Argv{"tool", "rm", "{path}"}}, Items: []Item{read("e"), read("f")}},
		{Rule: Rule{ID: "trash-mixed", Risk: RiskSafe}, Items: []Item{read("g"), unreadable("h"),
			{Label: "folders under ~/src", Key: "folders under ~/src", Partial: true}}},
		{Rule: Rule{ID: "item-mixed", Risk: RiskSafe, NativeCommand: Argv{"tool", "rm", "{path}"}}, Items: []Item{unreadable("i"), read("j")}},
		{Rule: Rule{ID: "whole-mixed", Risk: RiskSafe, NativeCommand: wipe}, Items: []Item{read("k"), unreadable("l")}},
		{Rule: Rule{ID: "keyless-read", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/m", Bytes: 1}, {Path: "/Users/t/n", Bytes: 2}}},
		{Rule: Rule{ID: "keyless-mixed", Risk: RiskSafe}, Items: []Item{{Path: "/Users/t/o", Bytes: 1}, {Path: "/Users/t/p", Partial: true}}},
		{Rule: Rule{ID: "all-unreadable", Risk: RiskSafe}, Items: []Item{unreadable("q")}},
		// Inside trash-read's first item: nesting resolves the same way.
		{Rule: Rule{ID: "nested", Risk: RiskSafe}, Items: []Item{read("a/inner")}},
	}
	ticked := map[string]bool{}
	for _, f := range findings {
		for _, atom := range Selectable(f) {
			ticked[atom] = true
		}
	}
	fromDefault := BuildPlan(testHost, findings, DefaultSelection(findings))
	fromTicks := BuildPlan(testHost, findings, ticked)
	if len(fromDefault.Actions) != 9 {
		t.Fatalf("the default plan has %d actions, want 9: %+v", len(fromDefault.Actions), fromDefault.Actions)
	}
	if !reflect.DeepEqual(fromTicks.Actions, fromDefault.Actions) {
		t.Errorf("actions from Selectable = %+v\nfrom DefaultSelection = %+v", fromTicks.Actions, fromDefault.Actions)
	}
	if !reflect.DeepEqual(fromTicks.Skipped, fromDefault.Skipped) || len(fromDefault.Skipped) != 1 {
		t.Errorf("skips from Selectable = %+v\nfrom DefaultSelection = %+v, want the one nested item in both", fromTicks.Skipped, fromDefault.Skipped)
	}
	if len(fromTicks.Unmatched)+len(fromDefault.Unmatched) != 0 {
		t.Errorf("unmatched: %v and %v", fromTicks.Unmatched, fromDefault.Unmatched)
	}
}

func TestBuildPlanWithLookPath(t *testing.T) {
	const reason = "`%s` is not on the PATH regrow was started with"
	whole := Finding{
		Rule:  Rule{ID: "whole-gone", Risk: RiskSafe, NativeCommand: Argv{"gone-tool", "clean"}},
		Items: []Item{{Path: "/Users/t/Library/Caches/gone", Bytes: 100}},
	}
	perItem := Finding{
		Rule: Rule{ID: "item-gone", Risk: RiskCaution, ToolQuery: "q", NativeCommand: Argv{"gone-ctl", "rm", "{arg}"}},
		Items: []Item{
			{Label: "one", Arg: "x1", Bytes: 10},
			{Label: "two", Arg: "x2", Bytes: 20},
		},
	}
	// Lives inside whole's item: a refused command covers nothing.
	nested := Finding{
		Rule:  Rule{ID: "nested", Risk: RiskSafe},
		Items: []Item{{Path: "/Users/t/Library/Caches/gone/inner", Bytes: 5}},
	}
	found := Finding{
		Rule:  Rule{ID: "found", Risk: RiskSafe, NativeCommand: Argv{"here-tool", "clean"}},
		Items: []Item{{Path: "/Users/t/Library/Caches/here", Bytes: 3}},
	}
	// Needs rights and is missing: missing is what the owner can fix.
	sudoGone := Finding{
		Rule:  Rule{ID: "sudo-gone", Risk: RiskSafe, NativeCommand: Argv{"gone-tool", "wipe"}, Sudo: true},
		Items: []Item{{Path: "/Users/t/Library/Caches/sudo-gone", Bytes: 9}},
	}
	trashed := Finding{
		Rule:  Rule{ID: "trashed", Risk: RiskSafe},
		Items: []Item{{Path: "/Users/t/Library/trashed", Bytes: 4}},
	}
	findings := []Finding{whole, perItem, nested, found, sudoGone, trashed}
	var looked []string
	opts := PlanOptions{NoSudo: true, LookPath: func(file string) (string, error) {
		looked = append(looked, file)
		if strings.HasPrefix(file, "gone-") {
			return "", fmt.Errorf("exec: %q: executable file not found in $PATH", file)
		}
		return "/opt/bin/" + file, nil
	}}

	tests := []struct {
		name        string
		selected    map[string]bool
		wantActions []string
		wantSkips   []Skip
		wantTotals  Totals
	}{
		{
			name:        "whole selections skip the rule",
			selected:    selectRules(findings...),
			wantActions: []string{"nested", "found", "trashed"},
			wantSkips: []Skip{
				{RuleID: "whole-gone", Reason: fmt.Sprintf(reason, "gone-tool")},
				{RuleID: "item-gone", Reason: fmt.Sprintf(reason, "gone-ctl")},
				{RuleID: "sudo-gone", Reason: fmt.Sprintf(reason, "gone-tool")},
			},
			wantTotals: Totals{FreesNow: 3, AfterTrash: 9},
		},
		{
			name:      "a partial pick skips the item",
			selected:  map[string]bool{"item-gone/x2": true},
			wantSkips: []Skip{{RuleID: "item-gone", ItemKey: "x2", Reason: fmt.Sprintf(reason, "gone-ctl")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := BuildPlanWith(testHost, findings, tt.selected, opts)
			var got []string
			for _, a := range plan.Actions {
				got = append(got, a.RuleID)
			}
			if !reflect.DeepEqual(got, tt.wantActions) {
				t.Errorf("actions = %v, want %v", got, tt.wantActions)
			}
			if !reflect.DeepEqual(plan.Skipped, tt.wantSkips) {
				t.Errorf("skipped = %+v, want %+v", plan.Skipped, tt.wantSkips)
			}
			if plan.Totals() != tt.wantTotals {
				t.Errorf("totals = %+v, want %+v: a skipped action adds nothing", plan.Totals(), tt.wantTotals)
			}
			if len(plan.Unmatched) != 0 {
				t.Errorf("a refused selector is not a typo: unmatched = %v", plan.Unmatched)
			}
		})
	}
	for _, file := range looked {
		if file == "osascript" || file == "sudo" {
			t.Errorf("looked up %q: only a rule's own program is looked up", file)
		}
	}

	// Without the option nothing is looked up and every action is planned.
	if plan := BuildPlan(testHost, findings, selectRules(findings...)); len(plan.Actions) != 6 || len(plan.Skipped) != 1 {
		t.Fatalf("BuildPlan without options = %+v, want six actions and the nested item covered", plan)
	}
}

func TestSudoSkipNamesTheEngineItself(t *testing.T) {
	f := Finding{
		Rule:  Rule{ID: "whole-sudo", Risk: RiskCaution, NativeCommand: Argv{"mdutil", "-E", "/"}, Sudo: true},
		Items: []Item{{Path: "/Users/t/Library/Metadata/index", Bytes: 1}},
	}
	tests := []struct {
		name string
		opts PlanOptions
		want string
	}{
		{"without Self it is regrow on PATH", PlanOptions{NoSudo: true},
			"needs administrator rights — run `regrow clean whole-sudo` in Terminal"},
		{"Self is quoted for the shell and comes before the flags",
			PlanOptions{NoSudo: true, Self: "/Users/t/Applications/My Regrow.app/Contents/MacOS/regrow-engine", CleanFlags: []string{"--beta-rules"}},
			"needs administrator rights — run `'/Users/t/Applications/My Regrow.app/Contents/MacOS/regrow-engine' clean --beta-rules whole-sudo` in Terminal"},
		{"a plain path stays bare", PlanOptions{NoSudo: true, Self: "/opt/regrow/bin/regrow"},
			"needs administrator rights — run `/opt/regrow/bin/regrow clean whole-sudo` in Terminal"},
	}
	for _, tt := range tests {
		plan := BuildPlanWith(testHost, []Finding{f}, selectRules(f), tt.opts)
		if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != tt.want {
			t.Errorf("%s: plan = %+v, want the one skip %q", tt.name, plan, tt.want)
		}
	}
}
