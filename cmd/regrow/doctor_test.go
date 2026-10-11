package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
)

func TestDoctorTextShowsThreeVerdicts(t *testing.T) {
	doc := func(id string, flag int64, items ...engine.Item) engine.Finding {
		return engine.Finding{
			Rule:  engine.Rule{ID: id, Title: id, Risk: engine.RiskSafe, Doctor: &engine.Doctor{FlagAbove: engine.ByteSize(flag), Story: "known bug"}},
			Items: items,
		}
	}
	rep := engine.BuildDoctorReport([]engine.Finding{
		doc("runaway", 1<<30, engine.Item{Bytes: 3 << 30, Partial: true}),
		doc("healthy", 1<<30, engine.Item{Bytes: 1 << 20}),
		doc("gated", 1<<30, engine.Item{Partial: true}),
		doc("half-read", 1<<30, engine.Item{Bytes: 1 << 20, Partial: true}),
	}, nil)
	var buf bytes.Buffer
	writeDoctorReport(&buf, rep)
	out := buf.String()
	for _, want := range [][]string{
		{"runaway", "≥ 3.0 GiB", "RUNAWAY"},
		{"✓", "healthy", "normal"},
		{"?", "gated", "unreadable", "unknown", "Full Disk Access may be needed"},
		{"?", "half-read", "≥ 1.0 MiB", "unknown", "some folders unreadable"},
		{"1 runaway bug(s) found"},
	} {
		if !lineWith(out, want...) {
			t.Errorf("no line with %q in:\n%s", want, out)
		}
	}
	if lineWith(out, "✓", "gated") || lineWith(out, "✓", "half-read") {
		t.Errorf("a partial check under the line must not read healthy:\n%s", out)
	}
}

func causeRow(check string, verdict engine.Verdict, detail string) engine.CauseCheck {
	return engine.CauseCheck{
		RuleID:  "owner",
		Verdict: verdict,
		Detail:  detail,
		Cause: engine.Cause{
			Check: check, Title: "Title of " + check, Story: "story of " + check,
			Fix: []string{"first step of " + check, "second step of " + check},
		},
	}
}

func TestDoctorTextCauseRows(t *testing.T) {
	rep := engine.BuildDoctorReport(nil, []engine.CauseCheck{
		causeRow("in-effect", engine.VerdictFlagged, "15 videos, 8.4 GiB"),
		causeRow("fine", engine.VerdictNormal, "capped at 4.0 GiB"),
		causeRow("unread", engine.VerdictUnknown, "settings unreadable"),
	})
	var buf bytes.Buffer
	writeDoctorReport(&buf, rep)
	out := buf.String()
	for _, want := range [][]string{
		{"FIX THE CAUSE", "changes nothing"},
		{"🚩", "Title of in-effect", "15 videos, 8.4 GiB"},
		{"story of in-effect"},
		{"fix:", "first step of in-effect"},
		{"second step of in-effect"},
		{"✓", "Title of fine", "capped at 4.0 GiB"},
		{"?", "Title of unread", "unknown", "settings unreadable"},
		{"1 cause(s) to fix above", "1 more could not be checked", "regrow changes no setting"},
	} {
		if !lineWith(out, want...) {
			t.Errorf("no line with %q in:\n%s", want, out)
		}
	}
	// A cause that is not in effect prints its row, not its fix.
	for _, absent := range []string{"story of fine", "step of fine", "story of unread", "step of unread", "nothing needs fixing"} {
		if strings.Contains(out, absent) {
			t.Errorf("output must not hold %q:\n%s", absent, out)
		}
	}
	if strings.Index(out, "HERO BUGS") > strings.Index(out, "FIX THE CAUSE") || strings.Index(out, "FIX THE CAUSE") > strings.Index(out, "PHANTOM SPACE") {
		t.Errorf("sections out of order:\n%s", out)
	}
}

func TestDoctorTextWithoutCauses(t *testing.T) {
	tests := []struct {
		name   string
		causes []engine.CauseCheck
		want   []string
		absent []string
	}{
		{"no rows declared", nil, []string{"nothing needs fixing"}, []string{"FIX THE CAUSE"}},
		{"every cause clear", []engine.CauseCheck{causeRow("fine", engine.VerdictNormal, "set")}, []string{"FIX THE CAUSE", "nothing needs fixing"}, []string{"cause(s) to fix"}},
		{"only unknown", []engine.CauseCheck{causeRow("unread", engine.VerdictUnknown, "unreadable")}, []string{"1 cause check(s) unknown above"}, []string{"cause(s) to fix", "nothing needs fixing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeDoctorReport(&buf, engine.BuildDoctorReport(nil, tt.causes))
			out := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("output must not hold %q:\n%s", absent, out)
				}
			}
		})
	}
}

