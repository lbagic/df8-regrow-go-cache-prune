package main

import (
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/oplog"
)

func TestRunBytesCountsOnlyFinishedActions(t *testing.T) {
	trashStart := func(seq int, bytes int64) oplog.Entry {
		return oplog.Entry{Run: "r1", Seq: seq, Event: oplog.EventStart, Kind: "trash", Bytes: bytes}
	}
	pruneStart := func(seq int, bytes int64) oplog.Entry {
		return oplog.Entry{Run: "r1", Seq: seq, Event: oplog.EventStart, Kind: "prune", Bytes: bytes}
	}
	done := func(seq int) oplog.Entry { return oplog.Entry{Run: "r1", Seq: seq, Event: oplog.EventDone} }
	fail := func(seq int) oplog.Entry {
		return oplog.Entry{Run: "r1", Seq: seq, Event: oplog.EventFail, Error: "finder said no"}
	}
	pruned := func(e oplog.Entry, bytes int64) oplog.Entry {
		e.Pruned = &oplog.Pruned{Files: 3, Bytes: bytes}
		return e
	}

	tests := []struct {
		name    string
		entries []oplog.Entry
		want    int64
	}{
		{"a done action counts its planned bytes", []oplog.Entry{trashStart(1, 100), done(1)}, 100},
		{
			"a failed action counts nothing beside a done one",
			[]oplog.Entry{trashStart(1, 100), done(1), trashStart(2, 50), fail(2)},
			100,
		},
		{
			"an action with no done line counts nothing: the run was cut short",
			[]oplog.Entry{trashStart(1, 100), done(1), trashStart(2, 50)},
			100,
		},
		{"a run cut short at its first start line counts nothing", []oplog.Entry{trashStart(1, 100)}, 0},
		{
			"a done prune counts what it measured, not its estimate",
			[]oplog.Entry{trashStart(1, 100), done(1), pruneStart(2, 41*gib), pruned(done(2), 7*gib)},
			100 + 7*gib,
		},
		{
			"a failed prune counts nothing, whatever it measured",
			[]oplog.Entry{pruneStart(1, 5*gib), pruned(fail(1), gib)},
			0,
		},
		{"a prune cut short counts nothing", []oplog.Entry{pruneStart(1, 5*gib)}, 0},
		{"a done prune with no measurement counts nothing", []oplog.Entry{pruneStart(1, 5*gib), done(1)}, 0},
		{
			"an undo line adds nothing",
			[]oplog.Entry{trashStart(1, 100), done(1), {Run: "r1", Seq: 1, Event: oplog.EventUndo}},
			100,
		},
		{
			"a done line pairs with the start line of its own seq",
			[]oplog.Entry{trashStart(1, 100), trashStart(2, 50), fail(1), done(2)},
			50,
		},
	}
	for _, tt := range tests {
		runs := oplog.Runs(tt.entries)
		if len(runs) != 1 {
			t.Fatalf("%s: %d runs, want 1", tt.name, len(runs))
		}
		if got := runBytes(runs[0]); got != tt.want {
			t.Errorf("%s: runBytes = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestHistoryLinesSayWhatTheSizeCounts(t *testing.T) {
	runs := oplog.Runs([]oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventStart, Kind: "trash", Bytes: 3 * gib},
		{Run: "r1", Seq: 1, Event: oplog.EventDone},
		{Run: "r1", Seq: 2, Event: oplog.EventStart, Kind: "trash", Bytes: 5 * gib},
		{Run: "r1", Seq: 2, Event: oplog.EventFail, Error: "finder said no"},
	})
	lines := historyLines(runs)
	if len(lines) != 3 {
		t.Fatalf("want one line per run and a two-line note, got %q", lines)
	}
	if !strings.Contains(lines[0], "3.0 GiB cleaned") || strings.Contains(lines[0], "8.0 GiB") {
		t.Errorf("a run's size must be labelled and count only the done action, got %q", lines[0])
	}
	for _, want := range []string{"Trash", "failed", "cut-short"} {
		if note := strings.Join(lines[1:], " "); !strings.Contains(note, want) {
			t.Errorf("the note must say what the size counts (%q missing), got %q", want, note)
		}
	}
}
