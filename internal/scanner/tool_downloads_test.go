package scanner

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// downloadsFixture is a fake machine: a root that re-anchors
// /Applications, a home with ~/Downloads, and a clock 30 days past the
// moment the fixture was made, so every file it writes is old unless a
// case stamps it recent. installed records each app's date added.
type downloadsFixture struct {
	t         *testing.T
	host      engine.Host
	made      time.Time
	now       time.Time
	installed map[string]time.Time
	d         *downloadedInstallers
}

func newDownloadsFixture(t *testing.T) *downloadsFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &downloadsFixture{
		t:         t,
		host:      engine.Host{OS: "darwin", Home: filepath.Join(root, "Users", "dev"), Root: root},
		made:      time.Now(),
		installed: map[string]time.Time{},
	}
	f.now = f.made.Add(30 * 24 * time.Hour)
	if err := os.MkdirAll(f.downloads(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.host.ExpandPath("/Applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.d = &downloadedInstallers{
		w:     testWalker(),
		host:  f.host,
		quiet: downloadsQuiet,
		now:   func() time.Time { return f.now },
		added: func(path string) (time.Time, error) {
			if at, ok := f.installed[path]; ok {
				return at, nil
			}
			return time.Time{}, errors.New("no date added in the fixture")
		},
		plist: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("no plutil in the fixture") },
	}
	return f
}

func (f *downloadsFixture) downloads(name ...string) string {
	return filepath.Join(append([]string{f.host.Home, "Downloads"}, name...)...)
}

