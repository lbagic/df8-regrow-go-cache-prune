package scanner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Without Full Disk Access, open(2) on another app's container can
// block forever, and a blocked syscall ignores ctx. So every
// filesystem call a scan makes runs where the scan can stop waiting
// for it: single calls on a side goroutine, tree walks under a
// watchdog. A call is written off once it has made no progress for the
// stall window and neither has anything else the walker is doing.
// The second half matters: the same gated open that answers in about a
// second on an idle disk was measured at 13 s while a full scan loaded
// it, and waiting for it costs nothing while other walks still run. A
// written-off call keeps its goroutine (nothing can interrupt it), so
// its path stays blocked, and is never touched again, until that call
// answers: one stuck goroutine per path at most, however many scans
// the process runs.

const (
	// stallWindow is how long a filesystem call, and everything else
	// the walker is doing, may go without progress before the scan
	// gives up on the call.
	stallWindow = 5 * time.Second
	// walkWorkers bounds the goroutines walking one tree. Measured on
	// APFS: 4 workers cut a 411k-entry walk from 11.6 s to 3.2 s; 8–32
	// were no faster, the kernel's metadata path is the ceiling.
	walkWorkers = 4
	// readBatch is how many entries a worker reads, and visits, between
	// two progress marks: a huge healthy directory, or a visit that
	// stats or reads every entry, keeps making progress and is never
	// mistaken for a blocked one.
	readBatch = 32
	// maxWriteOffs bounds the replacement workers one walk may start
	// for written-off directories; past it the walk ends partial.
	maxWriteOffs = 16
)

// errBlocked reports a call that was written off, now or earlier in
// this process, and has not answered since.
var errBlocked = errors.New("blocked: the filesystem did not answer")

// dirHandle is an open directory. *os.File satisfies it.
type dirHandle interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

func openDir(path string) (dirHandle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// walker bounds a scan's filesystem calls. open is the seam tests use
// to make a directory block.
type walker struct {
	open    func(path string) (dirHandle, error)
	stall   time.Duration
	workers int
	blocked *blockedSet
	// progress is when any call of this walker last got an answer
	// (UnixNano): the scan-wide half of the write-off rule.
	progress atomic.Int64
}

// defaultWalker serves every real scan, so its blocked paths and its
// progress clock are process-wide.
var defaultWalker = &walker{open: openDir, stall: stallWindow, workers: walkWorkers, blocked: &blockedSet{}}

func (w *walker) progressed() { w.progress.Store(time.Now().UnixNano()) }

// idleFor is how long the walker as a whole has had no answer.
func (w *walker) idleFor(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, w.progress.Load()))
}

// blockedSet holds the paths of written-off calls that have not
// answered yet. A path is blocked when it or one of its ancestors is
// in the set.
type blockedSet struct {
	n     atomic.Int32
	mu    sync.RWMutex
	paths map[string]bool
}

func (b *blockedSet) add(p string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paths == nil {
		b.paths = map[string]bool{}
	}
	if !b.paths[p] {
		b.paths[p] = true
		b.n.Add(1)
	}
}

func (b *blockedSet) remove(p string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paths[p] {
		delete(b.paths, p)
		b.n.Add(-1)
	}
}

