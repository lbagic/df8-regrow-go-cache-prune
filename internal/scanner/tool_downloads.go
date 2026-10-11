package scanner

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lbagic/regrow/internal/engine"
)

// Installers in ~/Downloads for apps already installed. A .dmg, .pkg
// or .zip at the top of ~/Downloads is offered when it installs an app
// in /Applications or ~/Applications, and every failure keeps it:
//
//   - a .zip is matched by what it holds: exactly one app at its top
//     level, read from the archive's directory without extracting,
//     whose bundle identifier an installed app carries;
//   - a .dmg or .pkg cannot be read without mounting or expanding it,
//     so it is matched by name: its file name, less versions,
//     architectures and platform words, spells an installed app's;
//   - it is not newer than the installed app: a zip's app version is
//     not higher than the installed one's, nor is a version a .dmg or
//     .pkg spells in its name, and a download whose version is unknown,
//     or not comparable, arrived before the installed app was put in
//     place (the bundle's date added);
//   - nothing about it changed for downloadsQuiet.
//
// Each installed copy of the app must pass, and when the apps cannot
// all be listed every download is kept. The
// downloaded-installer-recheck pre-action runs the same judgement on
// the one download right before it goes to the Trash.

const (
	// downloadsQuiet is how long a download must go untouched.
	downloadsQuiet = 14 * 24 * time.Hour
	// maxInfoPlist bounds the Info.plist read out of a zip.
	maxInfoPlist = 1 << 20
)

// installerExts are the download kinds the rule judges.
var installerExts = []string{".dmg", ".pkg", ".zip"}

// appRoots are where installed apps are looked for: their top level
// and one folder down (/Applications/Utilities).
var appRoots = []string{"/Applications", "~/Applications"}

// noiseWords say nothing about which app a download installs.
var noiseWords = map[string]bool{
	"aarch64": true, "amd64": true, "apple": true, "arm": true, "arm64": true,
	"darwin": true, "install": true, "installer": true, "intel": true,
	"latest": true, "mac": true, "macos": true, "os": true, "osx": true,
	"release": true, "setup": true, "silicon": true, "stable": true,
	"universal": true, "x64": true, "x86": true,
}

// keptDownload is a download the rule leaves alone, and why.
type keptDownload struct{ path, why string }

// installedApp is one app bundle found under the app roots.
type installedApp struct {
	path  string
	key   string    // nameKey of the bundle's name
	added time.Time // zero when its date added could not be read
}

// bundleInfo is what an app's Info.plist says about it.
type bundleInfo struct{ id, version, build string }

// downloadedInstallers is the query with its seams: tests hand in the
// walker, the host (its Root re-anchors /Applications), the clock, the
// date-added reader and the plist converter.
type downloadedInstallers struct {
	w     *walker
	host  engine.Host
	quiet time.Duration
	now   func() time.Time
	added func(path string) (time.Time, error)
	plist func(ctx context.Context, data []byte) ([]byte, error)
}

func newDownloadedInstallers(home string) *downloadedInstallers {
	return &downloadedInstallers{
		w:     defaultWalker,
		host:  engine.Host{Home: home},
		quiet: downloadsQuiet,
		now:   time.Now,
		added: addedTime,
		plist: plutilXML,
	}
}

func queryDownloadedInstallers(ctx context.Context) ([]engine.Item, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	items, _, err := newDownloadedInstallers(home).scan(ctx)
	return items, err
}

// RecheckDownloadedInstaller is the downloaded-installers pre-action:
// right before a download goes to the Trash it judges the download
// again and refuses when anything keeps it now. The scan may be hours
// old; the download may have been opened, or the app removed.
func RecheckDownloadedInstaller(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	// The run's context has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	return newDownloadedInstallers(home).recheck(ctx, path)
}

func (d *downloadedInstallers) downloadsDir() string {
	return filepath.Join(d.host.Home, "Downloads")
}

