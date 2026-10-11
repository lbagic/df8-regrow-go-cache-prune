package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// aerialActive is how recent the newest video must be for a download
// to count as running. idleassetsd writes one video at a time, each
// taking a few minutes.
const aerialActive = 15 * time.Minute

// aerialDownloads looks for aerial videos under the rule's own paths:
// any .mov there was downloaded by macOS, and a recent one means a
// batch is still coming in.
func (c *causeChecks) aerialDownloads(ctx context.Context, r engine.Rule) (engine.Verdict, string) {
	w := c.walker()
	var (
		mu     sync.Mutex
		closed bool
		count  int
		bytes  int64
		newest time.Time
	)
	found, partial := false, false
	for _, pattern := range c.host.ResolvePaths(r) {
		dirs, unlisted := w.glob(ctx, pattern)
		partial = partial || unlisted
		for _, dir := range dirs {
			if _, err := w.lstat(ctx, dir); err != nil {
				partial = partial || !absent(err)
				continue
			}
			found = true
			incomplete, _ := w.walk(ctx, dir, func(d string, batch []fs.DirEntry) ([]string, bool) {
				var subdirs []string
				ok := true
				for _, e := range batch {
					if e.IsDir() {
						subdirs = append(subdirs, childPath(d, e.Name()))
						continue
					}
					if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".mov") {
						continue
					}
					fi, err := e.Info()
					if err != nil {
						ok = ok && !unreadable(err)
						continue
					}
					mu.Lock()
					if !closed {
						count++
						bytes += physicalSize(fi)
						if fi.ModTime().After(newest) {
							newest = fi.ModTime()
						}
					}
					mu.Unlock()
				}
				return subdirs, ok
			})
			partial = partial || incomplete
		}
	}
	mu.Lock()
	defer mu.Unlock()
	closed = true

	switch {
	case count == 0 && partial:
		return engine.VerdictUnknown, "the video folder is " + engine.UnreadableNote
	case count == 0 && !found:
		return engine.VerdictNormal, "does not apply: no aerial video folder on this machine"
	case count == 0:
		return engine.VerdictNormal, "no aerial videos on disk"
	}
	detail := fmt.Sprintf("%d videos, %s", count, engine.SizeText(bytes, partial))
	if count == 1 {
		detail = fmt.Sprintf("1 video, %s", engine.SizeText(bytes, partial))
	}
	age := c.clock().Sub(newest)
	detail += "; newest written " + agoText(age)
	if age <= aerialActive {
		detail += ", so a batch is downloading now"
	}
	return engine.VerdictFlagged, detail
}
