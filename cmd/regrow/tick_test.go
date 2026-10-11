package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

const gib = int64(1) << 30

func hasLine(lines []string, parts ...string) bool {
	for _, line := range lines {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(line, p)
		}
		if all {
			return true
		}
	}
	return false
}

func TestTickLines(t *testing.T) {
	days := 2.44
	sample := headroom.Sample{At: time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC),
		Total: 460 * gib, Free: 38 * gib, Purgeable: 23 * gib, SwapUsed: 5 * gib}

	with := tickLines(headroom.Tick{Sample: sample, DaysToFull: &days,
		Alerts: []headroom.Alert{{Kind: headroom.AlertFreeBelow, Message: "Free space is below 50.0 GiB: 38.0 GiB left."}},
		Pruned: &headroom.PruneResult{RuleID: "go-build-cache", Skipped: "a build is running (compile); deferred since 12:15"}}, false, true)
	for _, want := range [][]string{
		{"free 38.0 GiB of 460.0 GiB", "purgeable 23.0 GiB", "swap 5.0 GiB"},
		{"Days to full: 2.4"},
		{"ALERT", "below 50.0 GiB"},
		{"Prune go-build-cache: not run", "a build is running (compile)"},
	} {
		if !hasLine(with, want...) {
			t.Errorf("tick output needs a line with %q, got:\n%s", want, strings.Join(with, "\n"))
		}
	}

	without := tickLines(headroom.Tick{Sample: sample}, false, false)
	if len(without) != 2 || !hasLine(without, "Days to full: no forecast") {
		t.Errorf("a tick with no forecast, alert or prune prints two lines, got:\n%s", strings.Join(without, "\n"))
	}

	noHistory := tickLines(headroom.Tick{Sample: sample,
		Pruned: &headroom.PruneResult{RuleID: "go-build-cache", Run: "r1", Files: 3, Bytes: gib}}, true, true)
	for _, want := range [][]string{
		{"No forecast or alerts", "history cannot be read"},
		{"Autotrim judged headroom by the 50.0 GiB line alone"},
		{"Prune go-build-cache: deleted 1.0 GiB in 3 entries"},
	} {
		if !hasLine(noHistory, want...) {
			t.Errorf("a tick without its history needs a line with %q, got:\n%s", want, strings.Join(noHistory, "\n"))
		}
	}
	if hasLine(noHistory, "Days to full: no forecast (it needs") {
		t.Errorf("without its history the tick must not blame a short history, got:\n%s", strings.Join(noHistory, "\n"))
	}
	if hasLine(tickLines(headroom.Tick{Sample: sample}, true, false), "Autotrim") {
		t.Error("without --autotrim the tick says nothing about autotrim")
	}
}

