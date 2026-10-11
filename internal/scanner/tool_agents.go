package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Claude Code gives every session a scratch directory,
// <tmp>/claude-<uid>/<project>/<session-id>/, and nothing clears it
// before a reboot. The scratch of an ended session is offered to the
// Trash (agent-scratch); every other session is kept, with the reason
// in its row (agent-scratch-kept).
//
// A session is live while a process runs it or works inside its
// scratch, or while its transcript or its scratch changed within
// agentWindow. A session whose scratch holds either end of a git
// worktree link that leaves it (a worktree of a repository elsewhere,
// or a repository with a worktree elsewhere) is kept too: worktrees are
// the git-worktrees rule's to remove, and trashing one end breaks the
// other. Whatever cannot be read in full is kept.

// agentWindow is how recent a change to a session's transcript or
// scratch keeps it live without a process.
const agentWindow = 24 * time.Hour

var windowText = strconv.Itoa(int(agentWindow.Hours())) + "h"

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// liveness is what the running processes say about the sessions.
type liveness struct {
	// sessions holds the ids of sessions a running process belongs to.
	sessions map[string]bool
	// paths are where running processes work: each registered
	// session's working directory and every process's argv[0] that is
	// an absolute path (ps comm, the executable only for a process
	// started by its full path).
	paths []string
	// configs are the config folders whose projects/ may hold a
	// session's transcript. configsPartial: the search for them was
	// cut short, so a transcript may sit in one that is not listed.
	configs        []string
	configsPartial bool
	// since is when the oldest running claude process that registers
	// nowhere started; zero when every one is registered. Which
	// session it runs is unknown, so nothing changed since then can be
	// shown to have ended.
	since time.Time
}

// recent says why a change at t keeps a session; "" when it does not.
func (l liveness) recent(t, now time.Time) string {
	switch {
	case now.Sub(t) < agentWindow:
		return "changed within " + windowText
	case !l.since.IsZero() && !t.Before(l.since):
		return "changed since an unregistered claude process started"
	}
	return ""
}