func infoPlist(id, version string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDocumentTypes</key>
	<array><dict><key>CFBundleTypeName</key><string>not the id</string></dict></array>
	<key>CFBundleIdentifier</key>
	<string>%s</string>
	<key>LSRequiresNativeExecution</key>
	<true/>
	<key>CFBundleShortVersionString</key>
	<string>%s</string>
</dict>
</plist>
`, id, version)
}

// app installs an app bundle at a rule path (/Applications/Foo.app),
// put in place at added; a zero added leaves its date added unknown.
func (f *downloadsFixture) app(path, id, version string, added time.Time) string {
	f.t.Helper()
	p := f.host.ExpandPath(path)
	if err := os.MkdirAll(filepath.Join(p, "Contents"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "Contents", "Info.plist"), []byte(infoPlist(id, version)), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if !added.IsZero() {
		f.installed[p] = added
	}
	return p
}

func (f *downloadsFixture) file(name string) string {
	f.t.Helper()
	p := f.downloads(name)
	writeFile(f.t, p, 4096)
	return p
}

// zipFile writes a zip into ~/Downloads holding the given entries.
func (f *downloadsFixture) zipFile(name string, entries map[string]string) string {
	f.t.Helper()
	p := f.downloads(name)
	out, err := os.Create(p)
	if err != nil {
		f.t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for entry, content := range entries {
		w, err := zw.Create(entry)
		if err != nil {
			f.t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func appZip(app, id, version string) map[string]string {
	return map[string]string{
		app + "/":                           "",
		app + "/Contents/Info.plist":        infoPlist(id, version),
		app + "/Contents/MacOS/" + app:      "binary",
		"__MACOSX/" + app + "/._Info.plist": "resource fork",
	}
}

func (f *downloadsFixture) scan() (offered map[string]string, kept map[string]string) {
	f.t.Helper()
	items, keptOut, err := f.d.scan(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	offered = map[string]string{}
	for _, it := range items {
		if it.Path == "" || it.Bytes <= 0 || it.LastUsed.IsZero() || it.Partial {
			f.t.Errorf("%s: want a complete measurement with a path, got %+v", it.Label, it)
		}
		offered[filepath.Base(it.Path)] = it.Label
	}
	kept = map[string]string{}
	for _, k := range keptOut {
		kept[filepath.Base(k.path)] = k.why
	}
	return offered, kept
}

func TestDownloadedInstallers(t *testing.T) {
	f := newDownloadsFixture(t)
	after, before := f.made.Add(24*time.Hour), f.made.Add(-24*time.Hour)
	f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", after)
	f.app("/Applications/Slack.app", "com.tinyspeck.slackmacgap", "4.41.105", after)
	f.app("/Applications/ChatGPT.app", "com.openai.chat", "1.2025.1", after)
	f.app("/Applications/Figma.app", "com.figma.Desktop", "125.0", before)
	f.app("/Applications/Recent.app", "com.example.recent", "1.0", after)
	f.app("/Applications/NoDate.app", "com.example.nodate", "1.0", time.Time{})
	f.app("/Applications/Utilities/Tool.app", "com.example.tool", "1.0", after)
	f.app("~/Applications/Foo.app", "com.example.foo", "1.2.0", before)
	f.app("/Applications/Bar.app", "com.example.bar", "1.0", after)
	f.app("/Applications/Baz.app", "com.example.baz", "1.0", before)
	f.app("/Applications/Auto.app", "com.example.auto", "4.1", after)
	f.app("/Applications/Affinity Photo.app", "com.example.photo", "1.0", after)
	f.app("/Applications/MyApp.app", "com.example.myapp", "1.0", after)

	for _, name := range []string{"Docker.dmg", "Slack-4.41.105-macOS.dmg", "Slack-4.42.0-macOS.dmg", "ChatGPTInstaller.dmg", "Tool (1).DMG", "Figma.pkg",
		"Minecraft.dmg", "1.0-arm64.dmg", "Recent.dmg", "NoDate.dmg", "notes.txt", "corrupt.zip",
		"Auto.4.2.dmg", "Auto-4.1.dmg", "Affinity Photo 2.dmg"} {
		f.file(name)
	}
	recent := f.now.Add(-24 * time.Hour)
	if err := os.Chtimes(f.downloads("Recent.dmg"), recent, recent); err != nil {
		t.Fatal(err)
	}
	// A folder or a symlink with an installer's name is no download.
	if err := os.MkdirAll(f.downloads("Folder.dmg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.downloads("Docker.dmg"), f.downloads("Link.dmg")); err != nil {
		t.Fatal(err)
	}
	// A zip is matched by the app it holds, whatever its name; its
	// version, when comparable, decides over the times.
	f.zipFile("foo-same.zip", appZip("Foo.app", "com.example.foo", "1.2"))
	f.zipFile("foo-next.zip", appZip("Foo.app", "COM.EXAMPLE.FOO", "1.3.0"))
	f.zipFile("bar-beta.zip", appZip("Bar.app", "com.example.bar", "1.0 beta"))
	f.zipFile("baz-beta.zip", appZip("Baz.app", "com.example.baz", "1.0 beta"))
	f.zipFile("photos.zip", map[string]string{"a.jpg": "jpeg", "Docker.app.txt": "text"})
	two := appZip("One.app", "com.example.one", "1.0")
	for k, v := range appZip("Two.app", "com.example.two", "1.0") {
		two[k] = v
	}
	f.zipFile("two.zip", two)
	f.zipFile("other.zip", appZip("Other.app", "com.example.other", "1.0"))
	f.zipFile("noplist.zip", map[string]string{"Odd.app/Contents/MacOS/Odd": "binary"})
	build := appZip("MyApp.app", "com.example.myapp", "1.0")
	build["MyApp.app.dSYM/Contents/Info.plist"] = "symbols"
	build["notes.txt"] = "release notes"
	f.zipFile("myapp-build-42.zip", build)

	offered, kept := f.scan()
	wantOffered := map[string]string{
		"Docker.dmg":               "Docker.dmg · Docker.app",
		"Slack-4.41.105-macOS.dmg": "Slack-4.41.105-macOS.dmg · Slack.app",
		"ChatGPTInstaller.dmg":     "ChatGPTInstaller.dmg · ChatGPT.app",
		"Tool (1).DMG":             "Tool (1).DMG · Tool.app",
		"foo-same.zip":             "foo-same.zip · Foo.app",
		"bar-beta.zip":             "bar-beta.zip · Bar.app",
		"Auto-4.1.dmg":             "Auto-4.1.dmg · Auto.app",
	}
	if !reflect.DeepEqual(offered, wantOffered) {
		t.Errorf("offered:\n%v\nwant:\n%v", offered, wantOffered)
	}
	apps := f.host.ExpandPath("/Applications")
	wantKept := map[string]string{
		"Figma.pkg":              "it arrived after " + filepath.Join(apps, "Figma.app") + " was installed",
		"Minecraft.dmg":          "no installed app has its name",
		"Slack-4.42.0-macOS.dmg": "its name's version is newer than " + filepath.Join(apps, "Slack.app") + " (4.42.0 over 4.41.105)",
		"1.0-arm64.dmg":          "its name names no app",
		"Recent.dmg":             "it changed in the last 14 days",
		"NoDate.dmg":             "when " + filepath.Join(apps, "NoDate.app") + " was installed cannot be read",
		"corrupt.zip":            "it cannot be read as a zip archive",
		"foo-next.zip":           "its app is newer than " + f.host.ExpandPath("~/Applications/Foo.app") + " (1.3.0 over 1.2.0)",
		"baz-beta.zip":           "it arrived after " + filepath.Join(apps, "Baz.app") + " was installed",
		"photos.zip":             "it holds no app at its top level",
		"two.zip":                "it holds more than one app",
		"other.zip":              "no installed app is com.example.other",
		"noplist.zip":            "its app's Info.plist cannot be read",
		"Auto.4.2.dmg":           "its name's version is newer than " + filepath.Join(apps, "Auto.app") + " (4.2 over 4.1)",
		// A lone number names another app.
		"Affinity Photo 2.dmg": "no installed app has its name",
		"myapp-build-42.zip":   "it holds more than the app",
	}
	if !reflect.DeepEqual(kept, wantKept) {
		t.Errorf("kept:\n%v\nwant:\n%v", kept, wantKept)
	}
}

// Every installed copy must be older than the download.
func TestDownloadedInstallersEveryCopyCounts(t *testing.T) {
	f := newDownloadsFixture(t)
	f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", f.made.Add(24*time.Hour))
	old := f.app("~/Applications/Docker.app", "com.docker.docker", "4.30.0", f.made.Add(-24*time.Hour))
	f.file("Docker.dmg")
	offered, kept := f.scan()
	if len(offered) != 0 || kept["Docker.dmg"] != "it arrived after "+old+" was installed" {
		t.Fatalf("offered %v, kept %v; want Docker.dmg kept for the older copy", offered, kept)
	}
}

// The download's own date added counts as a time it changed, and a
// download that cannot be stat'd is kept.
func TestDownloadedInstallersDownloadTimes(t *testing.T) {
	f := newDownloadsFixture(t)
	f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", f.made.Add(24*time.Hour))
	f.app("/Applications/Slack.app", "com.tinyspeck.slackmacgap", "4.41.105", f.made.Add(24*time.Hour))
	f.file("Slack.dmg")
	f.installed[f.file("Docker.dmg")] = f.now.Add(-24 * time.Hour)
	offered, kept := f.scan()
	if len(offered) != 1 || offered["Slack.dmg"] == "" || kept["Docker.dmg"] != "it changed in the last 14 days" {
		t.Fatalf("offered %v, kept %v; want Docker.dmg kept for its date added", offered, kept)
	}
	// Listing needs read, a stat needs search.
	if err := os.Chmod(f.downloads(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.downloads(), 0o755) })
	offered, kept = f.scan()
	if len(offered) != 0 || kept["Slack.dmg"] != "it cannot be read" {
		t.Fatalf("offered %v, kept %v; want Slack.dmg kept as unreadable", offered, kept)
	}
}

// An installed app whose Info.plist cannot be read keeps what it could
// be the copy of: a disk image whose name spells a version, a zip of
// an app by its name.
func TestDownloadedInstallersUnreadableInstalledPlist(t *testing.T) {
	f := newDownloadsFixture(t)
	slack := f.app("/Applications/Slack.app", "com.tinyspeck.slackmacgap", "4.41.105", f.made.Add(24*time.Hour))
	if err := os.WriteFile(filepath.Join(slack, "Contents", "Info.plist"), []byte(`<plist><dict><key>CFBundleName</key><string>Slack</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.file("Slack-4.50.0-macOS.dmg")
	f.app("/Applications/Foo.app", "com.example.foo", "1.2", f.made.Add(-24*time.Hour))
	other := f.app("~/Applications/Foo.app", "com.example.foo", "9.0", f.made.Add(24*time.Hour))
	if err := os.WriteFile(filepath.Join(other, "Contents", "Info.plist"), []byte("bplist00garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.zipFile("foo.zip", appZip("Foo.app", "com.example.foo", "1.0"))
	offered, kept := f.scan()
	want := map[string]string{
		"Slack-4.50.0-macOS.dmg": "the Info.plist of " + slack + " cannot be read, so its version cannot be compared with 4.50.0",
		"foo.zip":                "the Info.plist of " + other + " cannot be read",
	}
	if len(offered) != 0 || !reflect.DeepEqual(kept, want) {
		t.Fatalf("offered %v, kept %v; want %v", offered, kept, want)
	}
}

// App folders are read through symlinks; one that cannot be read makes
// the listing incomplete, and a dangling one is no app.
func TestDownloadedInstallersSymlinkedApps(t *testing.T) {
	f := newDownloadsFixture(t)
	after := f.made.Add(24 * time.Hour)
	f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", after)
	tool := f.app("~/Linked/Tool.app", "com.example.tool", "1.0", after)
	if err := os.Symlink(filepath.Dir(tool), f.host.ExpandPath("/Applications/Linked")); err != nil {
		t.Fatal(err)
	}
	// getattrlist follows the link; the fixture's reader goes by path.
	f.installed[f.host.ExpandPath("/Applications/Linked/Tool.app")] = after
	if err := os.Symlink(f.host.ExpandPath("~/Gone.app"), f.host.ExpandPath("/Applications/Gone.app")); err != nil {
		t.Fatal(err)
	}
	f.file("Docker.dmg")
	f.file("Tool.dmg")
	offered, kept := f.scan()
	if len(offered) != 2 || len(kept) != 0 {
		t.Fatalf("offered %v, kept %v; want both offered, Tool through the linked folder", offered, kept)
	}

	hidden := f.app("~/Hidden/Docker.app", "com.docker.docker", "4.30.0", f.made.Add(-24*time.Hour))
	if err := os.MkdirAll(f.host.ExpandPath("~/Applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hidden, f.host.ExpandPath("~/Applications/Docker.app")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(hidden), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(hidden), 0o755) })
	offered, kept = f.scan()
	if len(offered) != 0 || kept["Docker.dmg"] != "the installed apps could not all be listed" {
		t.Fatalf("offered %v, kept %v; want every download kept while a linked copy cannot be read", offered, kept)
	}
}

func TestDownloadedInstallersKeepAllWhenAppsCannotBeListed(t *testing.T) {
	f := newDownloadsFixture(t)
	f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", f.made.Add(24*time.Hour))
	f.app("/Applications/Locked/Hidden.app", "com.example.hidden", "1.0", f.made.Add(24*time.Hour))
	locked := f.host.ExpandPath("/Applications/Locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	f.file("Docker.dmg")
	offered, kept := f.scan()
	if len(offered) != 0 || kept["Docker.dmg"] != "the installed apps could not all be listed" {
		t.Fatalf("offered %v, kept %v; want every download kept", offered, kept)
	}
}

func TestDownloadedInstallersUnreadableDownloadsIsARow(t *testing.T) {
	f := newDownloadsFixture(t)
	f.file("Docker.dmg")
	if err := os.Chmod(f.downloads(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.downloads(), 0o755) })
	items, _, err := f.d.scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "" || !items[0].Partial {
		t.Fatalf("items %+v, want one unreadable row with no path", items)
	}
}

// A binary Info.plist is read through plutil, inside a zip and in an
// installed app alike.
func TestDownloadedInstallersBinaryPlist(t *testing.T) {
	f := newDownloadsFixture(t)
	app := f.app("/Applications/Foo.app", "unused", "1.0", f.made.Add(24*time.Hour))
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("bplist00installed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.zipFile("foo.zip", map[string]string{"Foo.app/Contents/Info.plist": "bplist00zipped"})
	var converted []string
	f.d.plist = func(_ context.Context, data []byte) ([]byte, error) {
		converted = append(converted, string(data))
		version := map[string]string{"bplist00installed": "2.0", "bplist00zipped": "1.0"}[string(data)]
		return []byte(infoPlist("com.example.foo", version)), nil
	}
	offered, kept := f.scan()
	if offered["foo.zip"] == "" || len(kept) != 0 {
		t.Fatalf("offered %v, kept %v; want foo.zip offered", offered, kept)
	}
	if len(converted) != 2 {
		t.Errorf("plutil saw %q, want both binary plists", converted)
	}
}

func TestDownloadedInstallerRecheck(t *testing.T) {
	f := newDownloadsFixture(t)
	app := f.app("/Applications/Docker.app", "com.docker.docker", "4.40.0", f.made.Add(24*time.Hour))
	dmg := f.file("Docker.dmg")
	ctx := context.Background()
	if err := f.d.recheck(ctx, dmg); err != nil {
		t.Fatalf("an old installer of an installed app: %v, want it allowed", err)
	}
	outside := f.host.ExpandPath("~/Docker.dmg")
	writeFile(t, outside, 4096)
	if err := f.d.recheck(ctx, outside); err == nil {
		t.Error("a file outside ~/Downloads was allowed")
	}
	if err := f.d.recheck(ctx, f.downloads("Gone.dmg")); err == nil {
		t.Error("a download that is gone was allowed")
	}
	recent := f.now.Add(-time.Hour)
	if err := os.Chtimes(dmg, recent, recent); err != nil {
		t.Fatal(err)
	}
	if err := f.d.recheck(ctx, dmg); err == nil || !strings.Contains(err.Error(), "changed in the last 14 days") {
		t.Errorf("a download touched an hour ago: %v, want a refusal naming why", err)
	}
	old := f.made.Add(-48 * time.Hour)
	if err := os.Chtimes(dmg, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if err := f.d.recheck(ctx, dmg); err == nil || !strings.Contains(err.Error(), "no installed app") {
		t.Errorf("after the app was removed: %v, want a refusal naming why", err)
	}
}

func TestNameKey(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Docker", "docker"},
		{"Slack-4.41.105-macOS", "slack"},
		{"Foo.4.2", "foo"},
		{"ChatGPTInstaller", "chatgpt"},
		{"Google Chrome", "googlechrome"},
		{"googlechrome", "googlechrome"},
		{"zoom.us", "zoomus"},
		{"DBeaver-25.2.0-macos-aarch64", "dbeaver"},
		{"VLC-3.0.21-arm64", "vlc"},
		{"Flow-v1.5.339", "flow"},
		{"Claude (1)", "claude"},
		{"orca-macos-x86_64", "orca"},
		{"1Password 8", "1password8"},
		{"Affinity Photo 2", "affinityphoto2"},
		{"Dev1.2", "dev12"},
		{"Install macOS Sequoia", "sequoia"},
		{"Setup", ""},
		{"FooSetup", "foo"},
		{"installer", ""},
		{"1.0-universal", ""},
		{"Café", foldName("café")},
	} {
		if got := nameKey(tc.name); got != tc.want {
			t.Errorf("nameKey(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNameVersion(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Slack-4.41.105-macOS", "4.41.105"},
		{"Flow-v1.5.339", "1.5.339"},
		{"Foo.4.2", "4.2"},
		{"Foo-4.2.0b1", "4.2.0"},
		{"VLC-3.0.21-arm64", "3.0.21"},
		{"Docker", ""},
		{"Claude (1)", ""},
		{"orca-macos-x86_64", ""},
		{"app2.0beta", ""},
	} {
		if got := nameVersion(tc.name); got != tc.want {
			t.Errorf("nameVersion(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCompareAppVersion(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.2", "1.2.0", 0, true},
		{"2.19675.0", "2.2", 1, true},
		{"1.9", "1.10", -1, true},
		{"1.0 beta", "1.0", 0, false},
		{"", "1.0", 0, false},
		{"1..2", "1.2", 0, false},
	} {
		got, ok := compareAppVersion(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("compareAppVersion(%q, %q) = %d, %v; want %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
	zipped := bundleInfo{version: "1.2", build: "200"}
	if newer, known := zipped.newerThan(bundleInfo{version: "1.2", build: "100"}); !newer || !known {
		t.Errorf("same version, later build: newer %v, known %v; want newer", newer, known)
	}
	if _, known := zipped.newerThan(bundleInfo{version: "1.2"}); known {
		t.Error("same version, one build unknown: want the comparison unknown")
	}
}

func TestPlistStrings(t *testing.T) {
	got, err := plistStrings([]byte(infoPlist("com.example.foo", "1.2")))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CFBundleIdentifier": "com.example.foo", "CFBundleShortVersionString": "1.2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plistStrings = %v, want %v", got, want)
	}
	for _, bad := range []string{`<plist><array><string>x</string></array></plist>`, `<string>garbage</string>`, `not xml`, `<plist><dict><key>a</key>`} {
		if _, err := plistStrings([]byte(bad)); err == nil {
			t.Errorf("plistStrings(%q) read a dictionary", bad)
		}
	}
}

func TestDownloadedInstallersRule(t *testing.T) {
	r := catalogRule(t, "downloaded-installers")
	if r.Risk != engine.RiskCaution || len(r.NativeCommand) != 0 {
		t.Errorf("risk %q, native_command %v; want a caution rule moving downloads to the Trash", r.Risk, r.NativeCommand)
	}
	if r.PreAction != engine.PreActionDownloadedInstallerRecheck {
		t.Errorf("pre_action = %q, want %q", r.PreAction, engine.PreActionDownloadedInstallerRecheck)
	}
	if !r.Beta {
		t.Error("downloaded-installers is not beta")
	}
	if _, ok := DefaultQueries()[r.ToolQuery]; !ok || r.ToolQuery != "downloaded-installers" {
		t.Errorf("tool_query %q is not the registered downloaded-installers query", r.ToolQuery)
	}
}
