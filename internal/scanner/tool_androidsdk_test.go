package scanner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// androidFixture is a fixture home with an SDK where Android Studio
// puts it, two NDK versions and an ndk-bundle inside, and no Android
// Studio anywhere.
type androidFixture struct {
	t         *testing.T
	home      string
	root      string
	sdk       string
	env       map[string]string
	spotlight []string
	a         *androidSDK
}

func newAndroidFixture(t *testing.T) *androidFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &androidFixture{t: t, home: filepath.Join(root, "home"), root: filepath.Join(root, "sys"), env: map[string]string{}}
	f.sdk = filepath.Join(f.home, "Library", "Android", "sdk")
	writeFile(t, filepath.Join(f.sdk, "platforms", "android-34", "android.jar"), 100_000)
	writeFile(t, filepath.Join(f.sdk, "ndk", "26.1.10909125", "source.properties"), 20_000)
	writeFile(t, filepath.Join(f.sdk, "ndk", "27.0.12077973", "source.properties"), 30_000)
	writeFile(t, filepath.Join(f.sdk, "ndk-bundle", "source.properties"), 5_000)
	if err := os.MkdirAll(filepath.Join(f.root, "Applications", "Utilities"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.a = &androidSDK{
		Home:      f.home,
		Root:      f.root,
		Getenv:    func(k string) string { return f.env[k] },
		Spotlight: func(context.Context) []string { return f.spotlight },
		w:         testWalker(),
	}
	return f
}

func (f *androidFixture) items() (ndks, sdk []engine.Item) {
	f.t.Helper()
	ndks, err := f.a.NDKs(context.Background())
	if err != nil {
		f.t.Fatalf("NDKs: %v", err)
	}
	sdk, err = f.a.SDK(context.Background())
	if err != nil {
		f.t.Fatalf("SDK: %v", err)
	}
	return ndks, sdk
}

func (f *androidFixture) app(path string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(path, "Contents", "Info.plist"), 10)
}

func TestAndroidSDKOfferedWithoutStudio(t *testing.T) {
	f := newAndroidFixture(t)
	f.env["ANDROID_HOME"] = f.sdk + "/"
	ndks, sdk := f.items()
	want := []struct{ label, path string }{
		{"NDK (ndk-bundle)", filepath.Join(f.sdk, "ndk-bundle")},
		{"NDK 26.1.10909125", filepath.Join(f.sdk, "ndk", "26.1.10909125")},
		{"NDK 27.0.12077973", filepath.Join(f.sdk, "ndk", "27.0.12077973")},
	}
	if len(ndks) != len(want) {
		t.Fatalf("NDK items %+v, want one per NDK version", ndks)
	}
	for i, w := range want {
		if ndks[i].Label != w.label || ndks[i].Path != w.path || ndks[i].Bytes == 0 {
			t.Errorf("NDK item %d = %+v, want %q at %s with its bytes", i, ndks[i], w.label, w.path)
		}
	}
	if len(sdk) != 1 || sdk[0].Path != f.sdk || sdk[0].Label != "Android SDK — ANDROID_HOME points here" {
		t.Errorf("SDK items %+v, want the SDK folder, labelled with ANDROID_HOME", sdk)
	}
}

func TestAndroidSDKKeptWhileStudioInstalled(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *androidFixture)
	}{
		{"in /Applications", func(f *androidFixture) { f.app(filepath.Join(f.root, "Applications", "Android Studio.app")) }},
		{"a preview in ~/Applications", func(f *androidFixture) {
			f.app(filepath.Join(f.home, "Applications", "Android Studio Preview.app"))
		}},
		{"one folder below /Applications", func(f *androidFixture) {
			f.app(filepath.Join(f.root, "Applications", "Development", "Android Studio.app"))
		}},
		{"one folder below /Applications, through a link", func(f *androidFixture) {
			ext := filepath.Join(f.root, "Ext", "Dev")
			f.app(filepath.Join(ext, "Android Studio.app"))
			if err := os.Symlink(ext, filepath.Join(f.root, "Applications", "Dev")); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"in JetBrains Toolbox's store", func(f *androidFixture) {
			f.app(filepath.Join(f.home, "Library", "Application Support", "JetBrains", "Toolbox", "apps", "AndroidStudio", "ch-0", "241.18034.62", "Android Studio.app"))
		}},
		{"where Spotlight knows one", func(f *androidFixture) {
			p := filepath.Join(f.home, "Tools", "Studio.app")
			f.app(p)
			f.spotlight = []string{p}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAndroidFixture(t)
			tc.setup(f)
			if ndks, sdk := f.items(); len(ndks)+len(sdk) > 0 {
				t.Errorf("offered %+v and %+v while Android Studio is installed", ndks, sdk)
			}
		})
	}
}