// inside reports whether a running process works in or below dir.
func (l liveness) inside(dir string) bool {
	for _, p := range l.paths {
		if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// agentSessions owns one scan's view of the agent sessions: listed
// once, read by both rules. The zero value reads the real machine;
// tests inject the root, both listers and the clock.
type agentSessions struct {
	// Root holds one directory per project, each holding one scratch
	// directory per session; "" means <tmp>/claude-<uid>.
	Root string
	// Live reports what running processes keep; nil reads Claude
	// Code's session registries and ps.
	Live func(ctx context.Context) (liveness, error)
	// Activity returns the newest change to a session's transcript
	// under one project, zero when it has none, and whether all of it
	// could be read; nil reads the projects folder of every config
	// folder Live found.
	Activity func(ctx context.Context, project, id string) (newest time.Time, complete bool)
	Now      func() time.Time
	w        *walker

	mu    sync.Mutex
	done  bool
	ended []engine.Item
	kept  []engine.Item
	err   error
}

// Ended is the agent-scratch query: sessions nothing keeps.
func (a *agentSessions) Ended(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.ended, a.err
}

// Kept is the agent-scratch-kept query: every other session.
func (a *agentSessions) Kept(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.kept, a.err
}

// RecheckAgentScratch is the agent-scratch pre-action: right before a
// session's scratch goes to the Trash it judges the session again and
// refuses when anything keeps it now. The scan may be hours old, and
// the session may have been resumed since.
func RecheckAgentScratch(ctx context.Context, path string) error {
	return (&agentSessions{}).recheck(ctx, path)
}

func (a *agentSessions) load(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	a.ended, a.kept, a.err = a.list(ctx)
}

func (a *agentSessions) walker() *walker {
	if a.w != nil {
		return a.w
	}
	return defaultWalker
}

func (a *agentSessions) live(ctx context.Context) (liveness, error) {
	if a.Live != nil {
		return a.Live(ctx)
	}
	return claudeLiveness(ctx, a.walker(), claudeHost{own: claudeConfigDir(), home: claudeHomeConfig(), procs: psProcesses, lines: psLines})
}

// transcript is the newest change to the session's transcript across
// the projects holding scratch for its id: a session resumed from
// another working directory writes its transcript under the new
// project, and that keeps its scratch under the first one too.
func (a *agentSessions) transcript(ctx context.Context, live liveness, projects []string, id string) (newest time.Time, complete bool) {
	complete = !live.configsPartial
	for _, project := range projects {
		var t time.Time
		var ok bool
		if a.Activity != nil {
			t, ok = a.Activity(ctx, project, id)
		} else {
			t, ok = claudeActivity(ctx, a.walker(), live.configs, project, id)
		}
		if t.After(newest) {
			newest = t
		}
		complete = complete && ok
	}
	return newest, complete
}

func (a *agentSessions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

type agentSession struct{ project, id, path string }

func (a *agentSessions) list(ctx context.Context) (ended, kept []engine.Item, err error) {
	w := a.walker()
	root := a.Root
	if root == "" {
		root = agentScratchRoot()
	}
	if root == "" {
		return nil, nil, nil
	}
	switch fi, err := w.stat(ctx, root); {
	case ctx.Err() != nil:
		return nil, nil, ctx.Err()
	case absent(err), err == nil && !fi.IsDir():
		return nil, nil, nil
	case err != nil:
		return nil, []engine.Item{unreadableMarker("sessions under " + root)}, nil
	}
	// Items carry the real path: git reports worktrees by real path,
	// and the containment forest compares paths as written.
	given := root
	root, err = bounded(ctx, w, given, func() (string, error) { return filepath.EvalSymlinks(given) })
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %s: %w", given, err)
	}
	sessions, partial, err := listSessions(ctx, w, root)
	if err != nil {
		return nil, nil, err
	}
	if partial {
		kept = append(kept, unreadableMarker("sessions under "+root))
	}
	if len(sessions) == 0 {
		return nil, kept, nil
	}
	live, err := a.live(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot tell which agent sessions are running: %w", err)
	}
	holders := map[string][]string{}
	for _, s := range sessions {
		holders[s.id] = append(holders[s.id], s.project)
	}
	for _, s := range sessions {
		item, reason, found, err := a.judge(ctx, s, holders[s.id], live)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
		}
		// A folder that could not be read may hold scratch, and so a
		// transcript, for any session id; the recheck refuses then too.
		if reason == "" && partial {
			reason = "a project folder could not be read, and it may hold this session too"
		}
		if reason == "" {
			ended = append(ended, item)
			continue
		}
		item.Label += " — " + reason
		kept = append(kept, item)
	}
	return ended, kept, nil
}

// judge measures one session and says why it is kept; "" means it has
// ended. projects are the ones holding scratch for its id. found is
// false when its scratch is gone. The item is keyed by path: a session
// id can own scratch under more than one project.
func (a *agentSessions) judge(ctx context.Context, s agentSession, projects []string, live liveness) (item engine.Item, reason string, found bool, err error) {
	w := a.walker()
	u, links, found, err := measureSession(ctx, w, s.path)
	if err != nil || !found {
		return engine.Item{}, "", found, err
	}
	changed, complete := a.transcript(ctx, live, projects, s.id)
	now := a.now()
	switch {
	case live.sessions[s.id]:
		reason = "running"
	case live.inside(s.path):
		reason = "a running process works inside it"
	case live.recent(changed, now) != "":
		reason = "transcript " + live.recent(changed, now)
	case live.recent(u.Newest, now) != "":
		reason = "scratch " + live.recent(u.Newest, now)
	case !complete:
		reason = "transcript not fully readable"
	case u.Partial:
		reason = "not fully readable, so git worktrees inside cannot be ruled out"
	default:
		reason = worktreeReason(linkedOutside(ctx, w, s.path, links))
	}
	if err := ctx.Err(); err != nil {
		return engine.Item{}, "", false, err
	}
	item = engine.Item{
		Path:     s.path,
		Label:    "session " + s.id[:8] + " · " + s.project,
		Bytes:    u.Bytes,
		LastUsed: u.Newest,
		Partial:  u.Partial,
	}
	if changed.After(item.LastUsed) {
		item.LastUsed = changed
	}
	return item, reason, true, nil
}

func (a *agentSessions) recheck(ctx context.Context, path string) error {
	id := filepath.Base(path)
	if !sessionIDRe.MatchString(id) {
		return fmt.Errorf("%s is not an agent session's scratch", path)
	}
	live, err := a.live(ctx)
	if err != nil {
		return fmt.Errorf("cannot tell whether session %s is running: %w", id[:8], err)
	}
	project := filepath.Base(filepath.Dir(path))
	projects, err := holdersOf(ctx, a.walker(), filepath.Dir(filepath.Dir(path)), id)
	if err != nil {
		return fmt.Errorf("cannot tell which projects hold session %s: %w", id[:8], err)
	}
	if !slices.Contains(projects, project) {
		projects = append(projects, project)
	}
	_, reason, found, err := a.judge(ctx, agentSession{project, id, path}, projects, live)
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("%s is gone", path)
	case reason != "":
		return fmt.Errorf("session %s is kept now: %s", id[:8], reason)
	}
	return nil
}

