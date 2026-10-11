package scanner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// Go keys a build-cache entry by the absolute directory of its package
// unless -trimpath is set, so every worktree of a module compiles its
// own copy of it. The check needs both halves: GOFLAGS without
// -trimpath, and a repository with enough worktrees to matter.

// projectRoots are the conventional project locations, the ones the
// discover rules list.
var projectRoots = []string{"~/workspace", "~/dev", "~/projects", "~/src", "~/code"}

const (
	// minGoWorktrees is how many linked worktrees of one Go module
	// make the row.
	minGoWorktrees = 3
	// repoSearchDepth is how far below a project root a repository's
	// .git directory is looked for.
	repoSearchDepth = 4
)

func (c *causeChecks) goWorktreesTrimpath(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
	goEnv := c.goEnv
	if goEnv == nil {
		goEnv = engine.GoEnv
	}
	flags, err := goEnv(ctx, "GOFLAGS")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return engine.VerdictNormal, "does not apply: no go command on PATH"
	case err != nil:
		return engine.VerdictUnknown, fmt.Sprintf("`go env GOFLAGS` failed: %v", err)
	case hasTrimpath(flags):
		return engine.VerdictNormal, "GOFLAGS has -trimpath"
	}

	// Only a main checkout holds a .git directory, and its worktrees/
	// folder is git's own list of the linked ones, wherever they are.
	gitDirs, unread := discover(ctx, c.walker(), c.host, engine.Discover{
		Roots: projectRoots, Name: ".git", Markers: []string{"worktrees"}, MaxDepth: repoSearchDepth,
	})
	partial := len(unread) > 0
	type repo struct {
		path      string
		worktrees int
	}
	var repos []repo
	for _, gitDir := range gitDirs {
		n, incomplete := c.goWorktrees(ctx, gitDir)
		partial = partial || incomplete
		if n >= minGoWorktrees {
			repos = append(repos, repo{filepath.Dir(gitDir), n})
		}
	}
	if len(repos) == 0 {
		none := fmt.Sprintf("no Go module with %d or more worktrees under %s", minGoWorktrees, strings.Join(projectRoots, ", "))
		if partial {
			return engine.VerdictUnknown, none + ", but some folders there are " + engine.UnreadableNote
		}
		return engine.VerdictNormal, none
	}
	slices.SortFunc(repos, func(a, b repo) int {
		return cmp.Or(cmp.Compare(b.worktrees, a.worktrees), strings.Compare(a.path, b.path))
	})
	detail := fmt.Sprintf("%s has %d worktrees of one Go module", tilde(c.host, repos[0].path), repos[0].worktrees)
	if len(repos) > 1 {
		detail += fmt.Sprintf(" (%d repositories with %d or more)", len(repos), minGoWorktrees)
	}
	return engine.VerdictFlagged, detail + "; GOFLAGS has no -trimpath"
}

// hasTrimpath reports whether the flags switch -trimpath on; the last
// mention wins, as on a go command line.
func hasTrimpath(goflags string) bool {
	on := false
	for _, f := range strings.Fields(goflags) {
		name, value, hasValue := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if !strings.HasPrefix(f, "-") || name != "trimpath" {
			continue
		}
		on = true
		if hasValue {
			on, _ = strconv.ParseBool(value)
		}
	}
	return on
}

// goWorktrees counts the linked worktrees of the repository at gitDir
// whose checkout holds the repository's Go module. A registration
// whose checkout is gone is not counted. partial: something on the way
// could not be read.
func (c *causeChecks) goWorktrees(ctx context.Context, gitDir string) (n int, partial bool) {
	w := c.walker()
	mod, partial := goModFile(ctx, w, filepath.Dir(gitDir))
	if mod == "" {
		return 0, partial
	}
	registry := filepath.Join(gitDir, "worktrees")
	entries, incomplete, err := w.list(ctx, registry)
	if err != nil {
		return 0, true
	}
	partial = partial || incomplete
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// gitdir names the checkout's .git file; a relative one
		// resolves against its own folder.
		link := filepath.Join(registry, e.Name(), "gitdir")
		data, err := c.read(ctx, link)
		if err != nil {
			partial = partial || !absent(err)
			continue
		}
		line, _, _ := strings.Cut(string(data), "\n")
		dotGit := strings.TrimSpace(line)
		if dotGit == "" {
			continue
		}
		if !filepath.IsAbs(dotGit) {
			dotGit = filepath.Join(filepath.Dir(link), dotGit)
		}
		switch _, err := w.lstat(ctx, filepath.Join(filepath.Dir(dotGit), mod)); {
		case err == nil:
			n++
		case !absent(err):
			partial = true
		}
	}
	return n, partial
}

// goModFile finds the checkout's go.mod, at its root or one folder
// down (a module beside a frontend), as a path relative to the
// checkout; "" when there is none.
func goModFile(ctx context.Context, w *walker, checkout string) (rel string, partial bool) {
	if _, err := w.lstat(ctx, filepath.Join(checkout, "go.mod")); err == nil {
		return "go.mod", false
	}
	entries, partial, err := w.list(ctx, checkout)
	if err != nil {
		return "", true
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !builtinExcludes[e.Name()] {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		rel := filepath.Join(name, "go.mod")
		if _, err := w.lstat(ctx, filepath.Join(checkout, rel)); err == nil {
			return rel, partial
		}
	}
	return "", partial
}
