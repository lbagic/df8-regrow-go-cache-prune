package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Linked git worktrees that are finished. A worktree qualifies when all
// three hold: its HEAD is merged into the default branch (by ancestry,
// or its branch's upstream is gone, as after a squash merge and branch
// delete); no commit, checkout or file change touched it for a week;
// and `git status --ignored` shows nothing but whole ignored build
// directories, with no tracked file hidden from status. The rule
// removes it with `git worktree remove`, which keeps the branch,
// refuses a worktree with modified or untracked files, and deletes the
// ignored build output with the checkout. It does not refuse a new
// ignored file, an edit hidden from status, or a detached HEAD that no
// branch contains, so the worktree-recheck pre-action tests for those
// again right before the removal.

const (
	// worktreeIdle is how long a worktree must go untouched.
	worktreeIdle = 7 * 24 * time.Hour
	// worktreeRepoDepth bounds the search for repositories below each
	// root: project trees, and agent scratch dirs, whose clones sit
	// about six levels down.
	worktreeRepoDepth = 6
)

// gitCallTimeout bounds one git call; a worktree or a build folder
// whose call times out is not offered.
var gitCallTimeout = 30 * time.Second

// worktreeBuildDirs are the ignored directories a finished worktree
// may still hold: build output and dependencies its tools regenerate.
// Any other ignored path (an .env file, editor or agent settings) keeps
// the worktree, because removing it would delete that path.
var worktreeBuildDirs = map[string]bool{
	".gradle":       true,
	".mypy_cache":   true,
	".next":         true,
	".nuxt":         true,
	".parcel-cache": true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".svelte-kit":   true,
	".turbo":        true,
	".venv":         true,
	"__pycache__":   true,
	"build":         true,
	"coverage":      true,
	"dist":          true,
	"node_modules":  true,
	"target":        true,
}

// gitInProgress are the admin dir entries git keeps while a rebase,
// merge, cherry-pick, revert or bisect is under way.
var gitInProgress = []string{"rebase-merge", "rebase-apply", "sequencer", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"}

// worktreeRoots are where the query looks for repositories: the
// project roots the discover rules use, and agent scratch dirs, where
// agents clone repositories and add worktrees.
func worktreeRoots() []string {
	return []string{"~/workspace", "~/dev", "~/projects", "~/src", "~/code",
		filepath.Join("/tmp", fmt.Sprintf("claude-%d", os.Getuid()))}
}

func queryGitWorktrees(ctx context.Context) ([]engine.Item, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	return scanWorktrees(ctx, defaultWalker, engine.Host{Home: home}, worktreeRoots(), time.Now())
}

// worktree is one record of `git worktree list --porcelain`.
type worktree struct {
	path, head, branch string // branch is a full ref, empty when detached
	bare, locked       bool
	prunable           bool
}

// scanWorktrees lists every finished linked worktree of the
// repositories found under roots, one item each. The main worktree is
// never an item, and two worktrees of one repository are two items:
// the key is the worktree's path.
func scanWorktrees(ctx context.Context, w *walker, host engine.Host, roots []string, now time.Time) ([]engine.Item, error) {
	// git reports real paths; roots resolved the same way keep the
	// repositories found under them comparable with what git says.
	resolved := make([]string, len(roots))
	for i, r := range roots {
		p := host.ExpandPath(r)
		resolved[i] = p
		if rp, err := bounded(ctx, w, p, func() (string, error) { return filepath.EvalSymlinks(p) }); err == nil {
			resolved[i] = rp
		}
	}
	repos, partialRoots := discover(ctx, w, host, engine.Discover{Roots: resolved, Markers: []string{".git"}, MaxDepth: worktreeRepoDepth})
	var items []engine.Item
	for _, root := range partialRoots {
		items = append(items, unreadableMarker("folders under "+tilde(host, root)))
	}
	seenCommon := map[string]bool{}
	seenMain := map[string]bool{}
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		// Most repositories have no linked worktree; their admin dirs
		// say so without starting git.
		common, ok := commonDir(ctx, w, repo)
		if !ok || seenCommon[common] {
			continue
		}
		seenCommon[common] = true
		if !w.isDir(ctx, filepath.Join(common, "worktrees")) {
			continue
		}
		wts, err := listWorktrees(ctx, repo)
		if err != nil || len(wts) < 2 || seenMain[wts[0].path] {
			continue // not a repository git can read, or no linked worktree
		}
		seenMain[wts[0].path] = true
		found, err := finishedWorktrees(ctx, w, wts[0].path, wts[1:], now)
		items = append(items, found...)
		if err != nil {
			return items, err
		}
	}
	slices.SortFunc(items, func(a, b engine.Item) int { return strings.Compare(a.Path, b.Path) })
	return items, nil
}

