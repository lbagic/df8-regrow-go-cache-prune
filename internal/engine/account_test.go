package engine

import (
	"maps"
	"testing"
)

func trashRule(id string) Rule {
	return Rule{ID: id, Category: "test", Risk: RiskCaution}
}

func nativeRule(id string) Rule {
	return Rule{ID: id, Category: "test", Risk: RiskSafe, NativeCommand: Argv{"tool", "clean"}}
}

// Exclusive subtracts direct children only: A − B, not A − B − C.
func TestAccountThreeLevelNesting(t *testing.T) {
	findings := []Finding{
		{Rule: trashRule("a"), Items: []Item{{Path: "/Users/t/a", Bytes: 100}}},
		{Rule: trashRule("b"), Items: []Item{{Path: "/Users/t/a/b", Bytes: 60}}},
		{Rule: trashRule("c"), Items: []Item{{Path: "/Users/t/a/b/c", Bytes: 25}}},
	}
	led := Account(findings)
	want := map[string]int64{"a//Users/t/a": 40, "b//Users/t/a/b": 35, "c//Users/t/a/b/c": 25}
	if !maps.Equal(led.Exclusive, want) {
		t.Fatalf("exclusive = %v, want %v", led.Exclusive, want)
	}
	if led.Totals.AfterTrash != 100 {
		t.Errorf("after-trash total = %d, want the outer item's 100, each byte once", led.Totals.AfterTrash)
	}
}

// On equal paths the later rule is the child, so it holds the bytes
// and the earlier rule's item holds none.
func TestAccountEqualPaths(t *testing.T) {
	findings := []Finding{
		{Rule: nativeRule("earlier"), Items: []Item{{Path: "/Users/t/same", Bytes: 50}}},
		{Rule: trashRule("later"), Items: []Item{{Path: "/Users/t/same/", Bytes: 50}}},
	}
	led := Account(findings)
	if got := led.Exclusive["earlier//Users/t/same"]; got != 0 {
		t.Errorf("earlier rule's share = %d, want 0", got)
	}
	if got := led.Exclusive["later//Users/t/same/"]; got != 50 {
		t.Errorf("later rule's share = %d, want 50", got)
	}
	if got := (Totals{AfterTrash: 50}); led.Totals != got {
		t.Errorf("totals = %+v, want %+v", led.Totals, got)
	}
}

// Two actionable rules, one inside the other, land in two buckets
// without double counting: the steward command's bytes leave the
// Trash rule's total.
func TestAccountTotalsAcrossActionableRules(t *testing.T) {
	findings := []Finding{
		{Rule: trashRule("caches"), Items: []Item{{Path: "/Users/t/cache", Bytes: 100}}},
		{Rule: nativeRule("go-build"), Items: []Item{{Path: "/Users/t/cache/go-build", Bytes: 70}}},
		{Rule: nativeRule("docker"), Items: []Item{{Label: "build cache", Bytes: 9}}},
		{Rule: Rule{ID: "backups", Risk: RiskSurfaceOnly}, Items: []Item{{Path: "/Users/t/backups", Bytes: 5}}},
		{Rule: Rule{ID: "snapshots", Category: CategoryPhantomSpace, Risk: RiskCaution, NativeCommand: Argv{"tmutil", "delete"}},
			Items: []Item{{Label: "snap", Bytes: 3}}},
		{Rule: trashRule("gated"), Items: []Item{{Path: "/Users/t/gated", Partial: true}}},
	}
	want := Totals{FreesNow: 79, AfterTrash: 30, ShownOnly: 5, MacOSManaged: 3, Partial: 1}
	if got := Account(findings).Totals; got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}

// A partial parent can measure less than a child it could not fully
// read; it holds nothing rather than a negative share.
func TestAccountNeverNegative(t *testing.T) {
	findings := []Finding{
		{Rule: trashRule("outer"), Items: []Item{{Path: "/Users/t/o", Bytes: 10, Partial: true}}},
		{Rule: nativeRule("inner"), Items: []Item{{Path: "/Users/t/o/i", Bytes: 50}}},
	}
	led := Account(findings)
	if got := led.Exclusive["outer//Users/t/o"]; got != 0 {
		t.Errorf("partial parent share = %d, want 0", got)
	}
	if want := (Totals{FreesNow: 50, Partial: 1}); led.Totals != want {
		t.Errorf("totals = %+v, want %+v", led.Totals, want)
	}
}

// Items that share an ID share one Exclusive entry; a finding's share
// counts that entry once, not once per item.
func TestLedgerShareCountsASharedIDOnce(t *testing.T) {
	f := Finding{Rule: trashRule("twice"), Items: []Item{
		{Path: "/Users/t/x", Key: "~/x", Bytes: 10},
		{Path: "/Users/t/x", Key: "~/x", Bytes: 10},
	}}
	led := Account([]Finding{f})
	if got := led.Exclusive["twice/~/x"]; got != 10 {
		t.Fatalf("exclusive = %d, want 10", got)
	}
	if got := led.Share(f); got != 10 {
		t.Errorf("share = %d, want 10: one target, counted once", got)
	}
}