func (b *blockedSet) covers(p string) bool {
	if b.n.Load() == 0 {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for {
		if b.paths[p] {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// bounded runs fn, a call that touches path, on its own goroutine and
// stops waiting when the call is written off or ctx ends. A written-off
// call blocks path until it answers; a blocked path is not called.
func bounded[T any](ctx context.Context, w *walker, path string, fn func() (T, error)) (T, error) {
	var zero T
	// An ended scan starts no call. Started, the call could lose the
	// race below to ctx and block its path for a rescan.
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if w.blocked.covers(path) {
		return zero, errBlocked
	}
	type result struct {
		v   T
		err error
	}
	var (
		mu         sync.Mutex
		answered   bool
		writtenOff bool
	)
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		mu.Lock()
		answered = true
		if writtenOff {
			w.blocked.remove(path)
		}
		mu.Unlock()
		ch <- result{v, err}
	}()
	t := time.NewTimer(w.stall)
	defer t.Stop()
	for {
		select {
		case r := <-ch:
			w.progressed()
			return r.v, r.err
		case now := <-t.C:
			// The call itself has waited at least the stall window;
			// keep waiting only while something else still answers.
			if idle := w.idleFor(now); idle < w.stall {
				t.Reset(w.stall - idle)
				continue
			}
			mu.Lock()
			if answered {
				mu.Unlock()
				r := <-ch
				return r.v, r.err
			}
			writtenOff = true
			w.blocked.add(path)
			mu.Unlock()
			return zero, errBlocked
		case <-ctx.Done():
			// A call still in flight may be stuck: block its path
			// until it answers, so a rescan does not join it.
			mu.Lock()
			if !answered {
				writtenOff = true
				w.blocked.add(path)
			}
			mu.Unlock()
			return zero, ctx.Err()
		}
	}
}

func (w *walker) lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	return bounded(ctx, w, path, func() (fs.FileInfo, error) { return os.Lstat(path) })
}

func (w *walker) stat(ctx context.Context, path string) (fs.FileInfo, error) {
	return bounded(ctx, w, path, func() (fs.FileInfo, error) { return os.Stat(path) })
}

func (w *walker) isDir(ctx context.Context, path string) bool {
	fi, err := w.stat(ctx, path)
	return err == nil && fi.IsDir()
}

// list reads one directory's entries through the walk machinery:
// batched, under the watchdog, cancellable. partial means the
// directory refused, blocked or failed mid-listing; err is ctx's.
func (w *walker) list(ctx context.Context, dir string) (entries []fs.DirEntry, partial bool, err error) {
	var mu sync.Mutex
	closed := false
	var out []fs.DirEntry
	partial, err = w.walk(ctx, dir, func(_ string, batch []fs.DirEntry) ([]string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			out = append(out, batch...)
		}
		return nil, true
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	return out, partial, err
}

// absent reports an error that means nothing is at the path: it does
// not exist, or a component of it is a file, not a directory.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// unreadable reports an error that leaves a measurement incomplete: a
// refusal or a block, as opposed to a path that is absent or vanished
// mid-scan.
func unreadable(err error) bool {
	return err != nil && !absent(err)
}

// visitFunc handles one batch of a directory's entries and returns the
// subdirectories to walk next. ok is false when something in the batch
// could not be read, which makes the walk partial. It runs on walk
// workers, concurrently, and may run after walk has returned for a
// directory that answered late.
type visitFunc func(dir string, batch []fs.DirEntry) (subdirs []string, ok bool)

// walk visits every directory under root, root first, with the
// walker's workers. It returns once every directory is visited or
// written off, or ctx ends; it never waits for a worker stuck in a
// syscall. partial is true when any directory could not be read.
func (w *walker) walk(ctx context.Context, root string, visit visitFunc) (partial bool, err error) {
	s := &walkState{w: w, visit: visit, dirs: []string{root}, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	s.mu.Lock()
	for range w.workers {
		s.startWorker()
	}
	s.mu.Unlock()

	tick := time.NewTicker(max(w.stall/4, time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.partial, nil
		case <-ctx.Done():
			s.mu.Lock()
			defer s.mu.Unlock()
			// Directories still in flight may be stuck: block them
			// until their workers come back, so a rescan does not
			// join them.
			for _, sl := range s.slots {
				if sl.busy && !sl.gone {
					sl.gone = true
					s.w.blocked.add(sl.dir)
				}
			}
			s.stop()
			return true, ctx.Err()
		case now := <-tick.C:
			s.writeOffStalled(now)
		}
	}
}

// walkState is one walk's queue. Workers pop directories, read them in
// batches, and push the subdirectories visit returns; the walk is done
// when the queue is empty and no worker holds a directory.
type walkState struct {
	w     *walker
	visit visitFunc

	mu        sync.Mutex
	cond      *sync.Cond
	dirs      []string
	active    int
	slots     []*slot
	writeOffs int
	partial   bool
	stopped   bool
	done      chan struct{}
}

// slot is one worker's claim on a directory. The watchdog reads it to
// find a directory that stopped making progress.
type slot struct {
	dir   string
	since time.Time
	busy  bool
	// gone: the watchdog wrote this directory off and took over its
	// accounting; whatever the stuck worker does later is discarded,
	// except that its return unblocks the directory.
	gone bool
}

// startWorker runs a new worker. Called with s.mu held.
func (s *walkState) startWorker() {
	sl := &slot{}
	s.slots = append(s.slots, sl)
	go s.work(sl)
}

// stop ends the walk. Called with s.mu held.
func (s *walkState) stop() {
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.done)
	s.cond.Broadcast()
}

func (s *walkState) work(sl *slot) {
	for {
		dir, ok := s.pop(sl)
		if !ok {
			return
		}
		subdirs, complete := s.read(sl, dir)
		if !s.finish(sl, subdirs, complete) {
			return
		}
	}
}

// answered reports a written-off slot whose worker came back: the
// call is no longer stuck, so the directory may be tried again.
// Called with s.mu held.
func (s *walkState) answered(sl *slot) bool {
	if sl.gone {
		s.w.blocked.remove(sl.dir)
	}
	return sl.gone
}

func (s *walkState) pop(sl *slot) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.dirs) == 0 && s.active > 0 && !s.stopped {
		s.cond.Wait()
	}
	if s.stopped || sl.gone {
		return "", false
	}
	if len(s.dirs) == 0 {
		s.stop()
		return "", false
	}
	dir := s.dirs[len(s.dirs)-1]
	s.dirs = s.dirs[:len(s.dirs)-1]
	s.active++
	sl.dir, sl.since, sl.busy = dir, time.Now(), true
	return dir, true
}

// touch records progress on the slot's directory. false means the
// worker must abandon it: written off, or the walk is over.
func (s *walkState) touch(sl *slot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answered(sl) || s.stopped {
		return false
	}
	sl.since = time.Now()
	s.w.progress.Store(sl.since.UnixNano())
	return true
}

func (s *walkState) read(sl *slot, dir string) (subdirs []string, complete bool) {
	if s.w.blocked.covers(dir) {
		return nil, false
	}
	h, err := s.w.open(dir)
	if err != nil {
		return nil, !unreadable(err)
	}
	defer func() { _ = h.Close() }()
	complete = true
	for {
		if !s.touch(sl) {
			return nil, false
		}
		batch, err := h.ReadDir(readBatch)
		if len(batch) > 0 {
			sub, ok := s.visit(dir, batch)
			subdirs = append(subdirs, sub...)
			complete = complete && ok
		}
		if errors.Is(err, io.EOF) {
			return subdirs, complete
		}
		if err != nil {
			return subdirs, complete && !unreadable(err)
		}
	}
}

// finish hands a visited directory's subdirectories to the queue.
// false tells the worker to exit.
func (s *walkState) finish(sl *slot, subdirs []string, complete bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answered(sl) {
		return false
	}
	sl.busy = false
	if !complete {
		s.partial = true
	}
	s.active--
	if !s.stopped {
		s.dirs = append(s.dirs, subdirs...)
		if s.active == 0 && len(s.dirs) == 0 {
			s.stop()
		}
	}
	s.cond.Broadcast()
	return !s.stopped
}

// writeOffStalled is the watchdog: once nothing the walker does has
// answered for the stall window, each directory that made no progress
// for that long is blocked, its worker abandoned and replaced, and the
// walk marked partial. Past maxWriteOffs the walk ends with what it
// has.
func (s *walkState) writeOffStalled(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.w.idleFor(now) < s.w.stall {
		return
	}
	for _, sl := range s.slots {
		if !sl.busy || sl.gone || now.Sub(sl.since) < s.w.stall {
			continue
		}
		sl.gone = true
		s.w.blocked.add(sl.dir)
		s.partial = true
		s.active--
		if s.writeOffs == maxWriteOffs {
			s.stop()
			return
		}
		s.writeOffs++
		s.startWorker()
	}
	if s.active == 0 && len(s.dirs) == 0 {
		s.stop()
	}
	s.cond.Broadcast()
}

// childPath joins a directory and an entry name without Clean: walk
// paths are already clean, and this runs once per entry.
func childPath(dir, name string) string {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return dir + name
	}
	return dir + string(filepath.Separator) + name
}