func TestAndroidSDKSpotlightNamingAGoneAppKeepsNothing(t *testing.T) {
	f := newAndroidFixture(t)
	f.spotlight = []string{filepath.Join(f.home, "Gone", "Android Studio.app")}
	if ndks, sdk := f.items(); len(ndks) != 3 || len(sdk) != 1 {
		t.Errorf("got %d NDK and %d SDK items; a stale Spotlight answer is no install", len(ndks), len(sdk))
	}
}

// A link in or as an Applications folder that cannot be followed may
// hide a Studio: deleted, on a disk not mounted, or a loop.
func TestAndroidSDKLinkThatCannotBeFollowedOffersNone(t *testing.T) {
	for name, link := range map[string]func(f *androidFixture){
		"since deleted": func(f *androidFixture) {
			if err := os.Symlink(filepath.Join(f.root, "Gone"), filepath.Join(f.root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
		},
		"on a volume not mounted": func(f *androidFixture) {
			if err := os.Symlink("/Volumes/NotMounted/Dev", filepath.Join(f.root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
		},
		"a link to a link into a disk not mounted": func(f *androidFixture) {
			links := filepath.Join(f.root, "Links")
			if err := os.MkdirAll(links, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/Volumes/NotMounted/Dev", filepath.Join(links, "Dev")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(links, "Dev"), filepath.Join(f.root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
		},
		"in a loop": func(f *androidFixture) {
			apps := filepath.Join(f.root, "Applications")
			if err := os.Symlink(filepath.Join(apps, "B"), filepath.Join(apps, "A")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(apps, "A"), filepath.Join(apps, "B")); err != nil {
				t.Fatal(err)
			}
		},
		"~/Applications itself": func(f *androidFixture) {
			if err := os.Symlink("/Volumes/NotMounted/Applications", filepath.Join(f.home, "Applications")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAndroidFixture(t)
			link(f)
			for name, q := range map[string]ToolQuery{"NDKs": f.a.NDKs, "SDK": f.a.SDK} {
				items, err := q(context.Background())
				if err == nil || !strings.Contains(err.Error(), "cannot be ruled out") || len(items) > 0 {
					t.Errorf("%s: items %+v, err %v; want an error and nothing offered", name, items, err)
				}
			}
		})
	}
}

// A link to a file holds no Studio.
func TestAndroidSDKLinkToAFileKeepsNothing(t *testing.T) {
	f := newAndroidFixture(t)
	file := filepath.Join(f.root, "notes.txt")
	writeText(t, file, "x")
	if err := os.Symlink(file, filepath.Join(f.root, "Applications", "Dev")); err != nil {
		t.Fatal(err)
	}
	if ndks, sdk := f.items(); len(ndks) != 3 || len(sdk) != 1 {
		t.Errorf("got %d NDK and %d SDK items; a link to a file is no install", len(ndks), len(sdk))
	}
}

func TestAndroidSDKUnknownStudioOffersNone(t *testing.T) {
	cases := []struct {
		name   string
		locked func(f *androidFixture) string
	}{
		{"/Applications", func(f *androidFixture) string { return filepath.Join(f.root, "Applications") }},
		{"a folder in /Applications", func(f *androidFixture) string { return filepath.Join(f.root, "Applications", "Utilities") }},
		{"~/Applications", func(f *androidFixture) string {
			p := filepath.Join(f.home, "Applications")
			if err := os.MkdirAll(p, 0o755); err != nil {
				f.t.Fatal(err)
			}
			return p
		}},
		{"JetBrains Toolbox's store", func(f *androidFixture) string {
			p := filepath.Join(f.home, "Library", "Application Support", "JetBrains", "Toolbox", "apps", "AndroidStudio")
			if err := os.MkdirAll(p, 0o755); err != nil {
				f.t.Fatal(err)
			}
			return p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAndroidFixture(t)
			chmod(t, tc.locked(f), 0)
			for name, q := range map[string]ToolQuery{"NDKs": f.a.NDKs, "SDK": f.a.SDK} {
				items, err := q(context.Background())
				if err == nil || !strings.Contains(err.Error(), "cannot be ruled out") || len(items) > 0 {
					t.Errorf("%s: items %+v, err %v; want an error and nothing offered", name, items, err)
				}
			}
		})
	}
}

func TestAndroidSDKElsewhereNotOffered(t *testing.T) {
	t.Run("no SDK", func(t *testing.T) {
		f := newAndroidFixture(t)
		if err := os.RemoveAll(f.sdk); err != nil {
			t.Fatal(err)
		}
		if ndks, sdk := f.items(); len(ndks)+len(sdk) > 0 {
			t.Errorf("offered %+v and %+v with no SDK", ndks, sdk)
		}
	})
	t.Run("a symlink above the SDK", func(t *testing.T) {
		f := newAndroidFixture(t)
		android := filepath.Dir(f.sdk)
		elsewhere := filepath.Join(f.home, "ext", "Android")
		if err := os.MkdirAll(filepath.Dir(elsewhere), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(android, elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, android); err != nil {
			t.Fatal(err)
		}
		if ndks, sdk := f.items(); len(ndks)+len(sdk) > 0 {
			t.Errorf("offered %+v and %+v through a symlinked ~/Library/Android", ndks, sdk)
		}
	})
	t.Run("a folder above the SDK that cannot be looked at", func(t *testing.T) {
		f := newAndroidFixture(t)
		chmod(t, filepath.Dir(f.sdk), 0)
		for name, query := range map[string]func(context.Context) ([]engine.Item, error){"NDKs": f.a.NDKs, "SDK": f.a.SDK} {
			if items, err := query(context.Background()); err == nil || len(items) > 0 {
				t.Errorf("%s: items %+v, err %v; want an error and nothing offered", name, items, err)
			}
		}
	})
	t.Run("a symlink to an SDK put there by hand", func(t *testing.T) {
		f := newAndroidFixture(t)
		elsewhere := filepath.Join(f.home, "android-sdk")
		if err := os.Rename(f.sdk, elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, f.sdk); err != nil {
			t.Fatal(err)
		}
		if ndks, sdk := f.items(); len(ndks)+len(sdk) > 0 {
			t.Errorf("offered %+v and %+v through a symlink", ndks, sdk)
		}
	})
}

// A symlink at ndk or ndk-bundle leads to NDKs put somewhere by hand:
// no row for them, and the SDK row's share is what its walk measured.
func TestAndroidSDKSymlinkedNDKsNotOffered(t *testing.T) {
	t.Run("the ndk folder", func(t *testing.T) {
		f := newAndroidFixture(t)
		elsewhere := filepath.Join(f.home, "android-ndk")
		if err := os.Rename(filepath.Join(f.sdk, "ndk"), elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(f.sdk, "ndk")); err != nil {
			t.Fatal(err)
		}
		ndks, sdk := f.items()
		if len(ndks) != 1 || ndks[0].Label != "NDK (ndk-bundle)" {
			t.Errorf("NDK rows %+v, want the real ndk-bundle alone", ndks)
		}
		led := engine.Account([]engine.Finding{
			{Rule: catalogRule(t, "android-ndk"), Items: ndks},
			{Rule: catalogRule(t, "android-sdk"), Items: sdk},
		})
		if led.Totals.AfterTrash != sdk[0].Bytes {
			t.Errorf("the ledger moves %d bytes to the Trash, want the SDK's measured %d", led.Totals.AfterTrash, sdk[0].Bytes)
		}
	})
	t.Run("ndk-bundle and one NDK", func(t *testing.T) {
		f := newAndroidFixture(t)
		for _, link := range []string{filepath.Join(f.sdk, "ndk-bundle"), filepath.Join(f.sdk, "ndk", "27.0.12077973")} {
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.sdk, "ndk", "26.1.10909125"), link); err != nil {
				t.Fatal(err)
			}
		}
		ndks, _ := f.items()
		if len(ndks) != 1 || ndks[0].Label != "NDK 26.1.10909125" {
			t.Errorf("NDK rows %+v, want the one real NDK", ndks)
		}
	})
}

// The SDK's row holds the NDKs; the ledger counts each NDK's bytes
// once, on its own row.
func TestAndroidSDKNoByteCountedTwice(t *testing.T) {
	f := newAndroidFixture(t)
	ndks, sdk := f.items()
	findings := []engine.Finding{
		{Rule: catalogRule(t, "android-ndk"), Items: ndks},
		{Rule: catalogRule(t, "android-sdk"), Items: sdk},
	}
	led := engine.Account(findings)
	var ndkBytes int64
	for _, it := range ndks {
		ndkBytes += it.Bytes
	}
	if led.Totals.AfterTrash != sdk[0].Bytes {
		t.Errorf("the ledger moves %d bytes to the Trash, want the SDK's %d once", led.Totals.AfterTrash, sdk[0].Bytes)
	}
	var sdkShare int64
	for id, n := range led.Exclusive {
		if strings.HasPrefix(id, "android-sdk") {
			sdkShare += n
		}
	}
	if sdkShare != sdk[0].Bytes-ndkBytes {
		t.Errorf("the SDK row's share is %d, want %d less the NDKs' %d", sdkShare, sdk[0].Bytes, ndkBytes)
	}
}

func TestAndroidSDKUnreadableNDKFolderIsMarked(t *testing.T) {
	t.Run("ndk cannot be listed", func(t *testing.T) {
		wantNDKMarker(t, func(f *androidFixture) string { return filepath.Join(f.sdk, "ndk") })
	})
	// The SDK folder is listable but not searchable: ndk cannot be told
	// to be a folder.
	t.Run("ndk cannot be looked at", func(t *testing.T) {
		ndks := wantNDKMarker(t, func(f *androidFixture) string {
			chmod(t, f.sdk, 0o644)
			return ""
		})
		if !slices.ContainsFunc(ndks, func(it engine.Item) bool { return it.Path == "" && it.Label == "NDK (ndk-bundle)" }) {
			t.Errorf("items %+v, want ndk-bundle's row labelled as when it is measured", ndks)
		}
	})
}

func wantNDKMarker(t *testing.T, lock func(f *androidFixture) string) []engine.Item {
	t.Helper()
	f := newAndroidFixture(t)
	if p := lock(f); p != "" {
		chmod(t, p, 0)
	}
	ndks, err := f.a.NDKs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, it := range ndks {
		if it.Path == "" && it.Partial && strings.HasPrefix(it.Label, "NDK versions under ") {
			marked = true
		}
		if strings.HasPrefix(it.Path, filepath.Join(f.sdk, "ndk")+"/") {
			t.Errorf("offered %s from a folder that could not be listed", it.Path)
		}
	}
	if !marked {
		t.Errorf("items %+v, want a row saying NDK versions could not be listed", ndks)
	}
	return ndks
}

// Both rules move to the Trash, so undo restores what a command-line
// build turns out to need.
func TestAndroidRules(t *testing.T) {
	for _, id := range []string{"android-ndk", "android-sdk"} {
		r := catalogRule(t, id)
		if r.Risk != engine.RiskCaution || len(r.NativeCommand) > 0 || !r.Beta {
			t.Errorf("%s: risk %q, command %q, beta %v; want caution, to the Trash, beta", id, r.Risk, r.NativeCommand, r.Beta)
		}
	}
}