// scan judges every download of an installer kind: the items offered,
// and the ones kept, each with its reason.
func (d *downloadedInstallers) scan(ctx context.Context) ([]engine.Item, []keptDownload, error) {
	dir := d.downloadsDir()
	entries, partial, err := d.w.list(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	var items []engine.Item
	if partial {
		items = append(items, unreadableMarker("downloads under "+dir))
	}
	var paths []string
	for _, e := range entries {
		if isInstallerName(e.Name()) {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	if len(paths) == 0 {
		return items, nil, nil
	}
	slices.Sort(paths)
	got, kept, err := d.judge(ctx, paths)
	return append(items, got...), kept, err
}

// recheck judges one download again, as the pre-action does.
func (d *downloadedInstallers) recheck(ctx context.Context, path string) error {
	if filepath.Dir(path) != d.downloadsDir() || !isInstallerName(filepath.Base(path)) {
		return fmt.Errorf("%s is not an installer at the top of %s: not moved", path, d.downloadsDir())
	}
	items, kept, err := d.judge(ctx, []string{path})
	if err != nil {
		return fmt.Errorf("recheck %s: %w: not moved", path, err)
	}
	if len(kept) == 1 {
		return fmt.Errorf("download %s is kept now (%s): not moved", path, kept[0].why)
	}
	if len(items) != 1 {
		return fmt.Errorf("download %s is gone or no longer a file: not moved", path)
	}
	return nil
}

func isInstallerName(name string) bool {
	return slices.Contains(installerExts, strings.ToLower(filepath.Ext(name)))
}

// judge decides each download. Its only error is ctx's.
func (d *downloadedInstallers) judge(ctx context.Context, paths []string) ([]engine.Item, []keptDownload, error) {
	var kept []keptDownload
	apps, complete := d.installedApps(ctx)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !complete {
		for _, p := range paths {
			kept = append(kept, keptDownload{p, "the installed apps could not all be listed"})
		}
		return nil, kept, nil
	}
	infos := map[string]*bundleInfo{} // by app path; nil when unreadable
	var items []engine.Item
	for _, p := range paths {
		item, why, ok := d.judgeOne(ctx, p, apps, infos)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		switch {
		case !ok:
		case why != "":
			kept = append(kept, keptDownload{p, why})
		default:
			items = append(items, item)
		}
	}
	return items, kept, nil
}

// judgeOne decides one download: an item, or why it stays. ok is
// false when the path is not a download to judge: gone, or no file.
func (d *downloadedInstallers) judgeOne(ctx context.Context, path string, apps []installedApp, infos map[string]*bundleInfo) (engine.Item, string, bool) {
	fi, err := d.w.lstat(ctx, path)
	switch {
	case absent(err):
		return engine.Item{}, "", false
	case err != nil:
		return engine.Item{}, "it cannot be read", true
	case !fi.Mode().IsRegular():
		return engine.Item{}, "", false
	}
	// The latest of every time the download reports: a download
	// opened, moved into the folder or rewritten is a recent one.
	last := slices.MaxFunc(fileTimes(fi), time.Time.Compare)
	if at, err := bounded(ctx, d.w, path, func() (time.Time, error) { return d.added(path) }); err == nil && at.After(last) {
		last = at
	}
	if d.now().Sub(last) < d.quiet {
		return engine.Item{}, fmt.Sprintf("it changed in the last %d days", int(d.quiet.Hours()/24)), true
	}

	var matched []installedApp
	var zipped *bundleInfo
	named := ""
	if strings.EqualFold(filepath.Ext(path), ".zip") {
		info, why := d.zippedApp(ctx, path)
		if why != "" {
			return engine.Item{}, why, true
		}
		zipped = &info
		for _, a := range apps {
			if ai := d.appInfo(ctx, a, infos); ai != nil && strings.EqualFold(ai.id, info.id) {
				matched = append(matched, a)
			}
		}
		if len(matched) == 0 {
			return engine.Item{}, fmt.Sprintf("no installed app is %s", info.id), true
		}
	} else {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		named = nameVersion(base)
		key := nameKey(base)
		if key == "" {
			return engine.Item{}, "its name names no app", true
		}
		for _, a := range apps {
			if a.key == key {
				matched = append(matched, a)
			}
		}
		if len(matched) == 0 {
			return engine.Item{}, "no installed app has its name", true
		}
	}

	for _, a := range matched {
		if named != "" {
			if ai := d.appInfo(ctx, a, infos); ai != nil {
				if c, ok := compareAppVersion(named, ai.version); ok && c > 0 {
					return engine.Item{}, fmt.Sprintf("its name's version is newer than %s (%s over %s)", a.path, named, ai.version), true
				}
			}
		}
		if zipped != nil {
			if ai := d.appInfo(ctx, a, infos); ai != nil {
				if newer, known := zipped.newerThan(*ai); known {
					if newer {
						return engine.Item{}, fmt.Sprintf("its app is newer than %s (%s over %s)", a.path, zipped.version, ai.version), true
					}
					continue
				}
			}
		}
		if a.added.IsZero() {
			return engine.Item{}, fmt.Sprintf("when %s was installed cannot be read", a.path), true
		}
		if !last.Before(a.added) {
			return engine.Item{}, fmt.Sprintf("it arrived after %s was installed", a.path), true
		}
	}
	return engine.Item{
		Path:     path,
		Label:    filepath.Base(path) + " · " + filepath.Base(matched[0].path),
		Bytes:    physicalSize(fi),
		LastUsed: last,
	}, "", true
}

// installedApps lists the app bundles under the app roots, each with
// its name key and date added. complete is false when a root or a
// folder in it could not be listed, which could hide an installed copy.
func (d *downloadedInstallers) installedApps(ctx context.Context) ([]installedApp, bool) {
	var apps []installedApp
	complete := true
	add := func(path string) {
		at, err := bounded(ctx, d.w, path, func() (time.Time, error) { return d.added(path) })
		if err != nil {
			at = time.Time{}
		}
		apps = append(apps, installedApp{path: path, key: nameKey(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))), added: at})
	}
	for _, root := range appRoots {
		root = d.host.ExpandPath(root)
		entries, partial, err := d.w.list(ctx, root)
		if err != nil || partial {
			return nil, false
		}
		for _, e := range entries {
			p := filepath.Join(root, e.Name())
			if strings.EqualFold(filepath.Ext(e.Name()), ".app") {
				if d.w.isDir(ctx, p) {
					add(p)
				}
				continue
			}
			if !e.IsDir() {
				continue
			}
			sub, partial, err := d.w.list(ctx, p)
			if err != nil || partial {
				complete = false
				continue
			}
			for _, s := range sub {
				sp := filepath.Join(p, s.Name())
				if strings.EqualFold(filepath.Ext(s.Name()), ".app") && d.w.isDir(ctx, sp) {
					add(sp)
				}
			}
		}
	}
	return apps, complete
}

// appInfo reads an installed app's Info.plist once; nil when it
// cannot be read or names no bundle identifier.
func (d *downloadedInstallers) appInfo(ctx context.Context, a installedApp, infos map[string]*bundleInfo) *bundleInfo {
	if info, ok := infos[a.path]; ok {
		return info
	}
	plistPath := filepath.Join(a.path, "Contents", "Info.plist")
	var info *bundleInfo
	data, err := bounded(ctx, d.w, plistPath, func() ([]byte, error) { return readLimited(plistPath, maxInfoPlist) })
	if err == nil {
		if bi, err := d.parseInfo(ctx, data); err == nil {
			info = &bi
		}
	}
	infos[a.path] = info
	return info
}

// zippedApp reads the one app at the top of a zip, or says why the zip
// is kept.
func (d *downloadedInstallers) zippedApp(ctx context.Context, path string) (bundleInfo, string) {
	type result struct {
		data []byte
		why  string
	}
	r, err := bounded(ctx, d.w, path, func() (result, error) {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return result{why: "it cannot be read as a zip archive"}, nil
		}
		defer func() { _ = zr.Close() }()
		var bundles []string
		var plist *zip.File
		for _, f := range zr.File {
			top, rest, _ := strings.Cut(f.Name, "/")
			if !strings.EqualFold(filepath.Ext(top), ".app") || top == ".app" {
				continue
			}
			if !slices.Contains(bundles, top) {
				bundles = append(bundles, top)
			}
			if rest == "Contents/Info.plist" {
				plist = f
			}
		}
		switch {
		case len(bundles) == 0:
			return result{why: "it holds no app at its top level"}, nil
		case len(bundles) > 1:
			return result{why: "it holds more than one app"}, nil
		case plist == nil || plist.UncompressedSize64 > maxInfoPlist:
			return result{why: "its app's Info.plist cannot be read"}, nil
		}
		rc, err := plist.Open()
		if err != nil {
			return result{why: "its app's Info.plist cannot be read"}, nil
		}
		defer func() { _ = rc.Close() }()
		data, err := io.ReadAll(io.LimitReader(rc, maxInfoPlist))
		if err != nil {
			return result{why: "its app's Info.plist cannot be read"}, nil
		}
		return result{data: data}, nil
	})
	if err != nil {
		return bundleInfo{}, "it cannot be read"
	}
	if r.why != "" {
		return bundleInfo{}, r.why
	}
	info, err := d.parseInfo(ctx, r.data)
	if err != nil {
		return bundleInfo{}, "its app's Info.plist cannot be read"
	}
	return info, ""
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// parseInfo reads the bundle identifier and versions of an Info.plist,
// XML or binary. A plist that names no identifier is an error.
func (d *downloadedInstallers) parseInfo(ctx context.Context, data []byte) (bundleInfo, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		x, err := d.plist(ctx, data)
		if err != nil {
			return bundleInfo{}, err
		}
		data = x
	}
	s, err := plistStrings(data)
	if err != nil {
		return bundleInfo{}, err
	}
	info := bundleInfo{id: s["CFBundleIdentifier"], version: s["CFBundleShortVersionString"], build: s["CFBundleVersion"]}
	if info.id == "" {
		return bundleInfo{}, errors.New("the Info.plist names no CFBundleIdentifier")
	}
	return info, nil
}