// listSessions finds <root>/<project>/<session-id> directories, sorted
// by path. Anything else under the root is not a session's and is left
// out. partial: a folder on the way refused or blocked.
func listSessions(ctx context.Context, w *walker, root string) (sessions []agentSession, partial bool, err error) {
	projects, partial, err := w.list(ctx, root)
	if err != nil {
		return nil, true, err
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		entries, incomplete, err := w.list(ctx, dir)
		if err != nil {
			return nil, true, err
		}
		// A folder can list and still refuse a look inside (r--). The
		// recheck stats inside it, so the scan has to fail there too.
		if _, err := w.lstat(ctx, dir+string(filepath.Separator)+"."); unreadable(err) {
			incomplete = true
		}
		partial = partial || incomplete
		for _, e := range entries {
			if e.IsDir() && sessionIDRe.MatchString(e.Name()) {
				sessions = append(sessions, agentSession{p.Name(), e.Name(), filepath.Join(dir, e.Name())})
			}
		}
	}
	slices.SortFunc(sessions, func(a, b agentSession) int { return strings.Compare(a.path, b.path) })
	return sessions, partial, nil
}

// holdersOf lists the projects under root that hold scratch for the
// session id. A project folder that cannot be looked into is an error:
// it could hold the session's scratch, and so its transcript.
func holdersOf(ctx context.Context, w *walker, root, id string) ([]string, error) {
	projects, partial, err := w.list(ctx, root)
	if err != nil {
		return nil, err
	}
	if partial {
		return nil, fmt.Errorf("%s is unreadable", root)
	}
	var out []string
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		switch fi, err := w.lstat(ctx, filepath.Join(root, p.Name(), id)); {
		case err == nil && fi.IsDir():
			out = append(out, p.Name())
		case err != nil && !absent(err):
			return nil, fmt.Errorf("%s: %w", filepath.Join(root, p.Name()), err)
		}
	}
	slices.Sort(out)
	return out, nil
}

// gitLinks are the files that tie a checkout to its repository: a
// checkout's .git file names the repository's git directory, and the
// repository's worktrees/<name>/gitdir names the checkout's .git file.
type gitLinks struct{ checkouts, registrations []string }

