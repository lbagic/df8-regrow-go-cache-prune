package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lbagic/regrow/internal/trash"
)

// ActionKind says how an action deletes: a steward command, or a move
// to the Trash (ARCHITECTURE.md invariant 4: native commands first).
// A prune deletes old cache entries directly, and only PrunePlan
// emits one.
type ActionKind string

const (
	ActionNative ActionKind = "native"
	ActionTrash  ActionKind = "trash"
	ActionPrune  ActionKind = "prune"
)

// Action is one exact command the plan would run. Nothing here
// executes in Phase 1: the planner's output is the contract the
// executor (Phase 2) fulfils.
type Action struct {
	RuleID string `json:"rule_id"`
	// ItemKey identifies the item this action targets; empty for
	// whole-rule commands, which act on every item at once.
	ItemKey string     `json:"item_key,omitempty"`
	Kind    ActionKind `json:"kind"`
	// Command is the exact argv, sudo included when the rule needs it.
	Command []string `json:"command"`
	// PreAction names the executor hook that must succeed before
	// Command runs or the Trash move happens (docker volume export to
	// staging, agent session recheck).
	PreAction string `json:"pre_action,omitempty"`
	// Path is the filesystem target for trash actions.
	Path string `json:"path,omitempty"`
	// Bytes counts only items that no other action of the plan
	// contains.
	Bytes int64 `json:"bytes"`
	// EmptiesTrash: the action permanently empties the Trash. The
	// planner puts it first; if it fails, the executor skips the run's
	// Trash moves, since Finder may still be emptying.
	EmptiesTrash bool `json:"empties_trash,omitempty"`
	// Sudo: Command starts with sudo and prompts for a password, so it
	// needs a terminal.
	Sudo bool `json:"sudo,omitempty"`
	// Includes lists the items inside this action's targets that no
	// action of the plan deletes on its own: they go with it, and Bytes
	// already counts them.
	Includes []Included `json:"includes,omitempty"`
}

// Included is an item an action deletes along with its own target,
// such as a node_modules inside a removed worktree.
type Included struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

// Skip records why a selected finding produced no action.
type Skip struct {
	RuleID  string `json:"rule_id"`
	ItemKey string `json:"item_key,omitempty"`
	Reason  string `json:"reason"`
}

// Plan is the dry-run output: the exact command list, plus what was
// deliberately not planned and why.
type Plan struct {
	Actions []Action `json:"actions"`
	Skipped []Skip   `json:"skipped,omitempty"`
	// Unmatched lists selection atoms that addressed nothing in these
	// findings — an unknown rule id, or an item key absent from the
	// scan. A typo'd selector must be visible, never a silent no-op:
	// `clean` refuses to run on it, `plan` warns.
	Unmatched []string `json:"unmatched,omitempty"`
}

// TotalBytes is the union of what the plan deletes: BuildPlan keeps
// action bytes disjoint, so the sum counts every byte once.
func (p Plan) TotalBytes() int64 {
	var n int64
	for _, a := range p.Actions {
		n += a.Bytes
	}
	return n
}

// Totals buckets the plan's bytes by when they become free space: a
// steward command frees them as it runs, a Trash move frees nothing
// until the Trash is emptied.
func (p Plan) Totals() Totals {
	var t Totals
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionNative, ActionPrune:
			t.FreesNow += a.Bytes
		case ActionTrash:
			t.AfterTrash += a.Bytes
		}
	}
	return t
}

