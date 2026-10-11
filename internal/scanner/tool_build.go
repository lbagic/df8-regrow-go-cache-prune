package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/lbagic/regrow/internal/engine"
)

// Build output of idle projects: .next, dist, .turbo and .venv folders
// under the project roots. A folder is offered only when every test
// holds, and each failure keeps it:
//
//   - it sits in a git repository found at or below a project root, and
//     git names that folder as the repository's top level;
//   - `git check-ignore` reports it ignored by a rule that names the
//     folder itself, and the index holds nothing inside it and
//     something in the folder that holds it;
//   - it holds no git repository of its own, and a .venv holds a
//     pyvenv.cfg;
//   - its project is idle: no file under the project folder changed
//     for buildOutputIdle, git's HEAD did not move, and no running
//     process of the user works in it. A submodule's project is its
//     outermost superproject.
//
// The build-output-recheck pre-action runs the same judgement on the
// one folder right before it goes to the Trash.

const (
	// buildOutputIdle is how long a project must go unchanged.
	buildOutputIdle = 30 * 24 * time.Hour
	// buildOutputDepth is the max_depth of node-modules-dirs.
	buildOutputDepth = 10
	// maxSuperprojects bounds the climb from a submodule to the
	// project that holds it.
	maxSuperprojects = 8
	// processLocale is set for lsof and ps: without a UTF-8 locale
	// both escape every non-ASCII byte of a path (lsof as \xc5\xa1, ps
	// as M-EM-!), and no project with such a name would match.
	processLocale = "LC_ALL=en_US.UTF-8"
)

// buildOutputNames are the folder names the rule offers. A name
// belongs here only when a folder of that name that an ignore rule
// names is build output and nothing else;
// TestBuildOutputNamesHaveOneOwner keeps every name out of the
// discover rules.
var buildOutputNames = []string{".next", ".turbo", ".venv", "dist"}

// processes is what the user's running processes say they work on.
type processes struct {
	cwds []string // working directories
	args []string // command lines
}

// workIn reports a process whose working directory is dir or inside
// it, or whose command line names dir or a path inside it. Paths are
// compared folded: ps prints a path as it was typed, and the
// filesystem answers to it in any letter case or composition.
func (p processes) workIn(dir string) bool {
	dir = foldName(dir)
	for _, cwd := range p.cwds {
		if cwd = foldName(cwd); cwd == dir || strings.HasPrefix(cwd, dir+"/") {
			return true
		}
	}
	return slices.ContainsFunc(p.args, func(line string) bool { return namesPath(foldName(line), dir) })
}

var caseFold = cases.Fold()

// foldName spells a path close to the way a filesystem that ignores
// letter case and Unicode composition compares it: Dist as dist,
// STRASSE as Straße, a precomposed š as s and its accent. It also
// joins a few names such a filesystem tells apart (iota folds to its
// combining form, which then sorts past a following accent), and on a
// filesystem that tells letter case apart two paths that fold alike
// are two folders. So it is for comparisons where a match keeps.
func foldName(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= utf8.RuneSelf }) {
		return strings.ToLower(s)
	}
	return norm.NFD.String(strings.Map(foldRune, caseFold.String(norm.NFD.String(s))))
}

// foldRune gives every letter of one case orbit in Go's tables the
// same rune, the smallest. caseFold alone leaves pairs apart that a
// filesystem joins: it swaps Cherokee's two cases, and its tables are
// those of the pinned x/text, older than Go's. ASCII stays as caseFold
// lowered it, which is how foldName's short way spells it.
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		return r
	}
	least := r
	for o := unicode.SimpleFold(r); o != r; o = unicode.SimpleFold(o) {
		least = min(least, o)
	}
	return least
}

// namesPath reports whether a command line holds dir as a whole path
// or as the start of one. Only a byte that could continue a folder's
// name (app-mobile after app) makes a match another folder's.
func namesPath(line, dir string) bool {
	for rest := line; ; {
		i := strings.Index(rest, dir)
		if i < 0 {
			return false
		}
		rest = rest[i+len(dir):]
		if rest == "" {
			return true
		}
		c := rest[0]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !letter && c != '-' && c != '_' && c != '.' && c < 0x80 {
			return true
		}
	}
}

