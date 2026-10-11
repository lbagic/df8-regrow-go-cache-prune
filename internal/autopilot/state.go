package autopilot

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/headroom"
)

const stateFileName = "prune.state"

const (
	// deferLimit is how long builds may put a prune off.
	deferLimit = 2 * time.Hour
	// deferGap is the longest pause between two deferrals that still
	// counts as one unbroken run: a laptop asleep for hours was not
	// building for hours. It needs ticks at least hourly; the menubar
	// ticks every 15 minutes. With rarer ticks every deferral starts a
	// new clock and the 2 h override never fires.
	deferGap = time.Hour
	// criticalFree is the free space under which a prune stops
	// waiting for builds once deferLimit has passed.
	criticalFree = 10 * headroom.GiB
)

// Deferral is how long builds have been putting a rule's prune off.
type Deferral struct {
	Since time.Time `json:"since,omitzero"`
	Last  time.Time `json:"last,omitzero"`
}

// Next decides one tick. With no build running the prune goes ahead.
// With one it is deferred, until builds have deferred it for
// deferLimit without a break and free space is under criticalFree:
// the cutoff spares every entry used within min_age regardless.
func (d Deferral) Next(now time.Time, building bool, free int64) (next Deferral, run bool) {
	if !building {
		return Deferral{}, true
	}
	if d.Since.IsZero() || now.Sub(d.Last) > deferGap {
		d.Since = now
	}
	d.Last = now
	if now.Sub(d.Since) >= deferLimit && free < criticalFree {
		return Deferral{}, true
	}
	return d, false
}

var errBusy = errors.New("prune state is locked")

// lockedState is the prune state file, held under an exclusive flock
// for the length of one prune: the lock is the single flight. The file
// is rewritten in place, never renamed over, so the lock and the data
// stay on one inode.
type lockedState struct {
	f         *os.File
	deferrals map[string]Deferral
}

func lockState(path string) (*lockedState, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errBusy
		}
		return nil, err
	}
	s := &lockedState{f: f, deferrals: map[string]Deferral{}}
	data, err := io.ReadAll(f)
	if err != nil {
		s.unlock()
		return nil, err
	}
	var disk struct {
		Deferrals map[string]Deferral `json:"deferrals"`
	}
	// A file cut short by a full disk only forgets a deferral clock.
	if json.Unmarshal(data, &disk) == nil && disk.Deferrals != nil {
		s.deferrals = disk.Deferrals
	}
	return s, nil
}

func (s *lockedState) save() error {
	for id, d := range s.deferrals {
		if d.Since.IsZero() && d.Last.IsZero() {
			delete(s.deferrals, id)
		}
	}
	data, err := json.Marshal(struct {
		Deferrals map[string]Deferral `json:"deferrals"`
	}{s.deferrals})
	if err != nil {
		return err
	}
	if err := s.f.Truncate(0); err != nil {
		return err
	}
	_, err = s.f.WriteAt(append(data, '\n'), 0)
	return err
}

// unlock closes the file, which drops the flock.
func (s *lockedState) unlock() { _ = s.f.Close() }