// DefaultSelection is the selection every face starts from (the TUI's
// pre-ticks, `plan` and `clean` without ids): safe rules that found
// items and reported no error, leaving out unreadable items (nothing
// measured). A rule read in full is a rule atom. With some items
// unreadable, a rule that acts per item contributes item atoms for the
// rest, and a whole-rule command, which cannot leave an item alone, is
// left out. So is a rule whose read items lack keys: a key derived
// here, without the host's home, would not be the one BuildPlan
// derives for a path under home.
func DefaultSelection(findings []Finding) map[string]bool {
	sel := map[string]bool{}
	for _, f := range findings {
		if f.Rule.Risk != RiskSafe || len(f.Items) == 0 || f.Err != "" {
			continue
		}
		var read []string
		keyed := true
		for _, it := range f.Items {
			if !it.Unreadable() {
				read = append(read, ItemID(f.Rule.ID, it.Key))
				keyed = keyed && it.Key != ""
			}
		}
		switch {
		case len(read) == len(f.Items):
			sel[f.Rule.ID] = true
		case f.Rule.PerItemActionable() && keyed:
			for _, id := range read {
				sel[id] = true
			}
		}
	}
	return sel
}

// selection is the parsed form of the atom map: whole-rule atoms and
// per-item atoms ("ruleID/key"), kept separate so partial selections
// are detectable.
type selection struct {
	rules map[string]bool
	items map[string]map[string]bool
}

func parseSelection(selected map[string]bool) selection {
	sel := selection{rules: map[string]bool{}, items: map[string]map[string]bool{}}
	for atom, on := range selected {
		if !on {
			continue
		}
		ruleID, key, isItem := SplitItemID(atom)
		if !isItem {
			sel.rules[ruleID] = true
			continue
		}
		if sel.items[ruleID] == nil {
			sel.items[ruleID] = map[string]bool{}
		}
		sel.items[ruleID][key] = true
	}
	return sel
}

func (s selection) empty() bool { return len(s.rules) == 0 && len(s.items) == 0 }

// itemsFor resolves the selection against one finding whose items
// carry keys: the indexes of the items to plan, whether the rule is
// engaged at all, and whether the pick is a strict subset (partial).
// Matched item atoms are marked in `matched` so unaddressed atoms can
// be reported.
func (s selection) itemsFor(f Finding, matched map[string]bool) (idx []int, engaged, partial bool) {
	if s.rules[f.Rule.ID] {
		for i := range f.Items {
			idx = append(idx, i)
		}
		return idx, true, false
	}
	keys := s.items[f.Rule.ID]
	if len(keys) == 0 {
		return nil, false, false
	}
	for i, it := range f.Items {
		if it.Key != "" && keys[it.Key] {
			idx = append(idx, i)
			matched[ItemID(f.Rule.ID, it.Key)] = true
		}
	}
	return idx, true, len(idx) < len(f.Items)
}

// unmatched returns every selection atom that addressed nothing:
// rule atoms whose rule is not among the findings, and item atoms not
// marked in `matched`. Rule atoms whose rule found zero items are NOT
// unmatched — an absent target is a normal scan outcome, not a typo.
func (s selection) unmatched(findings []Finding, matched map[string]bool) []string {
	known := make(map[string]bool, len(findings))
	for _, f := range findings {
		known[f.Rule.ID] = true
	}
	var out []string
	for ruleID := range s.rules {
		if !known[ruleID] {
			out = append(out, ruleID)
		}
	}
	for ruleID, keys := range s.items {
		for key := range keys {
			if !matched[ItemID(ruleID, key)] {
				out = append(out, ItemID(ruleID, key))
			}
		}
	}
	sort.Strings(out)
	return out
}

// draft is an action before nesting is resolved, with the items it
// deletes.
type draft struct {
	action Action
	items  []itemRef
}

// BuildPlan turns selected findings into the exact command list.
// Selection atoms are rule ids ("sim-devices") or item ids
// ("sim-devices/AAA-111"); a nil or empty selection plans nothing.
// Invariants are enforced here, not in the UI:
//   - a surface-only item is never deleted: not when selected, not
//     inside a selected item, not at a path another rule shares;
//   - every trash target passes the path guard;
//   - a whole-rule command is never planned for a partial selection;
//   - an item inside another planned item is not planned again, so
//     action bytes are disjoint and TotalBytes is their union;
//   - actions that empty the Trash run first, so this run's moves
//     stay restorable.
func BuildPlan(host Host, findings []Finding, selected map[string]bool) Plan {
	return BuildPlanWith(host, findings, selected, PlanOptions{})
}

