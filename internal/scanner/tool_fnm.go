package scanner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf16"

	"github.com/lbagic/regrow/internal/engine"
)

// fnm keeps every installed Node version in its own folder,
// <dir>/node-versions/<version>/, with its aliases as symlinks in
// <dir>/aliases. A version is offered for `fnm uninstall` only when
// nothing names it: no alias, no .nvmrc or .node-version in a project,
// no launchd job, no running process. When any of those sources cannot
// be read or understood, no version can be shown to be unused, so the
// rule reports an error and offers none.

// projectDepth is how deep below a project root version files are
// looked for, the depth node-modules-dirs discovers to.
const projectDepth = 10

var nodeVersionFiles = map[string]bool{".nvmrc": true, ".node-version": true}

// launchdDirs hold the jobs launchd starts by itself; the first is
// relative to home.
var (
	userLaunchdDir    = "Library/LaunchAgents"
	systemLaunchdDirs = []string{"/Library/LaunchAgents", "/Library/LaunchDaemons"}
)

// maxSourceFile bounds a version file or a launchd plist.
const maxSourceFile = 1 << 20

var nodeVersionRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// nodeSpecRe is a version as a version file writes it: one to three
// numbers, an optional v, and x or * for "any" (20, v20.10, 20.x).
var nodeSpecRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:\.[xX*]){0,2}$`)

type nodeVersion struct {
	// name is the folder's name, which is how fnm addresses it.
	name string
	num  [3]int
	path string
	// real is path with symlinks resolved: running processes and
	// resolved links are compared against it.
	real string
	// keptBy lists what names the version; empty means it is offered.
	keptBy []string
}

// fnmVersions is the fnm-node-versions query. The zero value
// reads the real machine; tests inject the home, the environment, the
// system root and the process list.
type fnmVersions struct {
	// Home is the user's home; "" means os.UserHomeDir.
	Home string
	// Root re-anchors the system launchd folders; "" means /.
	Root string
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
	// HasFnm reports whether fnm can be run; nil looks on PATH.
	HasFnm func() bool
	// Mapped lists the files running processes execute or map, spelled
	// as lsof prints them; nil runs lsof.
	Mapped func(ctx context.Context) ([]string, error)
	// Getuid is the user regrow runs as; nil means os.Getuid.
	Getuid func() int
	w      *walker
}

// Unreferenced lists the installed versions nothing names.
func (n *fnmVersions) Unreferenced(ctx context.Context) ([]engine.Item, error) {
	versions, err := n.survey(ctx)
	if err != nil {
		return nil, err
	}
	var items []engine.Item
	for _, v := range versions {
		if len(v.keptBy) > 0 {
			continue
		}
		u, found, err := n.walker().usage(ctx, v.path)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		items = append(items, engine.Item{
			Path:     v.path,
			Label:    "Node " + v.name,
			Arg:      v.name,
			Bytes:    u.Bytes,
			LastUsed: u.Newest,
			Partial:  u.Partial,
		})
	}
	return items, nil
}

// RecheckFnmVersion is the fnm-node-versions pre-action: right before
// `fnm uninstall` it reads every source again and refuses when
// something names the version now. The scan may be hours old.
func RecheckFnmVersion(ctx context.Context, path string) error {
	return (&fnmVersions{}).recheck(ctx, path)
}

func (n *fnmVersions) recheck(ctx context.Context, path string) error {
	versions, err := n.survey(ctx)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if v.path != filepath.Clean(path) {
			continue
		}
		if len(v.keptBy) > 0 {
			return fmt.Errorf("version %s is referenced now: %s", v.name, v.keptBy[0])
		}
		return nil
	}
	return fmt.Errorf("%s is not an installed Node version of the fnm on PATH", path)
}

func (n *fnmVersions) walker() *walker {
	if n.w != nil {
		return n.w
	}
	return defaultWalker
}

func (n *fnmVersions) getenv(key string) string {
	if n.Getenv != nil {
		return n.Getenv(key)
	}
	return os.Getenv(key)
}

func (n *fnmVersions) hasFnm() bool {
	if n.HasFnm != nil {
		return n.HasFnm()
	}
	_, err := exec.LookPath("fnm")
	return err == nil
}

// survey lists the installed versions, each with what keeps it. No fnm
// folder, or one without versions, is nothing to report.
func (n *fnmVersions) survey(ctx context.Context) ([]nodeVersion, error) {
	home := n.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return nil, nil
		}
	}
	w := n.walker()
	dir, err := n.fnmDir(ctx, home)
	if err != nil || dir == "" {
		return nil, err
	}
	if err := n.owned(ctx, home, dir); err != nil {
		return nil, err
	}
	versions, err := listNodeVersions(ctx, w, dir)
	if err != nil || len(versions) == 0 {
		return nil, err
	}
	if !n.hasFnm() {
		return nil, fmt.Errorf("fnm is not on PATH, so the Node versions in %s cannot be uninstalled through it", dir)
	}
	mapped := n.Mapped
	if mapped == nil {
		mapped = lsofMapped
	}
	s := &nodeSurvey{w: w, home: home, root: n.Root, dir: dir, versions: versions, mapped: mapped}
	for _, source := range []func(context.Context) error{s.aliases, s.versionFiles, s.launchdJobs, s.processes} {
		if err := source(ctx); err != nil {
			return nil, fmt.Errorf("cannot tell which Node versions are in use: %w", err)
		}
	}
	return s.versions, nil
}

// owned refuses a home or fnm folder another user owns. lsof lists the
// processes of the user regrow runs as, and under sudo the home,
// FNM_DIR or XDG_DATA_HOME can still lead to the user's versions, so a
// process running from one would go unseen.
func (n *fnmVersions) owned(ctx context.Context, folders ...string) error {
	uid := os.Getuid()
	if n.Getuid != nil {
		uid = n.Getuid()
	}
	for _, folder := range folders {
		fi, err := n.walker().stat(ctx, folder)
		if err != nil {
			return fmt.Errorf("%s: %w", folder, err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			return fmt.Errorf("%s belongs to another user than the one regrow runs as, whose running processes it cannot list", folder)
		}
	}
	return nil
}

// fnmDir is the folder `fnm uninstall` acts on in this environment:
// FNM_DIR when set, else the one default location that exists. fnm
// picks among several by an order that has changed between releases,
// so more than one is an error rather than a guess.
func (n *fnmVersions) fnmDir(ctx context.Context, home string) (string, error) {
	if dir := n.getenv("FNM_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("FNM_DIR %q is not an absolute path", dir)
		}
		dir = filepath.Clean(dir)
		// fnm makes FNM_DIR on its first install.
		if _, err := n.walker().stat(ctx, dir); absent(err) {
			return "", nil
		}
		return dir, nil
	}
	data := n.getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	var found []string
	for _, dir := range []string{
		filepath.Join(data, "fnm"),
		filepath.Join(home, ".fnm"),
		filepath.Join(home, "Library", "Application Support", "fnm"),
	} {
		fi, err := n.walker().stat(ctx, dir)
		switch {
		case absent(err):
		case err != nil:
			return "", fmt.Errorf("%s: %w", dir, err)
		case fi.IsDir() && !slices.Contains(found, dir):
			found = append(found, dir)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("fnm has folders at %s; set FNM_DIR to the one it uses", strings.Join(found, " and "))
}

// listNodeVersions reads <dir>/node-versions, oldest version first. A
// folder that is not named like a release (vX.Y.Z) is left alone.
func listNodeVersions(ctx context.Context, w *walker, dir string) ([]nodeVersion, error) {
	root := filepath.Join(dir, "node-versions")
	entries, partial, err := w.list(ctx, root)
	if err != nil {
		return nil, err
	}
	if partial {
		return nil, fmt.Errorf("%s is not fully readable", root)
	}
	var versions []nodeVersion
	for _, e := range entries {
		m := nodeVersionRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		v := nodeVersion{name: e.Name(), path: filepath.Join(root, e.Name())}
		ok := true
		for i := range v.num {
			n, err := strconv.Atoi(m[i+1])
			v.num[i], ok = n, ok && err == nil
		}
		if !ok {
			continue
		}
		if v.real, err = bounded(ctx, w, v.path, func() (string, error) { return filepath.EvalSymlinks(v.path) }); err != nil {
			return nil, fmt.Errorf("%s: %w", v.path, err)
		}
		versions = append(versions, v)
	}
	slices.SortFunc(versions, func(a, b nodeVersion) int { return slices.Compare(a.num[:], b.num[:]) })
	return versions, nil
}

// nodeSurvey collects what names each version.
type nodeSurvey struct {
	w        *walker
	home     string
	root     string
	dir      string
	versions []nodeVersion
	aliasSet map[string]bool
	mapped   func(ctx context.Context) ([]string, error)
}

func (s *nodeSurvey) keep(i int, reason string) {
	if !slices.Contains(s.versions[i].keptBy, reason) {
		s.versions[i].keptBy = append(s.versions[i].keptBy, reason)
	}
}

// keepResolved keeps the version that path leads into, following
// symlinks, and returns where it leads. A path that does not exist
// names nothing; one that cannot be followed is an error.
func (s *nodeSurvey) keepResolved(ctx context.Context, path, reason string) (string, error) {
	real, err := bounded(ctx, s.w, path, func() (string, error) { return filepath.EvalSymlinks(path) })
	switch {
	case absent(err), namesNoFile(path, err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("%s: %w", path, err)
	}
	target := foldName(real)
	for i, v := range s.versions {
		if folded := foldName(v.real); target == folded || strings.HasPrefix(target, folded+string(filepath.Separator)) {
			s.keep(i, reason)
		}
	}
	return real, nil
}

// aliases keeps every version an fnm alias points at; fnm's default
// is the alias named default.
func (s *nodeSurvey) aliases(ctx context.Context) error {
	dir := filepath.Join(s.dir, "aliases")
	entries, partial, err := s.w.list(ctx, dir)
	if err != nil {
		return err
	}
	if partial {
		return fmt.Errorf("%s is not fully readable", dir)
	}
	s.aliasSet = map[string]bool{}
	for _, e := range entries {
		s.aliasSet[e.Name()] = true
		reason := "fnm alias " + e.Name()
		if e.Name() == "default" {
			reason = "the fnm default"
		}
		if _, err := s.keepResolved(ctx, filepath.Join(dir, e.Name()), reason); err != nil {
			return err
		}
	}
	return nil
}

// versionFiles keeps the version each .nvmrc and .node-version names:
// those in a project under the project roots, and the home's own.
func (s *nodeSurvey) versionFiles(ctx context.Context) error {
	files, err := s.findVersionFiles(ctx)
	if err != nil {
		return err
	}
	for _, f := range files {
		err := s.keepVersionFile(ctx, f, func(spec string) string {
			return fmt.Sprintf("%s (%s)", tilde(engine.Host{Home: s.home}, f), spec)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// keepVersionFile keeps what one .nvmrc or .node-version resolves to.
// A file that is not there names nothing, nor does a name no file can
// have; a link that leads nowhere is an error, as it may lead into a
// disk that is not mounted. A path too long to open by names nothing
// when its folder is not there (a shell string read as a path); in a
// folder that is, it is an error, as fnm opens the file relative to
// its working directory.
func (s *nodeSurvey) keepVersionFile(ctx context.Context, path string, reason func(spec string) string) error {
	data, err := readSource(ctx, s.w, path)
	switch {
	case absent(err):
		switch _, lerr := s.w.lstat(ctx, path); {
		case lerr == nil:
			if !s.goneFromItsFolder(ctx, path) {
				return fmt.Errorf("%s is a link that leads nowhere, so the version it names cannot be known", tilde(engine.Host{Home: s.home}, path))
			}
		case !absent(lerr):
			return withPath(path, lerr)
		}
		return nil
	case namesNoFile(path, err):
		return nil
	case errors.Is(err, syscall.ENAMETOOLONG):
		// EvalSymlinks looks the folder up one name at a time, which a
		// path too long to open by does not stop.
		dir := filepath.Dir(path)
		if _, derr := bounded(ctx, s.w, dir, func() (string, error) { return filepath.EvalSymlinks(dir) }); absent(derr) {
			return nil
		}
		return err
	case err != nil:
		return err
	}
	spec, err := parseVersionFile(data)
	if err == nil {
		err = s.keepSpec(spec, reason(spec))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// withPath names path in err unless err already does.
func withPath(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return err
	}
	return fmt.Errorf("%s: %w", path, err)
}

// isVersionFileName reports a name fnm opening a version file finds
// where case is ignored: APFS folds as strings.EqualFold does (.NVMRC,
// and a long s for s).
func isVersionFileName(name string) bool {
	for v := range nodeVersionFiles {
		if strings.EqualFold(name, v) {
			return true
		}
	}
	return false
}

// leadsNowhere is an error when path is a link to nothing, which may
// lead into a disk that is not mounted. Whatever else keeps path from
// being looked at is left to the read that follows.
func (s *nodeSurvey) leadsNowhere(ctx context.Context, path string) error {
	if fi, err := s.w.lstat(ctx, path); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	if _, err := s.w.stat(ctx, path); absent(err) && !s.goneFromItsFolder(ctx, path) {
		return fmt.Errorf("%s is a link that leads nowhere", tilde(engine.Host{Home: s.home}, path))
	}
	return nil
}

// goneFromItsFolder reports a link that leads nowhere only because its
// target, a name in the link's own folder, is not there: a lock a
// build or an editor left. That folder is there to be read, so no disk
// that is not mounted can be behind it. A target reached through
// another link is not gone.
func (s *nodeSurvey) goneFromItsFolder(ctx context.Context, link string) bool {
	target, err := bounded(ctx, s.w, link, func() (string, error) { return os.Readlink(link) })
	i := strings.LastIndexByte(link, '/')
	if err != nil || i < 0 {
		return false
	}
	dir := link[:max(i, 1)]
	if !filepath.IsAbs(target) {
		target = dir + "/" + target
	}
	j := strings.LastIndexByte(target, '/')
	here, err := s.w.stat(ctx, dir)
	if err != nil {
		return false
	}
	there, err := s.w.stat(ctx, target[:max(j, 1)])
	if err != nil || !os.SameFile(here, there) {
		return false
	}
	_, err = s.w.lstat(ctx, target)
	return absent(err)
}

// versionFileSkips are the folders the version-file walk does not
// enter, by name: what they hold is no pin of the user's projects (a
// package's own under node_modules, git's data, what was deleted).
var versionFileSkips = map[string]bool{".git": true, ".Trash": true, "node_modules": true}

// folderID is a folder's identity, however a path spells it.
type folderID struct {
	dev uint64
	ino uint64
}

// folderIDOf is false where the filesystem gives no inode, so every
// folder there is its own.
func folderIDOf(fi fs.FileInfo) (folderID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Ino == 0 {
		return folderID{}, false
	}
	return folderID{uint64(st.Dev), st.Ino}, true
}

// findVersionFiles walks the project roots to projectDepth, following
// symlinks as the folders or files they lead to. A folder is walked
// again only from a shallower depth than every walk of it so far, which
// bounds a link back to a folder above it. A link that cannot be
// followed may hide a version file, as a folder that cannot be read
// may.
func (s *nodeSurvey) findVersionFiles(ctx context.Context) ([]string, error) {
	var files []string
	for name := range nodeVersionFiles {
		files = append(files, filepath.Join(s.home, name))
	}
	// A directory that answered after its walk gave up on it may still
	// be visited; closed keeps it off the returned list.
	var mu sync.Mutex
	closed := false
	seen := map[folderID]int{}
	var broken []string
	host := engine.Host{Home: s.home}
	brokenLink := func(path string, err error) string {
		if absent(err) {
			return tilde(host, path) + " is a link that leads nowhere"
		}
		return fmt.Sprintf("%s is a link that cannot be followed (%v)", tilde(host, path), err)
	}
	// enter reports whether a folder reached at depth is walked from
	// there.
	enter := func(fi fs.FileInfo, depth int) bool {
		id, ok := folderIDOf(fi)
		if !ok {
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		if d, ok := seen[id]; ok && d <= depth {
			return false
		}
		seen[id] = depth
		return true
	}
	for _, raw := range projectRoots {
		root := filepath.Clean(host.ExpandPath(raw))
		fi, err := s.w.stat(ctx, root)
		switch {
		case absent(err):
			switch _, lerr := s.w.lstat(ctx, root); {
			case lerr == nil:
				if !s.goneFromItsFolder(ctx, root) {
					return nil, fmt.Errorf("%s, so a version file behind it cannot be ruled out", brokenLink(root, err))
				}
			case !absent(lerr):
				return nil, withPath(root, lerr)
			}
			continue
		case err != nil:
			return nil, fmt.Errorf("%s: %w", root, err)
		case !fi.IsDir() || !enter(fi, 0):
			continue
		}
		partial, err := s.w.walk(ctx, root, func(dir string, batch []fs.DirEntry) ([]string, bool) {
			depth := relDepth(root, dir) + 1
			var subdirs, hits, failed []string
			for _, e := range batch {
				name := e.Name()
				path := childPath(dir, name)
				link := e.Type()&fs.ModeSymlink != 0
				enterable := depth <= projectDepth && !versionFileSkips[name]
				folder := enterable && (link || e.IsDir())
				versionFile := isVersionFileName(name)
				if !folder && !versionFile {
					continue
				}
				var fi fs.FileInfo
				var err error
				switch {
				case link:
					fi, err = s.w.stat(ctx, path)
					if absent(err) && s.goneFromItsFolder(ctx, path) {
						continue
					}
					if err != nil {
						failed = append(failed, brokenLink(path, err))
						continue
					}
				case e.IsDir():
					if fi, err = e.Info(); err != nil {
						if !absent(err) {
							failed = append(failed, fmt.Sprintf("%s: %v", tilde(host, path), err))
						}
						continue
					}
				}
				switch {
				case fi != nil && fi.IsDir():
					if enterable && enter(fi, depth) {
						subdirs = append(subdirs, path)
					}
				case versionFile:
					hits = append(hits, path)
				}
			}
			if len(hits) > 0 || len(failed) > 0 {
				mu.Lock()
				if !closed {
					files = append(files, hits...)
					broken = append(broken, failed...)
				}
				mu.Unlock()
			}
			return subdirs, len(failed) == 0
		})
		if err != nil {
			return nil, err
		}
		if partial {
			mu.Lock()
			defer mu.Unlock()
			closed = true
			if len(broken) > 0 {
				slices.Sort(broken)
				return nil, fmt.Errorf("%s, so a version file behind it cannot be ruled out", broken[0])
			}
			return nil, fmt.Errorf("folders under %s are not fully readable, so a version file there cannot be ruled out", root)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	closed = true
	slices.Sort(files)
	return slices.Compact(files), nil
}

// parseVersionFile returns the one version a .nvmrc or .node-version
// names, "" for a file that names none. Comments run from # to the
// end of the line.
func parseVersionFile(data []byte) (string, error) {
	spec := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if spec != "" {
			return "", errors.New("names more than one version")
		}
		spec = line
	}
	return spec, nil
}

// keepSpec keeps what a version file's spec resolves to among the
// installed versions, the way fnm and nvm resolve it: a partial
// version (20, 20.10) is the newest installed release it matches. A
// spec that reads as neither a version nor an alias is an error.
func (s *nodeSurvey) keepSpec(spec, reason string) error {
	lower := strings.ToLower(spec)
	switch {
	case lower == "" || lower == "system":
		return nil
	case strings.HasPrefix(lower, "lts/"):
		// fnm reads lts/<name> as its alias lts-<name>, lts/* as
		// lts-latest. The alias already keeps its version; without the
		// alias fnm resolves the spec to nothing.
		return nil
	case lower == "node" || lower == "stable" || lower == "latest" || lower == "current":
		s.keep(len(s.versions)-1, reason)
		return nil
	}
	m := nodeSpecRe.FindStringSubmatch(spec)
	if m == nil {
		if s.aliasSet[spec] {
			return nil
		}
		return fmt.Errorf("names version %q, which is neither a version number nor an fnm alias", spec)
	}
	var want []int
	for _, part := range m[1:] {
		if part == "" {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("names version %q: %w", spec, err)
		}
		want = append(want, n)
	}
	// Versions are sorted oldest first: the last match is the newest.
	for i := len(s.versions) - 1; i >= 0; i-- {
		if slices.Equal(s.versions[i].num[:len(want)], want) {
			s.keep(i, reason)
			break
		}
	}
	return nil
}

// launchdJobs keeps every version a launchd job's plist points into:
// any string of the plist (program, arguments, environment and the
// rest) that spells a version's folder, or names a path that resolves
// into one.
func (s *nodeSurvey) launchdJobs(ctx context.Context) error {
	dirs := []string{filepath.Join(s.home, userLaunchdDir)}
	for _, d := range systemLaunchdDirs {
		dirs = append(dirs, filepath.Join(s.root, d))
	}
	for _, dir := range dirs {
		if err := s.leadsNowhere(ctx, dir); err != nil {
			return err
		}
		entries, partial, err := s.w.list(ctx, dir)
		if err != nil {
			return err
		}
		if partial {
			return fmt.Errorf("%s is not fully readable", dir)
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return cmp.Compare(a.Name(), b.Name()) })
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".plist") {
				continue
			}
			file := filepath.Join(dir, e.Name())
			data, err := readSource(ctx, s.w, file)
			if absent(err) {
				// Listed, yet absent when opened: a link that leads
				// nowhere, unless the entry went meanwhile.
				switch _, lerr := s.w.lstat(ctx, file); {
				case lerr == nil:
					if !s.goneFromItsFolder(ctx, file) {
						return fmt.Errorf("%s is a link that leads nowhere", tilde(engine.Host{Home: s.home}, file))
					}
				case !absent(lerr):
					return withPath(file, lerr)
				}
				continue
			}
			if err != nil {
				return err
			}
			texts, err := plistTexts(data)
			if err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			reason := "launchd job " + tilde(engine.Host{Home: s.home}, file)
			if err := s.launchdJob(ctx, texts, reason); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	return nil
}

// launchdJob keeps the versions one job's plist strings point into, run
// through fnm, or lead fnm to through a version file.
func (s *nodeSurvey) launchdJob(ctx context.Context, texts []string, reason string) error {
	var words []string
	for _, text := range texts {
		for i, v := range s.versions {
			// The tail alone, so $HOME/…, ~/… and another spelling of the
			// same folder count too.
			if holdsFolder(foldName(text), foldName("/node-versions/"+v.name)) {
				s.keep(i, reason)
			}
		}
		ws, err := jobWords(text, s.home)
		if err != nil {
			return err
		}
		words = append(words, ws...)
	}
	paths := pathCandidates(words, s.home)
	var reals []string
	for _, p := range paths {
		real, err := s.keepResolved(ctx, p, reason)
		if err != nil {
			return err
		}
		if real != "" && real != p {
			reals = append(reals, real)
		}
	}
	s.fnmSpecs(words, reason)
	return s.jobVersionFiles(ctx, append(paths, reals...), reason)
}

// fnmSpecs keeps what a launchd job that runs fnm names as a version:
// `fnm exec --using=18`, `fnm use 18` in a shell string. A binary plist
// keeps no order among its strings, so a spec cannot be told from its
// neighbours: every word that reads as a version keeps what it
// resolves to, which only ever keeps more. A number too large to parse
// names no installed version.
func (s *nodeSurvey) fnmSpecs(words []string, reason string) {
	if !slices.ContainsFunc(words, func(w string) bool { return filepath.Base(w) == "fnm" }) {
		return
	}
	for _, w := range words {
		if _, spec, ok := strings.Cut(w, "="); ok && strings.HasPrefix(w, "--") {
			w = spec
		}
		if nodeSpecRe.MatchString(w) {
			_ = s.keepSpec(w, reason)
		}
	}
}

// jobVersionFiles keeps what the version files a launchd job may lead
// fnm to read resolve to: the .nvmrc and .node-version in each of
// paths and in each folder above it, any of which that is a link
// leading nowhere being an error. Given every path the plist names,
// as spelled and with symlinks resolved (fnm walks up from the working
// directory as getcwd returns it), that covers `--using=<file>`, a
// WorkingDirectory, a shell that cds, a login shell whose rc files run
// fnm, and a recursive version-file strategy, whether or not the plist
// names fnm; reading more only keeps more.
func (s *nodeSurvey) jobVersionFiles(ctx context.Context, paths []string, reason string) error {
	byReason := func(string) string { return reason }
	seen := map[string]bool{}
	for _, p := range paths {
		if err := s.linksBeforeDotDot(ctx, p); err != nil {
			return err
		}
		for dir := p; !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			if err := s.leadsNowhere(ctx, dir); err != nil {
				return err
			}
			for name := range nodeVersionFiles {
				if err := s.keepVersionFile(ctx, filepath.Join(dir, name), byReason); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// linksBeforeDotDot looks at each folder of path before its last ..
// as spelled: the system takes .. after the link before it, while
// filepath.Dir drops both by their text, so a link there that leads
// nowhere would go unseen.
func (s *nodeSurvey) linksBeforeDotDot(ctx context.Context, path string) error {
	parts := strings.Split(path, "/")
	last := 0
	for i, part := range parts {
		if part == ".." {
			last = i
		}
	}
	for i := 1; i < last; i++ {
		if err := s.leadsNowhere(ctx, strings.Join(parts[:i+1], "/")); err != nil {
			return err
		}
	}
	return nil
}

// holdsFolder reports whether text holds folder as whole path
// elements: followed by the end, a separator, or anything else that
// cannot continue a version's name.
func holdsFolder(text, folder string) bool {
	for rest := text; ; {
		i := strings.Index(rest, folder)
		if i < 0 {
			return false
		}
		rest = rest[i+len(folder):]
		if rest == "" || !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.-_+", rune(rest[0])) {
			return true
		}
	}
}

// pathCandidates lists the absolute paths a launchd job's words may
// name: each word whole, as the entries of a PATH-style list, and by
// the value after each = (NAME=value, --flag=value, NAME=--flag=value).
// A leading ~ is the home.
func pathCandidates(words []string, home string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/') {
			p = home + rest
		}
		if filepath.IsAbs(p) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	addList := func(text string) {
		add(text)
		for piece := range strings.SplitSeq(text, ":") {
			add(piece)
		}
	}
	for _, word := range words {
		addList(word)
		for rest := word; ; {
			_, value, ok := strings.Cut(rest, "=")
			if !ok {
				break
			}
			addList(value)
			rest = value
		}
	}
	return out
}

// jobWords lists the words a launchd string may hold, read three ways:
// as one argv word (a ProgramArguments item); as a shell reads the
// command line; and split at whitespace, every quote and ;&|()<>`=:,
// with a leading $HOME or ${HOME} read as the home. The last way reads
// what a shell hands on as one word: a command line nested in quotes
// (tmux, su -c, a second shell), or the rest of a script after an
// apostrophe in a comment.
func jobWords(text, home string) ([]string, error) {
	words, err := shellWords(text, home)
	if err != nil {
		return nil, err
	}
	words = append([]string{text}, words...)
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>`'\"=:", r)
	}) {
		for _, pre := range []string{"${HOME}", "$HOME"} {
			if rest, ok := strings.CutPrefix(w, pre); ok && (rest == "" || rest[0] == '/') {
				w = home + rest
				break
			}
		}
		words = append(words, w)
	}
	return words, nil
}

// wordEnd holds the characters that end a shell word.
const wordEnd = " \t\n\r;&|()<>`"

// shellWords splits a command line into the words a shell would run:
// wordEnd's characters end a word; quotes join what they hold into the
// word around them; a backslash outside single quotes keeps the next
// character as it is, and joins two lines when that is a newline.
// Outside single quotes $HOME, ${HOME} and ${HOME<anything>} are the
// home, and so is a ~ that starts a word (~user is that user's home).
// Every other expansion stays as written.
func shellWords(text, home string) ([]string, error) {
	var words []string
	var word strings.Builder
	started := false
	end := func() {
		if started {
			words = append(words, word.String())
		}
		word.Reset()
		started = false
	}
	// homeAt reads a $HOME expansion at text[i:], returning its length.
	homeAt := func(i int) int {
		rest := text[i:]
		if after, ok := strings.CutPrefix(rest, "${HOME"); ok {
			if j := strings.IndexByte(after, '}'); j >= 0 && (j == 0 || !isNameByte(after[0])) {
				return len("${HOME") + j + 1
			}
			return 0
		}
		if after, ok := strings.CutPrefix(rest, "$HOME"); ok && (after == "" || !isNameByte(after[0])) {
			return len("$HOME")
		}
		return 0
	}
	single, double := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			} else {
				word.WriteByte(c)
			}
		case c == '\\' && i+1 < len(text) && (!double || strings.IndexByte("$`\"\\\n", text[i+1]) >= 0):
			i++
			if text[i] == '\n' {
				continue
			}
			word.WriteByte(text[i])
			started = true
		case c == '$':
			if n := homeAt(i); n > 0 {
				word.WriteString(home)
				i += n - 1
			} else {
				word.WriteByte(c)
			}
			started = true
		case double:
			if c == '"' {
				double = false
			} else {
				word.WriteByte(c)
			}
		case c == '\'':
			single, started = true, true
		case c == '"':
			double, started = true, true
		case strings.IndexByte(wordEnd, c) >= 0:
			end()
		case c == '~' && !started:
			// The user name runs to a slash or the word's end; one that
			// is quoted, escaped or expanded is not looked up.
			j := i + 1
			for j < len(text) && text[j] != '/' && strings.IndexByte(wordEnd+"'\"\\$", text[j]) < 0 {
				j++
			}
			dir := ""
			if j == len(text) || text[j] == '/' || strings.IndexByte(wordEnd, text[j]) >= 0 {
				var err error
				if dir, err = userHome(text[i+1:j], home); err != nil {
					return nil, err
				}
			}
			if dir != "" {
				word.WriteString(dir)
				i = j - 1
			} else {
				word.WriteByte(c)
			}
			started = true
		default:
			word.WriteByte(c)
			started = true
		}
	}
	end()
	return words, nil
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// lookupUser is user.Lookup; tests replace it.
var lookupUser = user.Lookup