// measureSession sizes a session's scratch and collects its git links.
// found is false when the scratch no longer exists.
func measureSession(ctx context.Context, w *walker, path string) (Usage, gitLinks, bool, error) {
	var mu sync.Mutex
	closed := false
	var links gitLinks
	u, found, err := w.usageSeeing(ctx, path, func(dir string, e fs.DirEntry) {
		if !e.Type().IsRegular() {
			return
		}
		var list *[]string
		switch {
		case e.Name() == ".git":
			list = &links.checkouts
		case e.Name() == "gitdir" && filepath.Base(filepath.Dir(dir)) == "worktrees":
			list = &links.registrations
		default:
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			*list = append(*list, childPath(dir, e.Name()))
		}
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	slices.Sort(links.checkouts)
	slices.Sort(links.registrations)
	return u, links, found, err
}

// linkedOutside describes each git link that crosses the session's
// edge to something that still exists: a worktree whose repository
// lives elsewhere, or a repository with a worktree checked out
// elsewhere. Trashing either end breaks the other. A link that cannot
// be read counts.
func linkedOutside(ctx context.Context, w *walker, session string, links gitLinks) []string {
	var out []string
	for _, f := range links.checkouts {
		if crossesOut(ctx, w, session, f, "gitdir:") {
			out = append(out, "git worktree "+relTo(session, filepath.Dir(f)))
		}
	}
	for _, f := range links.registrations {
		if crossesOut(ctx, w, session, f, "") {
			repo := filepath.Dir(filepath.Dir(filepath.Dir(f)))
			if filepath.Base(repo) == ".git" {
				repo = filepath.Dir(repo)
			}
			out = append(out, "repository "+relTo(session, repo)+" with a worktree elsewhere")
		}
	}
	return out
}

// crossesOut reports whether the link file points outside the session
// at something that exists, comparing real paths. The target is the
// file's first line, after prefix; relative targets resolve against the
// file's folder. A file without a target is not a link.
func crossesOut(ctx context.Context, w *walker, session, file, prefix string) bool {
	data, err := bounded(ctx, w, file, func() ([]byte, error) {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return io.ReadAll(io.LimitReader(f, 4096))
	})
	if err != nil {
		return true
	}
	line, _, _ := strings.Cut(string(data), "\n")
	target, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
	target = strings.TrimSpace(target)
	if !ok || target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(file), target)
	}
	real, err := bounded(ctx, w, target, func() (string, error) { return filepath.EvalSymlinks(target) })
	if err != nil {
		return !absent(err)
	}
	return !strings.HasPrefix(real, session+string(filepath.Separator))
}

func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}

func worktreeReason(links []string) string {
	switch len(links) {
	case 0:
		return ""
	case 1:
		return "holds " + links[0] + "; remove the worktree first"
	}
	return fmt.Sprintf("holds %s and %d more; remove the worktrees first", links[0], len(links)-1)
}

// agentScratchRoot is where Claude Code keeps session scratch on
// macOS; "" elsewhere, where the layout is unverified.
func agentScratchRoot() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	return filepath.Join("/private/tmp", "claude-"+strconv.Itoa(os.Getuid()))
}

func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	return claudeHomeConfig()
}

// claudeHomeConfig is the config folder a process uses when its
// environment names none.
func claudeHomeConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// claudeActivity is the newest change to a session's transcript under
// one project, in any of the config folders: the <id>.jsonl file and
// the <id>/ folder beside it (subagent transcripts, tool results).
// complete is false when any of it could not be read.
func claudeActivity(ctx context.Context, w *walker, configs []string, project, id string) (newest time.Time, complete bool) {
	if len(configs) == 0 {
		return time.Time{}, false
	}
	complete = true
	for _, config := range configs {
		dir := filepath.Join(config, "projects", project)
		switch fi, err := w.lstat(ctx, filepath.Join(dir, id+".jsonl")); {
		case err == nil:
			if fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		case !absent(err):
			complete = false
		}
		if u, found, _ := w.usage(ctx, filepath.Join(dir, id)); found {
			if u.Newest.After(newest) {
				newest = u.Newest
			}
			complete = complete && !u.Partial
		}
	}
	return newest, complete
}

// process is one running process as ps reports it.
type process struct {
	ppid    int
	started time.Time
	exe     string
}

// claude reports whether the process is Claude Code by its argv[0]:
// the name claude, or the path a native install's binary starts itself
// by, <data>/claude/versions/<version>.
func (p process) claude() bool {
	if filepath.Base(p.exe) == "claude" {
		return true
	}
	versions := filepath.Dir(p.exe)
	return filepath.Base(versions) == "versions" && filepath.Base(filepath.Dir(versions)) == "claude"
}

// registration is one file of Claude Code's session registry.
type registration struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	ProcStart string `json:"procStart"`
}

// names reports whether the file is the running process's own, not one
// an earlier process with the same pid left behind: its pid field,
// where present, is the file's pid, and its procStart, where present,
// is when the process started. Claude Code writes procStart in ps
// lstart form, in UTC; local time is accepted too. A procStart that
// cannot be read is an error unless the pid field already rules the
// file out: accepting a leftover would hide the unregistered process
// at its pid.
func (r registration) names(pid int, p process) (bool, error) {
	if r.PID != 0 && r.PID != pid {
		return false, nil
	}
	if r.ProcStart == "" {
		return true, nil
	}
	utc, err := parseStart(r.ProcStart, time.UTC)
	if err != nil {
		return false, fmt.Errorf("procStart not understood: %w", err)
	}
	if p.started.IsZero() {
		return true, nil
	}
	local, _ := parseStart(r.ProcStart, time.Local)
	return utc.Equal(p.started) || local.Equal(p.started), nil
}

