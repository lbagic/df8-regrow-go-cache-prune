package protocol

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// FDA is what the engine could establish about Full Disk Access.
type FDA string

const (
	FDAGranted FDA = "granted"
	FDADenied  FDA = "denied"
	// FDAUnknown: no probe path exists, the probe did not answer in
	// time, or the host is not macOS.
	FDAUnknown FDA = "unknown"
)

const fdaProbeBound = 2 * time.Second

// fdaProbePaths are folders macOS lists only for a process with Full
// Disk Access. Another app's container must never be probed: opening
// one can block forever.
var fdaProbePaths = []string{".Trash", "Library/Safari"}

// ProbeFDA reads one entry of a TCC-guarded folder in the host's home.
func ProbeFDA(host engine.Host) FDA {
	if host.OS != "darwin" {
		return FDAUnknown
	}
	return probeFDA(host.Home, readOneEntry, fdaProbeBound)
}

func probeFDA(home string, readdir func(string) error, bound time.Duration) FDA {
	answer := make(chan FDA, 1)
	go func() {
		for _, rel := range fdaProbePaths {
			err := readdir(filepath.Join(home, rel))
			if err == nil {
				answer <- FDAGranted
				return
			}
			if errors.Is(err, fs.ErrPermission) {
				answer <- FDADenied
				return
			}
		}
		answer <- FDAUnknown
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case a := <-answer:
		return a
	case <-timer.C:
		return FDAUnknown
	}
}

func readOneEntry(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
