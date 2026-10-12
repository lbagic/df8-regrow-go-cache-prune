package scanner

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// fnmFixture is a fixture home with fnm's folder at its macOS default,
// a system root whose launchd folders are empty, and no running
// process.
type fnmFixture struct {
	t      *testing.T
	home   string
	root   string
	dir    string
	env    map[string]string
	mapped []string
	n      *fnmVersions
}

func newFnmFixture(t *testing.T, versions ...string) *fnmFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fnmFixture{t: t, home: filepath.Join(root, "home"), root: filepath.Join(root, "sys"), env: map[string]string{}}
	f.dir = filepath.Join(f.home, "Library", "Application Support", "fnm")
	for _, d := range []string{filepath.Join(f.dir, "aliases"), filepath.Join(f.home, userLaunchdDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range systemLaunchdDirs {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range versions {
		f.install(v)
	}
	f.n = &fnmVersions{
		Home:   f.home,
		Root:   f.root,
		Getenv: func(k string) string { return f.env[k] },
		HasFnm: func() bool { return true },
		Mapped: func(context.Context) ([]string, error) { return f.mapped, nil },
		w:      testWalker(),
	}
	return f
}

func (f *fnmFixture) version(v string) string {
	return filepath.Join(f.dir, "node-versions", v)
}

func (f *fnmFixture) node(v string) string {
	return filepath.Join(f.version(v), "installation", "bin", "node")
}

func (f *fnmFixture) install(v string) {
	f.t.Helper()
	writeFile(f.t, f.node(v), 4096)
}

// link makes ~/opt/node18, a symlink to v18.17.1's installation.
func (f *fnmFixture) link() string {
	f.t.Helper()
	link := filepath.Join(f.home, "opt", "node18")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.version("v18.17.1"), "installation"), link); err != nil {
		f.t.Fatal(err)
	}
	return link
}

// alias links aliases/<name> to a version's installation, as fnm does.
func (f *fnmFixture) alias(name, v string) {
	f.t.Helper()
	if err := os.Symlink(filepath.Join(f.version(v), "installation"), filepath.Join(f.dir, "aliases", name)); err != nil {
		f.t.Fatal(err)
	}
}

// offered runs the query and returns the offered versions' args.
func (f *fnmFixture) offered() []string {
	f.t.Helper()
	items, err := f.n.Unreferenced(context.Background())
	if err != nil {
		f.t.Fatalf("Unreferenced: %v", err)
	}
	var args []string
	for _, it := range items {
		args = append(args, it.Arg)
	}
	return args
}

// keptBy returns the reasons that keep a version.
func (f *fnmFixture) keptBy(v string) []string {
	f.t.Helper()
	versions, err := f.n.survey(context.Background())
	if err != nil {
		f.t.Fatalf("survey: %v", err)
	}
	for _, nv := range versions {
		if nv.name == v {
			return nv.keptBy
		}
	}
	f.t.Fatalf("version %s not surveyed", v)
	return nil
}

func (f *fnmFixture) wantOffered(want ...string) {
	f.t.Helper()
	if got := f.offered(); !slices.Equal(got, want) {
		f.t.Errorf("offered %q, want %q", got, want)
	}
}

// wantError asserts the query fails with an error holding frag and
// offers nothing.
func (f *fnmFixture) wantError(frag string) {
	f.t.Helper()
	items, err := f.n.Unreferenced(context.Background())
	if err == nil || !strings.Contains(err.Error(), frag) {
		f.t.Errorf("err = %v, want one holding %q", err, frag)
	}
	if len(items) > 0 {
		f.t.Errorf("offered %d versions with a source unknown", len(items))
	}
}