// PlanOptions adapts planning to the face that will execute the plan.
type PlanOptions struct {
	// NoSudo refuses every action that needs administrator rights: the
	// face has no terminal for sudo to prompt on. Each skip names the
	// `regrow clean` command to run in Terminal instead.
	NoSudo bool
	// CleanFlags go into that command before the selector, so Terminal
	// loads the same catalog (--rules-dir, --beta-rules).
	CleanFlags []string
}

// BuildPlanWith is BuildPlan with options.
func BuildPlanWith(host Host, findings []Finding, selected map[string]bool, opts PlanOptions) Plan {
	var plan Plan
	sel := parseSelection(selected)
	if sel.empty() {
		return plan
	}
	findings = withItemKeys(findings, host.Home)
	tree := buildForest(findings)
	matched := map[string]bool{}

	var drafts []draft
	for fi, f := range findings {
		idx, engaged, partial := sel.itemsFor(f, matched)
		if !engaged {
			continue
		}
		if !f.Rule.Risk.Actionable() {
			plan.Skipped = append(plan.Skipped, Skip{RuleID: f.Rule.ID, Reason: "surface-only: report, never delete"})
			continue
		}
		if len(idx) == 0 {
			continue
		}
		refs := make([]itemRef, len(idx))
		for i, ii := range idx {
			refs[i] = itemRef{fi, ii}
		}
		var ds []draft
		var skips []Skip
		if len(f.Rule.NativeCommand) > 0 {
			ds, skips = nativeDrafts(findings, tree, host, f.Rule, refs, partial)
			// Before nesting: a refused action must not cover the
			// items nested inside it.
			if opts.NoSudo && f.Rule.Sudo && len(ds) > 0 {
				skips = append(skips, sudoSkips(f.Rule.ID, ds, partial, opts.CleanFlags)...)
				ds = nil
			}
		} else {
			ds, skips = trashDrafts(findings, tree, host, f.Rule, refs)
		}
		drafts = append(drafts, ds...)
		plan.Skipped = append(plan.Skipped, skips...)
	}

	actions, skips := resolveNesting(findings, tree, drafts)
	plan.Actions = actions
	plan.Skipped = append(plan.Skipped, skips...)
	plan.Unmatched = sel.unmatched(findings, matched)
	return plan
}

// withItemKeys returns a copy of findings whose items carry keys:
// hand-built findings may lack them, and actions must name their item.
func withItemKeys(findings []Finding, home string) []Finding {
	out := make([]Finding, len(findings))
	for i, f := range findings {
		items := make([]Item, len(f.Items))
		copy(items, f.Items)
		for j := range items {
			if items[j].Key == "" {
				items[j].Key = items[j].DeriveKey(home)
			}
		}
		f.Items = items
		out[i] = f
	}
	return out
}

func itemAt(findings []Finding, ref itemRef) Item {
	return findings[ref.finding].Items[ref.item]
}

func refID(findings []Finding, ref itemRef) string {
	return ItemID(findings[ref.finding].Rule.ID, itemAt(findings, ref).Key)
}

// surfaceOnlyConflict finds a surface-only item that deleting ref
// would take with it: one nested below it, or one at the same path
// under an earlier rule (on equal paths the earlier rule is the parent,
// so it is not a descendant).
func surfaceOnlyConflict(findings []Finding, tree forest, ref itemRef) (itemRef, bool) {
	for _, d := range tree.descendants(ref) {
		if !findings[d.finding].Rule.Risk.Actionable() {
			return d, true
		}
	}
	for _, a := range tree.sharesPath(ref) {
		if !findings[a.finding].Rule.Risk.Actionable() {
			return a, true
		}
	}
	return itemRef{}, false
}

