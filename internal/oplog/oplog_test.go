package oplog

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/trash"
)

func t0(sec int) time.Time { return time.Date(2026, 7, 13, 12, 0, sec, 0, time.UTC) }

func TestAppendReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "oplog.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{Time: t0(0), Run: "r1", Seq: 1, Event: EventStart, RuleID: "npm-cache", Kind: "trash", Path: "/x", Bytes: 42, Command: []string{"osascript", "-e", "s"}},
		{Time: t0(1), Run: "r1", Seq: 1, Event: EventDone, RuleID: "npm-cache", Receipt: &trash.Receipt{Original: "/x", To: "/t/x", Method: trash.MethodFinder}},
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Event != EventStart || got[1].Receipt == nil || got[1].Receipt.To != "/t/x" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestReadMissingFileIsEmptyHistory(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Fatalf("missing journal must read as empty, got %v, %v", got, err)
	}
}

func TestReadCorruptLineFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oplog.jsonl")
	if err := os.WriteFile(path, []byte("{\"run\":\"r1\"}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("corrupt journal must not be silently skipped")
	}
}

func TestAppendAfterACutShortLineKeepsTheJournalReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oplog.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{Time: t0(0), Run: "r1", Seq: 1, Event: EventStart, RuleID: "go-build-cache", Kind: "prune"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// The done line hit a full disk halfway.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"time":"2026-07-13T12:00:01Z","run":"r1","seq":1,"event":"do`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path); err != nil || len(got) != 1 {
		t.Fatalf("a cut-short last line must not lock the journal: %d entries, %v", len(got), err)
	}

	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{Time: t0(2), Run: "r2", Seq: 1, Event: EventStart, RuleID: "npm-cache"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || len(got) != 2 || got[1].Run != "r2" {
		t.Fatalf("the line appended after a fragment must read back whole: %+v, %v", got, err)
	}
}

func TestReadSkipsOnlyCutShortLines(t *testing.T) {
	good := `{"time":"2026-07-13T12:00:00Z","run":"r1","seq":1,"event":"start"}`
	head := `{"time":"2026-07-13T12:00:01Z","run":"r1","seq":1,"event":"done"`
	tests := []struct {
		name   string
		middle string
		ok     bool
	}{
		{"cut inside a string", head[:len(head)-3], true},
		{"cut after a comma", head + `,`, true},
		{"cut where a value starts", head + `,"receipt":`, true},
		{"cut inside the first key", `{"ti`, true},
		{"just the brace", `{`, true},
		{"blank line", ``, true},
		{"not json", `not json`, false},
		{"zeros from a crash", "\x00\x00\x00\x00", false},
		{"two lines glued, cut inside a key", `{"time":"2026-07-13T12:00:01Z","se` + good, false},
		// The whole entry parses as the fragment's receipt, and the
		// line still ends early: without the second-entry check this
		// one would vanish.
		{"two lines glued, cut where a value starts", head + `,"receipt":` + good, false},
		{"two lines glued, cut inside the command array", head + `,"command":[` + good, false},
		{"whole object, wrong type", `{"time":"2026-07-13T12:00:01Z","seq":"one"}`, false},
		{"another object cut short", `{"run":"r1","seq":2,"event":"st`, false},
		{"array cut short", `[1,2`, false},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "oplog.jsonl")
		if err := os.WriteFile(path, []byte(good+"\n"+tt.middle+"\n"+good+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Read(path)
		if tt.ok && (err != nil || len(got) != 2) {
			t.Errorf("%s: want the two whole lines, got %d entries, %v", tt.name, len(got), err)
		}
		if !tt.ok && err == nil {
			t.Errorf("%s: corruption must fail loudly, got %d entries", tt.name, len(got))
		}
	}
}

func TestAppendWaitsForAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oplog.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	// Another process holds the journal and is cut short mid-line.
	other, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- l.Append(Entry{Time: t0(2), Run: "r2", Seq: 1, Event: EventStart, RuleID: "go-build-cache", Kind: "prune"})
	}()
	select {
	case err := <-done:
		t.Fatalf("Append must wait while another writer holds the journal, returned %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := other.WriteString(`{"time":"2026-07-13T12:00:01Z","run":"r1","seq":1,"event":"do`); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || len(got) != 1 || got[0].Run != "r2" {
		t.Fatalf("the waiting entry must start on a fresh line after the fragment: %+v, %v", got, err)
	}
}

func TestRunsGroupsAndOrders(t *testing.T) {
	entries := []Entry{
		{Time: t0(5), Run: "r2", Seq: 1, Event: EventStart},
		{Time: t0(0), Run: "r1", Seq: 1, Event: EventStart},
		{Time: t0(1), Run: "r1", Seq: 1, Event: EventDone},
	}
	runs := Runs(entries)
	if len(runs) != 2 || runs[0].ID != "r1" || runs[1].ID != "r2" || len(runs[0].Entries) != 2 {
		t.Fatalf("Runs = %+v", runs)
	}
}

func TestUndoableReverseOrderAndExcludesUndone(t *testing.T) {
	rc := func(p string) *trash.Receipt {
		return &trash.Receipt{Original: p, To: "/t" + p, Method: trash.MethodStaging}
	}
	r := Run{ID: "r1", Entries: []Entry{
		{Run: "r1", Seq: 1, Event: EventDone, Receipt: rc("/a")},
		{Run: "r1", Seq: 2, Event: EventDone, Receipt: rc("/b")},
		{Run: "r1", Seq: 3, Event: EventDone}, // native: no receipt, not undoable
		{Run: "r1", Seq: 4, Event: EventFail},
		{Run: "r1", Seq: 5, Event: EventDone, Receipt: rc("/c")},
		{Run: "r1", Seq: 6, Event: EventDone, Receipt: &trash.Receipt{
			Original: "docker volume vol", To: "/t/vol.tar", Method: trash.MethodExport,
		}}, // export tarball: recovery pointer, never auto-restored
		{Run: "r1", Seq: 2, Event: EventUndo}, // /b already restored
	}}
	got := r.Undoable()
	if len(got) != 2 || got[0].Receipt.Original != "/c" || got[1].Receipt.Original != "/a" {
		t.Fatalf("want [/c /a] (reverse order, /b undone, native and export skipped), got %+v", got)
	}

	// A failed undo attempt does not mark the action as undone.
	r.Entries = append(r.Entries, Entry{Run: "r1", Seq: 1, Event: EventUndo, Error: "locked"})
	if got := r.Undoable(); len(got) != 2 {
		t.Fatalf("failed undo must keep the action undoable, got %+v", got)
	}
}