// plutilXML converts a binary plist to XML with macOS's plutil.
func plutilXML(ctx context.Context, data []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", "-")
	cmd.WaitDelay = toolWaitDelay
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plutil could not read a binary plist: %w", err)
	}
	return out, nil
}

// plistStrings returns the string values of an XML plist's top-level
// dictionary, by key. A plist whose top level is not a dictionary is
// an error.
func plistStrings(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	inDict := false
	for !inDict {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not a plist dictionary: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		depth++
		switch {
		case depth == 1 && se.Name.Local == "plist":
		case depth == 2 && se.Name.Local == "dict":
			inDict = true
		default:
			return nil, errors.New("not a plist dictionary")
		}
	}
	out := map[string]string{}
	key := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not a plist dictionary: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			var text string
			if t.Name.Local == "key" || t.Name.Local == "string" {
				if err := dec.DecodeElement(&text, &t); err != nil {
					return nil, err
				}
			} else if err := dec.Skip(); err != nil {
				return nil, err
			}
			switch {
			case t.Name.Local == "key":
				key = text
				continue
			case t.Name.Local == "string" && key != "":
				out[key] = text
			}
			key = ""
		}
	}
}

// newerThan reports whether the zipped app's version is higher than
// the installed app's; known is false when the versions cannot be
// compared.
func (z bundleInfo) newerThan(installed bundleInfo) (newer, known bool) {
	c, ok := compareAppVersion(z.version, installed.version)
	if !ok {
		return false, false
	}
	if c == 0 && z.build != installed.build {
		if c, ok = compareAppVersion(z.build, installed.build); !ok {
			return false, false
		}
	}
	return c > 0, true
}

