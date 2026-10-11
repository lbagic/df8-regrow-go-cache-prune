// Package oplog is the append-only jsonl journal behind `regrow undo`
// and `regrow history`. Every action is journaled before it runs
// (ARCHITECTURE.md invariant 6); trash receipts recorded on completion
// are the undo contract.
package oplog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/jsonl"
	"github.com/lbagic/regrow/internal/trash"
)

// Event is the lifecycle stage a journal line records.
const (
	EventStart = "start" // written BEFORE the action runs (invariant 6)
	EventDone  = "done"  // action succeeded; trash actions carry the receipt
	EventFail  = "fail"  // action failed; Error says why
	EventUndo  = "undo"  // a done trash action was restored (or restore failed)
)

// Entry is one journal line. A run emits start→done/fail pairs per
// action, keyed by (Run, Seq); a later undo emits undo lines that
// reference the same keys.
type Entry struct {
	Time    time.Time      `json:"time"`
	Run     string         `json:"run"`
	Seq     int            `json:"seq"`
	Event   string         `json:"event"`
	RuleID  string         `json:"rule_id,omitempty"`
	ItemKey string         `json:"item_key,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Command []string       `json:"command,omitempty"`
	Path    string         `json:"path,omitempty"`
	Bytes   int64          `json:"bytes,omitempty"`
	Receipt *trash.Receipt `json:"receipt,omitempty"`
	// Pruned rides a prune action's done or fail line: what the command
	// deleted, as measured. Bytes on the start line is the estimate.
	Pruned *Pruned `json:"pruned,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// Pruned counts the cache entries a prune deleted.
type Pruned struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// StateDir is ~/.local/state/regrow (honouring XDG_STATE_HOME) — the
// journal's home, and the shared root for staging and the docker
// usage ledger.
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "regrow"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "regrow"), nil
}

// DefaultPath is StateDir()/oplog.jsonl.
func DefaultPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oplog.jsonl"), nil
}

// Log is an append-only jsonl journal. Every Append is synced before
// it returns: the invariant is "journaled before it runs", and an
// entry lost in a page cache on crash breaks undo.
type Log struct {
	f *os.File
}

// Open opens (creating parents) the journal for appending.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{f: f}, nil
}

// Append holds an exclusive flock on the journal from the check for a
// cut-short last line through the sync: two regrow processes (`clean`
// and `tick --autotrim`) can journal at once, and one must not write
// after the other's fragment without starting a fresh line.
func (l *Log) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	fd := int(l.f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock the oplog: %w", err)
	}
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()
	if _, err := l.f.Write(jsonl.Frame(l.f, line)); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *Log) Close() error { return l.f.Close() }

// Read parses the journal. A missing file is an empty history. A
// corrupt line fails loudly: the journal is the undo contract, and
// silently skipping lines could undo the wrong thing. Two exceptions
// carry no entry: blank lines, and a line cut short, which is the
// start of one entry and nothing else (a write that hit a full disk). Skipping a cut-short line can only miss a restore, never undo
// the wrong thing: a start line that failed refused its action, a lost
// done line drops its receipt, and a lost undo line leaves a receipt
// whose restore then fails because the item is back.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Read-only handle: a close error cannot corrupt anything.
	defer func() { _ = f.Close() }()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			if cutShort(line, err) {
				continue
			}
			return nil, fmt.Errorf("%s:%d: corrupt oplog line: %w", path, n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// entryStart opens every encoded Entry: Time is the first field and
// never omitted, and no nested type has a time key. It cannot occur
// inside a string value, whose quotes the encoder escapes.
var entryStart = []byte(`{"time":`)

// cutShort reports whether line is one unfinished entry: the parser ran
// out of input with nothing wrong before that, and no second entry
// starts inside it. A fragment followed by a whole entry with no
// newline between can parse to the end the same way (the entry lands
// as a value), and must fail loudly instead of hiding the whole one.
func cutShort(line []byte, err error) bool {
	var se *json.SyntaxError
	opens := bytes.HasPrefix(line, entryStart) || bytes.HasPrefix(entryStart, line)
	return opens && !bytes.Contains(line[1:], entryStart) &&
		errors.As(err, &se) && se.Offset == int64(len(line)) &&
		strings.Contains(se.Error(), "unexpected end of JSON input")
}

// Run is one execution's journal lines, grouped.
type Run struct {
	ID      string
	Start   time.Time
	Entries []Entry
}

// Runs groups entries by run id, oldest first. Undo entries group
// under the run they undo (they share its id).
func Runs(entries []Entry) []Run {
	byID := map[string]*Run{}
	var order []string
	for _, e := range entries {
		r, ok := byID[e.Run]
		if !ok {
			r = &Run{ID: e.Run, Start: e.Time}
			byID[e.Run] = r
			order = append(order, e.Run)
		}
		if e.Time.Before(r.Start) {
			r.Start = e.Time
		}
		r.Entries = append(r.Entries, e)
	}
	out := make([]Run, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Undoable returns the run's trash receipts that can still be
// restored — done actions minus those already undone — in reverse
// execution order (last moved, first restored). Non-restorable
// receipts (volume export tarballs) are excluded: they are recovery
// pointers, not rename-back moves.
func (r Run) Undoable() []Entry {
	undone := map[int]bool{}
	for _, e := range r.Entries {
		if e.Event == EventUndo && e.Error == "" {
			undone[e.Seq] = true
		}
	}
	var out []Entry
	for _, e := range r.Entries {
		if e.Event == EventDone && e.Receipt != nil && e.Receipt.Restorable() && !undone[e.Seq] {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}