// runningProcesses lists the working directory and command line of
// every process of the user. Either tool failing is an error: with the
// processes unknown no project can be called idle.
func runningProcesses(ctx context.Context) (processes, error) {
	uid := strconv.Itoa(os.Getuid())
	out, err := processTool(ctx, "lsof", "-a", "-d", "cwd", "-Fn", "-u", uid)
	if err != nil {
		return processes{}, err
	}
	cwds, err := parseLsofNames(string(out))
	if err != nil {
		return processes{}, err
	}
	out, err = processTool(ctx, "ps", "-x", "-U", uid, "-ww", "-o", "args=")
	if err != nil {
		return processes{}, err
	}
	return processes{cwds: cwds, args: strings.Split(string(out), "\n")}, nil
}

// processTool runs lsof or ps under processLocale.
func processTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s is not installed, so running processes cannot be listed", name)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = toolWaitDelay
	// The last value of a repeated variable is the one the tool sees.
	cmd.Env = append(os.Environ(), processLocale)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s could not list running processes: %w", name, err)
	}
	return out, nil
}

// parseLsofNames reads the name fields of `lsof -Fn` output: one field
// per line, each led by its letter. lsof writes a byte it will not
// print as \xNN: under processLocale a byte of a character the
// system calls unprintable (a zero-width joiner, a character newer
// than its tables). One such escape means the names cannot be
// compared with paths, which is an error.
func parseLsofNames(out string) ([]string, error) {
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		name, ok := strings.CutPrefix(line, "n")
		if !ok || name == "" {
			continue
		}
		for i := 0; i+1 < len(name); i++ {
			if name[i] != '\\' {
				continue
			}
			if name[i+1] == 'x' {
				return nil, fmt.Errorf("lsof escaped a working directory (%s), so running processes cannot be matched to projects", name)
			}
			i++ // \\ is a backslash; \t and its kin are control characters
		}
		names = append(names, name)
	}
	return names, nil
}

// escapedByTools reports a path lsof or ps would not print as it is:
// one with a backslash, a byte that is no UTF-8, or a character that
// is not printable (a control character, a zero-width joiner, a soft
// hyphen, a space other than the ASCII one).
func escapedByTools(path string) bool {
	return strings.ContainsFunc(path, func(r rune) bool { return r == '\\' || r == utf8.RuneError || !unicode.IsPrint(r) })
}

// keptOutput is a build folder the rule leaves alone, and why.
type keptOutput struct{ path, why string }

// buildOutput is the query with its seams: tests hand in the walker,
// the roots, git, the process list and the clock.
type buildOutput struct {
	w     *walker
	host  engine.Host
	roots []string
	idle  time.Duration
	git   func(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error)
	procs func(ctx context.Context) (processes, error)
	now   func() time.Time
}

func newBuildOutput(home string) *buildOutput {
	return &buildOutput{
		w:     defaultWalker,
		host:  engine.Host{Home: home},
		roots: projectRoots,
		idle:  buildOutputIdle,
		git:   gitWith,
		procs: runningProcesses,
		now:   time.Now,
	}
}

func queryBuildOutput(ctx context.Context) ([]engine.Item, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	items, _, err := newBuildOutput(home).scan(ctx)
	return items, err
}

// RecheckBuildOutput is the build-output pre-action: right before a
// build folder goes to the Trash it judges the folder again and
// refuses when anything keeps it now. The scan may be hours old; the
// project may have been picked up again, or the folder committed.
func RecheckBuildOutput(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	// The run's context has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	return newBuildOutput(home).recheck(ctx, path)
}

