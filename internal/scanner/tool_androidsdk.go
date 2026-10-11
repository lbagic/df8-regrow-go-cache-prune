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

// Android Studio installs its SDK, NDKs included, under
// ~/Library/Android/sdk and leaves it behind when the app goes. Two
// rules read this provider, and only while no Android Studio is
// installed: android-ndk (one item per NDK version) and android-sdk
// (the SDK folder, which holds the NDKs; the ledger takes them out of
// its share). An SDK somewhere else was put there by hand for
// command-line builds and is never offered.

// androidSDK answers both queries. The zero value reads the real
// machine; tests inject the home, the system root, the environment and
// the Spotlight answer.
type androidSDK struct {
	// Home is the user's home; "" means os.UserHomeDir.
	Home string
	// Root re-anchors /Applications; "" means /.
	Root string
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
	// Spotlight lists the installed apps with Android Studio's bundle
	// id, wherever they are; nil runs mdfind.
	Spotlight func(ctx context.Context) []string
	w         *walker
}

func (a *androidSDK) walker() *walker {
	if a.w != nil {
		return a.w
	}
	return defaultWalker
}

func (a *androidSDK) getenv(key string) string {
	if a.Getenv != nil {
		return a.Getenv(key)
	}
	return os.Getenv(key)
}

// NDKs is the android-ndk query: one item per NDK version.
func (a *androidSDK) NDKs(ctx context.Context) ([]engine.Item, error) {
	sdk, err := a.orphanedSDK(ctx)
	if err != nil || sdk == "" {
		return nil, err
	}
	w := a.walker()
	dir := filepath.Join(sdk, "ndk")
	var items []engine.Item
	paths := []string{filepath.Join(sdk, "ndk-bundle")}
	// A symlink at ndk, ndk-bundle or an NDK leads to NDKs put
	// somewhere by hand, which the SDK's size walk does not enter.
	real, err := a.realDir(ctx, dir)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		items = append(items, unreadableMarker("NDK versions under "+dir))
	case real:
		entries, partial, err := w.list(ctx, dir)
		if err != nil {
			return nil, err
		}
		if partial {
			items = append(items, unreadableMarker("NDK versions under "+dir))
		}
		for _, e := range entries {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		label := "NDK " + filepath.Base(p)
		if filepath.Dir(p) == sdk {
			label = "NDK (ndk-bundle)"
		}
		real, err := a.realDir(ctx, p)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			items = append(items, unreadableMarker(label))
		}
		if !real {
			continue
		}
		if item, ok := a.measure(ctx, p, label); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// SDK is the android-sdk query: the SDK folder as one item.
func (a *androidSDK) SDK(ctx context.Context) ([]engine.Item, error) {
	sdk, err := a.orphanedSDK(ctx)
	if err != nil || sdk == "" {
		return nil, err
	}
	label := "Android SDK"
	for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := a.getenv(key); v != "" && filepath.Clean(v) == sdk {
			label += " — " + key + " points here"
			break
		}
	}
	item, ok := a.measure(ctx, sdk, label)
	if !ok {
		return nil, nil
	}
	return []engine.Item{item}, nil
}

// realDir reports a folder that is no symlink itself. Nothing there, a
// file or a symlink is false; any other failure is an error.
func (a *androidSDK) realDir(ctx context.Context, path string) (bool, error) {
	fi, err := a.walker().lstat(ctx, path)
	switch {
	case absent(err):
		return false, nil
	case err != nil:
		return false, err
	}
	return fi.IsDir(), nil
}

func (a *androidSDK) measure(ctx context.Context, path, label string) (engine.Item, bool) {
	u, found, _ := a.walker().usage(ctx, path)
	if !found {
		return engine.Item{}, false
	}
	return engine.Item{Path: path, Label: label, Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}, true
}

