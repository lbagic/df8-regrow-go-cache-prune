package engine

import (
	"encoding/json"
	"maps"
	"testing"
)

func trashRule(id string) Rule {
	return Rule{ID: id, Category: "test", Risk: RiskCaution}
}

func nativeRule(id string) Rule {
	return Rule{ID: id, Category: "test", Risk: RiskSafe, NativeCommand: Argv{"tool", "clean"}}
}

// volumeRule removes a docker volume after exporting it, as the
// catalog's volume rules do.
func volumeRule(id string) Rule {
	return Rule{ID: id, Category: "test", Risk: RiskCaution, NativeCommand: Argv{"docker", "volume", "rm", "{arg}"}, PreAction: PreActionVolumeExport}
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
		{Rule: volumeRule("docker-volumes"), Items: []Item{{Label: "dune_db", Bytes: 11}}},
	}
	want := Totals{FreesNow: 79, AfterTrash: 30, Exported: 11, ShownOnly: 5, MacOSManaged: 3, Partial: 1}
	if got := Account(findings).Totals; got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}

// A rule's bucket is the Totals field its bytes land in, under that
// field's JSON name: a peer reads one from the other.
func TestBucketNamesTheTotalARuleCountsIn(t *testing.T) {
	tests := []struct {
		rule Rule
		want string
	}{
		{nativeRule("steward"), BucketFreesNow},
		{Rule{ID: "pruned", Risk: RiskSafe, Prune: &Prune{}}, BucketFreesNow},
		{trashRule("trashed"), BucketAfterTrash},
		{volumeRule("volumes"), BucketExported},
		{Rule{ID: "volumes-kept", Risk: RiskSurfaceOnly, NativeCommand: Argv{"docker", "volume", "rm", "{arg}"}, PreAction: PreActionVolumeExport}, BucketShownOnly},
		{Rule{ID: "shown", Risk: RiskSurfaceOnly}, BucketShownOnly},
		{Rule{ID: "phantom", Category: CategoryPhantomSpace, Risk: RiskCaution, NativeCommand: Argv{"tmutil", "delete"}}, BucketMacOSManaged},
		{Rule{ID: "phantom-shown", Category: CategoryPhantomSpace, Risk: RiskSurfaceOnly}, BucketMacOSManaged},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		if got := Bucket(tt.rule); got != tt.want {
			t.Errorf("Bucket(%s) = %q, want %q", tt.rule.ID, got, tt.want)
		}
		seen[tt.want] = true

		var totals Totals
		totals.add(tt.rule, 7)
		raw, err := json.Marshal(totals)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]int64
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for name, n := range fields {
			var want int64
			if name == tt.want {
				want = 7
			}
			if n != want {
				t.Errorf("%s: totals field %s = %d, want %d: the bytes land in the rule's bucket alone", tt.rule.ID, name, n, want)
			}
		}
		if _, ok := fields[tt.want]; !ok {
			t.Errorf("%s: Totals has no JSON field %q", tt.rule.ID, tt.want)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("the table walks %d buckets, want all five", len(seen))
	}
}

// The catalog's volume rules export before they remove, so their bytes
// are in the exported total, and a kept volume stays shown only.
func TestCatalogVolumeRulesCountAsExported(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"docker-volumes-named": BucketExported,
		"docker-volumes-anon":  BucketExported,
		"docker-volumes-kept":  BucketShownOnly,
	}
	for _, r := range catalog {
		if w, ok := want[r.ID]; ok {
			if got := Bucket(r); got != w {
				t.Errorf("Bucket(%s) = %q, want %q", r.ID, got, w)
			}
			delete(want, r.ID)
		}
		if r.PreAction == PreActionVolumeExport && Bucket(r) != BucketExported && r.Risk.Actionable() {
			t.Errorf("%s exports before it removes but counts in %q", r.ID, Bucket(r))
		}
	}
	if len(want) != 0 {
		t.Errorf("rules missing from the catalog: %v", want)
	}
}

// A scan's ledger and the plan of the same selection put a volume's
// bytes in the same total.
func TestPlanAndLedgerCountAVolumeExportAlike(t *testing.T) {
	rule := volumeRule("docker-volumes")
	findings := []Finding{{Rule: rule, Items: []Item{{Label: "dune_db", Arg: "dune_db", Bytes: 30}, {Label: "scratch", Arg: "scratch", Bytes: 12}}}}
	plan := BuildPlan(Host{OS: "darwin", Home: "/Users/t"}, findings, map[string]bool{rule.ID: true})
	if len(plan.Actions) != 2 {
		t.Fatalf("plan = %+v, want one action per volume", plan)
	}
	if got, want := plan.Totals(), Account(findings).Totals; got != want || got.Exported != 42 || got.FreesNow != 0 {
		t.Fatalf("plan totals = %+v, ledger totals = %+v, want both 42 exported and nothing freed now", got, want)
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
