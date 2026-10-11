package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// The npx cache: npx installs each package spec it runs into a folder
// of its own, ~/.npm/_npx/<hash>, and nothing removes one; `npm cache
// clean --force` deletes only ~/.npm/_cacache. Each folder is an item,
// to the Trash. A folder a running process works in or names on its
// command line is kept: node loads modules as it needs them, so a
// server npx started (an MCP server, a dev tool) fails at its next
// require once its folder moves. So is a folder npx ran in the last
// npxQuiet: npm 11.19 makes and removes a lock folder in it on every
// run, which stamps the folder, and a server that renames its process
// (process.title) shows no path to ps. The npx-recheck pre-action
// judges the folder again right before the move.

// npxCacheDir is npx's folder under npm's default cache, the cache
// npm-cache's ~/.npm/_cacache belongs to.
const npxCacheDir = ".npm/_npx"

// npxQuiet is how long a folder must go without a run.
const npxQuiet = 7 * 24 * time.Hour

// keptNpx is an npx folder the rule leaves alone, and why.
type keptNpx struct{ path, why string }

// npxCache is the query with its seams: tests hand in the walker, the
// folder, the process list and the clock.
type npxCache struct {
	w     *walker
	dir   string
	quiet time.Duration
	procs func(ctx context.Context) (processes, error)
	now   func() time.Time
}

func newNpxCache(home string) *npxCache {
	return &npxCache{w: defaultWalker, dir: filepath.Join(home, npxCacheDir), quiet: npxQuiet, procs: runningProcesses, now: time.Now}
}

func queryNpxCache(ctx context.Context) ([]engine.Item, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	items, _, err := newNpxCache(home).scan(ctx)
	return items, err
}

// RecheckNpxEntry is the npx-cache pre-action: right before an npx
// folder goes to the Trash it judges the folder again, and refuses when
// a running process uses it, npx ran it since the scan, or either
// cannot be told.
func RecheckNpxEntry(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	// The run's context has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	return newNpxCache(home).recheck(ctx, path)
}

// scan lists the npx folders: the items offered, and the ones kept,
// each with its reason. With the running processes unknown no folder
// can be offered, so that is an error.
func (n *npxCache) scan(ctx context.Context) ([]engine.Item, []keptNpx, error) {
	entries, partial, err := n.w.list(ctx, n.dir)
	if err != nil {
		return nil, nil, err
	}
	var items []engine.Item
	if partial {
		items = append(items, unreadableMarker("npx folders under "+n.dir))
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(n.dir, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return items, nil, nil
	}
	procs, err := n.procs(ctx)
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(dirs)
	var kept []keptNpx
	for _, d := range dirs {
		item, why, ok, err := n.judge(ctx, d, procs)
		switch {
		case err != nil:
			return nil, nil, err
		case !ok:
		case why != "":
			kept = append(kept, keptNpx{d, why})
		default:
			items = append(items, item)
		}
	}
	return items, kept, nil
}

// judge decides one folder: an item, or why it stays. ok is false when
// the folder is gone. Its only error is ctx's.
func (n *npxCache) judge(ctx context.Context, dir string, procs processes) (engine.Item, string, bool, error) {
	if why := keepNpx(dir, procs); why != "" {
		return engine.Item{}, why, true, nil
	}
	u, found, err := n.w.usage(ctx, dir)
	switch {
	case err != nil:
		return engine.Item{}, "", false, err
	case !found:
		return engine.Item{}, "", false, nil
	case u.Partial:
		return engine.Item{}, "it cannot all be read, so when npx last ran it is unknown", true, nil
	case n.now().Sub(u.Newest) < n.quiet:
		return engine.Item{}, fmt.Sprintf("npx ran it in the last %d days", int(n.quiet.Hours()/24)), true, nil
	}
	return engine.Item{Path: dir, Label: n.label(ctx, dir), Bytes: u.Bytes, LastUsed: u.Newest}, "", true, nil
}

// recheck judges one folder again, as the pre-action does.
func (n *npxCache) recheck(ctx context.Context, path string) error {
	if filepath.Dir(path) != n.dir {
		return fmt.Errorf("%s is not a folder of the npx cache %s: not moved", path, n.dir)
	}
	procs, err := n.procs(ctx)
	if err != nil {
		return fmt.Errorf("recheck %s: %w: not moved", path, err)
	}
	_, why, ok, err := n.judge(ctx, path, procs)
	switch {
	case err != nil:
		return fmt.Errorf("recheck %s: %w: not moved", path, err)
	case !ok:
		return fmt.Errorf("npx folder %s is gone: not moved", path)
	case why != "":
		return fmt.Errorf("npx folder %s is kept now (%s): not moved", path, why)
	}
	return nil
}

// keepNpx says why the folder stays, or "" when it may go.
func keepNpx(dir string, procs processes) string {
	if escapedByTools(dir) {
		return "lsof and ps would not print its path as it is, so no process can be matched to it"
	}
	if procs.workIn(dir) {
		return "a running process uses it"
	}
	return ""
}

// label names the packages npx installed in the folder, from the
// package.json npx writes there, as `npm cache npx ls` does; the
// folder's hash when that cannot be read.
func (n *npxCache) label(ctx context.Context, dir string) string {
	hash := filepath.Base(dir)
	pkgPath := filepath.Join(dir, "package.json")
	data, err := bounded(ctx, n.w, pkgPath, func() ([]byte, error) { return os.ReadFile(pkgPath) })
	if err != nil {
		return "npx " + hash
	}
	var pkg struct {
		Npx struct {
			Packages []string `json:"packages"`
		} `json:"_npx"`
	}
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Npx.Packages) == 0 {
		return "npx " + hash
	}
	return strings.Join(pkg.Npx.Packages, ", ") + " · npx " + hash
}