// claudeHost is how claudeLiveness sees the machine.
type claudeHost struct {
	// own is the config folder regrow itself runs with, home the one a
	// process uses when its environment names none.
	own, home string
	// procs lists the user's running processes by pid.
	procs func(context.Context) (map[int]process, error)
	// lines returns the `ps -E` line of each process that still runs:
	// its arguments, then its environment.
	lines func(ctx context.Context, pids []int) (map[int]string, error)
}

// claudeLiveness reads Claude Code's session registries: a running
// claude process keeps <config>/sessions/<pid>.json, naming its session
// and working directory, under the config folder it runs with. A
// registry file outlives a crashed process, so only files that name a
// running process count. A claude process in no registry is tolerated
// only below a registered session's process (a session can start
// claude processes that do not register), and only when its line names
// the config folder whose registry was read; the window then reaches
// back to its start. Any other unregistered claude process, a config
// folder a claude process names that is not there, or a registry or
// process listing that cannot be read, is an error: no session can be
// shown to have ended.
func claudeLiveness(ctx context.Context, w *walker, h claudeHost) (liveness, error) {
	procs, err := h.procs(ctx)
	if err != nil {
		return liveness{}, err
	}
	var claudes []int
	for pid, p := range procs {
		if p.claude() {
			claudes = append(claudes, pid)
		}
	}
	slices.Sort(claudes)

	lines, err := h.lines(ctx, claudes)
	if err != nil {
		return liveness{}, fmt.Errorf("command lines of the claude processes: %w", err)
	}
	registries := []string{h.own, h.home}
	// named is the config folder each claude process's line names.
	named := map[int]string{}
	for _, pid := range claudes {
		dir := lineConfigDir(lines[pid])
		if dir == "" {
			continue
		}
		// A running claude process has made its config folder. One that
		// is not there was misread off the line (a value is cut at a
		// space, ps prints a non-ASCII byte escaped, and an argument is
		// taken for the variable when the environment has none); its
		// registry and transcripts would then read as empty.
		switch fi, err := w.stat(ctx, dir); {
		case err != nil:
			return liveness{}, fmt.Errorf("config folder %s of claude process %d: %w", dir, pid, err)
		case !fi.IsDir():
			return liveness{}, fmt.Errorf("config folder %s of claude process %d is not a folder", dir, pid)
		}
		named[pid] = dir
		registries = append(registries, dir)
	}
	registries = distinctDirs(registries)

	reg := map[int][]registration{}
	for _, dir := range registries {
		if err := readRegistry(ctx, w, filepath.Join(dir, "sessions"), procs, reg); err != nil {
			return liveness{}, err
		}
	}

	live := liveness{sessions: map[string]bool{}}
	for _, pid := range claudes {
		// A session id the process was started with is its session,
		// registered or not.
		for _, id := range parseSessionArgs(lines[pid]) {
			live.sessions[id] = true
		}
		if len(reg[pid]) > 0 {
			continue
		}
		if !belowRegistered(procs, reg, pid) {
			return liveness{}, fmt.Errorf("claude process %d is in no session registry", pid)
		}
		// Only its arguments can name the session it resumed. A
		// registered process needs no line: its file names its session.
		if _, ok := lines[pid]; !ok {
			return liveness{}, fmt.Errorf("unregistered claude process %d: its command line is unknown", pid)
		}
		// It registers nowhere only if the registry of its own config
		// folder was read.
		if named[pid] == "" {
			return liveness{}, fmt.Errorf("unregistered claude process %d: its config folder is unknown", pid)
		}
		started := procs[pid].started
		if started.IsZero() {
			return liveness{}, fmt.Errorf("unregistered claude process %d: its start time is unknown", pid)
		}
		if live.since.IsZero() || started.Before(live.since) {
			live.since = started
		}
	}
	for _, rs := range reg {
		for _, r := range rs {
			live.sessions[strings.ToLower(r.SessionID)] = true
			live.paths = append(live.paths, scanSpelling(r.Cwd))
		}
	}
	for _, p := range procs {
		if filepath.IsAbs(p.exe) {
			live.paths = append(live.paths, scanSpelling(p.exe))
		}
	}
	slices.Sort(live.paths)
	live.paths = slices.Compact(live.paths)
	siblings, partial := siblingConfigs(ctx, w, h.home)
	live.configs, live.configsPartial = distinctDirs(append(registries, siblings...)), partial
	return live, nil
}

