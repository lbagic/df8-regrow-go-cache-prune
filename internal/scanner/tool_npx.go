package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// The npx cache: npx installs each package spec it runs into a folder
// of its own, ~/.npm/_npx/<hash>, and nothing removes one; `npm cache
// clean --force` deletes only ~/.npm/_cacache. Each folder is an item,
// to the Trash. A folder a running process works in or names on its
// command line is kept: node loads modules as it needs them, so a
// server npx started (an MCP server, a dev tool) fails at its next
// require once its folder moves. Running a folder npx installed earlier
// changes nothing in it, so its times cannot say whether it is in use.
// The npx-recheck pre-action asks again right before the move.

// npxCacheDir is npx's folder under npm's default cache, the cache
// npm-cache's ~/.npm/_cacache belongs to.
const npxCacheDir = ".npm/_npx"

// keptNpx is an npx folder the rule leaves alone, and why.
type keptNpx struct{ path, why string }

// npxCache is the query with its seams: tests hand in the walker, the
// folder and the process list.
type npxCache struct {
	w     *walker
	dir   string
	procs func(ctx context.Context) (processes, error)
}

func newNpxCache(home string) *npxCache {
	return &npxCache{w: defaultWalker, dir: filepath.Join(home, npxCacheDir), procs: runningProcesses}
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
// folder goes to the Trash it asks again whether a running process
// uses it, and refuses when one does or when that cannot be told.
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
		if why := keepNpx(d, procs); why != "" {
			kept = append(kept, keptNpx{d, why})
			continue
		}
		u, found, err := n.w.usage(ctx, d)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
		}
		items = append(items, engine.Item{Path: d, Label: n.label(ctx, d), Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial})
	}
	return items, kept, nil
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
	if why := keepNpx(path, procs); why != "" {
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
