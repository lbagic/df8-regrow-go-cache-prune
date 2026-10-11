package scanner

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Synthetic session ids, one per role in the fixture.
const (
	sidEnded      = "aaaaaaaa-0000-4000-8000-000000000001"
	sidRunning    = "bbbbbbbb-0000-4000-8000-000000000002"
	sidChatting   = "cccccccc-0000-4000-8000-000000000003"
	sidOldChat    = "dddddddd-0000-4000-8000-000000000004"
	sidWriting    = "eeeeeeee-0000-4000-8000-000000000005"
	sidWorktree   = "ffffffff-0000-4000-8000-000000000006"
	sidInnerLinks = "abababab-0000-4000-8000-000000000007"
	sidOrphanWT   = "cdcdcdcd-0000-4000-8000-000000000008"
	sidLocked     = "efefefef-0000-4000-8000-000000000009"
	sidRepo       = "fafafafa-0000-4000-8000-00000000000a"
	sidHost       = "acacacac-0000-4000-8000-00000000000b"
	sidTool       = "adadadad-0000-4000-8000-00000000000c"
	sidTornLog    = "aeaeaeae-0000-4000-8000-00000000000d"
	sidVanished   = "afafafaf-0000-4000-8000-00000000000e"
	sidMoved      = "babababa-0000-4000-8000-00000000000f"
	sidSince      = "bcbcbcbc-0000-4000-8000-000000000010"
	sidSinceChat  = "bdbdbdbd-0000-4000-8000-000000000011"

	projDir  = "-Users-dev-proj"
	otherDir = "-Users-dev-other"
)

