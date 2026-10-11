package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// simctlRuntimeList is `simctl runtime list -j` from CoreSimulator's
// simctl on a machine whose Xcode is gone, cut to the fields read.
const simctlRuntimeList = `{
  "11111111-2222-3333-4444-555555555555" : {
    "build" : "22D8075",
    "deletable" : true,
    "identifier" : "11111111-2222-3333-4444-555555555555",
    "kind" : "Disk Image",
    "lastUsedAt" : "2025-06-24T03:53:46Z",
    "runtimeIdentifier" : "com.apple.CoreSimulator.SimRuntime.iOS-18-3",
    "sizeBytes" : 8708125252,
    "state" : "Ready",
    "version" : "18.3.1"
  }
}`

func TestSimRuntimesNoXcode(t *testing.T) {
	listed := 0
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List: func(context.Context) ([]byte, bool, error) {
			listed++
			return []byte(simctlRuntimeList), true, nil
		},
	}
	items, err := s.Runtimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 6, 24, 3, 53, 46, 0, time.UTC)
	if len(items) != 1 || items[0].Label != "iOS 18.3 (22D8075)" || items[0].Arg != "11111111-2222-3333-4444-555555555555" ||
		items[0].Bytes != 8708125252 || !items[0].LastUsed.Equal(want) {
		t.Errorf("items %+v, want the one runtime with its identifier, size and last use", items)
	}

	// With Xcode back, sim-runtimes lists the same runtimes.
	s.HasSimctl = func(context.Context) bool { return true }
	listed = 0
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 || listed > 0 {
		t.Errorf("with xcrun finding simctl: items %v, err %v, listed %d times; want nothing", items, err, listed)
	}

	// Xcode installed with xcode-select on the Command Line Tools:
	// xcrun finds no simctl, and Xcode is not gone.
	s.HasSimctl = func(context.Context) bool { return false }
	s.HasXcode = func(context.Context) bool { return true }
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 || listed > 0 {
		t.Errorf("with an Xcode installed: items %v, err %v, listed %d times; want nothing", items, err, listed)
	}
}

func TestSimRuntimesNoXcodeWithoutCoreSimulator(t *testing.T) {
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List:      func(context.Context) ([]byte, bool, error) { return nil, false, nil },
	}
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 {
		t.Errorf("no CoreSimulator simctl: items %v, err %v; want nothing and no error", items, err)
	}
}

func TestSimRuntimesNoXcodeListFails(t *testing.T) {
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List:      func(context.Context) ([]byte, bool, error) { return nil, false, errors.New("exit status 1") },
	}
	if _, err := s.Runtimes(context.Background()); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("err = %v, want the failed listing", err)
	}
}

// The note tells the owner which simctl to run; it must name the one
// the query lists through.
func TestSimRuntimesNoXcodeRule(t *testing.T) {
	r := catalogRule(t, "sim-runtimes-no-xcode")
	if r.Risk != engine.RiskSurfaceOnly || len(r.NativeCommand) > 0 {
		t.Errorf("risk %q, native command %q; root-owned runtimes are surface-only", r.Risk, r.NativeCommand)
	}
	for _, want := range []string{coreSimulatorSimctl + " runtime list", "runtime delete <identifier> --dry-run"} {
		if !strings.Contains(r.Note, want) {
			t.Errorf("note %q does not say %q", r.Note, want)
		}
	}
}

func TestXcodeInstalled(t *testing.T) {
	app := func(t *testing.T, path, bundleID string) {
		writeText(t, filepath.Join(path, "Contents", "Info.plist"), launchdPlist("", nil, map[string]string{"CFBundleIdentifier": bundleID}))
	}
	cases := []struct {
		name      string
		setup     func(t *testing.T, root, home string) []string // returns Spotlight's answer
		installed bool
	}{
		{"nothing", func(*testing.T, string, string) []string { return nil }, false},
		{"the Xcodes manager", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Xcodes.app"), "com.robotsandpencils.XcodesApp")
			return nil
		}, false},
		{"Xcode in /Applications", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Xcode.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"a beta in ~/Applications", func(t *testing.T, _, home string) []string {
			app(t, filepath.Join(home, "Applications", "Xcode-beta.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"one folder below", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Dev", "Xcode 16.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"an Xcode on a volume not mounted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Applications/Xcode.app", filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a relative link to an Xcode on a volume not mounted", func(t *testing.T, root, _ string) []string {
			apps := filepath.Join(root, "Applications")
			rel, err := filepath.Rel(apps, "/Volumes/NotMounted/Applications/Xcode.app")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rel, filepath.Join(apps, "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder in a loop", func(t *testing.T, root, _ string) []string {
			apps := filepath.Join(root, "Applications")
			if err := os.Symlink(filepath.Join(apps, "B"), filepath.Join(apps, "A")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(apps, "A"), filepath.Join(apps, "B")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		// A link that cannot be followed may hide an install: the rows
		// stay hidden, even for a link left after Xcode was deleted.
		{"a link to an Xcode since deleted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink(filepath.Join(root, "Applications", "Xcode-16.app"), filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a link to a link into a disk not mounted", func(t *testing.T, root, _ string) []string {
			links := filepath.Join(root, "Links")
			if err := os.MkdirAll(links, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/Volumes/NotMounted/Xcode.app", filepath.Join(links, "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(links, "Xcode.app"), filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"~/Applications, a link that cannot be followed", func(t *testing.T, _, home string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Applications", filepath.Join(home, "Applications")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a link to a file", func(t *testing.T, root, _ string) []string {
			file := filepath.Join(root, "notes.txt")
			writeText(t, file, "x")
			if err := os.Symlink(file, filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, false},
		{"one folder below, through a link", func(t *testing.T, root, _ string) []string {
			ext := filepath.Join(root, "Ext", "Dev")
			app(t, filepath.Join(ext, "Xcode.app"), "com.apple.dt.Xcode")
			if err := os.Symlink(ext, filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder on a volume not mounted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Dev", filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder since deleted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink(filepath.Join(root, "Gone"), filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"an Xcode-named folder with no app in it", func(t *testing.T, root, _ string) []string {
			if err := os.MkdirAll(filepath.Join(root, "Applications", "Xcode.app"), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		}, false},
		{"an Xcode whose Info.plist cannot be read", func(t *testing.T, root, _ string) []string {
			p := filepath.Join(root, "Applications", "Xcode.app")
			app(t, p, "com.apple.dt.Xcode")
			chmod(t, filepath.Join(p, "Contents"), 0)
			return nil
		}, true},
		{"renamed, known to Spotlight", func(t *testing.T, _, home string) []string {
			p := filepath.Join(home, "Tools", "XC.app")
			app(t, p, "com.apple.dt.Xcode")
			return []string{p}
		}, true},
		{"Spotlight naming a gone app", func(t *testing.T, _, home string) []string { return []string{filepath.Join(home, "Gone", "Xcode.app")} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "Applications"), 0o755); err != nil {
				t.Fatal(err)
			}
			answer := tc.setup(t, root, home)
			got := xcodeInstalled(context.Background(), testWalker(), root, home, func(context.Context) []string { return answer })
			if got != tc.installed {
				t.Errorf("xcodeInstalled = %v, want %v", got, tc.installed)
			}
		})
	}
}