// userHome is the home of ~name, "" when no such user exists; ~ alone
// is the home being surveyed. A lookup that fails otherwise is an
// error: the path ~name starts is then unknown.
func userHome(name, home string) (string, error) {
	if name == "" {
		return home, nil
	}
	u, err := lookupUser(name)
	var unknown user.UnknownUserError
	switch {
	case errors.As(err, &unknown):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("~%s: %w", name, err)
	}
	return u.HomeDir, nil
}

// processes keeps every version a running process executes or maps a
// file from. Paths are compared folded, as lsof prints a path the way
// the filesystem stores it and FNM_DIR may spell it otherwise.
func (s *nodeSurvey) processes(ctx context.Context) error {
	names, err := s.mapped(ctx)
	if err != nil {
		return err
	}
	for i, v := range s.versions {
		if escapedByTools(v.real) {
			return fmt.Errorf("lsof would not print %s as it is, so a process running from it cannot be ruled out", v.real)
		}
		prefix := foldName(v.real) + "/"
		for _, name := range names {
			if strings.HasPrefix(foldName(name), prefix) {
				s.keep(i, "a running process uses "+name)
				break
			}
		}
	}
	return nil
}

// readSource reads a small file a keep decision depends on. One that
// is larger than any real version file or launchd plist is an error.
func readSource(ctx context.Context, w *walker, path string) ([]byte, error) {
	data, err := bounded(ctx, w, path, func() ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return io.ReadAll(io.LimitReader(f, maxSourceFile+1))
	})
	if err != nil {
		if absent(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(data) > maxSourceFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxSourceFile)
	}
	return data, nil
}

