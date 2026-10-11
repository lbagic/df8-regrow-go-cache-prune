// Package headroom watches the space the machine has left: free disk,
// purgeable, swap. It samples, keeps a short history, forecasts
// days-to-full and raises alerts on crossings. It imports no other
// layer of regrow, so every layer can use it.
package headroom

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Sample has no "used" field on purpose: on APFS total − used ≠ free.
type Sample struct {
	At        time.Time `json:"at"`
	Total     int64     `json:"total"`
	Free      int64     `json:"free"` // statfs f_bavail, what df shows; thresholds use this
	Purgeable int64     `json:"purgeable"`
	SwapUsed  int64     `json:"swap_used"`
}

// probeTimeout bounds the purgeable probe, which shells out.
const probeTimeout = 5 * time.Second

// Volume is the volume user data lives on.
func Volume() string {
	const data = "/System/Volumes/Data"
	if _, err := os.Stat(data); err == nil {
		return data
	}
	return "/"
}

// FreeSpace is the statfs pair for the data volume.
func FreeSpace() (total, free int64, err error) {
	var st syscall.Statfs_t
	vol := Volume()
	if err := syscall.Statfs(vol, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", vol, err)
	}
	bsize := int64(st.Bsize)
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}

// Take measures one sample. Only statfs can fail it: purgeable and swap
// are best-effort and read 0 when their probe fails.
func Take(ctx context.Context) (Sample, error) {
	total, free, err := FreeSpace()
	if err != nil {
		return Sample{}, err
	}
	s := Sample{At: time.Now().UTC().Truncate(time.Second), Total: total, Free: free}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if p, err := Purgeable(pctx, Volume()); err == nil {
		s.Purgeable = p
	}
	if sw, err := swapUsed(); err == nil {
		s.SwapUsed = sw
	}
	return s, nil
}