// walkRoots are the roots that are plain folders no other root leads
// into through a symlink. A root reached through a symlink is left
// out with everything under it, and so is a plain root that such a
// root resolves to, into or above: git and lsof spell those projects
// by their real path, a command line and the discover rules by the
// link, and a folder judged under one spelling would be missed under
// the other. A root that cannot be resolved could lead anywhere, so
// then no root is walked. A link's target is compared with the plain
// roots folded: EvalSymlinks returns a link's text as written, in
// whatever letter case. It is compared by file as well: a link can
// reach a root by another path to the same folder, as
// /System/Volumes/Data/Users/… reaches /Users/…, which EvalSymlinks
// leaves as written. That finds a link at or below a firmlink's
// folder; /System/Volumes/Data itself is not the same file as /, so a
// link to it, to /System/Volumes or to /System leaves the plain roots
// walked.
func (b *buildOutput) walkRoots(ctx context.Context) []string {
	var plain, linked []string
	var targets [][]os.FileInfo // each link's target, then the folders above it
	for _, r := range b.roots {
		p := filepath.Clean(b.host.ExpandPath(r))
		rp, err := bounded(ctx, b.w, p, func() (string, error) { return filepath.EvalSymlinks(p) })
		switch {
		case absent(err):
		case err != nil:
			return nil
		case rp == p:
			plain = append(plain, p)
		default:
			linked = append(linked, foldName(rp))
			up, ok := b.upward(ctx, rp)
			if !ok {
				return nil
			}
			targets = append(targets, up)
		}
	}
	if len(targets) == 0 {
		return plain
	}
	return slices.DeleteFunc(plain, func(p string) bool {
		folded := foldName(p)
		if slices.ContainsFunc(linked, func(target string) bool {
			return inRoots([]string{folded}, target) || inRoots([]string{target}, folded)
		}) {
			return true
		}
		up, ok := b.upward(ctx, p)
		if !ok {
			return true
		}
		return slices.ContainsFunc(targets, func(target []os.FileInfo) bool {
			// The link leads to p or into it, or above it.
			return holds(target, up[0]) || holds(up, target[0])
		})
	})
}

// upward stats path and every folder above it, nearest first. ok is
// false when one cannot be read.
func (b *buildOutput) upward(ctx context.Context, path string) ([]os.FileInfo, bool) {
	var infos []os.FileInfo
	for p := path; ; p = filepath.Dir(p) {
		fi, err := bounded(ctx, b.w, p, func() (os.FileInfo, error) { return os.Stat(p) })
		if err != nil {
			return nil, false
		}
		infos = append(infos, fi)
		if p == filepath.Dir(p) {
			return infos, true
		}
	}
}

func holds(infos []os.FileInfo, fi os.FileInfo) bool {
	return slices.ContainsFunc(infos, func(o os.FileInfo) bool { return os.SameFile(o, fi) })
}

// inRoots reports dir as one of the roots or inside one. The
// filesystem root holds every path.
func inRoots(roots []string, dir string) bool {
	return slices.ContainsFunc(roots, func(r string) bool {
		return dir == r || strings.HasPrefix(dir, strings.TrimSuffix(r, "/")+"/")
	})
}

// scan lists every build folder under the roots: the ones offered, as
// items, and the ones kept, each with its reason. An error offers
// nothing.
func (b *buildOutput) scan(ctx context.Context) ([]engine.Item, []keptOutput, error) {
	roots := b.walkRoots(ctx)
	spec := engine.Discover{Roots: roots, MaxDepth: buildOutputDepth}
	hits, partialRoots := discoverMatching(ctx, b.w, b.host, spec, func(_, name string) bool {
		return slices.Contains(buildOutputNames, name)
	})
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var items []engine.Item
	for _, root := range partialRoots {
		items = append(items, unreadableMarker("folders under "+tilde(b.host, root)))
	}
	if len(hits) == 0 {
		return items, nil, nil
	}
	procs, err := b.procs(ctx)
	if err != nil {
		return nil, nil, err
	}

	var kept []keptOutput
	var tops []string
	byTop := map[string][]string{}
	gitAt := map[string]bool{}
	for _, hit := range hits {
		top, why := b.projectOf(ctx, hit, roots, gitAt)
		if why != "" {
			kept = append(kept, keptOutput{hit, why})
			continue
		}
		if byTop[top] == nil {
			tops = append(tops, top)
		}
		byTop[top] = append(byTop[top], hit)
	}
	for _, top := range tops {
		found, k, err := b.judge(ctx, roots, top, byTop[top], procs)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, found...)
		kept = append(kept, k...)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(items, func(a, c engine.Item) int { return strings.Compare(a.Path, c.Path) })
	slices.SortFunc(kept, func(a, c keptOutput) int { return strings.Compare(a.path, c.path) })
	return items, kept, nil
}