func TestPruneResultLinesKeepDeletedBytesApartFromFreeSpace(t *testing.T) {
	lines := pruneResultLines(headroom.PruneResult{RuleID: "go-build-cache", Run: "20261004-123000-abc123",
		Files: 5210, Bytes: 4 * gib, FreeBefore: 38 * gib, FreeAfter: 38 * gib, Error: "go-build-cache: exit status 1"})
	for _, want := range [][]string{
		{"deleted 4.0 GiB in 5210 entries", "20261004-123000-abc123"},
		{"Free space", "38.0 GiB before, 38.0 GiB after"},
		{"failed:", "exit status 1"},
	} {
		if !hasLine(lines, want...) {
			t.Errorf("prune output needs a line with %q, got:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func TestPreviewLines(t *testing.T) {
	rule := engine.Rule{ID: "go-build-cache", Title: "Go build cache",
		Prune: &engine.Prune{MinAge: engine.Duration(48 * time.Hour), KeepUnder: 15 << 30}}
	cutoff := time.Date(2026, 10, 2, 9, 59, 59, 0, time.UTC)
	survey := engine.PruneSurvey{Cache: "/home/dev/ca[che]/go-build", CacheFiles: 85000, CacheBytes: 56 * gib,
		Cutoff: cutoff, Files: 60000, Bytes: 41 * gib}
	action := engine.Action{RuleID: "go-build-cache", Kind: engine.ActionPrune, Path: survey.Cache, Bytes: survey.Bytes,
		Command: engine.PruneCommand(survey.Cache, cutoff)}

	lines := previewLines(rule, autopilot.Preview{Plan: engine.Plan{Actions: []engine.Action{action}}, Survey: survey, Running: []string{"compile"}})
	for _, want := range [][]string{
		{"Go build cache", "/home/dev/ca[che]/go-build"},
		{"56.0 GiB in 85000 entries"},
		{"used in the last 48h", "an hour of entries at a time, to about 15.0 GiB"},
		{"DRY RUN", "nothing executed"},
		// The path, the pattern and the date are quoted, so the line can be
		// pasted: in double quotes the shell turns \\[ back into the \[
		// that find needs to match the bracket literally.
		{"[prune]", "41.0 GiB", "no undo",
			`/usr/bin/find "/home/dev/ca[che]/go-build" -ignore_readdir_race -mindepth 2 -maxdepth 2 -type f ` +
				`-path "/home/dev/ca\\[che\\]/go-build/[0123456789abcdef][0123456789abcdef]/*-[ad]" ` +
				`! -newermt "2026-10-02 09:59:59 UTC" -delete`},
		{"Would delete about 41.0 GiB in 60000 entries", "leaving about 15.0 GiB"},
		{"no Trash, no undo"},
		{"A build is running (compile)"},
		{"regrow prune go-build --yes"},
	} {
		if !hasLine(lines, want...) {
			t.Errorf("preview needs a line with %q, got:\n%s", want, strings.Join(lines, "\n"))
		}
	}

	refused := previewLines(rule, autopilot.Preview{Plan: engine.Plan{Skipped: []engine.Skip{{RuleID: rule.ID, Reason: "the cache is 12.0 GiB, within the 15.0 GiB it may keep"}}},
		Survey: engine.PruneSurvey{Cache: survey.Cache, CacheFiles: 9000, CacheBytes: 12 * gib}})
	if !hasLine(refused, "Nothing to prune: the cache is 12.0 GiB") || hasLine(refused, "--yes") || hasLine(refused, "DRY RUN") {
		t.Errorf("a skipped plan says why and offers nothing to run, got:\n%s", strings.Join(refused, "\n"))
	}
}

func TestPruneTakesFlagsAfterTheName(t *testing.T) {
	tests := []struct {
		args      []string
		wantNames []string
		wantYes   bool
		wantJSON  bool
	}{
		{[]string{"prune", "go-build"}, []string{"go-build"}, false, false},
		{[]string{"prune", "go-build", "--yes"}, []string{"go-build"}, true, false},
		{[]string{"prune", "--yes", "go-build"}, []string{"go-build"}, true, false},
		{[]string{"prune", "--yes", "go-build", "--json"}, []string{"go-build"}, true, true},
		{[]string{"prune", "go-build", "extra", "--yes"}, []string{"go-build", "extra"}, true, false},
	}
	for _, tt := range tests {
		cmd, opts, names, help, err := parseArgs(tt.args)
		if err != nil || cmd != "prune" || help {
			t.Fatalf("parseArgs(%q) = %q help=%v err=%v", tt.args, cmd, help, err)
		}
		if strings.Join(names, " ") != strings.Join(tt.wantNames, " ") || opts.yes != tt.wantYes || opts.asJSON != tt.wantJSON {
			t.Errorf("parseArgs(%q) = %q yes=%v json=%v; want %q yes=%v json=%v",
				tt.args, names, opts.yes, opts.asJSON, tt.wantNames, tt.wantYes, tt.wantJSON)
		}
	}
	if _, _, _, _, err := parseArgs([]string{"prune", "go-build", "--frobnicate"}); err == nil || !strings.Contains(err.Error(), "regrow help") {
		t.Errorf("an unknown flag must point at `regrow help`, got %v", err)
	}
	if _, _, _, help, err := parseArgs([]string{"prune", "go-build", "--help"}); err != nil || !help {
		t.Errorf("--help after the name must print help, got help=%v err=%v", help, err)
	}
	// Other commands keep flags-before-ids: `clean id --yes` must not
	// turn into a confirmed run.
	if _, opts, ids, _, err := parseArgs([]string{"clean", "go-build-cache", "--yes"}); err != nil || opts.yes || len(ids) != 2 {
		t.Errorf("clean must not read flags after ids: yes=%v ids=%q err=%v", opts.yes, ids, err)
	}
}

func TestPruneResultLinesWhenNothingRan(t *testing.T) {
	lines := pruneResultLines(headroom.PruneResult{RuleID: "go-build-cache"})
	if len(lines) != 1 || lines[0] != "Prune go-build-cache: not run." {
		t.Errorf("a prune that never ran must not report bytes or free space, got %q", lines)
	}
}

func TestStillLockedLines(t *testing.T) {
	rule := engine.Rule{ID: "go-build-cache"}
	locked := stillLockedLines(rule, autopilot.Gate(nil, rule))
	if len(locked) != 1 || !strings.Contains(locked[0], "Autotrim stays locked") || !strings.Contains(locked[0], "regrow prune go-build --yes") {
		t.Errorf("a skipped manual prune with the gate shut must say so, got %q", locked)
	}
	open := []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventStart, RuleID: rule.ID, Kind: "prune"},
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: rule.ID},
	}
	if got := stillLockedLines(rule, autopilot.Gate(open, rule)); got != nil {
		t.Errorf("with the gate open there is nothing to say, got %q", got)
	}
}

func TestHistoryCountsWhatAPruneMeasured(t *testing.T) {
	entries := []oplog.Entry{
		{Event: oplog.EventStart, Kind: "trash", Bytes: 100},
		{Event: oplog.EventDone},
		{Event: oplog.EventStart, Kind: "prune", Bytes: 41 << 30},
		{Event: oplog.EventDone, Pruned: &oplog.Pruned{Files: 3, Bytes: 7 << 30}},
		{Event: oplog.EventStart, Kind: "prune", Bytes: 5 << 30},
		{Event: oplog.EventFail, Pruned: &oplog.Pruned{Files: 1, Bytes: 1 << 30}},
	}
	var total int64
	for _, e := range entries {
		total += entryBytes(e)
	}
	if want := int64(100) + 7<<30 + 1<<30; total != want {
		t.Errorf("history total = %d, want %d: a prune counts what it deleted, not its estimate", total, want)
	}
}