func surfaceOnlyReason(findings []Finding, conflict itemRef) string {
	return fmt.Sprintf("contains %s, which regrow never deletes", refID(findings, conflict))
}

func trashDrafts(findings []Finding, tree forest, host Host, r Rule, refs []itemRef) ([]draft, []Skip) {
	var ds []draft
	var skips []Skip
	for _, ref := range refs {
		it := itemAt(findings, ref)
		if it.Path == "" {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: fmt.Sprintf("item %q has no path and the rule has no native command", it.Label)})
			continue
		}
		if err := trash.GuardPath(it.Path, host.Home); err != nil {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: err.Error()})
			continue
		}
		if c, ok := surfaceOnlyConflict(findings, tree, ref); ok {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: surfaceOnlyReason(findings, c)})
			continue
		}
		ds = append(ds, draft{
			action: Action{
				RuleID:    r.ID,
				ItemKey:   it.Key,
				Kind:      ActionTrash,
				Command:   trash.PreviewCommand(it.Path),
				PreAction: r.PreAction,
				Path:      it.Path,
			},
			items: []itemRef{ref},
		})
	}
	return ds, skips
}

// nativeDrafts expands the rule's native command over the selected
// items. With a placeholder the command runs once per item; without
// one it acts on everything at once, so a partial selection, or any
// item holding a surface-only one, refuses the whole command. The
// placeholder convention itself (which tokens exist, refusing empty
// substitutions) is owned by the schema (Argv). A per-item command's
// item path passes the path guard, as a Trash move's does: a tool can
// report any path, a mount root included.
func nativeDrafts(findings []Finding, tree forest, host Host, r Rule, refs []itemRef, partial bool) ([]draft, []Skip) {
	if !r.NativeCommand.PerItem() {
		if partial {
			return nil, []Skip{{RuleID: r.ID, Reason: "whole-rule command cannot target individual items — select the whole rule"}}
		}
		for _, ref := range refs {
			if c, ok := surfaceOnlyConflict(findings, tree, ref); ok {
				return nil, []Skip{{RuleID: r.ID, Reason: surfaceOnlyReason(findings, c)}}
			}
		}
		return []draft{{
			action: Action{
				RuleID:       r.ID,
				Kind:         ActionNative,
				Command:      withSudo(r.Sudo, r.NativeCommand),
				EmptiesTrash: r.EmptiesTrash,
				Sudo:         r.Sudo,
			},
			items: refs,
		}}, nil
	}
	var ds []draft
	var skips []Skip
	for _, ref := range refs {
		it := itemAt(findings, ref)
		cmd, err := r.NativeCommand.ExpandItem(it)
		if err == nil && it.Path != "" {
			err = trash.GuardPath(it.Path, host.Home)
		}
		if err != nil {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: err.Error()})
			continue
		}
		if c, ok := surfaceOnlyConflict(findings, tree, ref); ok {
			skips = append(skips, Skip{RuleID: r.ID, ItemKey: it.Key, Reason: surfaceOnlyReason(findings, c)})
			continue
		}
		ds = append(ds, draft{
			action: Action{
				RuleID:    r.ID,
				ItemKey:   it.Key,
				Kind:      ActionNative,
				Command:   withSudo(r.Sudo, cmd),
				PreAction: r.PreAction,
				Path:      it.Path,
				Sudo:      r.Sudo,
			},
			items: []itemRef{ref},
		})
	}
	return ds, skips
}

