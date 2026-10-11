package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The usage ledger persists derived volume last-used across container
// removal (research §2): the container×mount join is the only source
// of volume recency, and `docker rm` / `compose down` erases it. Every
// scan with a reachable daemon merges the join into the ledger, so
// "last used before its containers were removed" survives.
//
// Keys are name@CreatedAt: CreatedAt guards against name reuse — a
// recreated volume with an old name must not inherit stale history.
// Entries for volumes that no longer exist are pruned on merge; the
// ledger describes what is, not what was.

type ledgerEntry struct {
	LastUsed time.Time `json:"last_used,omitzero"`
}

type ledger map[string]ledgerEntry

func ledgerKey(v Volume) string {
	return v.Name + "@" + v.CreatedAt.UTC().Format(time.RFC3339)
}

// loadLedger reads the ledger; a missing file is an empty history. A
// corrupt file is an error — silently starting over would erase the
// only last-used history orphaned volumes have.
func loadLedger(path string) (ledger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ledger{}, nil
	}
	if err != nil {
		return nil, err
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: corrupt usage ledger (delete it to start over): %w", path, err)
	}
	return l, nil
}

// merge folds the snapshot into the ledger and writes each volume's
// merged last-used back onto the snapshot. The returned ledger holds
// exactly the currently-existing volumes.
func (l ledger) merge(snap *Snapshot) ledger {
	next := make(ledger, len(snap.Volumes))
	for i := range snap.Volumes {
		v := &snap.Volumes[i]
		key := ledgerKey(*v)
		if prev, ok := l[key]; ok && prev.LastUsed.After(v.LastUsed) {
			v.LastUsed = prev.LastUsed
		}
		next[key] = ledgerEntry{LastUsed: v.LastUsed}
	}
	return next
}

// saveLedger writes atomically: a scan interrupted mid-write must not
// corrupt the history it exists to protect.
func saveLedger(path string, l ledger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
