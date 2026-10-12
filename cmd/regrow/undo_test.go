package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// moved writes a moved copy at <dir>/moved/<name> unless gone, and
// returns its done line: what a Trash or staging move journals.
func moved(t *testing.T, dir, run string, seq int, at time.Time, name string, method trash.Method, gone bool) oplog.Entry {
	t.Helper()
	to := filepath.Join(dir, "moved", name)
	if !gone {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return oplog.Entry{Time: at, Run: run, Seq: seq, Event: oplog.EventDone, RuleID: "fixture",
		Receipt: &trash.Receipt{Original: filepath.Join(dir, "home", name), To: to, Method: method}}
}

// A move whose copy was emptied from the Trash, or taken out of regrow
// staging, is not counted as restorable, and the line says why.
func TestHistoryLinesCountOnlyCopiesOnDisk(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	runs := oplog.Runs([]oplog.Entry{
		moved(t, dir, "r1", 1, at, "emptied", trash.MethodFinder, true),
		moved(t, dir, "r2", 1, at.Add(time.Hour), "kept", trash.MethodFinder, false),
		moved(t, dir, "r2", 2, at.Add(time.Hour), "lost", trash.MethodStaging, true),
		moved(t, dir, "r3", 1, at.Add(2*time.Hour), "emptied-too", trash.MethodFinder, true),
		moved(t, dir, "r3", 2, at.Add(2*time.Hour), "lost-too", trash.MethodStaging, true),
	})
	lines := historyLines(runs)
	if !strings.HasSuffix(lines[0], "(nothing restorable: 1 gone from the Trash)") {
		t.Errorf("a run whose Trash copy is gone must say so, got %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "(1 restorable, 1 gone from regrow staging)") {
		t.Errorf("a run with one copy left of two must count one, got %q", lines[1])
	}
	if !strings.HasSuffix(lines[2], "(nothing restorable: 1 gone from the Trash, 1 gone from regrow staging)") {
		t.Errorf("a run must name both places its copies are gone from, got %q", lines[2])
	}
}

// Once the Trash is emptied, Finder gives the next item of the same
// name the same Trash path. The older run's copy is gone, whatever now
// sits there, and undoing that run must not move the newer run's item.
func TestUndoNeverRestoresAReusedCopy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	to := filepath.Join(dir, "Trash", "node_modules")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	done := func(run string, at time.Time, project string) oplog.Entry {
		return oplog.Entry{Time: at, Run: run, Seq: 1, Event: oplog.EventDone, RuleID: "fixture",
			Receipt: &trash.Receipt{Original: filepath.Join(dir, project, "node_modules"), To: to, Method: trash.MethodFinder}}
	}
	entries := []oplog.Entry{done("r1", at, "a"), done("r2", at.Add(time.Hour), "b")}
	log, err := oplog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	lines := historyLines(oplog.Runs(entries))
	if !strings.HasSuffix(lines[0], "(nothing restorable: 1 gone from the Trash)") || !strings.HasSuffix(lines[1], "(1 restorable)") {
		t.Errorf("want the older run gone and the newer restorable, got %q", lines[:2])
	}
	err = runUndo([]string{"r1"})
	if err == nil || !strings.Contains(err.Error(), "nothing left to restore in run r1") {
		t.Errorf("undo of the older run = %v, want it refused", err)
	}
	if _, err := os.Stat(to); err != nil {
		t.Errorf("the newer run's item left the Trash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "node_modules")); err == nil {
		t.Error("the newer run's item was restored to the older run's place")
	}
}

// A bare undo passes over a newer run whose copies are all gone to
// restore an older one, and naming the newer run refuses it.
func TestUndoSkipsARunWithNothingLeftOnDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	at := time.Now()
	older := moved(t, dir, "r1", 1, at, "older", trash.MethodStaging, false)
	newer := moved(t, dir, "r2", 1, at.Add(time.Hour), "newer", trash.MethodFinder, true)
	log, err := oplog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []oplog.Entry{older, newer} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(older.Receipt.Original); err != nil {
		t.Errorf("bare undo did not restore the older run: %v", err)
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == oplog.EventUndo && e.Run != "r1" {
			t.Errorf("bare undo touched run %s: %+v", e.Run, e)
		}
	}

	err = runUndo([]string{"r2"})
	if err == nil || !strings.Contains(err.Error(), "nothing left to restore in run r2: 1 gone from the Trash") {
		t.Errorf("undo of a run whose copies are gone = %v, want it refused with the reason", err)
	}
}

// An item the user trashed by hand, with the moved copy's name, is not
// that copy: history counts nothing to restore and undo refuses the run.
func TestUndoNeverRestoresAnItemTrashedByHand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	original := filepath.Join(dir, "a", "node_modules")
	if err := os.MkdirAll(original, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &trash.Mover{Home: "/nope", StagingDir: filepath.Join(dir, "staging"),
		RunFinder: func(context.Context, string) (string, error) { return "", errors.New("no Finder") }}
	receipt, err := m.Move(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(receipt.To, filepath.Join(dir, "emptied")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(receipt.To, "from-c"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := oplog.Entry{Time: time.Now(), Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "fixture", Receipt: &receipt}
	log, err := oplog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(entry); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if line := historyLines(oplog.Runs([]oplog.Entry{entry}))[0]; !strings.HasSuffix(line, "(nothing restorable: 1 gone from regrow staging)") {
		t.Errorf("history = %q, want nothing restorable", line)
	}
	if err := runUndo([]string{"r1"}); err == nil || !strings.Contains(err.Error(), "nothing left to restore in run r1") {
		t.Errorf("undo = %v, want it refused", err)
	}
	if _, err := os.Stat(filepath.Join(receipt.To, "from-c")); err != nil {
		t.Errorf("the item trashed by hand left its place: %v", err)
	}
}
