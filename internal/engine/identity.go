package engine

import (
	"fmt"
	"strings"
)

// Item identity (Prompt G0). An item's key is stable within its rule
// across scans; the full ID "ruleID/key" is what selection atoms, the
// TUI footer, and --json carry. Rule IDs are kebab-case and never
// contain "/", so splitting an ID on the first "/" is unambiguous even
// when the key itself is a path.

// ItemID joins a rule id and an item key into a selection atom.
func ItemID(ruleID, key string) string {
	return ruleID + "/" + key
}

// SplitItemID splits a selection atom. isItem is false for a bare rule
// atom ("go-build-cache"); true when the atom addresses one item.
func SplitItemID(atom string) (ruleID, key string, isItem bool) {
	return strings.Cut(atom, "/")
}

// DeriveKey computes the item's stable key: the tool's own handle
// (Arg) when it exists — that is what the delete command targets —
// else the path with home abbreviated to ~, else the label. Anonymous
// items (nothing set) return "" and get a positional key from
// FillItemKeys.
func (it Item) DeriveKey(home string) string {
	switch {
	case it.Arg != "":
		return it.Arg
	case it.Path != "":
		return tildePath(it.Path, home)
	default:
		return it.Label
	}
}

// FillItemKeys assigns every item its key. The scanner calls it once
// per finding; the TUI calls it on findings it did not scan itself.
// Items whose key cannot be derived get a positional "#n" — stable
// within one scan, honest about being anonymous. Duplicate keys are
// deliberately not uniquified: two items that derive the same key are
// the same target twice and select together.
func (f *Finding) FillItemKeys(home string) {
	for i := range f.Items {
		if f.Items[i].Key != "" {
			continue
		}
		key := f.Items[i].DeriveKey(home)
		if key == "" {
			key = fmt.Sprintf("#%d", i+1)
		}
		f.Items[i].Key = key
	}
}

// tildePath abbreviates home to ~ so keys are short, stable per
// machine, and safe to paste (a mid-word ~ never shell-expands).
func tildePath(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}