// finishedWorktrees checks one repository's linked worktrees, cheapest
// test first: merged (one git call for the repository), then idle
// (a walk of the tree), then clean and free of checked-out submodules
// (git calls per worktree). The only error is ctx's.
func finishedWorktrees(ctx context.Context, w *walker, mainPath string, linked []worktree, now time.Time) ([]engine.Item, error) {
	refs, err := readRefs(ctx, mainPath)
	if err != nil {
		return nil, ctx.Err()
	}
	bases := refs.bases()
	if len(bases) == 0 {
		return nil, ctx.Err()
	}
	// A base branch checked out in a linked worktree (the layout around
	// a bare repository) is the main checkout, and always reads merged.
	baseBranch := map[string]bool{}
	for _, b := range bases {
		name := strings.TrimPrefix(strings.TrimPrefix(b, "refs/heads/"), "refs/remotes/origin/")
		baseBranch["refs/heads/"+name] = true
	}
	var candidates []worktree
	var heads []string
	for _, wt := range linked {
		// An unborn branch lists an all-zero HEAD, which rev-list
		// rejects for the whole repository.
		unborn := strings.Trim(wt.head, "0") == ""
		if !wt.bare && !wt.locked && !wt.prunable && !unborn && !baseBranch[wt.branch] {
			candidates = append(candidates, wt)
			heads = append(heads, wt.head)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	unmerged, err := notReachable(ctx, mainPath, heads, bases)
	if err != nil {
		return nil, ctx.Err()
	}

	var items []engine.Item
	for _, wt := range candidates {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		var why string
		switch {
		case !unmerged[wt.head]:
			why = "merged"
		case wt.branch != "" && refs.gone[wt.branch]:
			why = "upstream gone"
		default:
			continue
		}
		u, found, err := w.usage(ctx, wt.path)
		if err != nil {
			return items, err
		}
		if !found || u.Partial {
			continue // an unread folder could hide recent work
		}
		last := u.Newest
		if t := lastCommitOrCheckout(ctx, w, wt.path); t.After(last) {
			last = t
		}
		if now.Sub(last) < worktreeIdle {
			continue
		}
		if dirty, err := firstUnclean(ctx, w, wt.path); err != nil || dirty != "" {
			continue
		}
		// `git worktree remove` refuses a worktree that has or had a
		// checked-out submodule unless forced, so offering it would
		// fail every run.
		if hasSubmodule(ctx, w, wt.path) {
			continue
		}
		name := strings.TrimPrefix(wt.branch, "refs/heads/")
		if name == "" {
			name = "detached " + wt.head[:min(7, len(wt.head))]
		}
		items = append(items, engine.Item{
			Path:     wt.path,
			Label:    name + " (" + why + ")",
			Bytes:    u.Bytes,
			LastUsed: last,
		})
	}
	return items, nil
}

// listWorktrees runs `git worktree list --porcelain -z` in dir; the
// first record is the main worktree.
func listWorktrees(ctx context.Context, dir string) ([]worktree, error) {
	out, err := git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

// parseWorktreeList reads NUL-separated attributes, records ended by an
// empty attribute.
func parseWorktreeList(out []byte) []worktree {
	var wts []worktree
	var cur *worktree
	for _, field := range strings.Split(string(out), "\x00") {
		key, val, _ := strings.Cut(field, " ")
		switch {
		case field == "":
			cur = nil
		case key == "worktree":
			wts = append(wts, worktree{path: val})
			cur = &wts[len(wts)-1]
		case cur == nil:
		case key == "HEAD":
			cur.head = val
		case key == "branch":
			cur.branch = val
		case key == "bare":
			cur.bare = true
		case key == "locked":
			cur.locked = true
		case key == "prunable":
			cur.prunable = true
		}
	}
	return wts
}

// repoRefs is what the merged test needs from a repository's refs.
type repoRefs struct {
	exists   map[string]bool
	originHD string // what refs/remotes/origin/HEAD points at
	gone     map[string]bool
}

func readRefs(ctx context.Context, dir string) (repoRefs, error) {
	out, err := git(ctx, dir, "for-each-ref", "--format=%(refname)%00%(upstream)%00%(upstream:track)%00%(symref)", "refs/heads", "refs/remotes")
	if err != nil {
		return repoRefs{}, err
	}
	r := repoRefs{exists: map[string]bool{}, gone: map[string]bool{}}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 4 {
			continue
		}
		r.exists[f[0]] = true
		if f[1] != "" && f[2] == "[gone]" {
			r.gone[f[0]] = true
		}
		if f[0] == "refs/remotes/origin/HEAD" {
			r.originHD = f[3]
		}
	}
	return r, nil
}

// bases are the refs a merged worktree's HEAD is reachable from: the
// remote's default branch and the usual local and remote main branches.
func (r repoRefs) bases() []string {
	var out []string
	for _, ref := range []string{r.originHD, "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if ref != "" && r.exists[ref] && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// notReachable returns the commits among heads that no base reaches:
// `git rev-list` prints what heads reach and the bases do not, so a
// head missing from its output is merged.
func notReachable(ctx context.Context, dir string, heads, bases []string) (map[string]bool, error) {
	args := append([]string{"rev-list"}, heads...)
	args = append(append(args, "--not"), bases...)
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, h := range heads {
		in[h] = true
	}
	unmerged := map[string]bool{}
	for _, c := range strings.Fields(string(out)) {
		if in[c] {
			unmerged[c] = true
		}
	}
	return unmerged, nil
}

// firstUnclean says why the worktree is not clean, or "" when it is.
// Clean means `git status --ignored` lists nothing but whole ignored
// directories named in worktreeBuildDirs, and no tracked file is hidden
// from status. Matching mode lists a directory only when an ignore
// pattern matches it; a directory whose files are ignored one by one
// (dist/.env under a `.env` pattern) is listed file by file, so that
// .env keeps the worktree. The flags override a user config that would
// hide untracked files. Status skips files flagged assume-unchanged or
// skip-worktree, and `git worktree remove` deletes an edit to one
// without asking, so `ls-files -v` is read for them: a lowercase tag
// (assume-unchanged), or S (skip-worktree) on a file that is on disk;
// a sparse checkout's S entries are absent. A rebase, merge,
// cherry-pick, revert or bisect in progress keeps its state in the
// admin dir, which goes with the worktree, and can leave HEAD on a
// merged commit with nothing to show in status.
func firstUnclean(ctx context.Context, w *walker, dir string) (string, error) {
	admin, ok := gitdirOf(ctx, w, dir)
	if !ok {
		return "its .git file cannot be read", nil
	}
	for _, name := range gitInProgress {
		if _, err := w.lstat(ctx, filepath.Join(admin, name)); !absent(err) {
			return "a git operation is in progress (" + name + ")", nil
		}
	}
	out, err := git(ctx, dir, "status", "--porcelain", "-z", "--ignored=matching", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		path, ignored := strings.CutPrefix(entry, "!! ")
		dirPath, isDir := strings.CutSuffix(path, "/")
		if !ignored || !isDir || !worktreeBuildDirs[filepath.Base(dirPath)] {
			return fmt.Sprintf("git status shows %q", entry), nil
		}
	}
	out, err = git(ctx, dir, "ls-files", "-v", "-z")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		tag, path, ok := strings.Cut(entry, " ")
		if !ok || len(tag) != 1 {
			continue
		}
		switch {
		case tag[0] >= 'a' && tag[0] <= 'z':
			return fmt.Sprintf("%s is flagged assume-unchanged, which hides edits from git status", path), nil
		case tag == "S":
			if _, err := w.lstat(ctx, filepath.Join(dir, path)); !absent(err) {
				return fmt.Sprintf("%s is flagged skip-worktree, which hides edits from git status", path), nil
			}
		}
	}
	return "", nil
}

// CheckWorktreeClean rechecks a worktree about to be removed for what
// the removal would destroy without git refusing: an ignored file
// created since the scan (the clean test, run again), and a commit made
// since on a detached HEAD, which no ref would hold once the worktree
// is gone. A worktree on a branch needs no second merged test: the
// branch keeps its commits.
func CheckWorktreeClean(ctx context.Context, path string) error {
	why, err := firstUnclean(ctx, defaultWalker, path)
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	if why != "" {
		return fmt.Errorf("worktree %s is no longer clean (%s): not removed", path, why)
	}
	branch, err := git(ctx, path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	if strings.TrimSpace(string(branch)) != "" {
		return nil
	}
	holder, err := git(ctx, path, "for-each-ref", "--count=1", "--contains", "HEAD", "refs/heads", "refs/remotes")
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	if strings.TrimSpace(string(holder)) == "" {
		return fmt.Errorf("worktree %s is detached at a commit no branch contains: not removed", path)
	}
	return nil
}

// hasSubmodule mirrors git's own refusal: a modules folder in the
// worktree's admin dir (left behind even by `submodule deinit`), or a
// gitlink in the index whose directory holds a .git. Unreadable counts
// as present.
func hasSubmodule(ctx context.Context, w *walker, dir string) bool {
	admin, ok := gitdirOf(ctx, w, dir)
	if !ok {
		return true
	}
	if _, err := w.lstat(ctx, filepath.Join(admin, "modules")); !absent(err) {
		return true
	}
	out, err := git(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return true
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		if _, err := w.lstat(ctx, filepath.Join(dir, path, ".git")); !absent(err) {
			return true
		}
	}
	return false
}

// lastCommitOrCheckout is when git last moved the worktree's HEAD (a
// commit, checkout or reset), from its admin dir; the working tree's
// own times miss a commit of files edited earlier. The admin dir itself
// and its index are not read: any tool that runs a plain `git status`
// takes index.lock there and may rewrite the index, which was seen
// stamping today's date on worktrees untouched for three weeks. Staged
// changes need no time, since they keep the worktree as unclean. Zero
// when the admin dir cannot be read.
func lastCommitOrCheckout(ctx context.Context, w *walker, wtPath string) time.Time {
	admin, ok := gitdirOf(ctx, w, wtPath)
	if !ok {
		return time.Time{}
	}
	var newest time.Time
	for _, p := range []string{filepath.Join(admin, "HEAD"), filepath.Join(admin, "logs", "HEAD")} {
		if fi, err := w.lstat(ctx, p); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// gitdirOf reads the "gitdir:" line of dir's .git file: a linked
// worktree's admin dir, or a submodule's git dir.
func gitdirOf(ctx context.Context, w *walker, dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	data, err := bounded(ctx, w, dotGit, func() ([]byte, error) { return os.ReadFile(dotGit) })
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	return filepath.Clean(gitdir), true
}

// commonDir finds the repository a .git entry belongs to, as git does:
// a .git directory is its own; a .git file names a git dir, and a
// linked worktree's git dir holds a commondir file naming the shared
// one.
func commonDir(ctx context.Context, w *walker, dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := w.lstat(ctx, dotGit)
	if err != nil {
		return "", false
	}
	if fi.IsDir() {
		return dotGit, true
	}
	gitdir, ok := gitdirOf(ctx, w, dir)
	if !ok {
		return "", false
	}
	cf := filepath.Join(gitdir, "commondir")
	data, err := bounded(ctx, w, cf, func() ([]byte, error) { return os.ReadFile(cf) })
	if err != nil {
		return gitdir, true
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	return filepath.Clean(common), true
}

// gitRepoEnv names the variables that point git at a repository; set,
// they would override -C.
var gitRepoEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"}

// git runs a read-only git command in dir. Optional locks are off, so
// `status` never rewrites the index, and fsmonitor is off, so no
// daemon starts.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitWith(ctx, dir, nil, args...)
}

// gitWith is git with stdin for the command to read; with nil it
// reads the null device.
func gitWith(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCallTimeout)
	defer cancel()
	argv := append([]string{"-C", dir, "--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.WaitDelay = toolWaitDelay
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(gitRepoEnv, name)
	})
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return out, nil
}