// orphanedSDK returns the SDK folder when it exists and no Android
// Studio is installed, "" otherwise. An SDK path that cannot be looked
// at is an error, and so is a place an app can be installed in that
// cannot be read: Android Studio cannot be ruled out there.
func (a *androidSDK) orphanedSDK(ctx context.Context) (string, error) {
	home := a.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", nil
		}
	}
	sdk := filepath.Join(home, "Library", "Android", "sdk")
	// A symlink here or above leads to an SDK put somewhere by hand.
	w := a.walker()
	resolved, err := bounded(ctx, w, sdk, func() (string, error) { return filepath.EvalSymlinks(sdk) })
	switch {
	case absent(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("%s: %w", sdk, err)
	case foldName(resolved) != foldName(sdk):
		return "", nil
	}
	if real, err := a.realDir(ctx, sdk); err != nil || !real {
		return "", err
	}
	studio, err := a.studio(ctx, home)
	if err != nil || studio != "" {
		return "", err
	}
	return sdk, nil
}

// studio returns where an Android Studio is installed, "" when none is
// found: in an Applications folder or one folder below it (a linked
// folder followed), in JetBrains Toolbox's own store, or wherever
// Spotlight knows one. A link there that cannot be followed is an
// error: a Studio behind it cannot be ruled out.
func (a *androidSDK) studio(ctx context.Context, home string) (string, error) {
	w := a.walker()
	unknown := func(dir string) error {
		return fmt.Errorf("%s is not fully readable, so an Android Studio there cannot be ruled out", dir)
	}
	for _, apps := range []string{filepath.Join(a.Root, "/Applications"), filepath.Join(home, "Applications")} {
		if unfollowableLink(ctx, w, apps) {
			return "", fmt.Errorf("%s cannot be followed to a folder, so an Android Studio there cannot be ruled out", apps)
		}
		entries, partial, err := w.list(ctx, apps)
		if err != nil {
			return "", err
		}
		if partial {
			return "", unknown(apps)
		}
		for _, e := range entries {
			p := filepath.Join(apps, e.Name())
			if isAndroidStudio(e.Name()) {
				return p, nil
			}
			if strings.HasSuffix(e.Name(), ".app") {
				continue
			}
			if e.Type()&fs.ModeSymlink != 0 {
				folder, unknown := linkedFolder(ctx, w, p)
				if unknown {
					return "", fmt.Errorf("%s cannot be followed to a folder, so an Android Studio there cannot be ruled out", p)
				}
				if !folder {
					continue
				}
			} else if !e.IsDir() {
				continue
			}
			inner, partial, err := w.list(ctx, p)
			if err != nil {
				return "", err
			}
			if partial {
				return "", unknown(p)
			}
			for _, in := range inner {
				if isAndroidStudio(in.Name()) {
					return filepath.Join(p, in.Name()), nil
				}
			}
		}
	}
	toolbox := filepath.Join(home, "Library", "Application Support", "JetBrains", "Toolbox", "apps")
	matches, partial := w.glob(ctx, filepath.Join(toolbox, "*", "ch-*", "*", "*.app"))
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if partial {
		return "", unknown(toolbox)
	}
	for _, m := range matches {
		if isAndroidStudio(filepath.Base(m)) {
			return m, nil
		}
	}
	spotlight := a.Spotlight
	if spotlight == nil {
		spotlight = mdfindAndroidStudio
	}
	for _, p := range spotlight(ctx) {
		if _, err := w.lstat(ctx, p); err == nil {
			return p, nil
		}
	}
	return "", ctx.Err()
}

// isAndroidStudio matches the app by the name it ships under, in every
// channel: "Android Studio.app", "Android Studio Preview.app".
func isAndroidStudio(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "android studio") && strings.HasSuffix(name, ".app")
}

// mdfindAndroidStudio asks Spotlight for apps with Android Studio's
// bundle id, which finds one a Toolbox was told to install elsewhere.
// It can only add a finding: no answer (Spotlight off, mdfind missing)
// is no Android Studio from this source.
func mdfindAndroidStudio(ctx context.Context) []string {
	out, ok, err := runTool(ctx, "mdfind", "kMDItemCFBundleIdentifier == 'com.google.android.studio*'")
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