// recheck judges one folder as the scan would now: it must be a folder
// the walk would list, and pass every test of judge. nil means it
// would still be offered.
func (b *buildOutput) recheck(ctx context.Context, path string) error {
	refuse := func(why string) error {
		return fmt.Errorf("build output %s is kept now (%s): not moved", path, why)
	}
	roots := b.walkRoots(ctx)
	if path != filepath.Clean(path) || !slices.Contains(buildOutputNames, filepath.Base(path)) || !walked(roots, path) {
		return refuse("not a build folder the scan lists")
	}
	if fi, err := b.w.lstat(ctx, path); err != nil || !fi.IsDir() {
		return refuse("it is not a folder any more")
	}
	procs, err := b.procs(ctx)
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	top, why := b.projectOf(ctx, path, roots, map[string]bool{})
	if why != "" {
		return refuse(why)
	}
	items, kept, err := b.judge(ctx, roots, top, []string{path}, procs)
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	if len(items) == 1 && items[0].Path == path {
		return nil
	}
	if len(kept) == 1 {
		return refuse(kept[0].why)
	}
	return refuse("it could not be judged")
}

// walked reports whether the scan's walk reaches path: under a root,
// within the depth, below no folder the walk does not enter (an
// excluded name, or another build folder).
func walked(roots []string, path string) bool {
	for _, r := range roots {
		rel, ok := strings.CutPrefix(path, r+"/")
		if !ok {
			continue
		}
		parts := strings.Split(rel, "/")
		above := parts[:len(parts)-1]
		return len(parts) <= buildOutputDepth && !slices.ContainsFunc(above, func(name string) bool {
			return builtinExcludes[name] || slices.Contains(buildOutputNames, name)
		})
	}
	return false
}