// belowRegistered reports whether an ancestor of pid is a registered
// session's process: a claude process that a registry file names. A
// file left at a reused pid can name any process, so the name counts.
func belowRegistered(procs map[int]process, reg map[int][]registration, pid int) bool {
	for range 64 {
		p, ok := procs[pid]
		if !ok || p.ppid == pid {
			return false
		}
		pid = p.ppid
		if len(reg[pid]) > 0 && procs[pid].claude() {
			return true
		}
	}
	return false
}

// siblingConfigs finds config folders beside the default one that no
// running process names any more: folders whose name starts with the
// default's and that hold a projects folder. partial: the folder
// holding them could not be listed in full, or one of them could not
// be looked into, so a config folder may be missing.
func siblingConfigs(ctx context.Context, w *walker, home string) (dirs []string, partial bool) {
	if home == "" {
		return nil, true
	}
	parent, base := filepath.Dir(home), filepath.Base(home)
	entries, partial, err := w.list(ctx, parent)
	if err != nil {
		return nil, true
	}
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), base) || e.Name() == base || e.Type().IsRegular() {
			continue
		}
		dir := filepath.Join(parent, e.Name())
		switch fi, err := w.stat(ctx, filepath.Join(dir, "projects")); {
		case err == nil && fi.IsDir():
			out = append(out, dir)
		case unreadable(err):
			partial = true
		}
	}
	slices.Sort(out)
	return out, partial
}

