package engine

// Totals splits bytes into buckets by when, if ever, they become free
// space. Containment is computed from the forest, never stored.
type Totals struct {
	FreesNow     int64 `json:"frees_now"`     // steward commands: freed as they run
	AfterTrash   int64 `json:"after_trash"`   // moved to the Trash: freed once it is emptied
	ShownOnly    int64 `json:"shown_only"`    // surface-only: regrow never deletes it
	MacOSManaged int64 `json:"macos_managed"` // phantom space macOS reclaims on its own
	Partial      int   `json:"partial"`       // items whose bytes are lower bounds
}

// Ledger is a scan's accounting: every measured byte in exactly one
// item, and each item's share in its rule's bucket.
type Ledger struct {
	// Exclusive maps item ID to Bytes minus the Bytes of the item's
	// direct children in the containment forest: what the item holds
	// that no other item reports.
	Exclusive map[string]int64 `json:"exclusive"`
	Totals    Totals           `json:"totals"`
}

// Account computes the ledger over the same containment forest the
// planner uses. A parent measured smaller than its children (a partial
// parent, or a tree that grew between two walks) holds 0, never a
// negative share. Items that share an ID add up.
func Account(findings []Finding) Ledger {
	findings = withItemKeys(findings, "")
	tree := buildForest(findings)
	led := Ledger{Exclusive: map[string]int64{}}
	for fi, f := range findings {
		for ii, it := range f.Items {
			own := it.Bytes
			for _, c := range tree.children[itemRef{fi, ii}] {
				own -= itemAt(findings, c).Bytes
			}
			own = max(own, 0)
			led.Exclusive[ItemID(f.Rule.ID, it.Key)] += own
			led.Totals.add(f.Rule, own)
			if it.Partial {
				led.Totals.Partial++
			}
		}
	}
	return led
}

// Share is the bytes only f's items hold: their exclusive shares, each
// distinct item ID once (items that share an ID share one entry).
func (l Ledger) Share(f Finding) int64 {
	seen := map[string]bool{}
	var n int64
	for _, it := range f.Items {
		id := ItemID(f.Rule.ID, it.Key)
		if !seen[id] {
			seen[id] = true
			n += l.Exclusive[id]
		}
	}
	return n
}

// add counts n bytes of rule r's items in the bucket the rule implies.
// Phantom space comes first: macOS manages it whatever command the
// rule carries.
func (t *Totals) add(r Rule, n int64) {
	switch {
	case r.Category == CategoryPhantomSpace:
		t.MacOSManaged += n
	case !r.Risk.Actionable():
		t.ShownOnly += n
	case len(r.NativeCommand) > 0 || r.Prune != nil:
		t.FreesNow += n
	default:
		t.AfterTrash += n
	}
}