// maxNameUnits is the longest name macOS gives a file, in UTF-16
// units: up to 765 bytes of UTF-8.
const maxNameUnits = 255

// namesNoFile reports an open that failed because no file can have the
// name: it holds a NUL, or a name in it is longer than maxNameUnits.
func namesNoFile(path string, err error) bool {
	if !errors.Is(err, syscall.ENAMETOOLONG) && !errors.Is(err, syscall.EINVAL) {
		return false
	}
	return strings.IndexByte(path, 0) >= 0 ||
		slices.ContainsFunc(strings.Split(path, "/"), func(name string) bool {
			return len(utf16.Encode([]rune(name))) > maxNameUnits
		})
}

// lsofMapped lists the files the user's running processes execute or
// have mapped (lsof's txt entries). `ps` cannot answer this: its
// command column is argv[0], which a process sets itself (node's
// process.title) and which keeps a symlink unresolved. lsof reads
// other users' processes only as root, so those are not covered.
func lsofMapped(ctx context.Context) ([]string, error) {
	out, err := processTool(ctx, "lsof", "-a", "-u", strconv.Itoa(os.Getuid()), "-d", "txt", "-F", "pn")
	if err != nil {
		return nil, err
	}
	return mappedNames(string(out), os.Getpid())
}

// mappedNames reads the file names of `lsof -F pn` output taken by
// the process self. A name lsof escaped is kept as printed: a file
// inside a version's folder starts with the folder's path, which holds
// nothing lsof escapes (processes checks), so it never matches by
// mistake. A listing without self is not a listing of the user's
// processes.
func mappedNames(out string, self int) ([]string, error) {
	var names []string
	listed := false
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case line == "p"+strconv.Itoa(self):
			listed = true
		case strings.HasPrefix(line, "n") && len(line) > 1:
			names = append(names, line[1:])
		}
	}
	if !listed {
		return nil, errors.New("lsof did not list regrow's own process, so its answer cannot be trusted")
	}
	return names, nil
}