// distinctDirs drops empty and repeated folders, keeping order.
func distinctDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if d = filepath.Clean(d); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// readRegistry adds to reg the registry files in dir that name a
// running process; one process can be named in several folders. A
// missing folder holds none. A folder that cannot be listed, or a
// running pid's file that cannot be read or parsed (files are
// rewritten in place), is an error. Whether a file is the process's
// own is asked next: a leftover registers nothing, whatever else it
// holds. A file that is the process's own and names no session id or
// no working directory is an error.
func readRegistry(ctx context.Context, w *walker, dir string, procs map[int]process, reg map[int][]registration) error {
	entries, partial, err := w.list(ctx, dir)
	if err != nil {
		return err
	}
	if partial {
		return fmt.Errorf("session registry %s is unreadable", dir)
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(stem)
		if err != nil {
			continue
		}
		p, running := procs[pid]
		if !running {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := bounded(ctx, w, path, func() ([]byte, error) { return os.ReadFile(path) })
		var r registration
		if err == nil {
			err = json.Unmarshal(data, &r)
		}
		own := false
		if err == nil {
			own, err = r.names(pid, p)
		}
		switch {
		case err != nil:
		case !own:
			continue
		case !sessionIDRe.MatchString(strings.ToLower(r.SessionID)):
			err = errors.New("sessionId is not a session id")
		case !filepath.IsAbs(r.Cwd):
			err = errors.New("no working directory")
		}
		if err != nil {
			return fmt.Errorf("registry file %s of running process %d: %w", path, pid, err)
		}
		reg[pid] = append(reg[pid], r)
	}
	return nil
}

// scanSpelling spells a path the way the scan does: on macOS /tmp,
// /var and /etc are symlinks into /private.
func scanSpelling(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "darwin" {
		for _, link := range []string{"/tmp", "/var", "/etc"} {
			if p == link || strings.HasPrefix(p, link+"/") {
				return "/private" + p
			}
		}
	}
	return p
}

// runPS runs ps in the C locale and UTC, so start times read the same
// on every machine and match what Claude Code records.
func runPS(ctx context.Context, args ...string) (string, error) {
	path, err := exec.LookPath("ps")
	if err != nil {
		return "", errors.New("ps is not available")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	cmd.WaitDelay = toolWaitDelay
	out, err := cmd.Output()
	return string(out), err
}

// psProcesses lists the user's running processes.
func psProcesses(ctx context.Context) (map[int]process, error) {
	out, err := runPS(ctx, "-x", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,ppid=,lstart=,comm=")
	if err != nil {
		return nil, err
	}
	return parsePS(out)
}

// psLineRe splits a `pid ppid lstart comm` line; lstart is five words
// ("Sun Oct  4 10:02:27 2026") and comm may hold spaces.
var psLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+[\d:]+\s+\d{4})\s+(.+)$`)

// parsePS reads the listing. A line or start time it cannot read is an
// error: a process left out would read as not running, and one without
// its start would let a leftover registry file name it.
func parsePS(out string) (map[int]process, error) {
	procs := map[int]process{}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := psLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("ps line not understood: %q", line)
		}
		started, err := parseStart(m[3], time.UTC)
		if err != nil {
			return nil, fmt.Errorf("ps start time not understood: %q", line)
		}
		pid, _ := strconv.Atoi(m[1])
		ppid, _ := strconv.Atoi(m[2])
		procs[pid] = process{ppid: ppid, started: started, exe: strings.TrimSpace(m[4])}
	}
	return procs, nil
}

// parseStart reads a ps lstart time in the given zone.
func parseStart(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(s), " "), loc)
}

// psLines returns the `ps -E` line of each process that still runs,
// arguments first and then the environment, which ps shows for the
// user's own processes but not for Apple's own binaries.
func psLines(ctx context.Context, pids []int) (map[int]string, error) {
	if len(pids) == 0 {
		return nil, nil
	}
	// ps leaves out a pid that exited since the listing and fails only
	// when it finds none of them. regrow's own pid is always found, so
	// a failure is ps's own.
	list := []string{strconv.Itoa(os.Getpid())}
	for _, pid := range pids {
		list = append(list, strconv.Itoa(pid))
	}
	out, err := runPS(ctx, "-ww", "-E", "-o", "pid=,command=", "-p", strings.Join(list, ","))
	if err != nil {
		return nil, err
	}
	return parsePSLines(out), nil
}

// parsePSLines reads `ps -o pid=,command=` output.
func parsePSLines(out string) map[int]string {
	lines := map[int]string{}
	for line := range strings.SplitSeq(out, "\n") {
		pid, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			lines[n] = rest
		}
	}
	return lines
}

var (
	configDirRe = regexp.MustCompile(`(?:^|\s)CLAUDE_CONFIG_DIR=(\S+)`)
	homeDirRe   = regexp.MustCompile(`(?:^|\s)HOME=(\S+)`)
)

// lineConfigDir is the config folder a process runs with, read from
// its `ps -E` line: CLAUDE_CONFIG_DIR, else .claude under its HOME; ""
// when the line shows neither, or a relative one.
func lineConfigDir(line string) string {
	dir := lastEnv(configDirRe, line)
	if dir == "" {
		if home := lastEnv(homeDirRe, line); home != "" {
			dir = filepath.Join(home, ".claude")
		}
	}
	if !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// lastEnv finds a variable's value on a `ps -E -o command=` line.
// Arguments come before the environment there, so the last match is
// the variable when the environment holds it; an argument that merely
// looks like it comes earlier, and is the only match when the
// environment does not. A value with spaces is cut short and names a
// folder that is not the process's; claudeLiveness refuses one that
// does not exist.
func lastEnv(re *regexp.Regexp, line string) string {
	all := re.FindAllStringSubmatch(line, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1]
}

var sessionArgRe = regexp.MustCompile(`(?:^|\s)(?:--resume|-r|--session-id)[=\s](?i:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}))\b`)

// parseSessionArgs finds the session ids a process was started with
// (`--resume <id>`, `-r <id>`, `--session-id <id>`) on its `ps -E`
// line, in the lower case session folders are named in.
func parseSessionArgs(line string) []string {
	var ids []string
	for _, m := range sessionArgRe.FindAllStringSubmatch(line, -1) {
		ids = append(ids, strings.ToLower(m[1]))
	}
	return ids
}
