package engine

import "time"

// Item is one concrete thing a rule found: a directory on disk, or a
// tool-reported entry (a docker image, an ollama model).
type Item struct {
	// Path is the filesystem location; empty for tool items that only
	// a steward command can address.
	Path string `json:"path,omitempty"`
	// Label is the human name shown in the UI (model name, sim
	// runtime, ...). Defaults to Path when empty.
	Label string `json:"label,omitempty"`
	// Key is the item's stable identity within its rule (see
	// identity.go). "ruleID/key" addresses this item in selection
	// atoms and `regrow clean`/`plan` arguments.
	Key string `json:"key,omitempty"`
	// Arg substitutes {arg} in the rule's native command (a model id
	// for `ollama rm {arg}`, a runtime id for simctl).
	Arg string `json:"arg,omitempty"`
	// Bytes is measured disk usage (physical blocks, du-style), or a
	// tool-reported size: the whole item, nested items included.
	Bytes int64 `json:"bytes"`
	// LastUsed is the newest mtime over the item's files and
	// directories, or a tool-reported time; zero when unknown.
	LastUsed time.Time `json:"last_used,omitzero"`
	// Partial: Bytes and LastUsed are lower bounds, because something
	// in the item refused or blocked.
	Partial bool `json:"partial,omitempty"`
}

// Unreadable: nothing of the item could be measured, so its size and
// recency are unknown, not zero.
func (it Item) Unreadable() bool { return it.Partial && it.Bytes == 0 }

// Finding is one rule with everything the scan measured for it.
type Finding struct {
	Rule  Rule   `json:"rule"`
	Items []Item `json:"items,omitempty"`
	// Err records a rule-level failure (tool missing, unknown query,
	// scan cancelled). A target that refuses or blocks is an item
	// with Partial set, not an error.
	Err string `json:"error,omitempty"`
}

// TotalBytes sums the finding's items.
func (f Finding) TotalBytes() int64 {
	var n int64
	for _, it := range f.Items {
		n += it.Bytes
	}
	return n
}

// Partial reports whether any item's size is a lower bound.
func (f Finding) Partial() bool {
	for _, it := range f.Items {
		if it.Partial {
			return true
		}
	}
	return false
}