// launchdPlist is an XML launchd job running program with args and
// env.
func launchdPlist(program string, args []string, env map[string]string) string {
	esc := func(text string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(text))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.example.job</string>
`)
	if program != "" {
		fmt.Fprintf(&b, "\t<key>Program</key>\n\t<string>%s</string>\n", esc(program))
	}
	if len(args) > 0 {
		b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
		for _, a := range args {
			fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
		}
		b.WriteString("\t</array>\n")
	}
	if len(env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range slices.Sorted(maps.Keys(env)) {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(env[k]))
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func TestFnmVersionsOneItemPerVersion(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1", "v20.10.0")
	// Not named like a release: fnm does not address these by version.
	writeFile(t, filepath.Join(f.dir, "node-versions", "v20", "installation", "bin", "node"), 10)
	writeFile(t, filepath.Join(f.dir, "node-versions", ".downloads", "x"), 10)
	items, err := f.n.Unreferenced(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want one per installed version: %+v", len(items), items)
	}
	for i, v := range []string{"v16.20.2", "v18.17.1", "v20.10.0"} {
		it := items[i]
		if it.Arg != v || it.Label != "Node "+v || it.Path != f.version(v) || it.Bytes < 4096 || it.LastUsed.IsZero() {
			t.Errorf("item %d = %+v, want arg and label %s at %s with its bytes and time", i, it, v, f.version(v))
		}
	}
}

func TestFnmVersionsNumericOrder(t *testing.T) {
	f := newFnmFixture(t, "v9.11.2", "v10.0.0", "v18.2.0", "v18.10.0")
	f.wantOffered("v9.11.2", "v10.0.0", "v18.2.0", "v18.10.0")
}

func TestFnmVersionsNoFnm(t *testing.T) {
	root := t.TempDir()
	n := &fnmVersions{Home: root, Root: root, Getenv: func(string) string { return "" }, HasFnm: func() bool { return false }, w: testWalker()}
	items, err := n.Unreferenced(context.Background())
	if err != nil || len(items) > 0 {
		t.Errorf("no fnm folder: items %v, err %v; want nothing and no error", items, err)
	}
}

func TestFnmVersionsKeptByAlias(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1", "v20.10.0")
	f.alias("default", "v20.10.0")
	f.alias("work", "v18.17.1")
	f.wantOffered("v16.20.2")
	if got := f.keptBy("v20.10.0"); !slices.Contains(got, "the fnm default") {
		t.Errorf("v20.10.0 kept by %q, want the fnm default", got)
	}
	if got := f.keptBy("v18.17.1"); !slices.Contains(got, "fnm alias work") {
		t.Errorf("v18.17.1 kept by %q, want fnm alias work", got)
	}
}

func TestFnmVersionsKeptByVersionFiles(t *testing.T) {
	installed := []string{"v16.20.2", "v18.17.1", "v18.19.0", "v20.10.0", "v22.16.0"}
	cases := []struct {
		name    string
		file    string // relative to home
		content string
		aliases map[string]string
		kept    []string
	}{
		{"full version in a project", "workspace/app/.nvmrc", "v16.20.2\n", nil, []string{"v16.20.2"}},
		{"without the v", "dev/app/.node-version", "16.20.2", nil, []string{"v16.20.2"}},
		{"a major is its newest install", "projects/app/.nvmrc", "18\n", nil, []string{"v18.19.0"}},
		{"major and minor", "src/app/.nvmrc", "20.10", nil, []string{"v20.10.0"}},
		{"an x for the rest", "code/app/.nvmrc", "18.x", nil, []string{"v18.19.0"}},
		{"deep in a monorepo", "workspace/mono/apps/web/.nvmrc", "16", nil, []string{"v16.20.2"}},
		{"in the home folder", ".nvmrc", "v22.16.0", nil, []string{"v22.16.0"}},
		{"comments and a CRLF", "workspace/app/.nvmrc", "# pinned\r\n18.17.1 # for the build\r\n", nil, []string{"v18.17.1"}},
		{"newest by name", "workspace/app/.nvmrc", "node", nil, []string{"v22.16.0"}},
		{"an fnm alias", "workspace/app/.nvmrc", "work", map[string]string{"work": "v18.17.1"}, []string{"v18.17.1"}},
		{"lts through fnm's alias", "workspace/app/.nvmrc", "lts/*", map[string]string{"lts-latest": "v20.10.0"}, []string{"v20.10.0"}},
		{"system names no install", "workspace/app/.nvmrc", "system", nil, nil},
		{"an empty file", "workspace/app/.nvmrc", "\n", nil, nil},
		{"inside node_modules", "workspace/app/node_modules/pkg/.nvmrc", "16", nil, nil},
		{"inside a repository's .git", "workspace/app/.git/modules/lib/.nvmrc", "16", nil, nil},
		{"inside a .Trash", "workspace/.Trash/app/.nvmrc", "16", nil, nil},
		{"in a folder named Library", "workspace/app/Library/tool/.nvmrc", "16", nil, []string{"v16.20.2"}},
		{"outside the project roots", "Documents/app/.nvmrc", "16", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, installed...)
			for name, v := range tc.aliases {
				f.alias(name, v)
			}
			writeText(t, filepath.Join(f.home, tc.file), tc.content)
			var want []string
			for _, v := range installed {
				if !slices.Contains(tc.kept, v) && !slices.Contains(slices.Collect(maps.Values(tc.aliases)), v) {
					want = append(want, v)
				}
			}
			f.wantOffered(want...)
		})
	}
}

func TestFnmVersionsKeptByVersionFilesSaysWhich(t *testing.T) {
	f := newFnmFixture(t, "v18.17.1")
	writeText(t, filepath.Join(f.home, "workspace", "app", ".nvmrc"), "18\n")
	if got := f.keptBy("v18.17.1"); !slices.Contains(got, "~/workspace/app/.nvmrc (18)") {
		t.Errorf("kept by %q, want the file and its spec", got)
	}
}

// symlink makes link, and the folders above it, lead to target.
func (f *fnmFixture) symlink(target, link string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		f.t.Fatal(err)
	}
}

func TestFnmVersionsKeptByVersionFilesBehindLinks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fnmFixture)
	}{
		{"a symlinked project", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "elsewhere", "app", ".nvmrc"), "16")
			f.symlink(filepath.Join(f.home, "elsewhere", "app"), filepath.Join(f.home, "workspace", "app"))
		}},
		{"a symlinked folder inside a project", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "shared", "web", ".node-version"), "16.20.2")
			f.symlink(filepath.Join(f.home, "shared", "web"), filepath.Join(f.home, "workspace", "mono", "apps", "web"))
		}},
		{"a relative link", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "shared", "web", ".nvmrc"), "16")
			f.symlink(filepath.Join("..", "..", "shared", "web"), filepath.Join(f.home, "workspace", "mono", "web"))
		}},
		{"a link to a link", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "disk", "app", ".nvmrc"), "16")
			f.symlink(filepath.Join(f.home, "disk", "app"), filepath.Join(f.home, "opt", "app"))
			f.symlink(filepath.Join(f.home, "opt", "app"), filepath.Join(f.home, "workspace", "app"))
		}},
		{"a symlinked project root", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "disk", "code", "app", ".nvmrc"), "16")
			f.symlink(filepath.Join(f.home, "disk", "code"), filepath.Join(f.home, "code"))
		}},
		{"a symlinked version file", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "shared", "nvmrc"), "16")
			f.symlink(filepath.Join(f.home, "shared", "nvmrc"), filepath.Join(f.home, "workspace", "app", ".nvmrc"))
		}},
		// The link sits at the walk's last depth, so its folder's own
		// folders are only reached from ~/dev, where it is shallow.
		{"a folder first reached deep through a link", func(f *fnmFixture) {
			deep := filepath.Join(append([]string{f.home, "workspace"}, slices.Repeat([]string{"d"}, projectDepth-1)...)...)
			f.symlink(filepath.Join(f.home, "dev", "x"), filepath.Join(deep, "link"))
			writeText(f.t, filepath.Join(f.home, "dev", "x", "y", ".nvmrc"), "16")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			tc.setup(f)
			f.wantOffered("v18.17.1")
		})
	}
}

// A link that leads nowhere only because its target, in the link's own
// folder, is gone hides nothing: a lock a build or an editor left.
func TestFnmVersionsLinksToAGoneSiblingNameNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fnmFixture)
	}{
		{"a compiler's lock, by its absolute path", func(f *fnmFixture) {
			dir := filepath.Join(f.home, "workspace", "app", ".build", "debug", "ModuleCache")
			f.symlink(filepath.Join(dir, "Foo-ABC.swiftmodule.lock-f44a32cd"), filepath.Join(dir, "Foo-ABC.swiftmodule.lock"))
		}},
		{"an editor's lock, by a bare name", func(f *fnmFixture) {
			f.symlink("dev@host.1234:1700000000", filepath.Join(f.home, "workspace", "app", ".#index.js"))
		}},
		{"a lock in a project reached through a link", func(f *fnmFixture) {
			real := filepath.Join(f.home, "elsewhere", "app")
			writeText(f.t, filepath.Join(real, "x"), "")
			f.symlink(filepath.Join(real, "y.lock-1"), filepath.Join(real, "y.lock"))
			f.symlink(real, filepath.Join(f.home, "workspace", "app"))
		}},
		{"a version file", func(f *fnmFixture) {
			f.symlink(".nvmrc-old", filepath.Join(f.home, "workspace", "app", ".nvmrc"))
		}},
		{"the home's version file", func(f *fnmFixture) {
			f.symlink(".nvmrc-old", filepath.Join(f.home, ".nvmrc"))
		}},
		{"a project root", func(f *fnmFixture) {
			f.symlink("code-old", filepath.Join(f.home, "code"))
		}},
		{"a launchd plist", func(f *fnmFixture) {
			f.symlink("dev.example.old.plist", filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"))
		}},
		{"a folder a launchd job names", func(f *fnmFixture) {
			dir := filepath.Join(f.home, "srv", "bot")
			f.symlink("bot-old", dir)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": dir}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			tc.setup(f)
			f.wantOffered("v16.20.2", "v18.17.1")
		})
	}
}

// Links back to a folder the walk is in, or several to one folder,
// read each folder once.
func TestFnmVersionsWalkReadsEachFolderOnce(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	app := filepath.Join(f.home, "workspace", "app")
	shared := filepath.Join(f.home, "shared")
	writeText(t, filepath.Join(shared, "web", ".nvmrc"), "16")
	f.symlink(app, filepath.Join(app, "src", "up"))
	f.symlink(filepath.Dir(app), filepath.Join(app, "src", "root"))
	f.symlink("..", filepath.Join(app, "src", "parent"))
	f.symlink(shared, filepath.Join(app, "a"))
	f.symlink(shared, filepath.Join(app, "b"))
	var mu sync.Mutex
	reads := map[string]int{}
	f.n.w.open = func(path string) (dirHandle, error) {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			mu.Lock()
			reads[real]++
			mu.Unlock()
		}
		return openDir(path)
	}
	f.wantOffered("v18.17.1")
	for dir, n := range reads {
		if n > 1 {
			t.Errorf("%s read %d times, want once", dir, n)
		}
	}
	if reads[filepath.Join(shared, "web")] != 1 {
		t.Errorf("the linked folder was not read: %v", reads)
	}
}

// fnm opens .nvmrc in a folder, which on a volume that ignores case
// finds .NVMRC too.
func TestFnmVersionsVersionFileNamesInAnyCase(t *testing.T) {
	for _, name := range []string{".NVMRC", ".Node-Version", ".node-ver\u017fion"} {
		t.Run(name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			writeText(t, filepath.Join(f.home, "workspace", "app", name), "16")
			f.wantOffered("v18.17.1")
		})
	}
}

func TestFnmVersionsUpperCaseLinkAtTheWalksLastDepth(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	writeText(t, filepath.Join(f.home, "dotfiles", "pin"), "16")
	deep := filepath.Join(append([]string{f.home, "workspace"}, slices.Repeat([]string{"d"}, projectDepth)...)...)
	f.symlink(filepath.Join(f.home, "dotfiles", "pin"), filepath.Join(deep, ".NVMRC"))
	f.wantOffered("v18.17.1")
}

// Through links that lead somewhere, a job's folder and a plist are
// read as what they lead to.
func TestFnmVersionsKeptByLinkedLaunchdJobs(t *testing.T) {
	t.Run("a job's folder", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		disk := filepath.Join(f.home, "disk", "bot")
		writeText(t, filepath.Join(disk, ".nvmrc"), "16")
		dir := filepath.Join(f.home, "srv", "bot")
		f.symlink(disk, dir)
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
			launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": dir}))
		f.wantOffered("v18.17.1")
	})
	t.Run("a plist", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		target := filepath.Join(f.home, "disk", "dev.example.job.plist")
		writeText(t, target, launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "--using=16", "node", "index.js"}, nil))
		f.symlink(target, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"))
		f.wantOffered("v18.17.1")
	})
	t.Run("a job's path with .. after a link", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		vol := filepath.Join(f.home, "disk", "vol")
		if err := os.MkdirAll(filepath.Join(vol, "proj"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeText(t, filepath.Join(vol, "bot", ".nvmrc"), "16")
		link := filepath.Join(f.home, "srv", "proj")
		f.symlink(filepath.Join(vol, "proj"), link)
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
			launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": link + "/../bot"}))
		f.wantOffered("v18.17.1")
	})
	t.Run("a launchd folder", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		target := filepath.Join(f.home, "disk", "LaunchAgents")
		writeText(t, filepath.Join(target, "dev.example.job.plist"),
			launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "--using=16", "node", "index.js"}, nil))
		agents := filepath.Join(f.home, userLaunchdDir)
		if err := os.Remove(agents); err != nil {
			t.Fatal(err)
		}
		f.symlink(target, agents)
		f.wantOffered("v18.17.1")
	})
	// The program is a file, so the version files below it name nothing.
	t.Run("a job naming a program file", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		tool := filepath.Join(f.home, "bin", "tool")
		writeText(t, tool, "#!/bin/sh\n")
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"), launchdPlist(tool, []string{tool, "--flag"}, nil))
		f.wantOffered("v16.20.2", "v18.17.1")
	})
}

// One folder reached through links at several depths, its version file
// where only the shallowest reach sees it. The walk's workers reach the
// links in no set order.
func TestFnmVersionsShallowestReachThroughLinks(t *testing.T) {
	pinned := func(t *testing.T, shallow bool) *fnmFixture {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		x := filepath.Join(f.home, "elsewhere", "x")
		writeText(t, filepath.Join(x, "a", "b", "c", "d", "e", "f", ".nvmrc"), "16")
		for i, n := range []int{8, 6, 4, 3} {
			chain := filepath.Join(append([]string{f.home, "workspace", "deep"}, slices.Repeat([]string{"d"}, n)...)...)
			for j := range 3 {
				f.symlink(x, filepath.Join(chain, fmt.Sprintf("l%d%d", i, j)))
			}
		}
		if shallow {
			f.symlink(x, filepath.Join(f.home, "workspace", "p", "q", "r", "shallow"))
		}
		return f
	}
	t.Run("one reach is shallow enough", func(t *testing.T) {
		pinned(t, true).wantOffered("v18.17.1")
	})
	t.Run("every reach is too deep", func(t *testing.T) {
		pinned(t, false).wantOffered("v16.20.2", "v18.17.1")
	})
}

// The walk's depth and its skipped folders bound which links it
// follows, as they bound the folders it enters.
func TestFnmVersionsLinksPastTheWalk(t *testing.T) {
	cases := []struct {
		name string
		link string // relative to home, leading nowhere
	}{
		{"below the walk's depth", filepath.Join(append([]string{"workspace"}, slices.Repeat([]string{"d"}, projectDepth)...)...) + "/gone"},
		{"inside node_modules", "workspace/app/node_modules/pkg/gone"},
		{"named node_modules", "workspace/app/node_modules"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			f.symlink(filepath.Join(f.home, "nowhere"), filepath.Join(f.home, tc.link))
			f.wantOffered("v16.20.2", "v18.17.1")
		})
	}
}

func TestFnmVersionsKeptByLaunchdJobs(t *testing.T) {
	cases := []struct {
		name  string
		dir   func(f *fnmFixture) string
		plist func(f *fnmFixture) string
	}{
		{"user agent's program", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string { return launchdPlist(f.node("v18.17.1"), nil, nil) }},
		{"user agent's arguments", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("", []string{"/bin/sh", "-c", "exec " + f.node("v18.17.1") + " server.js"}, nil)
			}},
		{"user agent's PATH", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("/usr/bin/env", []string{"/usr/bin/env", "node", "x.js"},
					map[string]string{"PATH": "/usr/bin:" + filepath.Dir(f.node("v18.17.1")) + ":/bin"})
			}},
		{"a NAME=value argument", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("", []string{"/usr/bin/env", "NODE=" + f.node("v18.17.1"), "/opt/tool/run"}, nil)
			}},
		{"spelled from another home", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("$HOME/Library/Application Support/fnm/node-versions/v18.17.1/installation/bin/node", nil, nil)
			}},
		{"through a symlink", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				link := filepath.Join(f.home, "bin", "node18")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					f.t.Fatal(err)
				}
				if err := os.Symlink(f.node("v18.17.1"), link); err != nil {
					f.t.Fatal(err)
				}
				return launchdPlist(link, nil, nil)
			}},
		{"a quoted word in a shell string", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("", []string{"/bin/sh", "-c", "exec '" + f.link() + "/bin/node' /srv/app.js"}, nil)
			}},
		{"a quoted path with a space", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				link := filepath.Join(f.home, "My Tools", "node18")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					f.t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.version("v18.17.1"), "installation"), link); err != nil {
					f.t.Fatal(err)
				}
				return launchdPlist("", []string{"/bin/sh", "-c", `cd /srv && exec "` + link + `/bin/node" app.js`}, nil)
			}},
		{"a quoted PATH in a shell string", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("", []string{"/bin/sh", "-c", `export PATH="` + f.link() + `/bin:$PATH"; exec node /srv/app.js`}, nil)
			}},
		{"a word before a semicolon", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				return launchdPlist("", []string{"/bin/sh", "-c", "cd /srv; " + f.link() + "/bin/node; exit 0"}, nil)
			}},
		{"a link under ~ and $HOME", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				f.link()
				return launchdPlist("", []string{"/bin/sh", "-c", "~/opt/node18/bin/node a.js && ${HOME}/opt/node18/bin/node b.js"}, nil)
			}},
		{"a value after a second =", func(f *fnmFixture) string { return filepath.Join(f.home, userLaunchdDir) },
			func(f *fnmFixture) string {
				link := filepath.Join(f.home, "My Tools", "node18")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					f.t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.version("v18.17.1"), "installation"), link); err != nil {
					f.t.Fatal(err)
				}
				return launchdPlist("", []string{"/bin/sh", "-c", `NODE_OPTIONS="--require=` + link + `/bin/node" exec node /srv/app.js`}, nil)
			}},
		{"system agent", func(f *fnmFixture) string { return filepath.Join(f.root, "/Library/LaunchAgents") },
			func(f *fnmFixture) string { return launchdPlist(f.node("v18.17.1"), nil, nil) }},
		{"system daemon", func(f *fnmFixture) string { return filepath.Join(f.root, "/Library/LaunchDaemons") },
			func(f *fnmFixture) string { return launchdPlist(f.node("v18.17.1"), nil, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			file := filepath.Join(tc.dir(f), "dev.example.job.plist")
			writeText(t, file, tc.plist(f))
			f.wantOffered("v16.20.2")
			want := "launchd job " + tilde(engine.Host{Home: f.home}, file)
			if got := f.keptBy("v18.17.1"); !slices.Contains(got, want) {
				t.Errorf("kept by %q, want %q", got, want)
			}
		})
	}
}

// A job that runs node through fnm names a spec, not a path.
func TestFnmVersionsKeptByFnmJobs(t *testing.T) {
	cases := []struct {
		name  string
		plist func(f *fnmFixture) string
		kept  string
	}{
		{"fnm exec --using=", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "--using=v18.17.1", "node", "/srv/app.js"}, nil)
		}, "v18.17.1"},
		{"--using and its spec apart", func(f *fnmFixture) string {
			return launchdPlist("", []string{"fnm", "exec", "--using", "16", "node", "/srv/app.js"}, nil)
		}, "v16.20.2"},
		{"fnm use in a shell string", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/bin/zsh", "-c", `eval "$(fnm env)"; fnm use 20.10 && node /srv/app.js`}, nil)
		}, "v20.10.0"},
		{"no spec, a version file where it works", func(f *fnmFixture) string {
			dir := filepath.Join(f.home, "srv", "bot")
			writeText(f.t, filepath.Join(dir, ".node-version"), "16\n")
			return launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": dir})
		}, "v16.20.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1", "v20.10.0")
			writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"), tc.plist(f))
			var want []string
			for _, v := range []string{"v16.20.2", "v18.17.1", "v20.10.0"} {
				if v != tc.kept {
					want = append(want, v)
				}
			}
			f.wantOffered(want...)
		})
	}
}

// fnm reads the version file where a job works, in the folders above
// it under a recursive strategy, or the one --using names; a login
// shell runs fnm from the user's rc files without naming it.
func TestFnmVersionsKeptByVersionFilesAJobReads(t *testing.T) {
	bot := func(f *fnmFixture) string {
		dir := filepath.Join(f.home, "srv", "my bot")
		writeText(f.t, filepath.Join(dir, ".nvmrc"), "16\n")
		return dir
	}
	cases := []struct {
		name  string
		plist func(f *fnmFixture) string
	}{
		{"a login shell that cds", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/zsh", "-lic", `cd ~/srv/my\ bot && node index.js`}, nil)
		}},
		{"--using a version file", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "--using=" + filepath.Join(bot(f), ".nvmrc"), "--", "node", "index.js"}, nil)
		}},
		{"a recursive strategy below the file", func(f *fnmFixture) string {
			dir := filepath.Join(bot(f), "server")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				f.t.Fatal(err)
			}
			return launchdPlist("/opt/homebrew/bin/fnm", []string{"fnm", "exec", "--version-file-strategy=recursive", "node", "index.js"},
				map[string]string{"WorkingDirectory": dir})
		}},
		{"a quoted $HOME", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/sh", "-c", `cd "$HOME"/srv/'my bot' && exec node index.js`}, nil)
		}},
		{"${HOME:-…}", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/sh", "-c", `cd "${HOME:-/Users/x}/srv/my bot"; node index.js`}, nil)
		}},
		{"a line continuation", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/zsh", "-c", "cd ~/srv/my\\ bot\\\n&& fnm use && exec node index.js"}, nil)
		}},
		// fnm walks up from the working directory as getcwd returns it,
		// symlinks resolved.
		{"a recursive strategy through a symlinked folder", func(f *fnmFixture) string {
			real := filepath.Join(f.home, "real", "proj")
			if err := os.MkdirAll(real, 0o755); err != nil {
				f.t.Fatal(err)
			}
			writeText(f.t, filepath.Join(f.home, "real", ".nvmrc"), "16\n")
			lnk := filepath.Join(f.home, "lnk")
			if err := os.Symlink(real, lnk); err != nil {
				f.t.Fatal(err)
			}
			return launchdPlist("/opt/homebrew/bin/fnm", []string{"fnm", "exec", "--version-file-strategy=recursive", "node", "x.js"},
				map[string]string{"WorkingDirectory": lnk})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"), tc.plist(f))
			f.wantOffered("v18.17.1")
		})
	}
}

// A command line nested in quotes (tmux, su -c, a second shell), or
// one after an apostrophe in a comment, is one word to a shell reading
// the outer line; what it names must still keep its version.
func TestFnmVersionsKeptByNestedCommandLines(t *testing.T) {
	bot := func(f *fnmFixture) {
		writeText(f.t, filepath.Join(f.home, "srv", "bot", ".nvmrc"), "16\n")
	}
	cases := []struct {
		name  string
		plist func(f *fnmFixture) string
		kept  string
	}{
		{"an fnm spec inside a tmux command", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/bin/zsh", "-c", "tmux new-session -d -s bot 'fnm exec --using=16 node /srv/app.js'"}, nil)
		}, "v16.20.2"},
		{"a cd inside a tmux command", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/zsh", "-c", "tmux new-session -d -s bot 'cd $HOME/srv/bot && fnm use && node index.js'"}, nil)
		}, "v16.20.2"},
		{"a second shell", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/bin/sh", "-c", `exec zsh -lc "fnm use 16 && node /srv/app.js"`}, nil)
		}, "v16.20.2"},
		{"a second shell that cds", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/sh", "-c", `exec zsh -lc 'cd ${HOME}/srv/bot; exec node index.js'`}, nil)
		}, "v16.20.2"},
		{"a link inside a tmux command", func(f *fnmFixture) string {
			return launchdPlist("", []string{"/bin/zsh", "-c", "tmux new-session -d '" + f.link() + "/bin/node /srv/app.js'"}, nil)
		}, "v18.17.1"},
		{"an apostrophe in a comment", func(f *fnmFixture) string {
			bot(f)
			return launchdPlist("", []string{"/bin/zsh", "-c", "# wait for the network, it's flaky at login\nsleep 5\ncd ~/srv/bot && fnm use && exec node index.js"}, nil)
		}, "v16.20.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"), tc.plist(f))
			want := "v16.20.2"
			if tc.kept == want {
				want = "v18.17.1"
			}
			f.wantOffered(want)
		})
	}
}

// Every path any plist names is read for version files. A name the
// system cannot open names no file; a folder the user cannot search is
// a source that refuses.
func TestFnmVersionsJobPathsThatCannotBeOpened(t *testing.T) {
	t.Run("a name longer than a file name can be", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		script := "/usr/local/bin/backup" + strings.Repeat(" --exclude node_modules", 12)
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.backup.plist"),
			launchdPlist("", []string{"/bin/sh", "-c", script}, nil))
		f.wantOffered("v16.20.2", "v18.17.1")
	})
	t.Run("a name holding a NUL", func(t *testing.T) {
		s := &nodeSurvey{w: testWalker()}
		err := s.keepVersionFile(context.Background(), "/srv/a\x00b/.nvmrc", func(string) string { return "" })
		if err != nil {
			t.Errorf("a version file whose name holds a NUL: %v, want nothing named", err)
		}
	})
	t.Run("a shell string past the path limit", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		script := "/usr/bin/true" + strings.Repeat(" && cd /srv/x", 90)
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
			launchdPlist("", []string{"/bin/sh", "-c", script}, nil))
		f.wantOffered("v16.20.2", "v18.17.1")
	})
	// fnm opens a version file relative to its working directory, which
	// works where regrow's open by the whole path does not.
	t.Run("a version file past the path limit, in a project", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		deepVersionFile(t, filepath.Join(f.home, "workspace"), "", "16")
		f.wantError("file name too long")
	})
	// macOS limits a name to 255 UTF-16 units, up to 765 bytes.
	t.Run("a version file past the path limit, below a long non-ASCII name", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		deepVersionFile(t, filepath.Join(f.home, "workspace"), strings.Repeat("日", 86), "16")
		f.wantError("file name too long")
	})
	t.Run("a version file past the path limit, where a job works", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		dir := deepVersionFile(t, filepath.Join(f.home, "srv"), "", "16")
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
			launchdPlist("/opt/homebrew/bin/fnm", []string{"fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": dir}))
		f.wantError("file name too long")
	})
	t.Run("a folder the user cannot search", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2", "v18.17.1")
		dir := filepath.Join(f.root, "private", "var", "db", "vendor")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		chmod(t, dir, 0o600)
		writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.daemon.plist"),
			launchdPlist("/usr/local/bin/vendord", nil, map[string]string{"WorkingDirectory": dir}))
		f.wantError("permission denied")
	})
}

// deepVersionFile makes a folder under parent, through first when it
// is not "", whose path fits the system's path limit while its
// .nvmrc's does not, and writes spec there through a handle on the
// folder.
func deepVersionFile(t *testing.T, parent, first, spec string) string {
	t.Helper()
	dir := filepath.Join(parent, first)
	for len(dir) < 1017 {
		dir = filepath.Join(dir, strings.Repeat("d", min(200, 1020-len(dir)-1)))
	}
	if len(dir) > 1023 || len(dir)+len("/.nvmrc") < 1024 {
		t.Fatalf("folder path is %d bytes, want 1017 to 1023", len(dir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(".nvmrc", []byte(spec+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNamesNoFile(t *testing.T) {
	long := fmt.Errorf("open: %w", syscall.ENAMETOOLONG)
	cases := []struct {
		path string
		want bool
	}{
		{"/srv/" + strings.Repeat("a", 255) + "/.nvmrc", false},
		{"/srv/" + strings.Repeat("a", 256) + "/.nvmrc", true},
		{"/srv/" + strings.Repeat("日", 255) + "/.nvmrc", false},
		{"/srv/" + strings.Repeat("日", 256) + "/.nvmrc", true},
		{"/srv/" + strings.Repeat("😀", 127) + "/.nvmrc", false},
		{"/srv/" + strings.Repeat("😀", 128) + "/.nvmrc", true},
		{"/srv/a\x00b/.nvmrc", true},
	}
	for _, tc := range cases {
		if got := namesNoFile(tc.path, long); got != tc.want {
			t.Errorf("namesNoFile(%d-byte path) = %v, want %v", len(tc.path), got, tc.want)
		}
	}
	if namesNoFile("/srv/"+strings.Repeat("a", 256), errors.New("permission denied")) {
		t.Error("namesNoFile read an unrelated error as a name no file can have")
	}
}

// FNM_DIR may name a folder fnm has not made yet: no versions.
func TestFnmVersionsFnmDirNotMadeYet(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2")
	f.env["FNM_DIR"] = filepath.Join(f.home, "nowhere", "fnm")
	f.wantOffered()
	if err := f.n.recheck(context.Background(), filepath.Join(f.env["FNM_DIR"], "node-versions", "v16.20.2")); err == nil || !strings.Contains(err.Error(), "not an installed Node version") {
		t.Errorf("recheck in an FNM_DIR not made yet: %v, want not an installed Node version", err)
	}
}

func TestShellWords(t *testing.T) {
	home := "/Users/dev"
	cases := []struct {
		line string
		want []string
	}{
		{`cd "$HOME"/srv/bot && node x.js`, []string{"cd", "/Users/dev/srv/bot", "node", "x.js"}},
		{`exec '/opt/my app/node' a;b`, []string{"exec", "/opt/my app/node", "a", "b"}},
		{`cd /srv/my\ app|tee`, []string{"cd", "/srv/my app", "tee"}},
		{`PATH=~/bin:$PATH node`, []string{"PATH=~/bin:$PATH", "node"}},
		{`echo '$HOME' "${HOME}x" ${HOME:+y} ~ ~/a`, []string{"echo", "$HOME", "/Users/devx", "/Users/dev", "/Users/dev", "/Users/dev/a"}},
		{"a\tb(c)`d`", []string{"a", "b", "c", "d"}},
		{"cd /srv/bot\\\n&& \"/srv/x\\\ny\"", []string{"cd", "/srv/bot", "/srv/xy"}},
	}
	for _, tc := range cases {
		if got, err := shellWords(tc.line, home); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("shellWords(%q) = %q, %v; want %q", tc.line, got, err, tc.want)
		}
	}
	if u, err := user.Current(); err == nil {
		if got, err := shellWords("cd ~"+u.Username+"/app", home); err != nil || !slices.Equal(got, []string{"cd", u.HomeDir + "/app"}) {
			t.Errorf("~%s/app read as %q, %v; want %s/app", u.Username, got, err, u.HomeDir)
		}
	}
}

// A macOS short name may hold . and -; a lookup that fails other than
// for an unknown user leaves the path unknown.
func TestShellWordsUserHomes(t *testing.T) {
	lookup := lookupUser
	t.Cleanup(func() { lookupUser = lookup })
	lookupUser = func(name string) (*user.User, error) {
		switch name {
		case "first.last-2":
			return &user.User{Username: name, HomeDir: "/Users/first.last-2"}, nil
		case "broken":
			return nil, errors.New("directory service unavailable")
		}
		return nil, user.UnknownUserError(name)
	}
	if got, err := shellWords("cd ~first.last-2/app ~nobody/x", "/Users/dev"); err != nil || !slices.Equal(got, []string{"cd", "/Users/first.last-2/app", "~nobody/x"}) {
		t.Errorf("shellWords = %q, %v; want ~first.last-2 read as its home, ~nobody left as written", got, err)
	}
	if _, err := shellWords("cd ~broken/app", "/Users/dev"); err == nil {
		t.Error("a failed lookup of ~broken read as no such user")
	}
	f := newFnmFixture(t, "v16.20.2")
	writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
		launchdPlist("", []string{"/bin/sh", "-c", "cd ~broken/app && node x.js"}, nil))
	f.wantError("directory service unavailable")
}

// Without fnm a bare number is a port or a count, not a version.
func TestFnmVersionsSpecWordsNeedFnm(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	writeText(t, filepath.Join(f.home, userLaunchdDir, "a.plist"), launchdPlist("/usr/local/bin/server", []string{"--port", "18", "--workers", "16"}, nil))
	f.wantOffered("v16.20.2", "v18.17.1")
}

func TestFnmVersionsKeptByBinaryAndXMLPlists(t *testing.T) {
	for _, name := range []string{"job.xml.plist", "job.bplist"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "plist", name))
			if err != nil {
				t.Fatal(err)
			}
			// The fixture's PATH names v22.16.0 under another home.
			f := newFnmFixture(t, "v22.16.0", "v22.1.0")
			writeText(t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"), string(data))
			f.wantOffered("v22.1.0")
		})
	}
}

func TestFnmVersionsLaunchdNamesOnlyWholeVersions(t *testing.T) {
	f := newFnmFixture(t, "v18.17.1")
	writeText(t, filepath.Join(f.home, userLaunchdDir, "a.plist"),
		launchdPlist("/opt/x/node-versions/v18.17.10/installation/bin/node", []string{"/x/node-versions/v18.17.1-rc/bin/node"}, nil))
	writeText(t, filepath.Join(f.home, userLaunchdDir, "notes.txt"), "not a plist: "+f.node("v18.17.1"))
	f.wantOffered("v18.17.1")
}

func TestFnmVersionsKeptByRunningProcess(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1", "v18.17.10")
	f.mapped = []string{"/usr/lib/dyld", f.node("v18.17.1"), f.dir + "/node-versions/v18.17.1x/bin/node"}
	f.wantOffered("v16.20.2", "v18.17.10")
	if got := f.keptBy("v18.17.1"); !slices.Contains(got, "a running process uses "+f.node("v18.17.1")) {
		t.Errorf("kept by %q, want the running process", got)
	}
}

// lsof prints a path the way the filesystem stores it; FNM_DIR may
// spell the same folder in other letters.
func TestFnmVersionsRunningProcessMatchesAnyCase(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	lower := strings.ToLower(f.dir)
	if _, err := os.Stat(lower); err != nil {
		t.Skipf("the filesystem tells letter case apart: %v", err)
	}
	f.env["FNM_DIR"] = lower
	f.mapped = []string{f.node("v18.17.1")}
	f.wantOffered("v16.20.2")
}

func TestFnmVersionsUnknownSourceOffersNone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fnmFixture)
		frag  string
	}{
		{"a plist that does not parse", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "bad.plist"), "<plist><dict><key>x</key>")
		}, "bad.plist"},
		{"a binary plist cut short", func(f *fnmFixture) {
			data, err := os.ReadFile(filepath.Join("testdata", "plist", "job.bplist"))
			if err != nil {
				f.t.Fatal(err)
			}
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "cut.plist"), string(data[:len(data)-40]))
		}, "cut.plist"},
		{"a plist too large for a job", func(f *fnmFixture) {
			writeFile(f.t, filepath.Join(f.root, "/Library/LaunchDaemons", "big.plist"), maxSourceFile+1)
		}, "larger than"},
		{"a plist that cannot be read", func(f *fnmFixture) {
			p := filepath.Join(f.home, userLaunchdDir, "locked.plist")
			writeText(f.t, p, launchdPlist("/bin/true", nil, nil))
			chmod(f.t, p, 0)
		}, "locked.plist"},
		{"a launchd folder that cannot be read", func(f *fnmFixture) {
			chmod(f.t, filepath.Join(f.root, "/Library/LaunchAgents"), 0)
		}, "LaunchAgents is not fully readable"},
		{"a version file naming two versions", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "workspace", "app", ".nvmrc"), "16\n18\n")
		}, "more than one version"},
		{"a version file naming no version or alias", func(f *fnmFixture) {
			writeText(f.t, filepath.Join(f.home, "workspace", "app", ".node-version"), ">=18")
		}, "neither a version number nor an fnm alias"},
		{"a version file that cannot be read", func(f *fnmFixture) {
			p := filepath.Join(f.home, "workspace", "app", ".nvmrc")
			writeText(f.t, p, "16")
			chmod(f.t, p, 0)
		}, ".nvmrc"},
		{"a project folder that cannot be read", func(f *fnmFixture) {
			p := filepath.Join(f.home, "workspace", "locked")
			writeText(f.t, filepath.Join(p, "x"), "")
			chmod(f.t, p, 0)
		}, "cannot be ruled out"},
		{"a folder deep in a project that cannot be read", func(f *fnmFixture) {
			p := filepath.Join(f.home, "workspace", "app", "packages", "locked")
			writeText(f.t, filepath.Join(p, "x"), "")
			chmod(f.t, p, 0)
		}, "cannot be ruled out"},
		{"a link that leads nowhere", func(f *fnmFixture) {
			f.symlink(filepath.Join(f.home, "gone", "app"), filepath.Join(f.home, "workspace", "app"))
		}, "~/workspace/app is a link that leads nowhere"},
		{"a link to a version file that is not there", func(f *fnmFixture) {
			f.symlink(filepath.Join(f.home, "gone", ".nvmrc"), filepath.Join(f.home, "workspace", "app", ".nvmrc"))
		}, "~/workspace/app/.nvmrc is a link that leads nowhere"},
		{"a link to a version file that is not there, in the walk's deepest folders", func(f *fnmFixture) {
			deep := filepath.Join(append([]string{f.home, "workspace"}, slices.Repeat([]string{"d"}, projectDepth)...)...)
			f.symlink(filepath.Join(f.home, "gone", ".nvmrc"), filepath.Join(deep, ".nvmrc"))
		}, "/.nvmrc is a link that leads nowhere"},
		{"a project root that leads nowhere", func(f *fnmFixture) {
			f.symlink(filepath.Join(f.home, "disk", "code"), filepath.Join(f.home, "code"))
		}, "~/code is a link that leads nowhere"},
		{"a link into a folder the user cannot search", func(f *fnmFixture) {
			locked := filepath.Join(f.home, "locked")
			writeText(f.t, filepath.Join(locked, "app", ".nvmrc"), "16")
			f.symlink(filepath.Join(locked, "app"), filepath.Join(f.home, "workspace", "app"))
			chmod(f.t, locked, 0)
		}, "~/workspace/app is a link that cannot be followed"},
		{"a link to itself", func(f *fnmFixture) {
			f.symlink("loop", filepath.Join(f.home, "workspace", "loop"))
		}, "~/workspace/loop is a link that cannot be followed"},
		{"a home version file that is a link leading nowhere", func(f *fnmFixture) {
			f.symlink(filepath.Join(f.home, "dotfiles", ".nvmrc"), filepath.Join(f.home, ".nvmrc"))
		}, "~/.nvmrc is a link that leads nowhere"},
		{"a version file a job reads that is a link leading nowhere", func(f *fnmFixture) {
			dir := filepath.Join(f.home, "srv", "bot")
			f.symlink(filepath.Join(f.home, "dotfiles", "bot.node-version"), filepath.Join(dir, ".node-version"))
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": dir}))
		}, "~/srv/bot/.node-version is a link that leads nowhere"},
		{"a launchd job's folder that is a link leading nowhere", func(f *fnmFixture) {
			dir := filepath.Join(f.home, "srv", "bot")
			f.symlink(filepath.Join(f.home, "disk", "bot"), dir)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": filepath.Join(dir, "app")}))
		}, "~/srv/bot is a link that leads nowhere"},
		{"a launchd plist that is a link leading nowhere", func(f *fnmFixture) {
			f.symlink(filepath.Join(f.home, "disk", "dev.example.job.plist"), filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"))
		}, "dev.example.job.plist is a link that leads nowhere"},
		// The kernel takes .. after the link before it, where a walk up by
		// the path's text does not.
		{"a launchd job's path with .. after a link leading nowhere", func(f *fnmFixture) {
			link := filepath.Join(f.home, "srv", "proj")
			f.symlink(filepath.Join(f.home, "disk", "vol", "proj"), link)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": link + "/../bot"}))
		}, "~/srv/proj is a link that leads nowhere"},
		{"a launchd job's path through a link leading nowhere, with a trailing slash", func(f *fnmFixture) {
			link := filepath.Join(f.home, "srv", "bot")
			f.symlink(filepath.Join(f.home, "disk", "bot"), link)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": link + "/"}))
		}, "~/srv/bot is a link that leads nowhere"},
		{"a log path a plist names through a link leading nowhere", func(f *fnmFixture) {
			logs := filepath.Join(f.home, "logs")
			f.symlink(filepath.Join(f.home, "old", "logs"), logs)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("/usr/bin/true", nil, map[string]string{"StandardOutPath": filepath.Join(logs, "job.log")}))
		}, "~/logs is a link that leads nowhere"},
		{"a launchd folder that is a link leading nowhere", func(f *fnmFixture) {
			agents := filepath.Join(f.home, userLaunchdDir)
			if err := os.Remove(agents); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(filepath.Join(f.home, "disk", "LaunchAgents"), agents)
		}, "~/Library/LaunchAgents is a link that leads nowhere"},
		{"a launchd plist that is a link to a link leading nowhere", func(f *fnmFixture) {
			mid := filepath.Join(f.home, "dotfiles", "job.plist")
			f.symlink(filepath.Join(f.home, "disk", "job.plist"), mid)
			f.symlink(mid, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"))
		}, "dev.example.job.plist is a link that leads nowhere"},
		{"a launchd plist that is a link through a file", func(f *fnmFixture) {
			file := filepath.Join(f.home, "dotfiles", "notadir")
			writeText(f.t, file, "x")
			f.symlink(filepath.Join(file, "job.plist"), filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"))
		}, "dev.example.job.plist is a link that leads nowhere"},
		{"a launchd job's folder that is a loop of links", func(f *fnmFixture) {
			a, b := filepath.Join(f.home, "srv", "a"), filepath.Join(f.home, "srv", "b")
			f.symlink(b, a)
			f.symlink(a, b)
			writeText(f.t, filepath.Join(f.home, userLaunchdDir, "dev.example.job.plist"),
				launchdPlist("", []string{"/opt/homebrew/bin/fnm", "exec", "node", "index.js"}, map[string]string{"WorkingDirectory": filepath.Join(a, "app")}))
		}, "too many"},
		{"a link to a sibling link that leads nowhere", func(f *fnmFixture) {
			app := filepath.Join(f.home, "workspace", "app")
			f.symlink(filepath.Join(f.home, "disk", "app"), filepath.Join(app, "b"))
			f.symlink("b", filepath.Join(app, "a"))
		}, "~/workspace/app/a is a link that leads nowhere"},
		{"a link to a gone name in a folder below its own", func(f *fnmFixture) {
			app := filepath.Join(f.home, "workspace", "app")
			f.symlink(filepath.Join("sub", "x"), filepath.Join(app, "x.lock"))
		}, "~/workspace/app/x.lock is a link that leads nowhere"},
		{"a link to a gone name in the folder above its own", func(f *fnmFixture) {
			app := filepath.Join(f.home, "workspace", "app")
			f.symlink(filepath.Join("..", "x"), filepath.Join(app, "x.lock"))
		}, "~/workspace/app/x.lock is a link that leads nowhere"},
		{"a symlinked folder that cannot be read", func(f *fnmFixture) {
			locked := filepath.Join(f.home, "shared", "locked")
			writeText(f.t, filepath.Join(locked, "x"), "")
			f.symlink(locked, filepath.Join(f.home, "workspace", "app"))
			chmod(f.t, locked, 0)
		}, "cannot be ruled out"},
		{"the aliases folder cannot be read", func(f *fnmFixture) {
			chmod(f.t, filepath.Join(f.dir, "aliases"), 0)
		}, "aliases is not fully readable"},
		{"the process list fails", func(f *fnmFixture) {
			f.n.Mapped = func(context.Context) ([]string, error) { return nil, errors.New("lsof: exit status 1") }
		}, "lsof: exit status 1"},
		{"fnm is not on PATH", func(f *fnmFixture) {
			f.n.HasFnm = func() bool { return false }
		}, "fnm is not on PATH"},
		{"two fnm folders and no FNM_DIR", func(f *fnmFixture) {
			if err := os.MkdirAll(filepath.Join(f.home, ".fnm"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, "set FNM_DIR"},
		{"a relative FNM_DIR", func(f *fnmFixture) {
			f.env["FNM_DIR"] = "fnm"
		}, "not an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFnmFixture(t, "v16.20.2", "v18.17.1")
			tc.setup(f)
			f.wantError(tc.frag)
		})
	}
}

// Under sudo lsof lists root's processes while the home, FNM_DIR or
// XDG_DATA_HOME may still lead to the user's versions.
func TestFnmVersionsAnotherUsersFoldersOfferNone(t *testing.T) {
	t.Run("another user's home", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2")
		f.n.Getuid = func() int { return os.Getuid() + 1 }
		f.wantError("another user")
		if err := f.n.recheck(context.Background(), f.version("v16.20.2")); err == nil {
			t.Error("recheck passed for another user's home")
		}
	})
	t.Run("root", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2")
		f.n.Getuid = func() int { return 0 }
		f.wantError("another user")
	})
	t.Run("another user's fnm folder", func(t *testing.T) {
		f := newFnmFixture(t, "v16.20.2")
		f.env["FNM_DIR"] = "/usr"
		f.wantError("another user")
	})
}

func TestFnmVersionsUnprintableFolderOffersNone(t *testing.T) {
	f := newFnmFixture(t)
	dir := filepath.Join(f.home, "fnm\u200dx")
	writeFile(t, filepath.Join(dir, "node-versions", "v18.17.1", "installation", "bin", "node"), 10)
	if err := os.MkdirAll(filepath.Join(dir, "aliases"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env["FNM_DIR"] = dir
	f.wantError("lsof would not print")
}

func TestFnmDirLocations(t *testing.T) {
	cases := []struct {
		name string
		dir  string // relative to home
		env  func(home string) map[string]string
	}{
		{"FNM_DIR", "elsewhere/fnm", func(home string) map[string]string {
			return map[string]string{"FNM_DIR": filepath.Join(home, "elsewhere", "fnm")}
		}},
		{"XDG_DATA_HOME", "xdg/fnm", func(home string) map[string]string {
			return map[string]string{"XDG_DATA_HOME": filepath.Join(home, "xdg")}
		}},
		{"the XDG default", ".local/share/fnm", func(string) map[string]string { return nil }},
		{"the legacy folder", ".fnm", func(string) map[string]string { return nil }},
		{"the macOS default", "Library/Application Support/fnm", func(string) map[string]string { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, tc.dir)
			writeFile(t, filepath.Join(dir, "node-versions", "v18.17.1", "installation", "bin", "node"), 10)
			env := tc.env(home)
			n := &fnmVersions{Home: home, Root: home, Getenv: func(k string) string { return env[k] }, HasFnm: func() bool { return true },
				Mapped: func(context.Context) ([]string, error) { return nil, nil }, w: testWalker()}
			items, err := n.Unreferenced(context.Background())
			if err != nil || len(items) != 1 || items[0].Path != filepath.Join(dir, "node-versions", "v18.17.1") {
				t.Errorf("items %+v, err %v; want the one version in %s", items, err, dir)
			}
		})
	}
}

func TestFnmVersionRecheck(t *testing.T) {
	ctx := context.Background()
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	if err := f.n.recheck(ctx, f.version("v16.20.2")); err != nil {
		t.Fatalf("recheck of an unreferenced version: %v", err)
	}
	writeText(t, filepath.Join(f.home, "workspace", "app", ".nvmrc"), "16")
	if err := f.n.recheck(ctx, f.version("v16.20.2")); err == nil || !strings.Contains(err.Error(), "referenced now") {
		t.Errorf("recheck after a project pinned it: %v, want referenced now", err)
	}
	f.mapped = []string{f.node("v18.17.1")}
	if err := f.n.recheck(ctx, f.version("v18.17.1")); err == nil {
		t.Error("recheck passed a version a process runs from now")
	}
	if err := f.n.recheck(ctx, f.version("v20.0.0")); err == nil || !strings.Contains(err.Error(), "not an installed Node version") {
		t.Errorf("recheck of a version not installed: %v", err)
	}
	f.n.Mapped = func(context.Context) ([]string, error) { return nil, errors.New("lsof failed") }
	if err := f.n.recheck(ctx, f.version("v18.17.1")); err == nil {
		t.Error("recheck passed with the process list unknown")
	}
}

type fakeDirInfo struct{ st *syscall.Stat_t }

func (fakeDirInfo) Name() string       { return "d" }
func (fakeDirInfo) Size() int64        { return 0 }
func (fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (fakeDirInfo) IsDir() bool        { return true }
func (f fakeDirInfo) Sys() any         { return f.st }

func TestFolderIDOfInodeZero(t *testing.T) {
	if _, ok := folderIDOf(fakeDirInfo{&syscall.Stat_t{Dev: 1, Ino: 0}}); ok {
		t.Error("inode 0 read as an identity")
	}
	if _, ok := folderIDOf(fakeDirInfo{&syscall.Stat_t{Dev: 1, Ino: 7}}); !ok {
		t.Error("inode 7 read as no identity")
	}
}

func TestFnmVersionRecheckThroughLinks(t *testing.T) {
	ctx := context.Background()
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	writeText(t, filepath.Join(f.home, "elsewhere", "app", ".nvmrc"), "16")
	f.symlink(filepath.Join(f.home, "elsewhere", "app"), filepath.Join(f.home, "workspace", "app"))
	if err := f.n.recheck(ctx, f.version("v16.20.2")); err == nil || !strings.Contains(err.Error(), "referenced now") {
		t.Errorf("recheck after a symlinked project pinned it: %v, want referenced now", err)
	}
	f.symlink(filepath.Join(f.home, "gone"), filepath.Join(f.home, "workspace", "dead"))
	if err := f.n.recheck(ctx, f.version("v18.17.1")); err == nil || !strings.Contains(err.Error(), "leads nowhere") {
		t.Errorf("recheck with a dead link under the roots: %v, want leads nowhere", err)
	}
}

func TestMappedNames(t *testing.T) {
	out := "p100\nftxt\nn/usr/lib/dyld\np4242\nftxt\nn/opt/x/bin/node\n"
	names, err := mappedNames(out, 4242)
	if err != nil || !slices.Equal(names, []string{"/usr/lib/dyld", "/opt/x/bin/node"}) {
		t.Errorf("names %q, err %v", names, err)
	}
	for _, self := range []int{424, 1} {
		if _, err := mappedNames(out, self); err == nil {
			t.Errorf("a listing without pid %d passed as the user's processes", self)
		}
	}
}

// An app whose path lsof escapes leaves the other names usable.
func TestFnmVersionsEscapedProcessNameElsewhere(t *testing.T) {
	f := newFnmFixture(t, "v16.20.2", "v18.17.1")
	names, err := mappedNames("p7\nftxt\nn/Applications/A\\xe2\\x80\\x8dB.app/Contents/MacOS/a\nftxt\nn"+f.node("v18.17.1")+"\n", 7)
	if err != nil {
		t.Fatal(err)
	}
	f.mapped = names
	f.wantOffered("v16.20.2")
}

// Removal is fnm's own uninstall of the one version, after the recheck.
func TestFnmNodeVersionsRule(t *testing.T) {
	r := catalogRule(t, "fnm-node-versions")
	if r.Risk != engine.RiskCaution || !slices.Equal(r.NativeCommand, engine.Argv{"fnm", "uninstall", "{arg}"}) ||
		r.PreAction != engine.PreActionFnmVersionRecheck || !r.Beta {
		t.Errorf("risk %q, command %q, pre-action %q, beta %v", r.Risk, r.NativeCommand, r.PreAction, r.Beta)
	}
	if !strings.Contains(r.Regen.Story, "fnm install") || !strings.Contains(r.Regen.Cost, "network") {
		t.Errorf("regen %+v must name the install command and the network", r.Regen)
	}
}