// ageTree sets every mtime under root to at.
func ageTree(t *testing.T, root string, at time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// agentFixture lays out a scratch root holding one session per role,
// plus entries that are not sessions, all last changed three days ago
// unless the role says otherwise. Everything, git links included, is
// written through a symlinked folder; real is the root's real path.
func agentFixture(t *testing.T, now time.Time) (root, real string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "via")
	if err := os.Mkdir(filepath.Join(filepath.Dir(tmp), "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", tmp); err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(tmp, "claude-1000")
	repo := filepath.Join(tmp, "repo", ".git", "worktrees", "wt")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, projDir)
	other := filepath.Join(root, otherDir)
	for _, sid := range []string{
		sidEnded, sidRunning, sidChatting, sidOldChat, sidWorktree, sidInnerLinks, sidOrphanWT,
		sidLocked, sidRepo, sidHost, sidTool, sidTornLog, sidVanished, sidMoved, sidSince, sidSinceChat,
	} {
		writeFile(t, filepath.Join(proj, sid, "scratchpad", "notes.txt"), 40_000)
	}
	writeFile(t, filepath.Join(other, sidWriting, "tasks", "job.output"), 1000)
	// One session id with scratch under two projects.
	writeFile(t, filepath.Join(other, sidMoved, "scratchpad", "notes.txt"), 1000)
	writeFile(t, filepath.Join(proj, sidHost, "scratchpad", "clone", "main.go"), 1000)
	writeFile(t, filepath.Join(proj, sidTool, "node_modules", ".bin", "tool"), 1000)
	writeText(t, filepath.Join(proj, sidWorktree, "scratchpad", "wt", ".git"), "gitdir: "+repo+"\n")
	if err := os.MkdirAll(filepath.Join(proj, sidInnerLinks, "clone", ".git", "modules", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", "sub", ".git"), "gitdir: ../.git/modules/sub\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", "inner", ".git"), "gitdir: ../.git/worktrees/inner\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", ".git", "worktrees", "inner", "gitdir"), filepath.Join(proj, sidInnerLinks, "clone", "inner", ".git")+"\n")
	writeText(t, filepath.Join(proj, sidInnerLinks, "clone", ".git", "worktrees", "pruned", "gitdir"), filepath.Join(tmp, "gone", "pruned", ".git")+"\n")
	elsewhere := filepath.Join(tmp, "elsewhere", "wt", ".git")
	writeText(t, elsewhere, "gitdir: "+filepath.Join(proj, sidRepo, "clone", ".git", "worktrees", "wt")+"\n")
	writeText(t, filepath.Join(proj, sidRepo, "clone", ".git", "worktrees", "wt", "gitdir"), elsewhere+"\n")
	writeText(t, filepath.Join(proj, sidOrphanWT, "wt", ".git"), "gitdir: "+filepath.Join(tmp, "gone", ".git", "worktrees", "wt")+"\n")
	writeFile(t, filepath.Join(proj, "not-a-session", "x"), 1000)
	writeFile(t, filepath.Join(proj, "cache-state.json"), 10)
	writeFile(t, filepath.Join(root, "stray.txt"), 10)

	ageTree(t, root, now.Add(-72*time.Hour))
	locked := filepath.Join(proj, sidLocked, "scratchpad")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	recent := now.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(other, sidWriting, "tasks", "job.output"), recent, recent); err != nil {
		t.Fatal(err)
	}
	yesterday := now.Add(-30 * time.Hour)
	if err := os.Chtimes(filepath.Join(proj, sidSince, "scratchpad", "notes.txt"), yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real == root {
		t.Fatalf("fixture root must be reached through a symlink: %q, %v", real, err)
	}
	return root, real
}

// sessionKey names an item by project and session id.
func sessionKey(it engine.Item) string {
	return filepath.Base(filepath.Dir(it.Path)) + "/" + filepath.Base(it.Path)
}

func TestAgentSessionsSplit(t *testing.T) {
	now := time.Now()
	root, real := agentFixture(t, now)
	liveCalls := 0
	type activity struct {
		at     time.Time
		unread bool
	}
	transcripts := map[string]activity{
		projDir + "/" + sidChatting: {at: now.Add(-2 * time.Hour)},
		projDir + "/" + sidOldChat:  {at: now.Add(-48 * time.Hour)},
		projDir + "/" + sidTornLog:  {unread: true},
		// Resumed from another working directory: the transcript moved
		// to the other project and keeps the scratch under both.
		otherDir + "/" + sidMoved:    {at: now.Add(-time.Hour)},
		projDir + "/" + sidSinceChat: {at: now.Add(-30 * time.Hour)},
		// Another session's transcript under the other project.
		otherDir + "/" + sidEnded: {at: now.Add(-time.Hour)},
	}
	a := &agentSessions{
		Root: root,
		Live: func(context.Context) (liveness, error) {
			liveCalls++
			// Gone between listing and measuring.
			if err := os.RemoveAll(filepath.Join(real, projDir, sidVanished)); err != nil {
				t.Fatal(err)
			}
			return liveness{
				sessions: map[string]bool{sidRunning: true},
				paths: []string{
					filepath.Join(real, projDir, sidHost, "scratchpad", "clone"),
					filepath.Join(real, projDir, sidTool, "node_modules", ".bin", "tool"),
					filepath.Join(real, projDir, sidEnded+"-sibling", "bin", "tool"),
				},
				// An unregistered claude process started 36 hours ago.
				since: now.Add(-36 * time.Hour),
			}, nil
		},
		Activity: func(_ context.Context, project, id string) (time.Time, bool) {
			tr := transcripts[project+"/"+id]
			return tr.at, !tr.unread
		},
		Now: func() time.Time { return now },
		w:   testWalker(),
	}

	ended, err := a.Ended(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kept, err := a.Kept(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if liveCalls != 1 {
		t.Errorf("process lister called %d times, want once per scan", liveCalls)
	}

	for _, it := range slices.Concat(ended, kept) {
		if !strings.HasPrefix(it.Path, real+"/") || !sessionIDRe.MatchString(filepath.Base(it.Path)) {
			t.Errorf("item path %q: want a session dir under the real root %q", it.Path, real)
		}
		if key := it.DeriveKey(""); key != it.Path {
			t.Errorf("item %q is keyed %q, want its path: one session id can own scratch under two projects", it.Path, key)
		}
	}

	var gotEnded []string
	for _, it := range ended {
		gotEnded = append(gotEnded, sessionKey(it))
		if it.Bytes < 40_000 {
			t.Errorf("ended item %q measured %d bytes, want its scratch (≥ 40000)", sessionKey(it), it.Bytes)
		}
		if sessionKey(it) == projDir+"/"+sidOldChat && !it.LastUsed.Equal(now.Add(-48*time.Hour)) {
			t.Errorf("last used of a session whose transcript is newer than its scratch = %v, want the transcript's", it.LastUsed)
		}
	}
	slices.Sort(gotEnded)
	wantEnded := []string{projDir + "/" + sidEnded, projDir + "/" + sidInnerLinks, projDir + "/" + sidOrphanWT, projDir + "/" + sidOldChat}
	if !slices.Equal(gotEnded, wantEnded) {
		t.Errorf("ended sessions = %v, want %v", gotEnded, wantEnded)
	}

	wantKept := map[string]string{
		projDir + "/" + sidRunning:   "running",
		projDir + "/" + sidHost:      "a running process works inside it",
		projDir + "/" + sidTool:      "a running process works inside it",
		projDir + "/" + sidChatting:  "transcript changed within 24h",
		projDir + "/" + sidMoved:     "transcript changed within 24h",
		otherDir + "/" + sidMoved:    "transcript changed within 24h",
		projDir + "/" + sidSince:     "scratch changed since an unregistered claude process started",
		projDir + "/" + sidSinceChat: "transcript changed since an unregistered claude process started",
		otherDir + "/" + sidWriting:  "scratch changed within 24h",
		projDir + "/" + sidTornLog:   "transcript not fully readable",
		projDir + "/" + sidLocked:    "not fully readable, so git worktrees",
		projDir + "/" + sidWorktree:  "holds git worktree scratchpad/wt; remove the worktree first",
		projDir + "/" + sidRepo:      "holds repository clone with a worktree elsewhere",
	}
	gotKept := map[string]string{}
	for _, it := range kept {
		gotKept[sessionKey(it)] = it.Label
	}
	if len(gotKept) != len(wantKept) {
		t.Errorf("kept %d sessions, want %d: %v", len(gotKept), len(wantKept), slices.Sorted(maps.Keys(gotKept)))
	}
	for key, reason := range wantKept {
		if label, ok := gotKept[key]; !ok {
			t.Errorf("session %s not kept (want %q)", key, reason)
		} else if !strings.Contains(label, " — "+reason) {
			t.Errorf("kept session %s label %q, want reason %q", key, label, reason)
		}
	}
}

func TestAgentSessionsUnknownLivenessOffersNothing(t *testing.T) {
	now := time.Now()
	root, _ := agentFixture(t, now)
	a := &agentSessions{
		Root:     root,
		Live:     func(context.Context) (liveness, error) { return liveness{}, errors.New("ps failed") },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        testWalker(),
	}
	ended, err := a.Ended(context.Background())
	if err == nil || len(ended) != 0 {
		t.Fatalf("Ended = %d items, err %v; want an error and nothing offered", len(ended), err)
	}
	if _, err := a.Kept(context.Background()); err == nil {
		t.Error("Kept: want the same error")
	}
}

func TestAgentSessionsNoRoot(t *testing.T) {
	a := &agentSessions{
		Root: filepath.Join(t.TempDir(), "claude-1000"),
		Live: func(context.Context) (liveness, error) {
			t.Fatal("no sessions: liveness must not be asked")
			return liveness{}, nil
		},
		w: testWalker(),
	}
	ended, err := a.Ended(context.Background())
	kept, _ := a.Kept(context.Background())
	if err != nil || len(ended)+len(kept) != 0 {
		t.Errorf("missing root: ended %v, kept %v, err %v; want nothing", ended, kept, err)
	}
}

// A root that refuses is not "no sessions": the kept rule says so, and
// nothing is offered.
func TestAgentSessionsUnreadableRoot(t *testing.T) {
	locked := filepath.Join(t.TempDir(), "locked")
	root := filepath.Join(locked, "claude-1000")
	writeFile(t, filepath.Join(root, projDir, sidEnded, "scratchpad", "notes.txt"), 1000)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	a := &agentSessions{
		Root: root,
		Live: func(context.Context) (liveness, error) {
			t.Fatal("no sessions listed: liveness must not be asked")
			return liveness{}, nil
		},
		w: testWalker(),
	}
	ended, err := a.Ended(context.Background())
	kept, _ := a.Kept(context.Background())
	if err != nil || len(ended) != 0 {
		t.Fatalf("Ended = %v, err %v; want nothing offered", ended, err)
	}
	if len(kept) != 1 || kept[0].Path != "" || !kept[0].Partial || !strings.Contains(kept[0].Label, root) {
		t.Errorf("Kept = %+v, want one unreadable marker naming the root", kept)
	}
}

// With a project folder that cannot be read, another project may hold
// scratch for any session, and so its transcript. The recheck refuses
// then, so the scan must not offer what the recheck would refuse.
func TestAgentSessionsKeepAllWhenAProjectCannotBeRead(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	session := filepath.Join(root, projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	locked := filepath.Join(root, otherDir)
	writeFile(t, filepath.Join(locked, sidEnded, "scratchpad", "notes.txt"), 1000)
	ageTree(t, root, now.Add(-72*time.Hour))
	scan := func() (ended, kept []engine.Item) {
		t.Helper()
		a := &agentSessions{
			Root:     root,
			Live:     func(context.Context) (liveness, error) { return liveness{}, nil },
			Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
			Now:      func() time.Time { return now },
			w:        testWalker(),
		}
		ended, err := a.Ended(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		kept, _ = a.Kept(context.Background())
		return ended, kept
	}
	if ended, kept := scan(); len(ended) != 2 || len(kept) != 0 {
		t.Fatalf("readable projects: ended %d, kept %d; want both sessions ended", len(ended), len(kept))
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ended, kept := scan()
	if len(ended) != 0 {
		t.Errorf("ended = %+v, want nothing offered", ended)
	}
	var marker, sessionKept bool
	for _, it := range kept {
		switch it.Path {
		case "":
			marker = it.Partial
		case session:
			sessionKept = strings.HasSuffix(it.Label, " — a project folder could not be read, and it may hold this session too")
		}
	}
	if len(kept) != 2 || !marker || !sessionKept {
		t.Errorf("kept = %+v, want the unreadable marker and the session with its reason", kept)
	}
}

// A guest session runs with its working directory inside a host
// session's scratch, and a tool started from a third session's scratch
// is still running. The host and the tool's session have no process of
// their own, yet both must be kept, through the registry and ps alone.
func TestAgentSessionsHostGuest(t *testing.T) {
	const host, guest, tool, ended = sidHost, sidRunning, sidTool, sidEnded
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	for _, sid := range []string{host, guest, tool, ended} {
		writeFile(t, filepath.Join(root, projDir, sid, "scratchpad", "notes.txt"), 1000)
	}
	clone := filepath.Join(root, projDir, host, "scratchpad", "clone")
	exe := filepath.Join(root, projDir, tool, "node_modules", ".bin", "tool")
	writeFile(t, filepath.Join(clone, "main.go"), 100)
	writeFile(t, exe, 100)
	ageTree(t, root, now.Add(-72*time.Hour))

	config := t.TempDir()
	writeText(t, filepath.Join(config, "sessions", "700.json"), `{"pid":700,"sessionId":"`+guest+`","cwd":"`+clone+`"}`)
	machine := claudeHost{own: config, home: config,
		procs: func(context.Context) (map[int]process, error) {
			return map[int]process{1: {exe: "/sbin/launchd"}, 700: {ppid: 1, exe: "claude"}, 701: {ppid: 1, exe: exe}}, nil
		},
		lines: func(context.Context, []int) (map[int]string, error) { return nil, nil },
	}
	w := testWalker()
	a := &agentSessions{
		Root:     root,
		Live:     func(ctx context.Context) (liveness, error) { return claudeLiveness(ctx, w, machine) },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        w,
	}
	endedItems, err := a.Ended(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keptItems, _ := a.Kept(context.Background())

	if len(endedItems) != 1 || filepath.Base(endedItems[0].Path) != ended {
		t.Errorf("ended = %+v, want only the session nothing touches", endedItems)
	}
	want := map[string]string{host: "a running process works inside it", tool: "a running process works inside it", guest: "running"}
	for _, it := range keptItems {
		sid := filepath.Base(it.Path)
		if reason, ok := want[sid]; !ok || !strings.HasSuffix(it.Label, " — "+reason) {
			t.Errorf("kept %s with label %q, want reason %q", sid, it.Label, want[sid])
		}
		delete(want, sid)
	}
	if len(want) != 0 {
		t.Errorf("not kept: %v", want)
	}
}

func TestRecheckAgentScratch(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "claude-1000", projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	writeFile(t, filepath.Join(root, "claude-1000", otherDir, sidEnded, "scratchpad", "notes.txt"), 1000)
	writeFile(t, filepath.Join(root, "claude-1000", projDir, "not-a-session", "x"), 10)
	// A session id that names a file: no project holds a folder for it.
	notAFolder := filepath.Join(root, "claude-1000", projDir, sidOldChat)
	writeFile(t, notAFolder, 10)
	ageTree(t, root, now.Add(-72*time.Hour))

	quiet := func(context.Context) (liveness, error) { return liveness{sessions: map[string]bool{}}, nil }
	cases := []struct {
		name      string
		path      string
		live      func(context.Context) (liveness, error)
		changed   time.Time
		changedIn string
		wantErr   string
	}{
		{name: "still ended", path: session, live: quiet},
		{name: "resumed since the scan", path: session, live: func(context.Context) (liveness, error) {
			return liveness{sessions: map[string]bool{sidEnded: true}}, nil
		}, wantErr: "running"},
		{name: "a process moved in", path: session, live: func(context.Context) (liveness, error) {
			return liveness{paths: []string{filepath.Join(session, "scratchpad")}}, nil
		}, wantErr: "works inside"},
		{name: "transcript written since", path: session, live: quiet, changed: now.Add(-time.Minute), changedIn: projDir, wantErr: "transcript changed within"},
		{name: "resumed from another project since", path: session, live: quiet, changed: now.Add(-time.Minute), changedIn: otherDir, wantErr: "transcript changed within"},
		{name: "a project without its scratch does not count", path: session, live: quiet, changed: now.Add(-time.Minute), changedIn: "-Users-dev-third"},
		{name: "its own project counts even when the path is no folder", path: notAFolder, live: quiet, changed: now.Add(-time.Minute), changedIn: projDir, wantErr: "transcript changed within"},
		{name: "an unregistered claude process started before its last change", path: session, live: func(context.Context) (liveness, error) {
			return liveness{since: now.Add(-80 * time.Hour)}, nil
		}, wantErr: "scratch changed since an unregistered claude process started"},
		{name: "liveness unknown", path: session, live: func(context.Context) (liveness, error) {
			return liveness{}, errors.New("ps failed")
		}, wantErr: "cannot tell"},
		{name: "already gone", path: filepath.Join(filepath.Dir(session), sidVanished), live: quiet, wantErr: "is gone"},
		{name: "not a session", path: filepath.Join(filepath.Dir(session), "not-a-session"), live: quiet, wantErr: "not an agent session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &agentSessions{
				Live: tc.live,
				Activity: func(_ context.Context, project, _ string) (time.Time, bool) {
					if project == tc.changedIn {
						return tc.changed, true
					}
					return time.Time{}, true
				},
				Now: func() time.Time { return now },
				w:   testWalker(),
			}
			err := a.recheck(context.Background(), tc.path)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("recheck refused an ended session: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("recheck = %v, want a refusal naming %q", err, tc.wantErr)
			}
		})
	}
}

// started is when the fixture's processes started.
var started = time.Date(2026, 10, 4, 10, 2, 27, 0, time.UTC)

const startedText = "Sun Oct  4 10:02:27 2026"

// At recheck a project folder that cannot be looked into could hold
// the session's scratch, and with it a transcript written since.
func TestRecheckRefusesWhenAProjectCannotBeRead(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "claude-1000", projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	locked := filepath.Join(root, "claude-1000", otherDir)
	writeFile(t, filepath.Join(locked, sidEnded, "scratchpad", "notes.txt"), 1000)
	ageTree(t, root, now.Add(-72*time.Hour))
	a := &agentSessions{
		Live:     func(context.Context) (liveness, error) { return liveness{}, nil },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        testWalker(),
	}
	if err := a.recheck(context.Background(), session); err != nil {
		t.Fatalf("readable projects: recheck refused an ended session: %v", err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := a.recheck(context.Background(), session); err == nil || !strings.Contains(err.Error(), "cannot tell which projects hold") {
		t.Errorf("recheck = %v, want a refusal: a project folder could not be read", err)
	}
}

// A scratch root that lists only in part may hide a project holding
// the session's scratch: the recheck must refuse, not go on with the
// projects it saw.
func TestRecheckRefusesWhenTheScratchRootCannotBeListed(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	session := filepath.Join(root, projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	ageTree(t, root, now.Add(-72*time.Hour))
	w := testWalker()
	w.open = func(path string) (dirHandle, error) {
		if path == root {
			return nil, syscall.EACCES
		}
		return openDir(path)
	}
	a := &agentSessions{
		Live:     func(context.Context) (liveness, error) { return liveness{}, nil },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        w,
	}
	if err := a.recheck(context.Background(), session); err == nil || !strings.Contains(err.Error(), "cannot tell which projects hold") {
		t.Errorf("recheck = %v, want a refusal: the scratch root could not be listed", err)
	}
}

// A project folder that lists but refuses a look inside (r--) is the
// same case as one that cannot be listed: the recheck's stat inside it
// fails, so the scan must offer nothing either.
func TestAgentSessionsKeepAllWhenAProjectListsButRefuses(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	session := filepath.Join(root, projDir, sidEnded)
	writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
	listOnly := filepath.Join(root, otherDir)
	writeFile(t, filepath.Join(listOnly, sidRunning, "scratchpad", "notes.txt"), 1000)
	ageTree(t, root, now.Add(-72*time.Hour))
	if err := os.Chmod(listOnly, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(listOnly, 0o755) })

	a := &agentSessions{
		Root:     root,
		Live:     func(context.Context) (liveness, error) { return liveness{}, nil },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        testWalker(),
	}
	ended, err := a.Ended(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 0 {
		t.Errorf("ended = %+v, want nothing offered", ended)
	}
	kept, _ := a.Kept(context.Background())
	sessionKept := false
	for _, it := range kept {
		if it.Path == session {
			sessionKept = strings.HasSuffix(it.Label, " — a project folder could not be read, and it may hold this session too")
		}
	}
	if !sessionKept {
		t.Errorf("kept = %+v, want the readable project's session with its reason", kept)
	}
	b := &agentSessions{Live: a.Live, Activity: a.Activity, Now: a.Now, w: testWalker()}
	if err := b.recheck(context.Background(), session); err == nil || !strings.Contains(err.Error(), "cannot tell which projects hold") {
		t.Errorf("recheck = %v, want the refusal the scan stands for", err)
	}
}

// Claude Code starts a team-mate in a terminal pane by the path of its
// own binary, outside its session's process tree, and the team-mate
// registers nowhere. Which session it runs is unknown, so nothing may be
// offered while it runs, and the recheck refuses.
func TestAgentSessionsUnregisteredClaudeByVersionedPath(t *testing.T) {
	const lead, mate = sidRunning, sidEnded
	const binary = "/Users/dev/.local/share/claude/versions/2.1.296"
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	for _, sid := range []string{lead, mate} {
		writeFile(t, filepath.Join(root, projDir, sid, "scratchpad", "notes.txt"), 1000)
	}
	ageTree(t, root, now.Add(-30*time.Hour))

	config := filepath.Join(t.TempDir(), ".claude")
	writeText(t, filepath.Join(config, "sessions", "100.json"), `{"pid":100,"sessionId":"`+lead+`","cwd":"/work/proj"}`)
	begun := now.Add(-40 * time.Hour).UTC().Truncate(time.Second)
	env := " TERM=xterm HOME=" + filepath.Dir(config)
	machine := claudeHost{own: config, home: config,
		procs: func(context.Context) (map[int]process, error) {
			return map[int]process{
				1:   {exe: "/sbin/launchd"},
				100: {ppid: 1, started: begun, exe: "claude"},
				800: {ppid: 1, started: begun, exe: "tmux"},
				810: {ppid: 800, started: begun, exe: "-zsh"},
				820: {ppid: 810, started: begun, exe: binary},
			}, nil
		},
		lines: func(context.Context, []int) (map[int]string, error) {
			return map[int]string{100: "claude" + env, 820: binary + " --agent-id a" + env}, nil
		},
	}
	w := testWalker()
	a := &agentSessions{
		Root:     root,
		Live:     func(ctx context.Context) (liveness, error) { return claudeLiveness(ctx, w, machine) },
		Activity: func(context.Context, string, string) (time.Time, bool) { return time.Time{}, true },
		Now:      func() time.Time { return now },
		w:        w,
	}
	ended, err := a.Ended(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude process 820 is in no session registry") {
		t.Errorf("Ended = %d offered, err %v; want an error naming the unregistered process", len(ended), err)
	}
	b := &agentSessions{Live: a.Live, Activity: a.Activity, Now: a.Now, w: w}
	if err := b.recheck(context.Background(), filepath.Join(root, projDir, mate)); err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
		t.Errorf("recheck = %v, want a refusal", err)
	}
}

// A config folder is read off the `ps -E` line, where a value is cut at
// a space and a non-ASCII byte is printed escaped. The folder named is
// then not there. Read as an empty registry it would make the process
// an unregistered one, tolerated below a registered session, and hide
// the session it registered and its transcript.
func TestAgentSessionsConfigFolderMisreadOffThePSLine(t *testing.T) {
	const lead, resumed = sidRunning, sidEnded
	spaced := filepath.Join(t.TempDir(), "Application Support", "claude-alt")
	accented := filepath.Join(t.TempDir(), "cfg-é")
	cases := []struct{ name, folder, printed string }{
		{"a space", spaced, spaced},
		{"a non-ASCII letter", accented, strings.ReplaceAll(accented, "é", "M-CM-)")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root = filepath.Join(root, "claude-1000")
			session := filepath.Join(root, projDir, resumed)
			writeFile(t, filepath.Join(session, "scratchpad", "notes.txt"), 1000)
			ageTree(t, root, now.Add(-30*time.Hour))

			home := filepath.Join(t.TempDir(), ".claude")
			writeText(t, filepath.Join(home, "sessions", "100.json"), `{"pid":100,"sessionId":"`+lead+`","cwd":"/work/proj"}`)
			// Process 500 resumed the session under its own config
			// folder and wrote its transcript there just now.
			writeText(t, filepath.Join(tc.folder, "sessions", "500.json"), `{"pid":500,"sessionId":"`+resumed+`","cwd":"/work/other"}`)
			writeText(t, filepath.Join(tc.folder, "projects", projDir, resumed+".jsonl"), "{}\n")
			begun := now.Add(-20 * time.Hour).UTC().Truncate(time.Second)
			machine := claudeHost{own: home, home: home,
				procs: func(context.Context) (map[int]process, error) {
					return map[int]process{
						100: {ppid: 1, started: begun.Add(-time.Hour), exe: "claude"},
						500: {ppid: 100, started: begun, exe: "claude"},
					}, nil
				},
				lines: func(context.Context, []int) (map[int]string, error) {
					return map[int]string{500: "claude --continue TERM=xterm CLAUDE_CONFIG_DIR=" + tc.printed + " HOME=" + filepath.Dir(home)}, nil
				},
			}
			w := testWalker()
			a := &agentSessions{
				Root: root,
				Live: func(ctx context.Context) (liveness, error) { return claudeLiveness(ctx, w, machine) },
				Now:  func() time.Time { return now },
				w:    w,
			}
			ended, err := a.Ended(context.Background())
			if err == nil || !strings.Contains(err.Error(), "of claude process 500") {
				t.Errorf("Ended = %d offered, err %v; want an error naming process 500's config folder", len(ended), err)
			}
			b := &agentSessions{Live: a.Live, Now: a.Now, w: w}
			if err := b.recheck(context.Background(), session); err == nil || !strings.Contains(err.Error(), "cannot tell whether") {
				t.Errorf("recheck = %v, want a refusal", err)
			}
		})
	}
}

func TestClaudeLiveness(t *testing.T) {
	const live, crashed, elsewhere = "11111111-aaaa-4aaa-8aaa-111111111111", "22222222-aaaa-4aaa-8aaa-222222222222", "33333333-aaaa-4aaa-8aaa-333333333333"
	// registry lays out <dir>/.claude/sessions and returns the folder.
	registry := func(t *testing.T, files map[string]string) string {
		t.Helper()
		config := filepath.Join(t.TempDir(), ".claude")
		for name, text := range files {
			writeText(t, filepath.Join(config, "sessions", name), text)
		}
		if err := os.MkdirAll(config, 0o755); err != nil {
			t.Fatal(err)
		}
		return config
	}
	files := map[string]string{
		"100.json":    `{"pid":100,"sessionId":"` + live + `","cwd":"/work/proj","procStart":"` + startedText + `","kind":"interactive"}`,
		"200.json":    `{"pid":200,"sessionId":"` + crashed + `","cwd":"/work/crashed"}`,
		"300.json":    `{"pid":300,"sess`,
		"100.abc.key": `{"sessionId":"not-a-registry-file"}`,
	}
	claude := func(ppid int) process { return process{ppid: ppid, started: started, exe: "claude"} }
	shell := func(ppid int) process { return process{ppid: ppid, started: started, exe: "-zsh"} }
	other := registry(t, map[string]string{"500.json": `{"pid":500,"sessionId":"` + elsewhere + `","cwd":"/work/other"}`})
	locked := registry(t, files)
	if err := os.Chmod(filepath.Join(locked, "sessions"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "sessions"), 0o755) })
	local := started.In(time.Local).Format("Mon Jan _2 15:04:05 2006")
	// shared is the default config folder of the cases whose lines name
	// it through HOME, as every real claude process's line does.
	shared := registry(t, files)
	env := " TERM=xterm HOME=" + filepath.Dir(shared)
	const versioned = "/Users/dev/.local/share/claude/versions/2.1.296"

	cases := []struct {
		name      string
		own, home string
		procs     map[int]process
		procsErr  error
		lines     map[int]string
		linesErr  error
		want      []string
		wantPaths []string
		wantSince time.Time
		wantErr   string
	}{
		{
			// An empty listing would read as "no claude is running".
			name:     "the process listing fails",
			home:     registry(t, files),
			procsErr: errors.New("ps failed"),
			wantErr:  "ps failed",
		},
		{
			name:      "running pid counts; a dead pid's file is skipped, torn or not",
			home:      registry(t, files),
			procs:     map[int]process{1: {exe: "/sbin/launchd"}, 100: claude(1), 77: {ppid: 1, exe: "/opt/tools/bin/esbuild"}, 78: shell(1)},
			want:      []string{live},
			wantPaths: []string{"/opt/tools/bin/esbuild", "/sbin/launchd", "/work/proj"},
		},
		{
			name:  "no claude running, nothing registered",
			home:  registry(t, nil),
			procs: map[int]process{78: shell(1)},
		},
		{
			name:    "a running pid's registry file is torn",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 300: claude(1)},
			wantErr: "300.json of running process 300",
		},
		{
			name:    "the registry folder cannot be listed",
			home:    locked,
			procs:   map[int]process{100: claude(1)},
			wantErr: "is unreadable",
		},
		{
			name:    "one claude registered, another in no registry",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 400: {ppid: 1, started: started, exe: "/opt/bin/claude"}},
			wantErr: "claude process 400 is in no session registry",
		},
		{
			name:      "a claude running under another config folder",
			home:      registry(t, files),
			procs:     map[int]process{100: claude(1), 500: claude(1)},
			lines:     map[int]string{500: "claude TERM=xterm CLAUDE_CONFIG_DIR=" + other},
			want:      []string{live, elsewhere},
			wantPaths: []string{"/work/other", "/work/proj"},
		},
		{
			name:    "its own config folder does not name it either",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 600: claude(1)},
			lines:   map[int]string{600: "claude TERM=xterm CLAUDE_CONFIG_DIR=" + other},
			wantErr: "claude process 600 is in no session registry",
		},
		{
			// regrow runs with a config folder of its own; the process's
			// environment names none, so it registers under the default.
			name:      "regrow's own config folder is not the plain process's",
			own:       other,
			home:      registry(t, files),
			procs:     map[int]process{100: claude(1)},
			want:      []string{live},
			wantPaths: []string{"/work/proj"},
		},
		{
			name: "a file an earlier process with the same pid left: other start time",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + crashed + `","cwd":"/work/crashed","procStart":"Sat Oct  3 08:00:00 2026"}`,
			}),
			procs: map[int]process{100: shell(1)},
		},
		{
			name: "a stale file does not register the claude process that reuses its pid",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + crashed + `","cwd":"/work/crashed","procStart":"Sat Oct  3 08:00:00 2026"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "claude process 100 is in no session registry",
		},
		{
			name: "a file whose pid field names another process",
			home: registry(t, map[string]string{
				"100.json": `{"pid":999,"sessionId":"` + crashed + `","cwd":"/work/crashed"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "claude process 100 is in no session registry",
		},
		{
			name: "procStart written in local time",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + live + `","cwd":"/work/proj","procStart":"` + local + `"}`,
			}),
			procs:     map[int]process{100: claude(1)},
			want:      []string{live},
			wantPaths: []string{"/work/proj"},
		},
		{
			// 100 is a registered session; it started a shell that runs
			// an unregistered claude, which started another.
			name: "unregistered claude processes below a registered session",
			home: shared,
			procs: map[int]process{
				100: claude(1), 150: shell(100),
				160: {ppid: 150, started: started.Add(2 * time.Hour), exe: "claude"},
				170: {ppid: 160, started: started.Add(3 * time.Hour), exe: "claude"},
			},
			lines:     map[int]string{160: "claude" + env, 170: "claude" + env},
			want:      []string{live},
			wantPaths: []string{"/work/proj"},
			wantSince: started.Add(2 * time.Hour),
		},
		{
			// Claude Code starts a team-mate in a pane by the path of its
			// own binary, outside the session's process tree; it
			// registers nowhere.
			name: "an unregistered claude process started by the binary's versioned path",
			home: shared,
			procs: map[int]process{
				100: claude(1), 800: {ppid: 1, started: started, exe: "tmux"}, 810: shell(800),
				820: {ppid: 810, started: started, exe: versioned},
			},
			lines:   map[int]string{820: versioned + " --agent-id a" + env},
			wantErr: "claude process 820 is in no session registry",
		},
		{
			name: "a versioned path below a registered session opens the window",
			home: shared,
			procs: map[int]process{
				100: claude(1),
				820: {ppid: 100, started: started.Add(time.Hour), exe: versioned},
			},
			lines:     map[int]string{820: versioned + " --agent-id a" + env},
			want:      []string{live},
			wantPaths: []string{versioned, "/work/proj"},
			wantSince: started.Add(time.Hour),
		},
		{
			// Its registry cannot have been looked into: the line shows
			// neither CLAUDE_CONFIG_DIR nor HOME.
			name:    "an unregistered claude process whose config folder is not on its line",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 160: claude(100)},
			lines:   map[int]string{160: "claude TERM=xterm"},
			wantErr: "claude process 160: its config folder is unknown",
		},
		{
			name:    "an unregistered claude process with a relative config folder",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 160: claude(100)},
			lines:   map[int]string{160: "claude TERM=xterm CLAUDE_CONFIG_DIR=alt/config HOME=" + filepath.Dir(shared)},
			wantErr: "claude process 160: its config folder is unknown",
		},
		{
			// A value with a space is cut on the ps line, and a non-ASCII
			// byte is printed escaped: the folder named is not there, and
			// its registry would read as empty.
			name:    "a claude process names a config folder that does not exist",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 500: claude(100)},
			lines:   map[int]string{500: "claude --continue TERM=xterm CLAUDE_CONFIG_DIR=" + filepath.Join(filepath.Dir(other), "cfg-M-CM-)")},
			wantErr: "of claude process 500",
		},
		{
			name:    "a registered claude process names a config folder that is a file",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1)},
			lines:   map[int]string{100: "claude TERM=xterm CLAUDE_CONFIG_DIR=" + filepath.Join(shared, "sessions", "100.json")},
			wantErr: "of claude process 100 is not a folder",
		},
		{
			name:    "an unregistered claude process below no session",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 150: shell(1), 160: claude(150)},
			wantErr: "claude process 160 is in no session registry",
		},
		{
			// The file names pid 100, but pid 100 is a shell now: a
			// leftover, and no session's process to be below.
			name: "an unregistered claude process below a leftover file's pid",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + crashed + `","cwd":"/work/crashed"}`,
			}),
			procs:   map[int]process{100: shell(1), 160: claude(100)},
			wantErr: "claude process 160 is in no session registry",
		},
		{
			name: "an unregistered claude process whose start time is unknown",
			home: shared,
			procs: map[int]process{
				100: claude(1),
				160: {ppid: 100, exe: "claude"},
			},
			lines:   map[int]string{160: "claude" + env},
			wantErr: "claude process 160: its start time is unknown",
		},
		{
			// Its arguments could name the session it resumed.
			name:    "an unregistered claude process whose command line is unknown",
			home:    registry(t, files),
			procs:   map[int]process{100: claude(1), 160: claude(100)},
			lines:   map[int]string{100: "claude TERM=xterm"},
			wantErr: "claude process 160: its command line is unknown",
		},
		{
			name:     "the command lines cannot be read",
			home:     registry(t, files),
			procs:    map[int]process{100: claude(1)},
			linesErr: errors.New("ps failed"),
			wantErr:  "command lines of the claude processes: ps failed",
		},
		{
			name: "a running pid's registry file holds a procStart that cannot be read",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + live + `","cwd":"/work/proj","procStart":"2026-10-04T10:02:27Z"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "100.json of running process 100: procStart not understood",
		},
		{
			name: "a running pid's registry file names no working directory",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + live + `"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "100.json of running process 100: no working directory",
		},
		{
			// No scratch folder is named after it, so which session the
			// process runs would be unknown.
			name: "a running pid's registry file names something that is no session id",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"not-a-session-id","cwd":"/work/proj"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "100.json of running process 100: sessionId is not a session id",
		},
		{
			name: "a running pid's registry file names no session",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"cwd":"/work/proj"}`,
			}),
			procs:   map[int]process{100: claude(1)},
			wantErr: "100.json of running process 100: sessionId is not a session id",
		},
		{
			// Leftovers register nothing, whatever else they lack: the
			// first names another pid, the second an earlier start.
			name: "a leftover file without a working directory is no error",
			own: registry(t, map[string]string{
				"78.json": `{"pid":999,"sessionId":"` + crashed + `"}`,
			}),
			home: registry(t, map[string]string{
				"78.json": `{"pid":78,"sessionId":"not-a-session-id","procStart":"Sat Oct  3 08:00:00 2026"}`,
			}),
			procs: map[int]process{78: shell(1)},
		},
		{
			// A case-insensitive volume reads the id as the same folder.
			name: "a session id written in upper case",
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + strings.ToUpper(live) + `","cwd":"/work/proj"}`,
			}),
			procs:     map[int]process{100: claude(1)},
			want:      []string{live},
			wantPaths: []string{"/work/proj"},
		},
		{
			// No CLAUDE_CONFIG_DIR: it registers under its own HOME.
			name:      "a claude started with another HOME",
			home:      registry(t, files),
			procs:     map[int]process{100: claude(1), 500: claude(1)},
			lines:     map[int]string{500: "claude TERM=xterm HOME=" + filepath.Dir(other)},
			want:      []string{live, elsewhere},
			wantPaths: []string{"/work/other", "/work/proj"},
		},
		{
			// The real registration is read first; a leftover for the
			// same pid in the folder read after it must not replace it.
			name: "one process named in two registries",
			own:  registry(t, files),
			home: registry(t, map[string]string{
				"100.json": `{"pid":100,"sessionId":"` + crashed + `","cwd":"/work/leftover"}`,
			}),
			procs:     map[int]process{100: claude(1)},
			want:      []string{live, crashed},
			wantPaths: []string{"/work/leftover", "/work/proj"},
		},
		{
			// 160 registers nowhere but was started to resume a session;
			// 100 is registered and was started with an id of its own.
			name: "a session id on the argument line counts as running",
			home: shared,
			procs: map[int]process{
				100: claude(1),
				160: {ppid: 100, started: started.Add(time.Hour), exe: "claude"},
			},
			lines: map[int]string{
				100: "claude --session-id=" + crashed + " TERM=xterm",
				160: "claude -p --resume " + elsewhere + " continue" + env + " CLAUDE_CODE_SESSION_ID=44444444-aaaa-4aaa-8aaa-444444444444",
			},
			want:      []string{live, crashed, elsewhere},
			wantPaths: []string{"/work/proj"},
			wantSince: started.Add(time.Hour),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			own := tc.own
			if own == "" {
				own = tc.home
			}
			h := claudeHost{own: own, home: tc.home,
				procs: func(context.Context) (map[int]process, error) { return tc.procs, tc.procsErr },
				lines: func(context.Context, []int) (map[int]string, error) { return tc.lines, tc.linesErr },
			}
			got, err := claudeLiveness(context.Background(), testWalker(), h)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ids := slices.Sorted(maps.Keys(got.sessions)); !slices.Equal(ids, tc.want) {
				t.Errorf("running sessions = %v, want %v", ids, tc.want)
			}
			if !slices.Equal(got.paths, tc.wantPaths) {
				t.Errorf("paths = %v, want %v", got.paths, tc.wantPaths)
			}
			if !got.since.Equal(tc.wantSince) {
				t.Errorf("since = %v, want %v", got.since, tc.wantSince)
			}
		})
	}
}

