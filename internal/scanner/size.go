package scanner

import (
	"context"
	"io/fs"
	"sync/atomic"
	"syscall"
	"time"
)

// Usage is the one definition of an item's size and recency.
type Usage struct {
	// Bytes is disk usage, du-style: physical blocks, not logical
	// length, so sparse files and APFS clones report what deletion
	// actually reclaims (research/02 §15). Symlinks are not followed.
	Bytes int64
	// Newest is the newest mtime over every file and directory,
	// directories included: package managers stamp fixed old times on
	// files (npm writes 1985), so only directory times show an install.
	Newest time.Time
	// Partial: something refused or blocked, so Bytes and Newest are
	// lower bounds.
	Partial bool
}

// DirSize measures path. A blocked or unreadable directory anywhere in
// the tree, root included, makes the result Partial instead of failing
// it, like `du 2>/dev/null`; a missing path is the zero Usage. The
// only error is ctx's, with the partial result so far.
func DirSize(ctx context.Context, path string) (Usage, error) {
	u, _, err := defaultWalker.usage(ctx, path)
	return u, err
}

// usage is DirSize that also reports whether path exists. A root whose
// Lstat is refused or blocked counts as found and Partial: the rule
// names it, and the reader needs to see that it could not be measured.
func (w *walker) usage(ctx context.Context, path string) (Usage, bool, error) {
	return w.usageSeeing(ctx, path, nil)
}

// usageSeeing is usage that also hands every entry below path to see.
// see runs on walk workers, concurrently, and may run after
// usageSeeing has returned for a directory that answered late.
func (w *walker) usageSeeing(ctx context.Context, path string, see func(dir string, e fs.DirEntry)) (Usage, bool, error) {
	info, err := w.lstat(ctx, path)
	switch {
	case absent(err):
		return Usage{}, false, nil
	case ctx.Err() != nil:
		return Usage{Partial: true}, true, ctx.Err()
	case err != nil:
		return Usage{Partial: true}, true, nil
	}
	if !info.IsDir() {
		return Usage{Bytes: physicalSize(info), Newest: info.ModTime()}, true, nil
	}

	var bytes, newest atomic.Int64
	var rootListed atomic.Bool
	newest.Store(info.ModTime().UnixNano())
	partial, err := w.walk(ctx, path, func(dir string, batch []fs.DirEntry) ([]string, bool) {
		if dir == path {
			rootListed.Store(true)
		}
		var sum, latest int64
		var subdirs []string
		ok := true
		for _, e := range batch {
			if see != nil {
				see(dir, e)
			}
			fi, err := e.Info()
			if err != nil {
				ok = ok && !unreadable(err)
				continue
			}
			sum += physicalSize(fi)
			latest = max(latest, fi.ModTime().UnixNano())
			if e.IsDir() {
				subdirs = append(subdirs, childPath(dir, e.Name()))
			}
		}
		bytes.Add(sum)
		storeMax(&newest, latest)
		return subdirs, ok
	})
	// The root's own blocks count only once it could be listed, so a
	// root that refused or blocked reads as nothing measured. A
	// complete walk listed it even when it held nothing to visit.
	if rootListed.Load() || !partial {
		bytes.Add(physicalSize(info))
	}
	return Usage{Bytes: bytes.Load(), Newest: time.Unix(0, newest.Load()), Partial: partial}, true, err
}

func storeMax(v *atomic.Int64, n int64) {
	for {
		cur := v.Load()
		if n <= cur || v.CompareAndSwap(cur, n) {
			return
		}
	}
}

// physicalSize prefers allocated blocks (512-byte units, the stat
// contract on darwin and linux) and falls back to logical size.
func physicalSize(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