// resolveNesting applies the containment forest to the drafts. An item
// with a planned ancestor is deleted by that ancestor's action, so its
// bytes are not counted again, and a draft left with no item of its
// own is dropped. Dropping one never uncovers anything: the topmost
// planned ancestor of a covered item has no planned ancestor itself,
// so its action always runs. A whole-rule command cannot leave items
// out, so it still runs while any of its items is uncovered.
// Trash-emptying actions move to the front.
func resolveNesting(findings []Finding, tree forest, drafts []draft) ([]Action, []Skip) {
	owner := map[itemRef]int{}
	for di, d := range drafts {
		for _, ref := range d.items {
			owner[ref] = di
		}
	}
	bytes := make([]int64, len(drafts))
	kept := make([]bool, len(drafts))
	includes := make([][]Included, len(drafts))
	for di, d := range drafts {
		for _, ref := range d.items {
			if !hasPlannedAncestor(tree, owner, ref) {
				kept[di] = true
				bytes[di] += itemAt(findings, ref).Bytes
				includes[di] = append(includes[di], unplannedInside(findings, tree, owner, ref)...)
			}
		}
	}
	var first, rest []Action
	var skips []Skip
	for di, d := range drafts {
		if !kept[di] {
			cover := runningAncestor(tree, owner, kept, d.items[0])
			skips = append(skips, Skip{RuleID: d.action.RuleID, ItemKey: d.action.ItemKey,
				Reason: fmt.Sprintf("inside %s, also selected", refID(findings, cover))})
			continue
		}
		a := d.action
		a.Bytes = bytes[di]
		a.Includes = includes[di]
		if a.EmptiesTrash {
			first = append(first, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(first, rest...), skips
}

// unplannedInside lists what deleting ref takes with it that no draft
// plans: unplanned items of earlier rules at ref's own path, and the
// topmost unplanned items below it. Below a planned item the search
// goes on, since that item's action is dropped in favour of ref's.
func unplannedInside(findings []Finding, tree forest, owner map[itemRef]int, ref itemRef) []Included {
	var out []Included
	add := func(r itemRef) {
		out = append(out, Included{ID: refID(findings, r), Bytes: itemAt(findings, r).Bytes})
	}
	for _, a := range tree.sharesPath(ref) {
		if _, planned := owner[a]; !planned {
			add(a)
		}
	}
	var below func(r itemRef)
	below = func(r itemRef) {
		for _, c := range tree.children[r] {
			if _, planned := owner[c]; planned {
				below(c)
			} else {
				add(c)
			}
		}
	}
	below(ref)
	return out
}

func hasPlannedAncestor(tree forest, owner map[itemRef]int, ref itemRef) bool {
	for _, a := range tree.ancestors(ref) {
		if _, planned := owner[a]; planned {
			return true
		}
	}
	return false
}

// runningAncestor is the nearest ancestor of a covered item whose
// action stays in the plan. The topmost planned ancestor always
// qualifies, so the loop returns before falling through.
func runningAncestor(tree forest, owner map[itemRef]int, kept []bool, ref itemRef) itemRef {
	var top itemRef
	for _, a := range tree.ancestors(ref) {
		oi, planned := owner[a]
		if !planned {
			continue
		}
		if kept[oi] {
			return a
		}
		top = a
	}
	return top
}

// sudoSkips turns a sudo rule's drafts into skips that name the
// `regrow clean` selector running the same selection: the rule id when
// the whole rule was selected, else each item id.
func sudoSkips(ruleID string, ds []draft, partial bool, flags []string) []Skip {
	reason := func(selector string) string {
		words := []string{"regrow", "clean"}
		for _, a := range flags {
			words = append(words, shellQuote(a))
		}
		words = append(words, shellQuote(selector))
		return "needs administrator rights — run `" + strings.Join(words, " ") + "` in Terminal"
	}
	if !partial {
		return []Skip{{RuleID: ruleID, Reason: reason(ruleID)}}
	}
	skips := make([]Skip, len(ds))
	for i, d := range ds {
		skips[i] = Skip{RuleID: ruleID, ItemKey: d.action.ItemKey, Reason: reason(ItemID(ruleID, d.action.ItemKey))}
	}
	return skips
}

// shellQuote single-quotes s when a shell would split or expand it.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n'\"\\$`*?[](){}<>|&;!#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func withSudo(sudo bool, argv []string) []string {
	if !sudo {
		return argv
	}
	return append([]string{"sudo"}, argv...)
}