// projectOf finds the work tree a build folder belongs to: the nearest
// folder above it that holds a .git entry, searching no higher than
// the root the folder was found under. A repository above the roots
// does not count: a home directory kept in git with a catch-all ignore
// would answer for every folder below it. gitAt remembers which
// folders hold a .git entry.
func (b *buildOutput) projectOf(ctx context.Context, hit string, roots []string, gitAt map[string]bool) (top, why string) {
	for dir := filepath.Dir(hit); inRoots(roots, dir); dir = filepath.Dir(dir) {
		has, seen := gitAt[dir]
		if !seen {
			_, err := b.w.lstat(ctx, filepath.Join(dir, ".git"))
			if unreadable(err) {
				return "", "the folders above it cannot be read"
			}
			has = err == nil
			gitAt[dir] = has
		}
		if has {
			return dir, ""
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return "", "not in a git repository under the project roots"
}

// workTree asks git about the work tree at top: git must name top as
// a work tree's top level, and says which superproject, if any, holds
// it as a submodule. An empty or broken .git folder makes git answer
// for a repository further up, which prints another path or a prefix.
func (b *buildOutput) workTree(ctx context.Context, top string) (super string, ok bool, err error) {
	out, err := b.git(ctx, top, nil, "rev-parse", "--show-toplevel", "--show-prefix", "--show-superproject-working-tree")
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 3 || lines[0] != top || lines[1] != "" || strings.Join(lines[3:], "") != "" {
		return "", false, nil
	}
	return lines[2], true, nil
}

// judge decides which of one work tree's build folders may go. hits
// are folders under top. Whatever cannot be established keeps a
// folder: a git call that fails or times out keeps every folder of the
// work tree. The only error is ctx's.
func (b *buildOutput) judge(ctx context.Context, roots []string, top string, hits []string, procs processes) ([]engine.Item, []keptOutput, error) {
	var kept []keptOutput
	keepAll := func(dirs []string, why string) ([]engine.Item, []keptOutput, error) {
		for _, d := range dirs {
			kept = append(kept, keptOutput{d, why})
		}
		return nil, kept, ctx.Err()
	}

	super, ok, err := b.workTree(ctx, top)
	if err != nil {
		return keepAll(hits, "git cannot read its repository")
	}
	if !ok {
		return keepAll(hits, "git does not name its project folder as a repository's top level")
	}

	var asked, rels []string
	for _, hit := range hits {
		rel := strings.TrimPrefix(hit, top+"/")
		// git reads a leading colon as an instruction: ":/dist" asks
		// about the top-level dist, whatever folder was meant.
		if strings.HasPrefix(rel, ":") {
			kept = append(kept, keptOutput{hit, "git cannot be asked about a path that starts with a colon"})
			continue
		}
		asked = append(asked, hit)
		rels = append(rels, rel)
	}
	if len(asked) == 0 {
		return nil, kept, nil
	}
	ignoredBy, err := b.ignored(ctx, top, rels)
	if err != nil {
		return keepAll(asked, "git could not say whether it is ignored")
	}
	tracked, beside, err := b.tracked(ctx, top, rels)
	if err != nil {
		return keepAll(asked, "git could not say whether it is tracked")
	}

	var candidates []string
	for i, hit := range asked {
		pattern, ignored := ignoredBy[rels[i]]
		switch {
		case tracked[rels[i]]:
			kept = append(kept, keptOutput{hit, "git tracks files in it"})
		case !ignored:
			kept = append(kept, keptOutput{hit, "git does not ignore it"})
		case !namesFolder(pattern, filepath.Base(hit)):
			kept = append(kept, keptOutput{hit, "the ignore rule that covers it does not name it"})
		case belowItsName(rels[i]):
			kept = append(kept, keptOutput{hit, "a folder above it has its name in another letter case"})
		case !beside[rels[i]]:
			kept = append(kept, keptOutput{hit, "git tracks nothing in the folder that holds it"})
		case filepath.Base(hit) == ".venv" && !b.isFile(ctx, filepath.Join(hit, "pyvenv.cfg")):
			kept = append(kept, keptOutput{hit, "no pyvenv.cfg, so not a Python environment"})
		default:
			candidates = append(candidates, hit)
		}
	}
	if len(candidates) == 0 {
		return nil, kept, nil
	}

	// A submodule is part of the project that holds it: work there
	// keeps the submodule's build output.
	trees := []string{top}
	for super != "" {
		if !inRoots(roots, super) {
			return keepAll(candidates, "its superproject is outside the project roots")
		}
		if len(trees) > maxSuperprojects {
			return keepAll(candidates, "its superprojects nest too deep to follow")
		}
		trees = append(trees, super)
		if super, ok, err = b.workTree(ctx, super); err != nil || !ok {
			return keepAll(candidates, "git cannot read its superproject")
		}
	}
	project := trees[len(trees)-1]
	// git names no superproject for a submodule it cannot match in the
	// superproject's index (the path spelled in another letter case, an
	// index it cannot read), so the climb can end on a submodule.
	if b.submoduleGitDir(ctx, project) {
		return keepAll(candidates, "git keeps its repository elsewhere and names no project that holds it")
	}

	if escapedByTools(project) {
		return keepAll(candidates, "its path holds a character the process list escapes")
	}
	if procs.workIn(project) {
		return keepAll(candidates, "a running process works in its project")
	}
	now := b.now()
	active := func(t time.Time) bool { return now.Sub(t) < b.idle }
	changed := fmt.Sprintf("its project changed in the last %d days", int(b.idle.Hours()/24))
	var newest time.Time
	for _, tree := range trees {
		moved, ok := headMoved(ctx, b.w, tree)
		if !ok {
			return keepAll(candidates, "its project's last commit or checkout cannot be read")
		}
		if moved.After(newest) {
			newest = moved
		}
	}
	if active(newest) {
		return keepAll(candidates, changed)
	}
	treeNewest, partial, err := b.treeNewest(ctx, project, now.Add(-b.idle))
	if err != nil {
		return nil, nil, err
	}
	if partial {
		return keepAll(candidates, "a folder in its project cannot be read")
	}
	if treeNewest.After(newest) {
		newest = treeNewest
	}
	if active(newest) {
		return keepAll(candidates, changed)
	}

	type measured struct {
		path   string
		usage  Usage
		hasGit bool
	}
	var sized []measured
	for _, c := range candidates {
		var hasGit atomic.Bool
		u, found, err := b.w.usageSeeing(ctx, c, func(_ string, e fs.DirEntry) {
			if e.Name() == ".git" {
				hasGit.Store(true)
			}
		})
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue // gone since the walk found it
		}
		if u.Partial {
			// An unread folder could hold recent work.
			return keepAll(candidates, "a folder in its project cannot be read")
		}
		sized = append(sized, measured{c, u, hasGit.Load()})
	}

	var items []engine.Item
	for _, m := range sized {
		if m.hasGit {
			kept = append(kept, keptOutput{m.path, "it holds a git repository"})
			continue
		}
		where := filepath.Join(filepath.Base(top), filepath.Dir(strings.TrimPrefix(m.path, top+"/")))
		items = append(items, engine.Item{
			Path:     m.path,
			Label:    filepath.Base(m.path) + " · " + where,
			Bytes:    m.usage.Bytes,
			LastUsed: newest,
		})
	}
	return items, kept, nil
}

// ignored asks `git check-ignore -v` about the paths, given relative
// to top, and returns the pattern that ignores each. A path is left
// out when no pattern matches it, when the last one that does is a
// negation (-v prints that too, led by "!"), and when the index holds
// something under the path as written. Exit status 1 means no pattern
// matched any path.
func (b *buildOutput) ignored(ctx context.Context, top string, rels []string) (map[string]string, error) {
	stdin := []byte(strings.Join(rels, "\x00") + "\x00")
	out, err := b.git(ctx, top, stdin, "check-ignore", "--stdin", "-z", "-v")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	// Records of four fields: source, line, pattern, path.
	fields := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	if len(out) == 0 || len(fields)%4 != 0 {
		return nil, fmt.Errorf("git check-ignore -v: %d fields, want records of four", len(fields))
	}
	patterns := map[string]string{}
	for i := 0; i < len(fields); i += 4 {
		if pattern := string(fields[i+2]); !strings.HasPrefix(pattern, "!") {
			patterns[string(fields[i+3])] = pattern
		}
	}
	return patterns, nil
}

// namesFolder reports whether an ignore pattern's last component is
// the folder's name, as in dist/, /dist and apps/web/dist. A pattern
// that ignores the folder some other way does not say it is build
// output: vendor/ covers every dist of an installed package below it,
// /* covers everything a repository does not list, .venv* is a guess.
func namesFolder(pattern, name string) bool {
	p := strings.TrimSuffix(pattern, "/")
	return p[strings.LastIndex(p, "/")+1:] == name
}

// belowItsName reports a path, relative to its work tree, with a
// folder above it that carries its own name in another letter case.
// git matches ignore patterns without regard to case where the
// filesystem does, so dist/ ignores Dist with everything in it, and
// the pattern git reports for Dist/sub/dist still ends in dist. The
// walk never enters a folder of the exact name.
func belowItsName(rel string) bool {
	parts := strings.Split(rel, "/")
	name := parts[len(parts)-1]
	return slices.ContainsFunc(parts[:len(parts)-1], func(above string) bool { return strings.EqualFold(above, name) })
}

// submoduleGitDir reports a work tree whose .git file names a
// submodule's git dir. A linked worktree's git dir holds a commondir
// file and a submodule's does not, wherever its superproject keeps
// it; a repository made with --separate-git-dir reads the same. A
// submodule's checkout can also be a linked worktree of the
// submodule's repository: git writes core.worktree into a submodule's
// repository, in its config, or in its config.worktree once
// sparse-checkout has turned on per-worktree config, so a worktree of
// one counts too. A repository made with its work tree apart from its
// git dir has it as well, and its worktrees are kept the same way. git
// submodule deinit and a recursive checkout without the submodule
// unset it in config only. Whatever cannot be read counts as a
// submodule; git itself warns and answers "unset" for a config it may
// not read.
func (b *buildOutput) submoduleGitDir(ctx context.Context, top string) bool {
	gitdir, ok := gitdirOf(ctx, b.w, top)
	if !ok {
		return false
	}
	path := filepath.Join(gitdir, "commondir")
	data, err := bounded(ctx, b.w, path, func() ([]byte, error) { return os.ReadFile(path) })
	if err != nil {
		return true
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	for _, name := range []string{"config", "config.worktree"} {
		file := filepath.Join(common, name)
		if _, err := bounded(ctx, b.w, file, func() ([]byte, error) { return os.ReadFile(file) }); err != nil {
			if name == "config.worktree" && absent(err) {
				continue
			}
			return true
		}
		// Exit status 1: the file sets no core.worktree.
		_, err := b.git(ctx, top, nil, "config", "--file", file, "--get", "core.worktree")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return true
		}
	}
	return false
}

// tracked reads the index for the paths, given relative to top.
// inside holds the paths with an index entry in them: a file, or a
// submodule at the path itself. beside holds the paths whose parent
// folder has an index entry outside the path: build output lies
// beside the source it was built from, and a folder of the name in a
// tree git tracks nothing of (an environment not named .venv, an
// unpacked archive) is a piece of something installed. It reads the
// whole index. inside is matched by foldName: the index can spell a
// folder Dist, or with a precomposed é, where the disk has dist, or e
// and an accent. beside is matched by bytes: there a match offers,
// and where the filesystem tells Vendor from vendor a tracked Vendor
// says nothing of the folder vendor.
func (b *buildOutput) tracked(ctx context.Context, top string, rels []string) (inside, beside map[string]bool, err error) {
	out, err := b.git(ctx, top, nil, "ls-files", "-z")
	if err != nil {
		return nil, nil, err
	}
	type prefixes struct{ self, parent string } // self folded, parent as walked
	asked := make([]prefixes, len(rels))
	for i, rel := range rels {
		asked[i].self = foldName(rel) + "/"
		// The parent of a top-level path is the work tree: every
		// entry lies in it.
		if dir := filepath.Dir(rel); dir != "." {
			asked[i].parent = dir + "/"
		}
	}
	inside, beside = map[string]bool{}, map[string]bool{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		p := string(entry) + "/"
		folded := foldName(p)
		for i, a := range asked {
			switch {
			case strings.HasPrefix(folded, a.self):
				inside[rels[i]] = true
			case strings.HasPrefix(p, a.parent):
				beside[rels[i]] = true
			}
		}
	}
	return inside, beside, nil
}

// treeNewest is the newest mtime over the files of a project, the
// ones inside its build folders included. A folder's own time does not
// count: a Trash move out of it stamps it, regrow's own included, so
// the first build folder to go would keep the project's second. A
// node_modules is the exception: it is not entered, and its own time
// shows an install. Left out as well: every .git entry (any `git
// status` stamps the admin dir; headMoved reads git's activity) and
// Finder's .DS_Store. Once a time after cutoff is seen the walk stops
// descending: the project is in use, whatever else it holds.
func (b *buildOutput) treeNewest(ctx context.Context, top string, cutoff time.Time) (time.Time, bool, error) {
	var newest atomic.Int64
	limit := cutoff.UnixNano()
	partial, err := b.w.walk(ctx, top, func(dir string, batch []fs.DirEntry) ([]string, bool) {
		var latest int64
		var subdirs []string
		ok := true
		for _, e := range batch {
			name := e.Name()
			if name == ".git" || name == ".DS_Store" {
				continue
			}
			if e.IsDir() && name != "node_modules" {
				subdirs = append(subdirs, childPath(dir, name))
				continue
			}
			fi, err := e.Info()
			if err != nil {
				ok = ok && !unreadable(err)
				continue
			}
			latest = max(latest, fi.ModTime().UnixNano())
		}
		storeMax(&newest, latest)
		if newest.Load() > limit {
			return nil, ok
		}
		return subdirs, ok
	})
	return time.Unix(0, newest.Load()), partial, err
}

func (b *buildOutput) isFile(ctx context.Context, path string) bool {
	fi, err := b.w.lstat(ctx, path)
	return err == nil && fi.Mode().IsRegular()
}

// headMoved is when git last moved HEAD in the work tree at top (a
// commit, a checkout, a reset), from its git dir: the .git folder, or
// what a .git file names (a linked worktree, a submodule). ok is false
// when the git dir, its HEAD or its logs/HEAD cannot be read, or
// logs/HEAD is missing: a commit on a branch rewrites the reflog and
// not HEAD, so a repository that keeps none shows no commit.
func headMoved(ctx context.Context, w *walker, top string) (time.Time, bool) {
	admin := filepath.Join(top, ".git")
	fi, err := w.lstat(ctx, admin)
	if err != nil {
		return time.Time{}, false
	}
	if !fi.IsDir() {
		var ok bool
		if admin, ok = gitdirOf(ctx, w, top); !ok {
			return time.Time{}, false
		}
	}
	head, err := w.lstat(ctx, filepath.Join(admin, "HEAD"))
	if err != nil {
		return time.Time{}, false
	}
	log, err := w.lstat(ctx, filepath.Join(admin, "logs", "HEAD"))
	if err != nil {
		return time.Time{}, false
	}
	if log.ModTime().After(head.ModTime()) {
		return log.ModTime(), true
	}
	return head.ModTime(), true
}