// Transcripts are looked for under every config folder that can be
// known: regrow's own, the default, each one a running claude process
// names, and folders beside the default that are named after it.
func TestClaudeLivenessConfigFolders(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, ".claude")
	own := filepath.Join(parent, "own-config")
	named := filepath.Join(parent, "elsewhere", "named-config")
	sibling := filepath.Join(parent, ".claude-work")
	for _, dir := range []string{home, own, named, filepath.Join(sibling, "projects"), filepath.Join(parent, ".claude-empty"), filepath.Join(parent, "unrelated", "projects")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(parent, ".claude.json"), "{}")
	writeText(t, filepath.Join(named, "sessions", "500.json"), `{"pid":500,"sessionId":"33333333-aaaa-4aaa-8aaa-333333333333","cwd":"/work/other"}`)
	h := claudeHost{own: own, home: home,
		procs: func(context.Context) (map[int]process, error) {
			return map[int]process{500: {ppid: 1, started: started, exe: "claude"}}, nil
		},
		lines: func(_ context.Context, pids []int) (map[int]string, error) {
			if !slices.Equal(pids, []int{500}) {
				t.Errorf("environments asked for %v, want the claude processes [500]", pids)
			}
			return map[int]string{500: "claude TERM=xterm CLAUDE_CONFIG_DIR=" + named + " HOME=/Users/dev"}, nil
		},
	}
	got, err := claudeLiveness(context.Background(), testWalker(), h)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{own, home, named, sibling}; !slices.Equal(got.configs, want) || got.configsPartial {
		t.Errorf("config folders = %v (partial %v), want %v, listed in full", got.configs, got.configsPartial, want)
	}

	// A folder named like a config folder refuses: it may hold
	// transcripts, and the result must say so.
	if err := os.Chmod(sibling, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sibling, 0o755) })
	got, err = claudeLiveness(context.Background(), testWalker(), h)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{own, home, named}; !slices.Equal(got.configs, want) || !got.configsPartial {
		t.Errorf("unreadable sibling: config folders = %v (partial %v), want %v, partial", got.configs, got.configsPartial, want)
	}
	if err := os.Chmod(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	// The folder beside the default cannot be listed: a config folder
	// may be missing, and the result must say so.
	w := testWalker()
	w.open = func(path string) (dirHandle, error) {
		if path == parent {
			return nil, syscall.EACCES
		}
		return openDir(path)
	}
	got, err = claudeLiveness(context.Background(), w, h)
	if err != nil {
		t.Fatal(err)
	}
	if !got.configsPartial || slices.Contains(got.configs, sibling) {
		t.Errorf("unlistable parent: config folders = %v (partial %v), want partial without the sibling", got.configs, got.configsPartial)
	}
}

