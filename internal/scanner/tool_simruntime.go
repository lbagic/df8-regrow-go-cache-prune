package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// Simulator runtimes outlive Xcode: the images stay on disk, and with
// Xcode gone `xcrun simctl` is gone too, so sim-runtimes lists nothing.
// CoreSimulator, which Xcode installs outside its app bundle, keeps a
// simctl of its own. This query lists the runtimes through that one
// for sim-runtimes-no-xcode, and only while xcrun finds no simctl
// (with one, sim-runtimes lists the same runtimes) and no Xcode app is
// installed: xcrun also finds none while xcode-select points at the
// Command Line Tools, and the rows would then say Xcode is gone.

// coreSimulatorSimctl is the simctl CoreSimulator keeps outside Xcode.
// The rule's note names the same path.
const coreSimulatorSimctl = "/Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/Resources/bin/simctl"

// simRuntimesNoXcode is the sim-runtimes-no-xcode query. The zero
// value asks the real machine; tests inject both tools.
type simRuntimesNoXcode struct {
	// HasSimctl reports whether xcrun finds simctl; nil asks xcrun.
	HasSimctl func(ctx context.Context) bool
	// HasXcode reports an installed Xcode app; nil looks for one.
	HasXcode func(ctx context.Context) bool
	// List runs CoreSimulator's `simctl runtime list -j`; ok is false
	// when that simctl is not there. nil runs it.
	List func(ctx context.Context) (out []byte, ok bool, err error)
}

func (s *simRuntimesNoXcode) Runtimes(ctx context.Context) ([]engine.Item, error) {
	hasSimctl, hasXcode, list := s.HasSimctl, s.HasXcode, s.List
	if hasSimctl == nil {
		hasSimctl = xcrunFindsSimctl
	}
	if hasXcode == nil {
		hasXcode = func(ctx context.Context) bool {
			home, _ := os.UserHomeDir()
			return xcodeInstalled(ctx, defaultWalker, "", home, spotlightXcodes)
		}
	}
	if list == nil {
		list = func(ctx context.Context) ([]byte, bool, error) {
			return runTool(ctx, coreSimulatorSimctl, "runtime", "list", "-j")
		}
	}
	if hasSimctl(ctx) || hasXcode(ctx) {
		return nil, nil
	}
	out, ok, err := list(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s runtime list: %w", coreSimulatorSimctl, err)
	}
	if !ok {
		return nil, nil
	}
	return parseSimRuntimes(out)
}

func xcrunFindsSimctl(ctx context.Context) bool {
	_, ok, err := runTool(ctx, "xcrun", "--find", "simctl")
	return ok && err == nil
}

// xcodeInstalled reports an app with Xcode's bundle id in either
// Applications folder or a folder directly inside one (a linked folder
// followed), or wherever Spotlight knows one. Only apps named like
// Xcode are opened; the Xcodes version manager is named so and is not
// Xcode. An Xcode-named app or a folder that cannot be read counts as
// Xcode, and so does a link among them, or an Applications folder that
// is a link, that cannot be followed.
func xcodeInstalled(ctx context.Context, w *walker, root, home string, spotlight func(context.Context) []string) bool {
	isXcode := func(app string) bool {
		data, err := readSource(ctx, w, filepath.Join(app, "Contents", "Info.plist"))
		if absent(err) {
			_, err := w.stat(ctx, app)
			return err != nil
		}
		if err != nil {
			return true
		}
		texts, err := plistTexts(data)
		return err != nil || slices.Contains(texts, xcodeBundleID)
	}
	apps := []string{filepath.Join(root, "/Applications")}
	if home != "" {
		apps = append(apps, filepath.Join(home, "Applications"))
	}
	for _, dir := range apps {
		if unfollowableLink(ctx, w, dir) {
			return true
		}
		entries, partial, err := w.list(ctx, dir)
		if partial || err != nil {
			return true
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if !strings.HasSuffix(e.Name(), ".app") && (e.IsDir() || e.Type()&fs.ModeSymlink != 0) {
				if e.Type()&fs.ModeSymlink != 0 {
					folder, unknown := linkedFolder(ctx, w, p)
					if unknown {
						return true
					}
					if !folder {
						continue
					}
				}
				inner, partial, err := w.list(ctx, p)
				if partial || err != nil {
					return true
				}
				for _, in := range inner {
					if xcodeNamed(in.Name()) && isXcode(filepath.Join(p, in.Name())) {
						return true
					}
				}
				continue
			}
			if xcodeNamed(e.Name()) && isXcode(p) {
				return true
			}
		}
	}
	for _, p := range spotlight(ctx) {
		if _, err := w.lstat(ctx, p); err == nil {
			return true
		}
	}
	return false
}

const xcodeBundleID = "com.apple.dt.Xcode"

// linkedFolder follows a link in an Applications folder: folder when
// it leads to one. A link that cannot be followed is unknown, as an app
// may be installed behind it on a disk not mounted now; a link whose
// target was deleted cannot be told from one of those.
func linkedFolder(ctx context.Context, w *walker, link string) (folder, unknown bool) {
	fi, err := w.stat(ctx, link)
	if err != nil {
		return false, true
	}
	return fi.IsDir(), false
}

// unfollowableLink reports a path that is a link that cannot be
// followed, as linkedFolder judges it.
func unfollowableLink(ctx context.Context, w *walker, path string) bool {
	fi, err := w.lstat(ctx, path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	_, unknown := linkedFolder(ctx, w, path)
	return unknown
}

func xcodeNamed(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "xcode") && strings.HasSuffix(name, ".app")
}

// spotlightXcodes asks Spotlight for apps with Xcode's bundle id. It can
// only add a finding: no answer is no Xcode from this source.
func spotlightXcodes(ctx context.Context) []string {
	out, ok, err := runTool(ctx, "mdfind", "kMDItemCFBundleIdentifier == '"+xcodeBundleID+"'")
	if !ok || err != nil {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths
}