func TestDoctorJSONCarriesCauseRows(t *testing.T) {
	raw, err := json.Marshal(engine.BuildDoctorReport(nil, []engine.CauseCheck{causeRow("in-effect", engine.VerdictFlagged, "seen")}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Causes []struct {
			RuleID  string `json:"rule_id"`
			Verdict string `json:"verdict"`
			Detail  string `json:"detail"`
			Cause   struct {
				Check string   `json:"check"`
				Fix   []string `json:"fix"`
			} `json:"cause"`
		} `json:"causes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Causes) != 1 {
		t.Fatalf("causes = %s", raw)
	}
	c := got.Causes[0]
	if c.RuleID != "owner" || c.Verdict != "flagged" || c.Detail != "seen" || c.Cause.Check != "in-effect" || len(c.Cause.Fix) != 2 {
		t.Errorf("cause row = %+v in %s", c, raw)
	}
}

// doctor scans only the rules with a doctor block (or in phantom
// space) and checks the causes of every rule: a rule that carries only
// causes gets its row without being scanned.
func TestDoctorReportChecksCausesOfUnscannedRules(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "runaway.log"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := []engine.Rule{
		{
			ID: "hero", Title: "Hero", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths:  map[string][]engine.PathEntry{"darwin": {{Path: "~/runaway.log"}}},
			Doctor: &engine.Doctor{FlagAbove: 1, Story: "known bug"},
		},
		{
			ID: "owner", Title: "Owner", Category: "docker", Risk: engine.RiskSafe,
			ToolQuery: "costly",
			Causes:    []engine.Cause{{Check: "limit-unset", Title: "Limit", Story: "why", Fix: []string{"set it"}}},
		},
	}
	var checked []string
	s := &scanner.Scanner{
		Host: engine.Host{OS: "darwin", Version: "15.7", Home: home},
		Queries: map[string]scanner.ToolQuery{
			"costly": func(context.Context) ([]engine.Item, error) {
				t.Error("a rule that carries only causes must not be scanned")
				return nil, nil
			},
		},
		CauseQueries: map[string]scanner.CauseQuery{
			"limit-unset": func(_ context.Context, r engine.Rule) (engine.Verdict, string) {
				checked = append(checked, r.ID)
				return engine.VerdictFlagged, "no limit"
			},
		},
	}
	rep := doctorReport(context.Background(), s, catalog)

	if len(rep.Hero) != 1 || rep.Hero[0].Finding.Rule.ID != "hero" || rep.Hero[0].Verdict != engine.VerdictFlagged {
		t.Errorf("hero section = %+v, want the hero rule flagged", rep.Hero)
	}
	if len(rep.Causes) != 1 || rep.Causes[0].RuleID != "owner" || rep.Causes[0].Verdict != engine.VerdictFlagged || rep.Causes[0].Detail != "no limit" {
		t.Errorf("cause section = %+v, want the owner rule's row as checked", rep.Causes)
	}
	if len(checked) != 1 || checked[0] != "owner" {
		t.Errorf("the check ran for %v, want the owner rule once", checked)
	}
}

// `regrow doctor` end to end on a fixture home: the scanner it builds
// can run a shipped check, and the row reaches both outputs.
func TestRunDoctorReportsCauses(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "daemon.json"), []byte(`{"builder": {"gc": {"enabled": false}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	host := engine.Host{OS: "darwin", Version: "15.7", Home: home, Root: home}
	catalog := []engine.Rule{{
		ID: "owner", Title: "Owner", Category: "docker", Risk: engine.RiskSafe,
		Paths:  map[string][]engine.PathEntry{"darwin": {{Path: "~/never-there"}}},
		Causes: []engine.Cause{{Check: "docker-build-cache-limit", Title: "Build-cache limit", Story: "why", Fix: []string{"switch it on"}}},
	}}

	var text bytes.Buffer
	if err := runDoctor(&text, host, catalog, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"🚩", "Build-cache limit", "switches build-cache garbage collection off"},
		{"fix:", "switch it on"},
		{"1 cause(s) to fix above"},
	} {
		if !lineWith(text.String(), want...) {
			t.Errorf("no line with %q in:\n%s", want, text.String())
		}
	}

	var raw bytes.Buffer
	if err := runDoctor(&raw, host, catalog, true); err != nil {
		t.Fatal(err)
	}
	var rep engine.DoctorReport
	if err := json.Unmarshal(raw.Bytes(), &rep); err != nil {
		t.Fatalf("%v in %s", err, raw.String())
	}
	if len(rep.Causes) != 1 || rep.Causes[0].RuleID != "owner" || rep.Causes[0].Verdict != engine.VerdictFlagged {
		t.Errorf("causes = %+v, want the owner's row flagged", rep.Causes)
	}
}