// When the search for config folders cannot run at all, one may be
// missing: the result must say so, never "there are none".
func TestSiblingConfigsUnknown(t *testing.T) {
	t.Run("no default folder known", func(t *testing.T) {
		if dirs, partial := siblingConfigs(context.Background(), testWalker(), ""); len(dirs) != 0 || !partial {
			t.Errorf("siblingConfigs = %v (partial %v), want none, partial", dirs, partial)
		}
	})
	t.Run("the listing ends with the scan", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.MkdirAll(filepath.Join(parent, ".claude-work", "projects"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A blocked listing cannot finish first, so the walk returns
		// ctx's error.
		w, _ := blockingWalker(t, parent)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if dirs, partial := siblingConfigs(ctx, w, filepath.Join(parent, ".claude")); len(dirs) != 0 || !partial {
			t.Errorf("siblingConfigs = %v (partial %v), want none, partial", dirs, partial)
		}
	})
}

func TestRegistrationNames(t *testing.T) {
	p := process{started: started, exe: "claude"}
	cases := []struct {
		name    string
		r       registration
		want    bool
		wantErr bool
	}{
		{"its pid and start", registration{PID: 100, ProcStart: startedText}, true, false},
		{"no pid or start to compare", registration{}, true, false},
		{"another pid", registration{PID: 999, ProcStart: startedText}, false, false},
		{"an earlier process's start", registration{PID: 100, ProcStart: "Sat Oct  3 08:00:00 2026"}, false, false},
		{"a start that cannot be read", registration{PID: 100, ProcStart: "2026-10-04T10:02:27Z"}, false, true},
		// The pid field already says it is another process's file.
		{"another pid and a start that cannot be read", registration{PID: 999, ProcStart: "2026-10-04T10:02:27Z"}, false, false},
	}
	for _, tc := range cases {
		got, err := tc.r.names(100, p)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: names = %v, err %v; want %v, error %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
	// With the process's start unknown, a start that cannot be read is
	// still an error: it cannot be shown to be the process's.
	if got, err := (registration{PID: 100, ProcStart: "2026-10-04T10:02:27Z"}).names(100, process{exe: "claude"}); got || err == nil {
		t.Errorf("unknown process start, unreadable procStart: names = %v, err %v; want an error", got, err)
	}
}

// ps fails when none of the pids it is asked for runs; one that exited
// since the listing must not make the others' lines unknown, and a ps
// that fails must not read as no lines.
func TestPSLines(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps -E is read on macOS only")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	gone := cmd.Process.Pid
	lines, err := psLines(context.Background(), []int{gone})
	if err != nil {
		t.Fatalf("psLines of an exited pid: %v", err)
	}
	if _, ok := lines[gone]; ok {
		t.Errorf("psLines gave a line for exited pid %d", gone)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lines, err := psLines(ctx, []int{os.Getpid()}); err == nil {
		t.Errorf("ps did not run: psLines = %v, want an error", lines)
	}
}

func TestBelowRegistered(t *testing.T) {
	session := []registration{{SessionID: "11111111-aaaa-4aaa-8aaa-111111111111"}}
	claude := func(ppid int) process { return process{ppid: ppid, exe: "claude"} }
	shell := func(ppid int) process { return process{ppid: ppid, exe: "-zsh"} }
	cases := []struct {
		name  string
		procs map[int]process
		reg   map[int][]registration
		want  bool
	}{
		{"a registered session's child", map[int]process{1: shell(0), 10: claude(1), 20: claude(10)}, map[int][]registration{10: session}, true},
		{"through a shell", map[int]process{1: shell(0), 10: claude(1), 15: shell(10), 20: claude(15)}, map[int][]registration{10: session}, true},
		{"no registered ancestor", map[int]process{1: shell(0), 10: claude(1), 20: claude(10)}, nil, false},
		{"its own registration is not an ancestor's", map[int]process{1: shell(0), 20: claude(1)}, map[int][]registration{20: session}, false},
		{"a registered pid that is not claude", map[int]process{1: shell(0), 10: shell(1), 20: claude(10)}, map[int][]registration{10: session}, false},
		{"its own parent", map[int]process{20: claude(20)}, map[int][]registration{20: session}, false},
		{"a cycle", map[int]process{20: claude(21), 21: shell(20)}, nil, false},
		{"a parent that is not listed", map[int]process{20: claude(999)}, map[int][]registration{999: session}, false},
	}
	for _, tc := range cases {
		if got := belowRegistered(tc.procs, tc.reg, 20); got != tc.want {
			t.Errorf("%s: belowRegistered = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLivenessRecent(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	since := now.Add(-40 * time.Hour)
	cases := []struct {
		name  string
		since time.Time
		at    time.Time
		want  string
	}{
		{"inside the window", time.Time{}, now.Add(-23 * time.Hour), "changed within 24h"},
		{"outside the window", time.Time{}, now.Add(-25 * time.Hour), ""},
		{"never", time.Time{}, time.Time{}, ""},
		{"after an unregistered process started", since, now.Add(-30 * time.Hour), "changed since an unregistered claude process started"},
		{"the moment it started", since, since, "changed since an unregistered claude process started"},
		{"before it started", since, now.Add(-41 * time.Hour), ""},
		{"inside the window, process or not", since, now.Add(-time.Hour), "changed within 24h"},
	}
	for _, tc := range cases {
		if got := (liveness{since: tc.since}).recent(tc.at, now); got != tc.want {
			t.Errorf("%s: recent = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestClaudeActivity(t *testing.T) {
	const sid = "33333333-aaaa-4aaa-8aaa-333333333333"
	config, second := t.TempDir(), t.TempDir()
	proj := filepath.Join(config, "projects", projDir)
	transcript := filepath.Join(proj, sid+".jsonl")
	subagent := filepath.Join(proj, sid, "subagents", "agent-1.jsonl")
	writeText(t, transcript, "{}\n")
	writeText(t, subagent, "{}\n")
	base := time.Now().Add(-100 * time.Hour).Truncate(time.Second)
	ageTree(t, proj, base)
	if err := os.Chtimes(subagent, base.Add(5*time.Hour), base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	configs := []string{config}

	if got, complete := claudeActivity(ctx, testWalker(), configs, projDir, sid); !got.Equal(base.Add(5*time.Hour)) || !complete {
		t.Errorf("activity = %v (complete %v), want the subagent transcript's %v", got, complete, base.Add(5*time.Hour))
	}
	if err := os.Chtimes(transcript, base.Add(9*time.Hour), base.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, complete := claudeActivity(ctx, testWalker(), configs, projDir, sid); !got.Equal(base.Add(9*time.Hour)) || !complete {
		t.Errorf("activity = %v (complete %v), want the main transcript's %v", got, complete, base.Add(9*time.Hour))
	}
	if got, complete := claudeActivity(ctx, testWalker(), configs, otherDir, sid); !got.IsZero() || !complete {
		t.Errorf("activity under another project = %v (complete %v), want none, read in full", got, complete)
	}

	// The same session under a second config folder, written later.
	later := filepath.Join(second, "projects", projDir, sid+".jsonl")
	writeText(t, later, "{}\n")
	if err := os.Chtimes(later, base.Add(20*time.Hour), base.Add(20*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, complete := claudeActivity(ctx, testWalker(), []string{config, second}, projDir, sid); !got.Equal(base.Add(20*time.Hour)) || !complete {
		t.Errorf("activity across two config folders = %v (complete %v), want the newer folder's %v", got, complete, base.Add(20*time.Hour))
	}
	if _, complete := claudeActivity(ctx, testWalker(), nil, projDir, sid); complete {
		t.Error("with no config folder known, a transcript cannot count as read in full")
	}

	// A transcript file whose stat does not answer is not "no
	// transcript": only that one path is blocked here.
	w := testWalker()
	w.blocked.add(filepath.Join(second, "projects", otherDir, sid+".jsonl"))
	if _, complete := claudeActivity(ctx, w, []string{second}, otherDir, sid); complete {
		t.Error("a transcript file that cannot be stat'ed must not count as read in full")
	}

	locked := filepath.Join(proj, sid, "subagents")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, complete := claudeActivity(ctx, testWalker(), []string{second, config}, projDir, sid); complete {
		t.Error("a transcript folder that cannot be read must not count as read in full")
	}
}

// With no Activity injected, transcripts are read under the config
// folders the process lister found, not only regrow's own.
func TestAgentSessionsReadEveryConfigFolder(t *testing.T) {
	now := time.Now()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "claude-1000")
	writeFile(t, filepath.Join(root, projDir, sidEnded, "scratchpad", "notes.txt"), 1000)
	ageTree(t, root, now.Add(-72*time.Hour))
	own, elsewhere := t.TempDir(), t.TempDir()
	transcript := filepath.Join(elsewhere, "projects", projDir, sidEnded+".jsonl")
	writeText(t, transcript, "{}\n")
	if err := os.Chtimes(transcript, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		configs   []string
		partial   bool
		wantEnded int
		wantWhy   string
	}{
		{[]string{own}, false, 1, ""},
		{[]string{own, elsewhere}, false, 0, "transcript changed within 24h"},
		{[]string{own}, true, 0, "transcript not fully readable"},
	} {
		a := &agentSessions{
			Root: root,
			Live: func(context.Context) (liveness, error) {
				return liveness{configs: tc.configs, configsPartial: tc.partial}, nil
			},
			Now: func() time.Time { return now },
			w:   testWalker(),
		}
		ended, err := a.Ended(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		kept, _ := a.Kept(context.Background())
		if len(ended) != tc.wantEnded || len(ended)+len(kept) != 1 {
			t.Errorf("config folders %d: ended %d, kept %d; want ended %d of 1", len(tc.configs), len(ended), len(kept), tc.wantEnded)
		}
		if tc.wantEnded == 0 && len(kept) == 1 && !strings.HasSuffix(kept[0].Label, tc.wantWhy) {
			t.Errorf("kept for %q, want %q", kept[0].Label, tc.wantWhy)
		}
	}
}

func TestParsePS(t *testing.T) {
	out := "    1     0 Thu Oct  1 12:20:01 2026 /sbin/launchd\n" +
		"  412   300 " + startedText + " claude\n" +
		" 9001     1 Sun Oct 11 09:00:00 2026 /Applications/Some App.app/Contents/MacOS/Some App\n\n"
	got, err := parsePS(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]process{
		1:    {ppid: 0, started: time.Date(2026, 10, 1, 12, 20, 1, 0, time.UTC), exe: "/sbin/launchd"},
		412:  {ppid: 300, started: started, exe: "claude"},
		9001: {ppid: 1, started: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC), exe: "/Applications/Some App.app/Contents/MacOS/Some App"},
	}
	if !maps.Equal(got, want) {
		t.Errorf("parsePS = %v, want %v", got, want)
	}
	// A process left out would read as not running.
	if got, err := parsePS(out + "  412 claude\n"); err == nil {
		t.Errorf("a line that cannot be read: parsePS = %v, want an error", got)
	}
	if got, err := parsePS(out + "  413   300 Sun Okt  4 10:02:27 2026 claude\n"); err == nil {
		t.Errorf("a start time that cannot be read: parsePS = %v, want an error", got)
	}
}

func TestLineConfigDir(t *testing.T) {
	cases := map[string]string{
		"claude TERM=xterm CLAUDE_CONFIG_DIR=/Users/dev/.claude-work HOME=/Users/dev": "/Users/dev/.claude-work",
		"claude --resume TERM=xterm HOME=/Users/dev":                                  "/Users/dev/.claude",
		"claude NOT_CLAUDE_CONFIG_DIR=/Users/dev/x HOME=/Users/dev":                   "/Users/dev/.claude",
		"claude CLAUDE_CONFIG_DIR=relative/dir":                                       "",
		"claude CLAUDE_CONFIG_DIR=relative/dir HOME=/Users/dev":                       "",
		"claude TERM=xterm HOME=relative":                                             "",
		"claude TERM=xterm JAVA_HOME=/opt/java":                                       "",
		"claude -p HOME=/x TERM=xterm HOME=/Users/dev":                                "/Users/dev/.claude",
		// Arguments come first: one that looks like the variable must
		// not shadow the environment's.
		"claude -p CLAUDE_CONFIG_DIR=/x TERM=xterm CLAUDE_CONFIG_DIR=/Users/dev/.claude-work HOME=/Users/dev": "/Users/dev/.claude-work",
	}
	for env, want := range cases {
		if got := lineConfigDir(env); got != want {
			t.Errorf("lineConfigDir(%q) = %q, want %q", env, got, want)
		}
	}
	out := "  412 claude TERM=xterm CLAUDE_CONFIG_DIR=/Users/dev/.claude-work HOME=/Users/dev\n" +
		"  413 claude -p hello TERM=xterm HOME=/Users/dev\n\nbogus\n"
	want := map[int]string{
		412: "claude TERM=xterm CLAUDE_CONFIG_DIR=/Users/dev/.claude-work HOME=/Users/dev",
		413: "claude -p hello TERM=xterm HOME=/Users/dev",
	}
	if got := parsePSLines(out); !maps.Equal(got, want) {
		t.Errorf("parsePSLines = %v, want %v", got, want)
	}
}

func TestParseSessionArgs(t *testing.T) {
	const a, b = "11111111-aaaa-4aaa-8aaa-111111111111", "22222222-aaaa-4aaa-8aaa-222222222222"
	cases := []struct {
		line string
		want []string
	}{
		{"claude --resume " + a + " TERM=xterm", []string{a}},
		{"claude --resume=" + a, []string{a}},
		{"claude -p -r " + a + " go on TERM=xterm", []string{a}},
		{"claude --session-id " + a + " --resume " + b + " TERM=xterm", []string{a, b}},
		{"claude --resume TERM=xterm", nil},
		{"claude TERM=xterm CLAUDE_CODE_SESSION_ID=" + a, nil},
		{"claude --add-dir /tmp/" + a + " TERM=xterm", nil},
		{"claude --resume " + strings.ToUpper(a), []string{a}},
		{"claude --resume " + a + "0", nil},
	}
	for _, tc := range cases {
		if got := parseSessionArgs(tc.line); !slices.Equal(got, tc.want) {
			t.Errorf("parseSessionArgs(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestScanSpelling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/tmp is a symlink into /private on macOS only")
	}
	cases := map[string]string{
		"/tmp/claude-1000/p/s":         "/private/tmp/claude-1000/p/s",
		"/private/tmp/claude-1000/p/s": "/private/tmp/claude-1000/p/s",
		"/var/folders/x/":              "/private/var/folders/x",
		"/tmpfiles/x":                  "/tmpfiles/x",
		"/Users/dev/proj":              "/Users/dev/proj",
	}
	for in, want := range cases {
		if got := scanSpelling(in); got != want {
			t.Errorf("scanSpelling(%q) = %q, want %q", in, got, want)
		}
	}
}
