package engine

import (
	"reflect"
	"testing"
)

func doctorRule(id string, flagAbove ByteSize) Rule {
	r := validRule()
	r.ID = id
	r.Doctor = &Doctor{FlagAbove: flagAbove, Story: "known bug"}
	return r
}

func phantomRule(id string) Rule {
	r := validRule()
	r.ID = id
	r.Category = CategoryPhantomSpace
	r.Risk = RiskSurfaceOnly
	return r
}

func TestDoctorRulesSelection(t *testing.T) {
	plain := validRule()
	catalog := []Rule{plain, doctorRule("hero", 1<<30), phantomRule("phantom")}
	got := DoctorRules(catalog)
	if len(got) != 2 || got[0].ID != "hero" || got[1].ID != "phantom" {
		t.Fatalf("DoctorRules selected %+v, want [hero phantom]", got)
	}
}

func TestBuildDoctorReport(t *testing.T) {
	hero := doctorRule("hero", 1<<30)
	phantom := phantomRule("phantom")
	findings := []Finding{
		{Rule: hero, Items: []Item{{Bytes: 2 << 30}}},
		{Rule: phantom, Items: []Item{{Bytes: 5}}},
	}
	causes := []CauseCheck{{RuleID: "hero", Cause: Cause{Check: "some-check"}, Verdict: VerdictFlagged, Detail: "seen"}}
	rep := BuildDoctorReport(findings, causes)
	if len(rep.Hero) != 1 || len(rep.Phantom) != 1 {
		t.Fatalf("report sections: hero=%d phantom=%d, want 1/1", len(rep.Hero), len(rep.Phantom))
	}
	if !reflect.DeepEqual(rep.Causes, causes) {
		t.Fatalf("report causes = %+v, want the rows as checked", rep.Causes)
	}
}

// A rule that carries only causes is not scanned by doctor: its check
// reads settings, and sizing the rule's target would cost a walk (the
// Go build cache) or a daemon call (the docker build cache).
func TestDoctorRulesLeaveCauseOnlyRulesUnscanned(t *testing.T) {
	owner := validRule()
	owner.ID = "owner"
	owner.Causes = []Cause{{Check: "some-check", Title: "t", Story: "s", Fix: []string{"f"}}}
	if got := DoctorRules([]Rule{owner, doctorRule("hero", 1<<30)}); len(got) != 1 || got[0].ID != "hero" {
		t.Fatalf("DoctorRules selected %+v, want [hero]", got)
	}
}

func TestHeroVerdict(t *testing.T) {
	hero := doctorRule("hero", 1<<30)
	tests := []struct {
		name  string
		items []Item
		err   string
		want  Verdict
	}{
		{"complete and over the line", []Item{{Bytes: 2 << 30}}, "", VerdictFlagged},
		// The threshold is "grew past", not "reached": a normal cache
		// sitting at its round number must not scream about a bug.
		{"complete and at the line", []Item{{Bytes: 1 << 30}}, "", VerdictNormal},
		{"absent target", nil, "", VerdictNormal},
		{"lower bound already over the line", []Item{{Bytes: 2 << 30, Partial: true}}, "", VerdictFlagged},
		{"lower bound under the line", []Item{{Bytes: 100 << 20, Partial: true}}, "", VerdictUnknown},
		{"unreadable", []Item{{Partial: true}}, "", VerdictUnknown},
		{"rule failed", nil, "scan cancelled", VerdictUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := BuildDoctorReport([]Finding{{Rule: hero, Items: tt.items, Err: tt.err}}, nil)
			if got := rep.Hero[0].Verdict; got != tt.want {
				t.Fatalf("verdict = %q, want %q", got, tt.want)
			}
		})
	}
}
