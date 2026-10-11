package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lbagic/regrow/internal/engine"
)

// buildFixture is a fixture home whose ~/workspace holds one project
// per case. Its clock runs a day past the idle window, so everything
// the fixture writes is old unless a case stamps it recent.
type buildFixture struct {
	t      *testing.T
	home   string
	ws     string
	now    time.Time
	recent time.Time // inside the idle window
	b      *buildOutput
}

func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	gitTestEnv(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &buildFixture{t: t, home: filepath.Join(root, "home"), now: time.Now().Add(buildOutputIdle + 24*time.Hour)}
	f.ws = filepath.Join(f.home, "workspace")
	f.recent = f.now.Add(-24 * time.Hour)
	f.b = &buildOutput{
		w:     testWalker(),
		host:  engine.Host{OS: "darwin", Home: f.home},
		roots: []string{"~/workspace"},
		idle:  buildOutputIdle,
		git:   gitWith,
		procs: func(context.Context) (processes, error) { return processes{}, nil },
		now:   func() time.Time { return f.now },
	}
	return f
}

func (f *buildFixture) path(parts ...string) string {
	return filepath.Join(append([]string{f.ws}, parts...)...)
}

// project makes a repository at ~/workspace/<name> with the given
// .gitignore and one tracked source file.
func (f *buildFixture) project(name, gitignore string) string {
	f.t.Helper()
	dir := f.path(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	runGit(f.t, dir, "init", "-q", "-b", "main")
	commitFile(f.t, dir, ".gitignore", gitignore)
	commitFile(f.t, dir, "main.js", "source\n")
	return dir
}

func (f *buildFixture) stampRecent(path string) {
	f.t.Helper()
	if err := os.Chtimes(path, f.recent, f.recent); err != nil {
		f.t.Fatal(err)
	}
}

func (f *buildFixture) scan() (offered []string, kept map[string]string) {
	f.t.Helper()
	items, keptOut, err := f.b.scan(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	for _, it := range items {
		if it.Path == "" || it.Bytes <= 0 || it.LastUsed.IsZero() || it.Partial {
			f.t.Errorf("%s: want a complete measurement with a path, got %+v", it.Label, it)
		}
		offered = append(offered, it.Path)
	}
	kept = map[string]string{}
	for _, k := range keptOut {
		kept[k.path] = k.why
	}
	return offered, kept
}

// expect compares a scan with what a test wants offered and kept, the
// kept ones by their reason.
func (f *buildFixture) expect(wantOffered []string, wantKept map[string]string) {
	f.t.Helper()
	offered, kept := f.scan()
	slices.Sort(wantOffered)
	if !slices.Equal(offered, wantOffered) {
		f.t.Errorf("offered = %v\nwant %v", offered, wantOffered)
	}
	for path, why := range wantKept {
		if kept[path] != why {
			f.t.Errorf("%s: kept for %q, want %q", path, kept[path], why)
		}
	}
	for path, why := range kept {
		if _, ok := wantKept[path]; !ok {
			f.t.Errorf("%s: kept (%s), want it left out of both lists or offered", path, why)
		}
	}
}

const (
	ignoreBuildOutput = "dist/\n.next/\n.turbo/\n.venv/\nnode_modules/\n"

	keptChanged  = "its project changed in the last 30 days"
	keptProcess  = "a running process works in its project"
	keptUnnamed  = "the ignore rule that covers it does not name it"
	keptTracked  = "git tracks files in it"
	keptNoIgnore = "git does not ignore it"
	keptAlone    = "git tracks nothing in the folder that holds it"
	keptHead     = "its project's last commit or checkout cannot be read"
	keptUnread   = "a folder in its project cannot be read"
	keptUnplaced = "git keeps its repository elsewhere and names no project that holds it"
)

func TestBuildOutputScan(t *testing.T) {
	f := newBuildFixture(t)

	// Offered: one item per ignored build folder of an idle project.
	// What sits inside node_modules, inside another hit, or behind a
	// symlink is nobody's item here.
	idle := f.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(idle, "dist", "app.js"))
	touch(t, filepath.Join(idle, "dist", "inner", "dist", "chunk.js"))
	touch(t, filepath.Join(idle, "web", ".next", "build.js"))
	commitFile(t, idle, filepath.Join("web", "page.js"), "source\n")
	touch(t, filepath.Join(idle, ".turbo", "cache", "hash.tar"))
	touch(t, filepath.Join(idle, ".venv", "pyvenv.cfg"))
	touch(t, filepath.Join(idle, ".venv", "lib", "site.py"))
	touch(t, filepath.Join(idle, "node_modules", "pkg", "dist", "index.js"))
	if err := os.Symlink(filepath.Join(idle, "dist"), filepath.Join(idle, "web", "dist")); err != nil {
		t.Fatal(err)
	}

	// Offered: a folder's own time is no change, in a build folder as
	// outside one. A Trash move out of a folder stamps it, and Finder
	// keeps its .DS_Store.
	stamped := f.project("stamped", ignoreBuildOutput)
	touch(t, filepath.Join(stamped, "dist", "x.js"))
	touch(t, filepath.Join(stamped, "src", "a.js"))
	touch(t, filepath.Join(stamped, ".DS_Store"))
	touch(t, filepath.Join(stamped, ".venv", "pyvenv.cfg"))
	touch(t, filepath.Join(stamped, ".venv", "lib", "site.py"))
	for _, p := range []string{"src", ".DS_Store", ".venv", filepath.Join(".venv", "lib"), ""} {
		f.stampRecent(filepath.Join(stamped, p))
	}

	// Kept, each for one reason.
	p := f.project("tracked", "node_modules/\n")
	if err := os.MkdirAll(filepath.Join(p, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, p, filepath.Join("dist", "lib.js"), "shipped\n")

	// Ignored by name, yet one file was added by force.
	p = f.project("forced", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "kept.js"))
	touch(t, filepath.Join(p, "dist", "built.js"))
	runGit(t, p, "add", "-f", filepath.Join("dist", "kept.js"))
	runGit(t, p, "commit", "-q", "-m", "ship one file")

	p = f.project("plain", "node_modules/\n")
	touch(t, filepath.Join(p, "dist", "x.js"))

	// A later rule takes one dist back out; check-ignore -v prints
	// that rule too.
	p = f.project("negated", "dist/\n!apps/keep/dist/\n")
	touch(t, filepath.Join(p, "apps", "keep", "dist", "x.js"))

	// Ignored, but not as build output: an installed package's own
	// dist under an ignored folder, with dist/ ignored as well.
	p = f.project("vendored", ignoreBuildOutput+"vendor/\nvenv/\nbower_components/\n")
	touch(t, filepath.Join(p, "vendor", "acme", "widget", "dist", "widget.js"))
	touch(t, filepath.Join(p, "venv", "lib", "python3.12", "site-packages", "panel", "dist", "bundle.js"))
	touch(t, filepath.Join(p, "bower_components", "jquery", "dist", "jquery.js"))

	// A repository that ignores everything it does not list.
	p = f.project("listed", "/*\n!/.gitignore\n!/main.js\n!/src/\n")
	touch(t, filepath.Join(p, "dist", "report.pdf"))

	// A pattern that happens to cover the name.
	p = f.project("wild", ".venv*\n")
	touch(t, filepath.Join(p, ".venv", "pyvenv.cfg"))

	// Named by an ignore rule, in a tree git tracks nothing of: an
	// environment under another name, holding an installed package's
	// dist.
	p = f.project("envs", "dist/\n")
	touch(t, filepath.Join(p, "py311", "pyvenv.cfg"))
	touch(t, filepath.Join(p, "py311", "lib", "python3.11", "site-packages", "panel", "dist", "bundle.js"))

	// A repository with nothing added yet.
	if err := os.MkdirAll(f.path("unborn", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.path("unborn"), "init", "-q", "-b", "main")
	touch(t, f.path("unborn", "dist", "x.js"))
	if err := os.WriteFile(f.path("unborn", ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Where the filesystem ignores case, dist/ ignores Dist and all in
	// it; the pattern git reports for a dist below is the same one.
	p = f.project("cased", "dist/\n")
	touch(t, filepath.Join(p, "Dist", "sub", "dist", "x.js"))
	touch(t, filepath.Join(p, "Dist", "sub", "kept.js"))
	runGit(t, p, "add", "-f", filepath.Join("Dist", "sub", "kept.js"))
	runGit(t, p, "commit", "-q", "-m", "keep one file")

	// Outside any repository under the roots. The home folder above
	// them is one, ignoring everything, as a dotfiles checkout does:
	// it must not answer for the folder.
	touch(t, f.path("loose", "dist", "x.js"))
	runGit(t, f.home, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(f.home, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p = f.project("active", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "old.js"))
	touch(t, filepath.Join(p, "src", "deep", "edited.js"))
	f.stampRecent(filepath.Join(p, "src", "deep", "edited.js"))

	// A build of one folder is use of the project: the other goes too.
	p = f.project("rebuilt", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "fresh.js"))
	f.stampRecent(filepath.Join(p, "dist", "fresh.js"))
	touch(t, filepath.Join(p, ".venv", "pyvenv.cfg"))

	// A commit moves HEAD without touching a file in the work tree.
	p = f.project("committed", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	f.stampRecent(filepath.Join(p, ".git", "logs", "HEAD"))

	// Without a reflog a commit leaves no time behind.
	p = f.project("quiet", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	if err := os.RemoveAll(filepath.Join(p, ".git", "logs")); err != nil {
		t.Fatal(err)
	}

	// An install shows on node_modules itself, not on a tracked file.
	p = f.project("installed", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	touch(t, filepath.Join(p, "node_modules", "pkg", "index.js"))
	f.stampRecent(filepath.Join(p, "node_modules"))

	p = f.project("novenv", ignoreBuildOutput)
	touch(t, filepath.Join(p, ".venv", "notes.txt"))

	// A deploy checkout kept in an ignored dist.
	p = f.project("pages", ignoreBuildOutput)
	if err := os.MkdirAll(filepath.Join(p, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Join(p, "dist"), "init", "-q", "-b", "gh-pages")
	commitFile(t, filepath.Join(p, "dist"), "index.html", "site\n")

	p = f.project("busy", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	p = f.project("served", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "server.js"))
	// A sibling whose name starts the same is not the served project.
	p = f.project("served-copy", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "server.js"))
	// ps prints a path as it was typed, here in another letter case.
	p = f.project("Loud", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "server.js"))
	f.b.procs = func(context.Context) (processes, error) {
		return processes{
			cwds: []string{"/", f.path("busy", "src")},
			args: []string{
				"node " + f.path("served", "dist", "server.js") + " --port 3000",
				"node " + f.path("loud", "dist", "server.js"),
			},
		}, nil
	}

	// lsof and ps would print this path with an escape.
	p = f.project(`back\slash`, ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))

	// An empty .git folder is no repository: git answers for the one
	// above it, whose ignore rules cover the folder.
	p = f.project("outer", ignoreBuildOutput)
	touch(t, filepath.Join(p, "sub", "dist", "x.js"))
	if err := os.MkdirAll(filepath.Join(p, "sub", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// git reads ":/dist" as the top-level dist, which is ignored; the
	// folder named ":" holds a dist that is not.
	p = f.project("colon", "/dist\n")
	touch(t, filepath.Join(p, "dist", "x.js"))
	touch(t, filepath.Join(p, ":", "dist", "x.js"))

	// HEAD's times cannot be read: the git dir is reached through a
	// symlink, or its logs never answer.
	p = f.project("linked-git", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	gitDir := filepath.Join(filepath.Dir(f.home), "gitdirs", "linked-git")
	if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(p, ".git"), gitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitDir, filepath.Join(p, ".git")); err != nil {
		t.Fatal(err)
	}
	p = f.project("silent-logs", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	f.stampRecent(filepath.Join(p, ".git", "logs", "HEAD"))
	f.b.w.blocked.add(filepath.Join(p, ".git", "logs"))

	// One repository's git failing, or timing out, costs that
	// repository alone.
	for _, name := range []string{"unreadable-repo", "no-ignore-answer", "no-tracked-answer", "odd-ignore-answer"} {
		p = f.project(name, ignoreBuildOutput)
		touch(t, filepath.Join(p, "dist", "x.js"))
	}
	f.b.git = func(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
		switch {
		case dir == f.path("unreadable-repo") && args[0] == "rev-parse":
			return nil, context.DeadlineExceeded
		case dir == f.path("no-ignore-answer") && args[0] == "check-ignore":
			return nil, errors.New("git check-ignore: exit status 128")
		case dir == f.path("no-tracked-answer") && args[0] == "ls-files":
			return nil, errors.New("git ls-files: exit status 128")
		case dir == f.path("odd-ignore-answer") && args[0] == "check-ignore":
			return []byte(".gitignore\x001\x00dist/\x00"), nil
		}
		return gitWith(ctx, dir, stdin, args...)
	}

	wantOffered := []string{
		f.path("colon", "dist"),
		f.path("idle", ".turbo"),
		f.path("idle", ".venv"),
		f.path("idle", "dist"),
		f.path("idle", "web", ".next"),
		f.path("served-copy", "dist"),
		f.path("stamped", ".venv"),
		f.path("stamped", "dist"),
	}
	f.expect(wantOffered, map[string]string{
		f.path("tracked", "dist"):                              keptTracked,
		f.path("forced", "dist"):                               keptTracked,
		f.path("plain", "dist"):                                keptNoIgnore,
		f.path("negated", "apps", "keep", "dist"):              keptNoIgnore,
		f.path("vendored", "vendor", "acme", "widget", "dist"): keptUnnamed,
		f.path("vendored", "venv", "lib", "python3.12", "site-packages", "panel", "dist"): keptUnnamed,
		f.path("vendored", "bower_components", "jquery", "dist"):                          keptUnnamed,
		f.path("envs", "py311", "lib", "python3.11", "site-packages", "panel", "dist"):    keptAlone,
		f.path("cased", "Dist", "sub", "dist"):                                            "a folder above it has its name in another letter case",
		f.path("unborn", "dist"):                                                          keptAlone,
		f.path("listed", "dist"):                                                          keptUnnamed,
		f.path("wild", ".venv"):                                                           keptUnnamed,
		f.path("loose", "dist"):                                                           "not in a git repository under the project roots",
		f.path("active", "dist"):                                                          keptChanged,
		f.path("rebuilt", "dist"):                                                         keptChanged,
		f.path("rebuilt", ".venv"):                                                        keptChanged,
		f.path("committed", "dist"):                                                       keptChanged,
		f.path("quiet", "dist"):                                                           keptHead,
		f.path("installed", "dist"):                                                       keptChanged,
		f.path("novenv", ".venv"):                                                         "no pyvenv.cfg, so not a Python environment",
		f.path("pages", "dist"):                                                           "it holds a git repository",
		f.path("busy", "dist"):                                                            keptProcess,
		f.path("served", "dist"):                                                          keptProcess,
		f.path("Loud", "dist"):                                                            keptProcess,
		f.path(`back\slash`, "dist"):                                                      "its path holds a character the process list escapes",
		f.path("outer", "sub", "dist"):                                                    "git does not name its project folder as a repository's top level",
		f.path("colon", ":", "dist"):                                                      "git cannot be asked about a path that starts with a colon",
		f.path("linked-git", "dist"):                                                      keptHead,
		f.path("silent-logs", "dist"):                                                     keptHead,
		f.path("unreadable-repo", "dist"):                                                 "git cannot read its repository",
		f.path("no-ignore-answer", "dist"):                                                "git could not say whether it is ignored",
		f.path("odd-ignore-answer", "dist"):                                               "git could not say whether it is ignored",
		f.path("no-tracked-answer", "dist"):                                               "git could not say whether it is tracked",
	})

	t.Run("items name the folder and its project", func(t *testing.T) {
		items, _, err := f.b.scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, it := range items {
			got[it.Path] = it.Label
		}
		if l := got[f.path("idle", "web", ".next")]; l != ".next · idle/web" {
			t.Errorf("label = %q, want %q", l, ".next · idle/web")
		}
		if l := got[f.path("idle", "dist")]; l != "dist · idle" {
			t.Errorf("label = %q, want %q", l, "dist · idle")
		}
	})

	// Under a git hook GIT_DIR names the enclosing repository and would
	// answer for every project.
	t.Run("ignores the variables that redirect git", func(t *testing.T) {
		elsewhere := t.TempDir()
		t.Setenv("GIT_DIR", filepath.Join(elsewhere, "other.git"))
		t.Setenv("GIT_WORK_TREE", elsewhere)
		if got, _ := f.scan(); !slices.Equal(got, wantOffered) {
			t.Errorf("with GIT_DIR set, offered = %v\nwant %v", got, wantOffered)
		}
	})
}

// The index can spell a tracked folder differently from the disk: in
// another letter case after a rename, or with a precomposed accent
// where the folder was made with a combining one. git status is clean
// in both, and the folder is still tracked.
func TestBuildOutputTrackedUnderAnotherSpelling(t *testing.T) {
	rename := func(t *testing.T, dir, from, to string) {
		t.Helper()
		if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, "tmp")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, to)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("another letter case", func(t *testing.T) {
		f := newBuildFixture(t)
		p := f.project("recased", "dist/\n")
		touch(t, filepath.Join(p, "Dist", "app.js"))
		runGit(t, p, "add", "-f", "Dist/app.js")
		runGit(t, p, "commit", "-q", "-m", "ship")
		rename(t, p, "Dist", "dist")
		if _, err := os.Stat(filepath.Join(p, "Dist", "app.js")); err != nil {
			t.Skip("the filesystem tells Dist from dist")
		}
		f.expect(nil, map[string]string{filepath.Join(p, "dist"): keptTracked})
	})

	// Straße and STRASSE name one folder there too, which lower-casing
	// does not show.
	t.Run("a letter that folds to two", func(t *testing.T) {
		f := newBuildFixture(t)
		p := f.project("folded", "dist/\n")
		touch(t, filepath.Join(p, "Straße", "dist", "app.js"))
		runGit(t, p, "add", "-f", ".")
		runGit(t, p, "commit", "-q", "-m", "ship")
		rename(t, p, "Straße", "STRASSE")
		if _, err := os.Stat(filepath.Join(p, "Straße", "dist", "app.js")); err != nil {
			t.Skip("the filesystem tells Straße from STRASSE")
		}
		f.expect(nil, map[string]string{filepath.Join(p, "STRASSE", "dist"): keptTracked})
	})

	// "cafe" and a combining acute on disk; git stores "café".
	t.Run("a composed accent", func(t *testing.T) {
		f := newBuildFixture(t)
		p := f.project("composed", "dist/\n")
		decomposed := filepath.Join(p, "café", "dist")
		touch(t, filepath.Join(decomposed, "lib.js"))
		runGit(t, p, "-c", "core.precomposeunicode=true", "add", "-f", ".")
		runGit(t, p, "-c", "core.precomposeunicode=true", "commit", "-q", "-m", "ship")
		if index := runGit(t, p, "-c", "core.quotepath=false", "ls-files"); !strings.Contains(index, "café/dist/lib.js") {
			t.Skipf("git did not store the precomposed name: %q", index)
		}
		f.expect(nil, map[string]string{decomposed: keptTracked})
	})
}

// What the index says of a folder: inside by any spelling of the path,
// beside only by the parent's own bytes. Where the filesystem tells
// Vendor from vendor, a tracked Vendor is another folder.
func TestBuildOutputTrackedReadsTheIndex(t *testing.T) {
	const rel = "vendor/dist"
	for _, c := range []struct {
		index          string
		inside, beside bool
	}{
		{".gitignore\x00vendor/README.md\x00", false, true},
		{".gitignore\x00vendor/lib/a.js\x00", false, true},
		{".gitignore\x00Vendor/README.md\x00", false, false},
		{".gitignore\x00vendor-old/README.md\x00", false, false},
		{".gitignore\x00vendor/dist/app.js\x00", true, false},
		{".gitignore\x00Vendor/Dist/app.js\x00", true, false},
		{".gitignore\x00", false, false},
	} {
		b := &buildOutput{git: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(c.index), nil }}
		inside, beside, err := b.tracked(context.Background(), "/Users/dev/workspace/app", []string{rel})
		if err != nil || inside[rel] != c.inside || beside[rel] != c.beside {
			t.Errorf("tracked with the index %q = inside %v, beside %v, %v; want %v, %v", c.index, inside[rel], beside[rel], err, c.inside, c.beside)
		}
	}
	// Every entry lies beside a folder at the top level.
	b := &buildOutput{git: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte("Main.js\x00"), nil }}
	if _, beside, err := b.tracked(context.Background(), "/Users/dev/workspace/app", []string{"dist"}); err != nil || !beside["dist"] {
		t.Errorf("tracked for a top-level dist = beside %v, %v; want true", beside["dist"], err)
	}
}

// A folder the scan cannot read could hold yesterday's work.
func TestBuildOutputKeepsAProjectItCannotReadInFull(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	f := newBuildFixture(t)
	seal := func(dir string) {
		t.Helper()
		touch(t, filepath.Join(dir, "notes.txt"))
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	// In the project, inside the build folder itself, and inside a
	// node_modules there, which only the size walk enters.
	inProject := f.project("sealed", ignoreBuildOutput)
	touch(t, filepath.Join(inProject, "dist", "x.js"))
	seal(filepath.Join(inProject, "private"))
	inBuild := f.project("sealed-build", ignoreBuildOutput)
	touch(t, filepath.Join(inBuild, "dist", "x.js"))
	seal(filepath.Join(inBuild, "dist", "private"))
	inModules := f.project("sealed-modules", ignoreBuildOutput)
	touch(t, filepath.Join(inModules, ".next", "build.js"))
	seal(filepath.Join(inModules, ".next", "standalone", "node_modules", "pkg", "private"))

	items, kept, err := f.b.scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The one item is the walk's own marker for the folder it could not
	// list, which no plan can act on.
	if want := []engine.Item{unreadableMarker("folders under ~/workspace")}; !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %+v, want only %+v", items, want)
	}
	want := []keptOutput{
		{filepath.Join(inBuild, "dist"), keptUnread},
		{filepath.Join(inModules, ".next"), keptUnread},
		{filepath.Join(inProject, "dist"), keptUnread},
	}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept = %+v, want %+v", kept, want)
	}
}

// With the processes unknown no project can be called idle.
func TestBuildOutputOffersNothingWithoutTheProcessList(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	if offered, _ := f.scan(); len(offered) != 1 {
		t.Fatalf("offered = %v, want the one dist before the process list fails", offered)
	}
	f.b.procs = func(context.Context) (processes, error) { return processes{}, errors.New("lsof: exit status 1") }
	items, _, err := f.b.scan(context.Background())
	if err == nil || len(items) != 0 {
		t.Fatalf("scan = %v, %v; want an error and no items", items, err)
	}
}

// A submodule is part of the project that holds it: an edit there, or
// a process working there, keeps the submodule's build output.
func TestBuildOutputSubmoduleIsPartOfItsSuperproject(t *testing.T) {
	f := newBuildFixture(t)
	addSubmodule := f.submodules()

	app := f.project("app", "node_modules/\n")
	dist := addSubmodule(app, filepath.Join("libs", "ui"))
	f.expect([]string{dist}, nil)

	touch(t, filepath.Join(app, "src", "page.js"))
	f.stampRecent(filepath.Join(app, "src", "page.js"))
	f.expect(nil, map[string]string{dist: keptChanged})
	if err := os.Remove(filepath.Join(app, "src", "page.js")); err != nil {
		t.Fatal(err)
	}

	f.stampRecent(filepath.Join(app, ".git", "logs", "HEAD"))
	f.expect(nil, map[string]string{dist: keptChanged})
	old := f.now.Add(-buildOutputIdle - time.Hour)
	if err := os.Chtimes(filepath.Join(app, ".git", "logs", "HEAD"), old, old); err != nil {
		t.Fatal(err)
	}

	f.b.procs = func(context.Context) (processes, error) { return processes{cwds: []string{app}}, nil }
	f.expect(nil, map[string]string{dist: keptProcess})
	if err := f.b.recheck(context.Background(), dist); err == nil || !strings.Contains(err.Error(), keptProcess) {
		t.Fatalf("recheck with a process in the superproject = %v, want a refusal", err)
	}
	f.b.procs = func(context.Context) (processes, error) { return processes{}, nil }
	f.expect([]string{dist}, nil)

	// The home folder above the roots holds this one as a submodule:
	// judging it would mean walking the whole home.
	runGit(t, f.home, "init", "-q", "-b", "main")
	commitFile(t, f.home, "README", "home\n")
	homeDist := addSubmodule(f.home, filepath.Join("workspace", "part"))
	f.expect([]string{dist}, map[string]string{homeDist: "its superproject is outside the project roots"})
}

// submodules makes a repository outside the home folder that ignores
// dist, and returns a function that adds it to super as a submodule
// at a path, builds a dist in it and returns that folder.
func (f *buildFixture) submodules() func(super, at string) string {
	f.t.Helper()
	origin := filepath.Join(filepath.Dir(f.home), "origin", "ui")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		f.t.Fatal(err)
	}
	runGit(f.t, origin, "init", "-q", "-b", "main")
	commitFile(f.t, origin, ".gitignore", "dist/\n")
	commitFile(f.t, origin, "index.js", "lib\n")
	return func(super, at string) string {
		f.t.Helper()
		runGit(f.t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", origin, at)
		runGit(f.t, super, "commit", "-q", "-m", "add "+at)
		dist := filepath.Join(super, at, "dist")
		touch(f.t, filepath.Join(dist, "ui.js"))
		return dist
	}
}

// git names no superproject for a submodule it cannot match in the
// superproject's index; the submodule's git dir still says it is one,
// and it is not judged alone.
func TestBuildOutputKeepsASubmoduleGitCannotPlace(t *testing.T) {
	f := newBuildFixture(t)
	addSubmodule := f.submodules()
	want := map[string]string{}

	// The superproject keeps its git dir outside a .git folder, so the
	// submodule's lies in no .git/modules. Such a repository is kept
	// whole: its own .git file reads like an unplaced submodule's.
	sep := f.path("sep")
	gitDir := filepath.Join(filepath.Dir(f.home), "gitdirs", "sep.git")
	for _, dir := range []string{sep, filepath.Dir(gitDir)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, sep, "init", "-q", "-b", "main", "--separate-git-dir", gitDir)
	commitFile(t, sep, ".gitignore", "node_modules/\n")
	commitFile(t, sep, "main.js", "source\n")
	sepDist := addSubmodule(sep, filepath.Join("libs", "ui"))
	want[sepDist] = keptUnplaced
	f.expect(nil, want)
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if super := runGit(t, filepath.Dir(sepDist), "rev-parse", "--show-superproject-working-tree"); super != "" {
		t.Fatalf("git names the superproject %s with its index unreadable", super)
	}
	f.expect(nil, want)

	// The superproject's index cannot be read.
	broken := f.project("broken", "node_modules/\n")
	dist := addSubmodule(broken, filepath.Join("libs", "ui"))
	f.expect([]string{dist}, want)
	if err := os.WriteFile(filepath.Join(broken, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	want[dist] = keptUnplaced
	f.expect(nil, want)

	// The submodule's checkout in a linked worktree of the superproject
	// is a linked worktree of the submodule's repository, and that
	// worktree's index cannot be read.
	main := f.project("main", "node_modules/\n")
	mainDist := addSubmodule(main, filepath.Join("libs", "ui"))
	mainWt := f.path("main-wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "side", mainWt)
	ui := filepath.Join(mainWt, "libs", "ui")
	runGit(t, filepath.Dir(mainDist), "worktree", "add", "-q", "-b", "side", ui)
	wtDist := filepath.Join(ui, "dist")
	touch(t, filepath.Join(wtDist, "ui.js"))
	page := filepath.Join(mainWt, "src", "page.js")
	touch(t, page)
	f.stampRecent(page)
	want[wtDist] = keptChanged
	f.expect([]string{mainDist}, want)
	if err := os.WriteFile(filepath.Join(main, ".git", "worktrees", "main-wt", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if super := runGit(t, ui, "rev-parse", "--show-superproject-working-tree"); super != "" {
		t.Fatalf("git names the superproject %s with its index unreadable", super)
	}
	want[wtDist] = keptUnplaced
	f.expect([]string{mainDist}, want)
	// sparse-checkout moves core.worktree into config.worktree.
	runGit(t, filepath.Dir(mainDist), "sparse-checkout", "set", "lib")
	modules := filepath.Join(main, ".git", "modules", "libs", "ui")
	if config, err := os.ReadFile(filepath.Join(modules, "config")); err != nil || strings.Contains(string(config), "\tworktree = ") {
		t.Fatalf("sparse-checkout left core.worktree in config: %v\n%s", err, config)
	}
	if moved, err := os.ReadFile(filepath.Join(modules, "config.worktree")); err != nil || !strings.Contains(string(moved), "\tworktree = ") {
		t.Fatalf("sparse-checkout did not move core.worktree to config.worktree: %v\n%s", err, moved)
	}
	f.expect([]string{mainDist}, want)

	// The submodule's folder was renamed to another letter case.
	recased := f.project("recased", "node_modules/\n")
	addSubmodule(recased, filepath.Join("Libs", "ui"))
	if err := os.Rename(filepath.Join(recased, "Libs"), filepath.Join(recased, "tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(recased, "tmp"), filepath.Join(recased, "libs")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(recased, "Libs", "ui")); err != nil {
		t.Skip("the filesystem tells Libs from libs")
	}
	if super := runGit(t, filepath.Join(recased, "libs", "ui"), "rev-parse", "--show-superproject-working-tree"); super != "" {
		t.Skipf("git places the renamed submodule in %s", super)
	}
	want[filepath.Join(recased, "libs", "ui", "dist")] = keptUnplaced
	f.expect([]string{mainDist}, want)
}

// A .git file names a submodule's git dir or a linked worktree's, and
// only a linked worktree's holds a commondir file, whatever the folders
// are called. A linked worktree of a submodule's repository counts as
// a submodule.
func TestSubmoduleGitDir(t *testing.T) {
	f := newBuildFixture(t)
	ctx := context.Background()
	app := f.project("app", ignoreBuildOutput)
	sub := filepath.Dir(f.submodules()(app, filepath.Join("libs", "ui")))
	worktree := func(of, name string) string {
		t.Helper()
		wt := f.path(name)
		runGit(t, of, "worktree", "add", "-q", "-b", name, wt)
		return wt
	}
	named := worktree(app, "modules")
	ofSubmodule := worktree(sub, "ui-side")
	runGit(t, named, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	sep := f.path("sep")
	if err := os.MkdirAll(sep, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, sep, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(filepath.Dir(f.home), "sep.git"))
	// The submodule's checkout in a linked worktree of the superproject,
	// made as a linked worktree of the submodule's repository.
	inWorktree := filepath.Join(worktree(app, "app-wt"), "libs", "ui")
	runGit(t, sub, "worktree", "add", "-q", "-b", "in-app-wt", inWorktree)
	// A linked worktree of a repository whose config git cannot read.
	broken := f.project("broken", "")
	brokenWt := worktree(broken, "broken-wt")
	if err := os.WriteFile(filepath.Join(broken, ".git", "config"), []byte("[core\nbroken"), 0o644); err != nil {
		t.Fatal(err)
	}
	odd := f.path("odd")
	touch(t, filepath.Join(odd, ".git"))
	if err := os.WriteFile(filepath.Join(odd, ".git"), []byte("not a gitdir line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what, top string
		want      bool
	}{
		{"a .git folder", app, false},
		{"a submodule", sub, true},
		{"a linked worktree in a folder named modules", named, false},
		{"a submodule inside that worktree", filepath.Join(named, "libs", "ui"), true},
		{"a linked worktree of a submodule", ofSubmodule, true},
		{"a linked worktree of a submodule at the submodule's path", inWorktree, true},
		{"a linked worktree of a repository git cannot read", brokenWt, true},
		{"a repository made with --separate-git-dir", sep, true},
		{"a .git file that names no git dir", odd, false},
	} {
		if got := f.b.submoduleGitDir(ctx, c.top); got != c.want {
			t.Errorf("submoduleGitDir for %s = %v, want %v", c.what, got, c.want)
		}
	}

	// A commondir, a config or a config.worktree that does not answer
	// is not a linked worktree's word, each on its own.
	for _, file := range []string{
		filepath.Join(app, ".git", "worktrees", "modules", "commondir"),
		filepath.Join(app, ".git", "config"),
		filepath.Join(app, ".git", "config.worktree"),
	} {
		f.b.w.blocked.add(file)
		if !f.b.submoduleGitDir(ctx, named) {
			t.Errorf("submoduleGitDir is false for a linked worktree with %s blocked", filepath.Base(file))
		}
		f.b.w.blocked.remove(file)
	}
	if f.b.submoduleGitDir(ctx, named) {
		t.Fatal("submoduleGitDir is true for a linked worktree in a folder named modules once nothing is blocked")
	}
	// git warns and answers "unset" for a config it may not read.
	if os.Getuid() != 0 {
		config := filepath.Join(app, ".git", "config")
		if err := os.Chmod(config, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(config, 0o644) })
		if !f.b.submoduleGitDir(ctx, named) {
			t.Error("submoduleGitDir is false for a linked worktree whose repository's config may not be read")
		}
	}
}

// A linked worktree is a project of its own, in a folder named modules
// too. A linked worktree of a submodule's repository is kept: git
// names no superproject for it, and one at the submodule's path in
// another checkout of the superproject is that checkout's submodule.
func TestBuildOutputLinkedWorktreeIsAProjectWhateverItsName(t *testing.T) {
	f := newBuildFixture(t)
	app := f.project("app", ignoreBuildOutput)
	sub := filepath.Dir(f.submodules()(app, filepath.Join("libs", "ui")))
	named, ofSubmodule := f.path("modules"), f.path("ui-side")
	runGit(t, app, "worktree", "add", "-q", "-b", "side", named)
	runGit(t, sub, "worktree", "add", "-q", "-b", "side", ofSubmodule)
	touch(t, filepath.Join(named, "dist", "x.js"))
	touch(t, filepath.Join(ofSubmodule, "dist", "ui.js"))
	f.expect([]string{filepath.Join(sub, "dist"), filepath.Join(named, "dist")}, map[string]string{filepath.Join(ofSubmodule, "dist"): keptUnplaced})
}

// What git prints for a work tree: its top level, an empty prefix and,
// for a submodule, the superproject. Anything else is no answer for
// the folder asked about.
func TestBuildOutputReadsGitsWorkTreeAnswer(t *testing.T) {
	const top, super = "/Users/dev/workspace/app", "/Users/dev/workspace/shell"
	for _, c := range []struct {
		out   string
		super string
		ok    bool
	}{
		{top + "\n\n", "", true},
		{top + "\n\n" + super + "\n", super, true},
		{"/Users/dev/workspace\napp/\n", "", false},
		{top + "\n\n" + super + "\nmore\n", "", false},
		{top + "\n", "", false},
		{"", "", false},
	} {
		b := &buildOutput{git: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(c.out), nil }}
		got, ok, err := b.workTree(context.Background(), top)
		if err != nil || ok != c.ok || got != c.super {
			t.Errorf("workTree with git printing %q = %q, %v, %v; want %q, %v", c.out, got, ok, err, c.super, c.ok)
		}
	}
}

// The climb from a submodule to the project that holds it stops after
// maxSuperprojects, at a superproject git cannot read, and at one that
// is a submodule git names no superproject for.
func TestBuildOutputSuperprojectClimb(t *testing.T) {
	f := newBuildFixture(t)
	leaf := f.path("s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "leaf")
	hit := filepath.Join(leaf, "dist")
	touch(t, filepath.Join(hit, "x.js"))
	// Every folder is a submodule of the one above it; git fails at
	// broken, and names no superproject at unplaced.
	nested := func(broken, unplaced string) func(context.Context, string, []byte, ...string) ([]byte, error) {
		return func(_ context.Context, dir string, _ []byte, args ...string) ([]byte, error) {
			switch {
			case dir == broken:
				return nil, errors.New("git rev-parse: exit status 128")
			case dir == unplaced:
				return []byte(dir + "\n\n"), nil
			case args[0] == "rev-parse":
				return []byte(dir + "\n\n" + filepath.Dir(dir) + "\n"), nil
			case args[0] == "check-ignore":
				return []byte(".gitignore\x001\x00dist/\x00dist\x00"), nil
			}
			return []byte("main.js\x00"), nil
		}
	}
	ctx := context.Background()
	roots := f.b.walkRoots(ctx)
	above := filepath.Dir(filepath.Dir(leaf))
	if err := os.WriteFile(filepath.Join(above, ".git"), []byte("gitdir: ../.git/modules/s8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(filepath.Dir(above), ".git", "modules", "s8", "HEAD"))
	for _, c := range []struct{ broken, unplaced, why string }{
		{"", "", "its superprojects nest too deep to follow"},
		{above, "", "git cannot read its superproject"},
		{"", above, keptUnplaced},
	} {
		f.b.git = nested(c.broken, c.unplaced)
		items, kept, err := f.b.judge(ctx, roots, leaf, []string{hit}, processes{})
		if want := []keptOutput{{hit, c.why}}; err != nil || len(items) != 0 || !reflect.DeepEqual(kept, want) {
			t.Errorf("judge = %+v, %+v, %v; want %+v", items, kept, err, want)
		}
	}
}

// A root reached through a symlink is left alone, and so is a plain
// root the link leads to, into or above: the projects there have two
// spellings, and a process naming one would be missed under the other.
func TestBuildOutputLeavesASymlinkedRootAlone(t *testing.T) {
	for _, c := range []struct {
		name   string
		roots  []string
		link   string // the root that is a symlink, under the home folder
		target string // where it leads: a folder under the fixture's top folder, or /
		text   string // what the link says, when not the target as the disk spells it: another spelling under the top folder, or a folder from / that leads to the same place
		real   string // the project, under the fixture's top folder
	}{
		{"to a folder outside every root", []string{"~/workspace"}, "workspace", "volume/workspace", "", "volume/workspace/served"},
		{"to another root", []string{"~/workspace", "~/dev"}, "workspace", "home/dev", "", "home/dev/served"},
		{"into another root", []string{"~/src", "~/dev"}, "src", "home/dev/team", "", "home/dev/team/served"},
		{"above another root", []string{"~/all", "~/dev"}, "all", "home", "", "home/dev/served"},
		{"to the filesystem root", []string{"~/all", "~/dev"}, "all", "/", "", "home/dev/served"},
		{"to another root, in another letter case", []string{"~/workspace", "~/dev"}, "workspace", "home/dev", "home/Dev", "home/dev/served"},
		{"into another root, in another letter case", []string{"~/src", "~/dev"}, "src", "home/dev/team", "home/DEV/Team", "home/dev/team/served"},
		{"to another root, by another path", []string{"~/workspace", "~/dev"}, "workspace", "home/dev", "/System/Volumes/Data", "home/dev/served"},
		{"into another root, by another path", []string{"~/src", "~/dev"}, "src", "home/dev/team", "/System/Volumes/Data", "home/dev/team/served"},
		{"above another root, by another path", []string{"~/all", "~/dev"}, "all", "home", "/System/Volumes/Data", "home/dev/served"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBuildFixture(t)
			top := filepath.Dir(f.home)
			target, text := filepath.Join(top, c.target), filepath.Join(top, c.text)
			if c.target == "/" {
				target = "/"
			}
			switch {
			case c.text == "":
				text = target
			case strings.HasPrefix(c.text, "/"):
				text = c.text + target
			}
			f.ws = filepath.Dir(filepath.Join(top, c.real))
			for _, dir := range []string{f.home, target, f.ws} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if a, err := os.Stat(text); err != nil {
				t.Skipf("%s does not lead to %s here: %v", text, target, err)
			} else if b, err := os.Stat(target); err != nil || !os.SameFile(a, b) {
				t.Skipf("%s is another folder than %s here", text, target)
			}
			if err := os.Symlink(text, filepath.Join(f.home, c.link)); err != nil {
				t.Fatal(err)
			}
			f.b.roots = c.roots
			p := f.project("served", ignoreBuildOutput)
			touch(t, filepath.Join(p, "dist", "server.js"))
			below, err := filepath.Rel(target, p)
			if err != nil {
				t.Fatal(err)
			}
			via := filepath.Join(f.home, c.link, below)
			if _, err := os.Stat(filepath.Join(via, "dist", "server.js")); err != nil {
				t.Fatalf("the link does not lead to the project: %v", err)
			}

			f.expect(nil, nil)
			for _, path := range []string{filepath.Join(p, "dist"), filepath.Join(via, "dist")} {
				if err := f.b.recheck(context.Background(), path); err == nil || !strings.Contains(err.Error(), "not a build folder the scan lists") {
					t.Fatalf("recheck %s = %v, want a refusal: the scan lists nothing there", path, err)
				}
			}
		})
	}
}

// A linked root costs what it leads to and no more: a plain root it
// does not reach is still walked.
func TestBuildOutputWalksAPlainRootBesideALinkedOne(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))

	plain := f.ws
	f.ws = filepath.Join(filepath.Dir(f.home), "volume", "code")
	linked := f.project("served", ignoreBuildOutput)
	touch(t, filepath.Join(linked, "dist", "server.js"))
	if err := os.Symlink(f.ws, filepath.Join(f.home, "code")); err != nil {
		t.Fatal(err)
	}
	f.ws = plain

	f.b.roots = []string{"~/workspace", "~/code"}
	f.expect([]string{filepath.Join(p, "dist")}, nil)
}

// A root that cannot be resolved could be a link to any other root.
func TestBuildOutputWalksNoRootWhenOneCannotBeResolved(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	f.b.roots = []string{"~/workspace", "~/dev"}
	f.expect([]string{filepath.Join(p, "dist")}, nil)

	f.b.w.blocked.add(filepath.Join(f.home, "dev"))
	f.expect(nil, nil)
	if err := f.b.recheck(context.Background(), filepath.Join(p, "dist")); err == nil || !strings.Contains(err.Error(), "not a build folder the scan lists") {
		t.Fatalf("recheck = %v, want a refusal: no root is walked", err)
	}

	// A link that resolves to a folder that cannot be read.
	g := newBuildFixture(t)
	p = g.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	target := filepath.Join(filepath.Dir(g.home), "volume", "code")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(g.home, "code")); err != nil {
		t.Fatal(err)
	}
	g.b.roots = []string{"~/workspace", "~/code"}
	g.expect([]string{filepath.Join(p, "dist")}, nil)
	g.b.w.blocked.add(target)
	g.expect(nil, nil)
}

// The query walks what node-modules-dirs walks: its roots, its depth,
// its excludes. A tool query cannot read a rule, so this holds the two
// together.
func TestBuildOutputWalksLikeNodeModules(t *testing.T) {
	nm := catalogRule(t, "node-modules-dirs").Discover
	if !reflect.DeepEqual(projectRoots, nm.Roots) {
		t.Errorf("projectRoots = %v, node-modules-dirs walks %v", projectRoots, nm.Roots)
	}
	if buildOutputDepth != nm.MaxDepth {
		t.Errorf("buildOutputDepth = %d, node-modules-dirs walks to %d", buildOutputDepth, nm.MaxDepth)
	}
	if len(nm.Exclude) != 0 {
		t.Errorf("node-modules-dirs gained excludes %v that the build-output walk does not carry", nm.Exclude)
	}
	if got := newBuildOutput("/Users/dev"); !reflect.DeepEqual(got.roots, projectRoots) || got.idle != buildOutputIdle {
		t.Errorf("the query runs with roots %v and idle %s", got.roots, got.idle)
	}

	f := newBuildFixture(t)
	p := f.project("deep", ignoreBuildOutput)
	// ~/workspace/deep is depth 1, so h is depth 9 and its dist depth 10.
	atLimit := filepath.Join(p, "a", "b", "c", "d", "e", "f", "g", "h", "dist")
	touch(t, filepath.Join(atLimit, "x.js"))
	commitFile(t, p, filepath.Join("a", "b", "c", "d", "e", "f", "g", "h", "lib.js"), "source\n")
	touch(t, filepath.Join(p, "a", "b", "c", "d", "e", "f", "g", "h", "i", "dist", "x.js"))
	touch(t, filepath.Join(p, "node_modules", "pkg", "dist", "index.js"))
	touch(t, filepath.Join(p, ".git", "dist", "x"))
	f.expect([]string{atLimit}, nil)
}

// One owner per byte: a name this query offers must not be a discover
// rule's name too, or both rules would claim every hit. The catalog
// test in internal/engine cannot see a tool query's names.
func TestBuildOutputNamesHaveOneOwner(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.Discover != nil && slices.Contains(buildOutputNames, r.Discover.Name) {
			t.Errorf("%s discovers %q, which build-output offers too", r.ID, r.Discover.Name)
		}
	}
	if want := []string{".next", ".turbo", ".venv", "dist"}; !reflect.DeepEqual(buildOutputNames, want) {
		t.Errorf("buildOutputNames = %v, want %v: a new name needs its regen story in the rule and a decisions-log entry", buildOutputNames, want)
	}
	// The walk never enters a node_modules, so nothing below one is an
	// item of both rules.
	if !builtinExcludes["node_modules"] {
		t.Error("node_modules is no longer excluded from discovery: its dist folders would be offered twice")
	}
}

// The other way round, a node_modules inside a build folder (Next.js
// standalone output) is an item of node-modules-dirs nested in the
// build folder's: selecting both plans one move.
func TestNodeModulesInsideBuildOutputIsPlannedOnce(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("site", ignoreBuildOutput)
	touch(t, filepath.Join(p, ".next", "build.js"))
	touch(t, filepath.Join(p, ".next", "standalone", "node_modules", "next", "index.js"))

	ctx := context.Background()
	outputs, _, err := f.b.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modules := (&Scanner{Host: f.b.host, fs: f.b.w}).Scan(ctx, []engine.Rule{catalogRule(t, "node-modules-dirs")})
	findings := []engine.Finding{{Rule: catalogRule(t, "build-output"), Items: outputs}, modules[0]}
	for i := range findings {
		findings[i].FillItemKeys(f.home)
	}
	if len(outputs) != 1 || len(modules[0].Items) != 1 {
		t.Fatalf("items = %+v and %+v, want the .next and the node_modules inside it", outputs, modules[0].Items)
	}
	plan := engine.BuildPlan(f.b.host, findings, map[string]bool{"build-output": true, "node-modules-dirs": true})
	if len(plan.Actions) != 1 || plan.Actions[0].Path != filepath.Join(p, ".next") || plan.TotalBytes() != outputs[0].Bytes {
		t.Fatalf("actions = %+v, total %d; want one move of the .next and its %d bytes", plan.Actions, plan.TotalBytes(), outputs[0].Bytes)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].RuleID != "node-modules-dirs" {
		t.Fatalf("skips = %+v, want the node_modules skipped as inside the .next", plan.Skipped)
	}
}

func TestBuildOutputRule(t *testing.T) {
	r := catalogRule(t, "build-output")
	if r.Risk != engine.RiskCaution {
		t.Errorf("risk = %q, want caution: never pre-selected", r.Risk)
	}
	if len(r.NativeCommand) != 0 {
		t.Errorf("native_command = %v, want none: the folder goes to the Trash", r.NativeCommand)
	}
	if r.PreAction != engine.PreActionBuildOutputRecheck {
		t.Errorf("pre_action = %q, want %q", r.PreAction, engine.PreActionBuildOutputRecheck)
	}
	if !r.Beta || r.ToolQuery != "build-output" {
		t.Errorf("beta = %v, tool_query = %q; want a beta rule on the build-output query", r.Beta, r.ToolQuery)
	}
	// Registered, so the scanner runs it under the tool-query deadline
	// like every query.
	if _, ok := DefaultQueries()[r.ToolQuery]; !ok {
		t.Errorf("no query registered as %q", r.ToolQuery)
	}
	// The story says what brings each kind back: a build, or for a
	// .venv an install that needs the network.
	build, install, ok := strings.Cut(r.Regen.Story, ".venv")
	if !ok {
		t.Fatalf("regen story does not name .venv: %q", r.Regen.Story)
	}
	for _, name := range buildOutputNames {
		if name != ".venv" && !strings.Contains(build, name) {
			t.Errorf("regen story does not name %s before .venv: %q", name, r.Regen.Story)
		}
	}
	if !strings.Contains(build, "build") {
		t.Errorf("regen story does not say a build recreates the first three: %q", build)
	}
	if !strings.Contains(install, "install") || !strings.Contains(install, "download") {
		t.Errorf("regen story does not say a .venv needs an install that downloads: %q", install)
	}
	if !strings.Contains(r.Regen.Cost, "network") {
		t.Errorf("regen cost does not mention the network: %q", r.Regen.Cost)
	}
}

// A scan cut short, by the tool-query deadline or a cancel, is an
// error with no items: folders judged before the cut are not offered
// on their own.
func TestBuildOutputScanCutShortOffersNothing(t *testing.T) {
	f := newBuildFixture(t)
	for _, name := range []string{"first", "second"} {
		touch(t, filepath.Join(f.project(name, ignoreBuildOutput), "dist", "x.js"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.b.git = func(gctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
		if dir == f.path("second") {
			cancel()
			<-gctx.Done()
			return nil, gctx.Err()
		}
		return gitWith(gctx, dir, stdin, args...)
	}
	items, kept, err := f.b.scan(ctx)
	if !errors.Is(err, context.Canceled) || items != nil || kept != nil {
		t.Fatalf("scan = %+v, %+v, %v; want only the context's error", items, kept, err)
	}
}

// git itself is bounded: a call that never answers is cut off at
// gitCallTimeout, and the repository it was asked about offers
// nothing.
func TestBuildOutputGitCallsHaveADeadline(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("idle", ignoreBuildOutput)
	touch(t, filepath.Join(p, "dist", "x.js"))
	f.expect([]string{filepath.Join(p, "dist")}, nil)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 15\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	timeout := gitCallTimeout
	gitCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitCallTimeout = timeout })

	start := time.Now()
	if out, err := gitWith(context.Background(), p, nil, "status"); err == nil {
		t.Fatalf("a git that never answers returned %q", out)
	}
	f.expect(nil, map[string]string{filepath.Join(p, "dist"): "git cannot read its repository"})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("two calls to a git that never answers took %s: no deadline cut them off", took)
	}
}

// A build folder inside a finished worktree is a nested item: the
// worktree's removal names it, and selecting both plans it once.
func TestWorktreeRemovalNamesBuildOutputInside(t *testing.T) {
	f := newBuildFixture(t)
	app := f.project("app", ignoreBuildOutput)
	wt := filepath.Join(app, ".claude", "worktrees", "done")
	runGit(t, app, "worktree", "add", "-q", "-b", "done", wt)
	commitFile(t, wt, "feature.js", "done\n")
	runGit(t, app, "merge", "-q", "--ff-only", "done")
	dist := filepath.Join(wt, "dist")
	touch(t, filepath.Join(dist, "bundle.js"))

	ctx := context.Background()
	worktrees, err := scanWorktrees(ctx, f.b.w, f.b.host, f.b.roots, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 1 || worktrees[0].Path != wt {
		t.Fatalf("worktree items = %+v, want the finished one at %s", worktrees, wt)
	}
	outputs, _, err := f.b.scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].Path != dist {
		t.Fatalf("build-output items = %+v, want the dist at %s", outputs, dist)
	}

	// Catalog order, as a scan returns its findings.
	findings := []engine.Finding{
		{Rule: catalogRule(t, "build-output"), Items: outputs},
		{Rule: catalogRule(t, "git-worktrees"), Items: worktrees},
	}
	for i := range findings {
		findings[i].FillItemKeys(f.home)
	}
	wtID := engine.ItemID("git-worktrees", findings[1].Items[0].Key)
	distID := engine.ItemID("build-output", findings[0].Items[0].Key)

	plan := engine.BuildPlan(f.b.host, findings, map[string]bool{"git-worktrees": true})
	if len(plan.Actions) != 1 || plan.Actions[0].RuleID != "git-worktrees" {
		t.Fatalf("worktree alone: actions = %+v, want its removal only", plan.Actions)
	}
	want := []engine.Included{{ID: distID, Bytes: outputs[0].Bytes}}
	if got := plan.Actions[0].Includes; !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree alone: the removal also removes %+v, want %+v", got, want)
	}

	plan = engine.BuildPlan(f.b.host, findings, map[string]bool{"git-worktrees": true, "build-output": true})
	if len(plan.Actions) != 1 || plan.Actions[0].RuleID != "git-worktrees" {
		t.Fatalf("both selected: actions = %+v, want the worktree's removal only", plan.Actions)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].RuleID != "build-output" || plan.Skipped[0].Reason != "inside "+wtID+", also selected" {
		t.Fatalf("both selected: skips = %+v, want the dist skipped as inside %s", plan.Skipped, wtID)
	}
	if plan.TotalBytes() != worktrees[0].Bytes {
		t.Fatalf("both selected: total = %d, want the worktree's %d, the dist counted once", plan.TotalBytes(), worktrees[0].Bytes)
	}

	plan = engine.BuildPlan(f.b.host, findings, map[string]bool{"build-output": true})
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != engine.ActionTrash || plan.Actions[0].Path != dist {
		t.Fatalf("dist alone: actions = %+v, want one Trash move of %s", plan.Actions, dist)
	}
}

func TestRecheckBuildOutput(t *testing.T) {
	f := newBuildFixture(t)
	p := f.project("app", ignoreBuildOutput)
	dist := filepath.Join(p, "dist")
	touch(t, filepath.Join(dist, "x.js"))
	touch(t, filepath.Join(p, ".venv", "pyvenv.cfg"))
	ctx := context.Background()
	refused := func(when, path, want string) {
		t.Helper()
		err := f.b.recheck(ctx, path)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), path) {
			t.Fatalf("recheck %s = %v, want a refusal naming the folder and %q", when, err, want)
		}
	}

	if err := f.b.recheck(ctx, dist); err != nil {
		t.Fatalf("recheck of an unchanged folder: %v", err)
	}
	refused("of a folder the rule does not name", filepath.Join(p, "src"), "not a build folder")
	refused("of a path that is not clean", dist+"/../dist", "not a build folder")
	refused("of a folder that is gone", filepath.Join(p, "web", "dist"), "not a folder any more")

	// Folders the scan's walk never reaches, each ignored and idle.
	deep := filepath.Join(p, "a", "b", "c", "d", "e", "f", "g", "h", "i", "dist")
	for _, unlisted := range []string{
		filepath.Join(p, "node_modules", "pkg", "dist"),
		filepath.Join(dist, "inner", "dist"),
		deep,
	} {
		touch(t, filepath.Join(unlisted, "x.js"))
		refused("of a folder the walk does not reach", unlisted, "not a build folder the scan lists")
	}
	if err := f.b.recheck(ctx, filepath.Dir(filepath.Dir(deep))+"/dist"); err == nil || !strings.Contains(err.Error(), "not a folder any more") {
		t.Fatalf("recheck one level up, at the walk's depth = %v, want it past the walk test", err)
	}

	// Each change since the scan refuses, then is taken back.
	f.b.procs = func(context.Context) (processes, error) { return processes{cwds: []string{p}}, nil }
	refused("with a process in the project", dist, keptProcess)
	f.b.procs = func(context.Context) (processes, error) { return processes{}, errors.New("lsof: exit status 1") }
	if err := f.b.recheck(ctx, dist); err == nil || !strings.Contains(err.Error(), "lsof") {
		t.Fatalf("recheck without the process list = %v, want the failure", err)
	}
	f.b.procs = func(context.Context) (processes, error) { return processes{}, nil }

	edited := filepath.Join(p, "main.js")
	before, err := os.Stat(edited)
	if err != nil {
		t.Fatal(err)
	}
	f.stampRecent(edited)
	refused("after an edit in the project", dist, "its project changed")
	if err := os.Chtimes(edited, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := f.b.recheck(ctx, dist); err != nil {
		t.Fatalf("recheck once the edit is old again: %v", err)
	}

	// The other build folder of the project was rebuilt.
	touch(t, filepath.Join(p, ".venv", "lib", "new.py"))
	f.stampRecent(filepath.Join(p, ".venv", "lib", "new.py"))
	refused("after the project's .venv changed", dist, "its project changed")
	if err := os.RemoveAll(filepath.Join(p, ".venv", "lib")); err != nil {
		t.Fatal(err)
	}
	if err := f.b.recheck(ctx, dist); err != nil {
		t.Fatalf("recheck once the .venv change is gone: %v", err)
	}

	// A change that stamps only folders, here in the other build
	// folder, is none to the scan and none to the recheck.
	if err := os.MkdirAll(filepath.Join(p, ".venv", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.stampRecent(filepath.Join(p, ".venv", "lib"))
	f.stampRecent(filepath.Join(p, ".venv"))
	f.expect([]string{dist, filepath.Join(p, ".venv")}, nil)
	for _, path := range []string{dist, filepath.Join(p, ".venv")} {
		if err := f.b.recheck(ctx, path); err != nil {
			t.Fatalf("recheck of %s, which the scan offers: %v", path, err)
		}
	}

	link := filepath.Join(p, "web", "dist")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dist, link); err != nil {
		t.Fatal(err)
	}
	refused("of a symlink", link, "not a folder any more")

	// Last, since these change the repository for good.
	runGit(t, p, "add", "-f", filepath.Join("dist", "x.js"))
	runGit(t, p, "commit", "-q", "-m", "ship dist")
	refused("after the folder was committed", dist, keptTracked)
	runGit(t, p, "rm", "-q", "--cached", filepath.Join("dist", "x.js"))
	runGit(t, p, "commit", "-q", "-m", "unship dist")
	if err := f.b.recheck(ctx, dist); err != nil {
		t.Fatalf("recheck once the folder is untracked again: %v", err)
	}
	commitFile(t, p, ".gitignore", "node_modules/\n")
	refused("after the ignore rule was removed", dist, keptNoIgnore)
}

func TestProcessesWorkIn(t *testing.T) {
	const app = "/Users/dev/workspace/app"
	yes := []processes{
		{cwds: []string{"/", app}},
		{cwds: []string{app + "/web/src"}},
		{args: []string{"node " + app + "/dist/server.js --port 3000"}},
		{args: []string{`/bin/sh -c cd "` + app + `" && make`}},
		{args: []string{app + "/.venv/bin/python run.py"}},
		{args: []string{"tail -f " + app}},
		{args: []string{"rsync " + app + ":/backup"}},
		{args: []string{"docker run --mount type=bind,source=" + app + ",target=/app node:20"}},
		{args: []string{"/bin/sh -c (cd " + app + "; exec make watch)"}},
		{args: []string{"watchexec --watch " + app + "\t--restart make"}},
		{args: []string{"sh -c cd " + app + "&& make"}},
		// Another folder first, then the project itself.
		{args: []string{"diff -r " + app + "-old " + app}},
		// ps prints a path as it was typed; the filesystem answers to
		// it in another letter case.
		{args: []string{"node /users/dev/WORKSPACE/App/dist/server.js"}},
		{cwds: []string{"/users/dev/workspace/APP/src"}},
	}
	for _, p := range yes {
		if !p.workIn(app) {
			t.Errorf("workIn(%s) is false for %+v", app, p)
		}
	}
	// Another folder whose name starts the same.
	no := []processes{
		{cwds: []string{app + "-mobile", app + "2/src", "/Users/dev/workspace"}},
		{args: []string{"node " + app + "-mobile/server.js", "node " + app + "_old/x.js", "node " + app + ".bak/x.js", "node " + app + "2/x.js", "node " + app + "š/x.js", "node " + app + "X"}},
		{args: []string{"node /Users/dev/workspace/ap/x.js"}},
	}
	for _, p := range no {
		if p.workIn(app) {
			t.Errorf("workIn(%s) is true for %+v", app, p)
		}
	}

	// One folder under two spellings: an accent composed or apart, and
	// pairs only case folding joins.
	const ws = "/Users/dev/workspace/"
	for _, same := range [][2]string{
		{ws + "s\u030ckola", "tail -f " + ws + "\u0161kola/log.txt"},
		{ws + "Straße", "node " + ws + "STRASSE/server.js"},
		{ws + "Σίσυφος", "node " + ws + "ΣΊΣΥΦΟΣ/x.js"},
		// Cherokee, whose two cases x/text's folding swaps.
		{ws + "\u13e3\u13b3\u13a9", "node " + ws + "\uabb3\uab83\uab79/dist/server.js"},
	} {
		if !(processes{args: []string{same[1]}}).workIn(same[0]) {
			t.Errorf("workIn(%q) is false for %q", same[0], same[1])
		}
	}
	// An accent makes another name, composed or apart.
	for _, line := range []string{"node " + ws + "caf\u00e9/x.js", "node " + ws + "cafe\u0301/x.js"} {
		if (processes{args: []string{line}}).workIn(ws + "cafe") {
			t.Errorf("workIn(%q) is true for %q", ws+"cafe", line)
		}
	}
}

// A volume that ignores letter case joins every letter with its other
// case, the ones newer than x/text's tables too.
func TestFoldNameJoinsEveryCasePair(t *testing.T) {
	var apart []string
	for r := rune(0); r <= unicode.MaxRune; r++ {
		o := unicode.SimpleFold(r)
		if o == r || foldName("d"+string(r)) == foldName("d"+string(o)) {
			continue
		}
		apart = append(apart, fmt.Sprintf("%U/%U", r, o))
	}
	if len(apart) > 0 {
		t.Errorf("foldName tells %d letters from their other case: %s", len(apart), strings.Join(apart[:min(len(apart), 8)], " "))
	}
	// ASCII takes a shorter way that must agree with the long one.
	if short, long := foldName("/Users/Dev/App"), foldName("/Users/Dev/App/\u00e9"); !strings.HasPrefix(long, short+"/") {
		t.Errorf("foldName(%q) = %q, not below %q", "/Users/Dev/App/\u00e9", long, short)
	}
}

func TestEscapedByTools(t *testing.T) {
	const ws = "/Users/dev/workspace/"
	for _, path := range []string{ws + "my app", ws + "škola", ws + "s\u030ckola", ws + "文档", ws + "🚀 site"} {
		if escapedByTools(path) {
			t.Errorf("escapedByTools(%q) is true, want a path both tools print as it is", path)
		}
	}
	for _, path := range []string{
		ws + `back\slash`,
		ws + "tab\there",
		ws + "zero\u200bwidth",
		ws + "\U0001F468\u200d\U0001F4BB site",
		ws + "soft\u00adhyphen",
		ws + "mark\ufeff",
		ws + "wide\u00a0space",
		ws + "bad\xffbyte",
	} {
		if !escapedByTools(path) {
			t.Errorf("escapedByTools(%q) is false, want a path a tool escapes", path)
		}
	}
}

// A tool that is missing and one that fails are told apart.
func TestProcessToolTellsMissingFromFailed(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := processTool(context.Background(), "lsof"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("with no lsof on PATH: %v, want it called not installed", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "lsof"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := processTool(context.Background(), "lsof"); err == nil || !strings.Contains(err.Error(), "could not list") {
		t.Fatalf("with an lsof that fails: %v, want the failure", err)
	}
}

func TestParseLsofNames(t *testing.T) {
	out := "p146\nfcwd\nn/Users/dev/workspace/app\np168\nfcwd\nn/\np200\nfcwd\nn/Users/dev/with space/škola\n" +
		// A backslash in a name is written twice; this one is followed
		// by an x that belongs to the name.
		"p201\nfcwd\nn/Users/dev/back\\\\x41\n"
	want := []string{"/Users/dev/workspace/app", "/", "/Users/dev/with space/škola", `/Users/dev/back\\x41`}
	got, err := parseLsofNames(out)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %q, %v; want %q", got, err, want)
	}
	// What lsof writes for "škola" without a UTF-8 locale.
	if got, err := parseLsofNames("p1\nfcwd\nn/Users/dev/workspace/\\xc5\\xa1kola/src\n"); err == nil {
		t.Fatalf("names = %q with an escaped byte, want an error: the name cannot be compared", got)
	}
}

// lsof and ps escape non-ASCII bytes unless they run under a UTF-8
// locale, which a process started by an app or by launchd does not
// have. The real tools, a real process: its folder must be found by
// its working directory and by its command line.
func TestRunningProcessesReadNonASCIIPaths(t *testing.T) {
	for _, tool := range []string{"lsof", "ps", "sleep", "tail"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := func(dir string, argv ...string) {
		t.Helper()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	byCwd := filepath.Join(root, "škola app")
	byArgs := filepath.Join(root, "kuća-čđž")
	touch(t, filepath.Join(byCwd, "x"))
	touch(t, filepath.Join(byArgs, "log.txt"))
	start(byCwd, "sleep", "60")
	start("/", "tail", "-f", filepath.Join(byArgs, "log.txt"))

	var procs processes
	deadline := time.Now().Add(10 * time.Second)
	for {
		if procs, err = runningProcesses(context.Background()); err != nil {
			t.Fatal(err)
		}
		if procs.workIn(byCwd) && procs.workIn(byArgs) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !procs.workIn(byCwd) {
		t.Errorf("no process found working in %s", byCwd)
	}
	if !procs.workIn(byArgs) {
		t.Errorf("no process found naming %s", byArgs)
	}
	if procs.workIn(filepath.Join(root, "nobody-here")) {
		t.Error("a process found in a folder nothing runs in")
	}
}

func catalogRule(t *testing.T, id string) engine.Rule {
	t.Helper()
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("%s rule missing from the catalog", id)
	return engine.Rule{}
}
