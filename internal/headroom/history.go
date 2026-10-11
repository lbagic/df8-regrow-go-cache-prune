package headroom

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lbagic/regrow/internal/jsonl"
)

// FileName is the history's name inside regrow's state dir. It is not
// the oplog: nothing is undone from it and a lost line costs nothing.
const FileName = "headroom.jsonl"

// Retention is how much history Append keeps.
const Retention = 30 * 24 * time.Hour

// compactSlack lets the file overshoot Retention so the rewrite
// happens about once a day, not on every append.
const compactSlack = 24 * time.Hour

// Append adds one sample and drops history older than Retention.
func Append(path string, s Sample) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Best effort: a failed rewrite leaves the old file in place, and
	// the append below reports a full disk on its own.
	_ = compact(path, s.At.Add(-Retention))

	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(jsonl.Frame(f, line))
	return errors.Join(werr, f.Close())
}

func compact(path string, cutoff time.Time) error {
	all, err := Tail(path, time.Time{})
	if err != nil || len(all) == 0 || !all[0].At.Before(cutoff.Add(-compactSlack)) {
		return err
	}
	var buf bytes.Buffer
	for _, s := range all {
		if s.At.Before(cutoff) {
			continue
		}
		line, err := json.Marshal(s)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Tail returns the samples at or after since, oldest first. A missing
// file is an empty history. Lines that do not parse are skipped: a
// full disk cuts writes short, and one bad line must not hide the rest.
func Tail(path string, since time.Time) ([]Sample, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []Sample
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var s Sample
			if json.Unmarshal(line, &s) == nil && !s.At.IsZero() && s.Total > 0 && !s.At.Before(since) {
				out = append(out, s)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return out, rerr
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}
