package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/engine"
)

const defaultMaxDepth = 6

// builtinExcludes are directory names never descended into during
// discovery, on top of the rule's own excludes. Matched hits are also
// never descended into (a target/ inside a target/ belongs to the
// outer hit).
var builtinExcludes = map[string]bool{
	".git":         true,
	".Trash":       true,
	"Library":      true,
	"node_modules": true,
}

// DiscoverDefaults is what every discover walk does that its rule does
// not say: the depth below each root when max_depth is 0, and the
// folder names it never descends into, sorted.
func DiscoverDefaults() (maxDepth int, excludes []string) {
	for name := range builtinExcludes {
		excludes = append(excludes, name)
	}
	slices.Sort(excludes)
	return defaultMaxDepth, excludes
}

// discover walks the rule's roots looking for directories that match
// the discover spec: base name (if set) plus every marker file
// present. Missing roots are skipped; that lets rules list
// conventional project locations. A root that is a symlink is walked
// through it. A directory reached from two roots (a symlinked root, a
// root inside another) is reported once, under the first root's path.
// Hits are sorted. partial lists the roots whose walk met a blocked or
// unreadable directory. Cancelling ctx stops the walk; hits found so
// far are returned.
func discover(ctx context.Context, w *walker, host engine.Host, spec engine.Discover) (hits, partial []string) {
	return discoverMatching(ctx, w, host, spec, func(path, name string) bool { return matches(path, name, spec) })
}

// discoverMatching is discover with the hit test handed in: match
// decides by path and base name, and spec gives the roots, the depth
// and the excludes.
func discoverMatching(ctx context.Context, w *walker, host engine.Host, spec engine.Discover, match func(path, name string) bool) (hits, partial []string) {
	maxDepth := spec.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	specExclude := make(map[string]bool, len(spec.Exclude))
	for _, name := range spec.Exclude {
		specExclude[name] = true
	}

	// A worker whose directory answered after the walk gave up on it
	// may still call visit; closed keeps it off the returned hits.
	type hit struct{ real, path string }
	var mu sync.Mutex
	closed := false
	var found []hit
	for _, root := range discoverRoots(ctx, w, host, spec.Roots) {
		visit := func(dir string, batch []fs.DirEntry) ([]string, bool) {
			depth := relDepth(root.path, dir) + 1
			var subdirs []string
			var batchHits []hit
			for _, e := range batch {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				// Rule excludes win over everything; builtin excludes
				// yield to a name match so rules like node_modules
				// discovery still work.
				if specExclude[name] || depth > maxDepth {
					continue
				}
				p := childPath(dir, name)
				switch {
				case match(p, name):
					batchHits = append(batchHits, hit{root.realOf(p), p})
				case !builtinExcludes[name]:
					subdirs = append(subdirs, p)
				}
			}
			if len(batchHits) > 0 {
				mu.Lock()
				if !closed {
					found = append(found, batchHits...)
				}
				mu.Unlock()
			}
			return subdirs, true
		}
		incomplete, err := w.walk(ctx, root.path, visit)
		if err != nil {
			break
		}
		if incomplete {
			partial = append(partial, root.path)
		}
	}
	mu.Lock()
	closed = true
	seen := map[string]bool{}
	for _, h := range found {
		if !seen[h.real] {
			seen[h.real] = true
			hits = append(hits, h.path)
		}
	}
	mu.Unlock()
	slices.Sort(hits)
	return hits, partial
}

// discoverRoot is a root as walked (path) and where it really is.
type discoverRoot struct{ path, real string }

// realOf maps a path found under the root to its real location; walks
// never follow symlinks below the root, so only the root needs it.
func (r discoverRoot) realOf(p string) string {
	return filepath.Join(r.real, p[len(r.path):])
}

// discoverRoots expands the rule's roots, keeps the directories among
// them, and drops a root whose real path an earlier root already has.
func discoverRoots(ctx context.Context, w *walker, host engine.Host, raw []string) []discoverRoot {
	var out []discoverRoot
	for _, r := range raw {
		p := filepath.Clean(host.ExpandPath(r))
		if !w.isDir(ctx, p) {
			continue
		}
		real, err := bounded(ctx, w, p, func() (string, error) { return filepath.EvalSymlinks(p) })
		if err != nil {
			continue
		}
		if !slices.ContainsFunc(out, func(k discoverRoot) bool { return k.real == real }) {
			out = append(out, discoverRoot{p, real})
		}
	}
	return out
}

// relDepth counts the path segments of dir below root: 0 for root.
func relDepth(root, dir string) int {
	rel := strings.TrimPrefix(dir[len(root):], string(filepath.Separator))
	if rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func matches(path, name string, spec engine.Discover) bool {
	if spec.Name != "" && name != spec.Name {
		return false
	}
	for _, marker := range spec.Markers {
		if _, err := os.Stat(filepath.Join(path, marker)); err != nil {
			return false
		}
	}
	return true
}