// compareAppVersion compares dotted numeric versions ("2.19675.0");
// ok is false when either is anything else. Missing parts count as 0.
func compareAppVersion(a, b string) (int, bool) {
	pa, oka := versionParts(a)
	pb, okb := versionParts(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range max(len(pa), len(pb)) {
		var x, y uint64
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x > y {
				return 1, true
			}
			return -1, true
		}
	}
	return 0, true
}

func versionParts(v string) ([]uint64, bool) {
	if v == "" {
		return nil, false
	}
	var parts []uint64
	for p := range strings.SplitSeq(v, ".") {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// nameKey is how a download's name and an app's name are compared: the
// words of the name, folded and run together, less those that say
// nothing about which app it is: numbers, a v and a number, and
// noiseWords. A word ending in "installer" or "setup" loses the
// ending. "Slack-4.41.105-macOS" and "Slack" both read "slack",
// "ChatGPTInstaller" and "ChatGPT" both read "chatgpt".
func nameKey(name string) string {
	var b strings.Builder
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		w = foldName(w)
		for _, suffix := range []string{"installer", "setup"} {
			if rest, ok := strings.CutSuffix(w, suffix); ok && rest != "" {
				w = rest
			}
		}
		if noiseWords[w] || isNumber(w) || (strings.HasPrefix(w, "v") && isNumber(w[1:])) {
			continue
		}
		b.WriteString(w)
	}
	return b.String()
}

// versionInName is a dotted version in a file name, after an optional v.
var versionInName = regexp.MustCompile(`(?:^|[^0-9A-Za-z.])[vV]?([0-9]+(?:\.[0-9]+)+)(?:$|[^0-9A-Za-z.])`)

// nameVersion is the first dotted version a download's name spells
// ("Slack-4.41.105-macOS" spells 4.41.105), or "".
func nameVersion(name string) string {
	if m := versionInName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

func isNumber(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}
