package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// The page transcript is what the scan page is checked against: the
// events of this package's Server over a synthetic catalog, in named
// sections, each opened by a {"section": name} line. Refresh it after
// a deliberate change to the protocol or to the fixture:
//
//	go test ./internal/protocol -run TestPageTranscript -update
var update = flag.Bool("update", false, "rewrite testdata/page-transcript.jsonl")

const (
	pageTranscriptFile = "testdata/page-transcript.jsonl"

	pageHome    = "/Users/dev"
	pageSelf    = pageHome + "/Applications/Regrow.app/Contents/MacOS/regrow-engine"
	pageStaging = pageHome + "/.local/state/regrow/staging"

	// What the ids that differ from run to run are rewritten to.
	pageNonce       = "3fa9c1d2"
	pagePlanDefault = "6f1d2c8a94b3e07d5a61c2f8930be4d7"
	pagePlanAll     = "b82e41f7c09d3a65e1f4a7c20d9358b6"
)

// pageSections are the transcript's sections, in file order.
var pageSections = []string{
	"hello", "scan", "scan-errors", "plan-default", "plan-all", "execute", "execute-trash-failed",
	"tick", "tick-short", "tick-steady", "tick-locked",
}

var (
	pageScanAt = time.Date(2026, 10, 9, 9, 12, 0, 0, time.UTC)
	pageTickAt = pageScanAt.Add(-2 * time.Minute)
)

func gb(n float64) int64 { return int64(n*(1<<30)) &^ 4095 }
func mb(n float64) int64 { return int64(n*(1<<20)) &^ 4095 }

// used is a last-used time the given hours before the scan.
func used(hours int) time.Time {
	return pageScanAt.Add(-time.Duration(hours)*time.Hour - 17*time.Minute)
}

func pageSample(at time.Time, free int64) headroom.Sample {
	return headroom.Sample{At: at, Total: gb(926.4), Free: free, Purgeable: gb(21.4), SwapUsed: gb(3.25)}
}

// pageRow is one rule of the synthetic catalog with what its scan
// finds.
type pageRow struct {
	rule  engine.Rule
	items []engine.Item
	took  time.Duration
	// fails is the rule's error in the scan-errors section, failTook
	// how long it took to fail.
	fails    string
	failTook time.Duration
}

func pageRows() []pageRow {
	darwin := func(paths ...string) map[string][]engine.PathEntry {
		entries := make([]engine.PathEntry, len(paths))
		for i, p := range paths {
			entries[i] = engine.PathEntry{Path: p}
		}
		return map[string][]engine.PathEntry{"darwin": entries}
	}
	dir := func(rel string, bytes int64, hours int) engine.Item {
		return engine.Item{Path: pageHome + "/" + rel, Bytes: bytes, LastUsed: used(hours)}
	}
	unreadable := func(rel string) engine.Item {
		return engine.Item{Path: pageHome + "/" + rel, Partial: true}
	}
	atLeast := func(rel string, bytes int64, hours int) engine.Item {
		it := dir(rel, bytes, hours)
		it.Partial = true
		return it
	}
	tool := func(label, arg string, bytes int64, hours int) engine.Item {
		return engine.Item{Label: label, Arg: arg, Bytes: bytes, LastUsed: used(hours)}
	}
	const (
		day         = 24
		month       = 30 * day
		derivedData = "Library/Developer/Xcode/DerivedData/"
		dockerDown  = "tool gave no answer within 2m0s"
	)
	return []pageRow{
		{
			rule: engine.Rule{ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Caches/go-build"), NativeCommand: engine.Argv{"go", "clean", "-cache"},
				Regen: engine.Regen{Story: "Repopulated automatically by the next go build.", Cost: "next builds slower once; no network"}},
			items: []engine.Item{dir("Library/Caches/go-build", gb(58.3), 0)},
			took:  3900 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "yarn-cache", Title: "Yarn cache", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Caches/Yarn"), NativeCommand: engine.Argv{"yarn", "cache", "clean"},
				Regen: engine.Regen{Story: "Yarn re-downloads package tarballs on the next install.", Cost: "next installs re-download; network"}},
			items: []engine.Item{dir("Library/Caches/Yarn", gb(3.2), 6*day)},
			took:  420 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "npm-cache", Title: "npm cache", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/.npm"), NativeCommand: engine.Argv{"npm", "cache", "clean", "--force"},
				Regen: engine.Regen{Story: "npm re-fetches packages into the cache as installs need them.", Cost: "next installs re-download; network"}},
			items: []engine.Item{dir(".npm", mb(742), 20)},
			took:  96 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "brew-cache", Title: "Homebrew cache and old versions", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Caches/Homebrew", "~/Library/Logs/Homebrew"), NativeCommand: engine.Argv{"brew", "cleanup", "-s", "--prune=all"},
				Regen: engine.Regen{Story: "Homebrew re-downloads bottles on the next install or upgrade.", Cost: "next upgrades re-download; network"}},
			items: []engine.Item{dir("Library/Caches/Homebrew", mb(388), 22*day), dir("Library/Logs/Homebrew", mb(24), 22*day)},
			took:  120 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "pip-cache", Title: "pip cache", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Caches/pip"), NativeCommand: engine.Argv{"pip3", "cache", "purge"},
				Regen: engine.Regen{Story: "pip re-downloads wheels as installs need them.", Cost: "next installs re-download; network"}},
			items: []engine.Item{dir("Library/Caches/pip", mb(96), 2*month)},
			took:  14 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "pnpm-store", Title: "pnpm store (unreferenced packages)", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/pnpm/store"), NativeCommand: engine.Argv{"pnpm", "store", "prune"},
				Regen: engine.Regen{Story: "Only unreferenced packages are pruned; anything needed re-downloads.", Cost: "re-download if an old project resurfaces; network"}},
			items: []engine.Item{dir("Library/pnpm/store", gb(1.9), 5*month)},
			took:  310 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "sim-runtimes", Title: "Simulator runtimes", Category: "xcode", Risk: engine.RiskSafe, Sudo: true,
				ToolQuery: "simctl-runtimes", NativeCommand: engine.Argv{"xcrun", "simctl", "runtime", "delete", "{arg}"},
				Note:  "Existing simulators of a deleted runtime stop booting until it is downloaded again.",
				Regen: engine.Regen{Story: "Xcode downloads a runtime again on demand (Settings ▸ Components).", Cost: "a download of several GiB per runtime"}},
			items: []engine.Item{
				tool("iOS 17.5 (21F79)", "9C4E2A6B-1F3D-4B7A-8E52-0D6C3A1F7B90", gb(7.4), 4*month),
				tool("watchOS 10.5 (21T575)", "2B8D5F1C-7A04-4E39-B6C1-5E9F0A3D8C27", gb(3.9), 7*month),
			},
			took:     1150 * time.Millisecond,
			fails:    "simctl runtime list: exit status 72",
			failTook: 380 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "xcode-derived-data", Title: "Xcode DerivedData", Category: "xcode", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Developer/Xcode/DerivedData/*"),
				Regen: engine.Regen{Story: "Xcode rebuilds intermediates and re-indexes on the next build.", Cost: "first build per project slower; no network"}},
			items: []engine.Item{
				dir(derivedData+"Lighthouse-bkqzrjwhtnpafydgmsoexvlcuibh", gb(4.1), 20),
				dir(derivedData+"TideTable-fhwmzqoakcnyvjtrdlbxgeipsuhw", gb(2.6), 9*day),
				dir(derivedData+"Orchard-czrpjvelmqkuaythsnwgdxobifcb", mb(884), 36*day),
				dir(derivedData+"PaperKite-gmtdyxbnqzhfowkcvjlsaeurpiyd", mb(312), 8*month),
			},
			took: 780 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "node-modules-dirs", Title: "node_modules in project trees", Category: "dev-caches", Risk: engine.RiskCaution,
				Discover: &engine.Discover{Roots: []string{"~/code"}, Name: "node_modules", MaxDepth: 10},
				Note:     "Pick projects you are not working in: reinstalling is one command but takes minutes each.",
				Regen:    engine.Regen{Story: "npm, yarn or pnpm install recreates it from the project's lockfile.", Cost: "minutes per project; network"}},
			items: []engine.Item{
				dir("code/lighthouse-web/node_modules", gb(1.8), 0),
				dir("code/tidetable/node_modules", gb(1.1), 20),
				dir("code/orchard-admin/node_modules", mb(964), 4*day),
				dir("code/orchard-admin/packages/ui/node_modules", mb(212), 4*day),
				dir("code/paperkite/node_modules", mb(688), 22*day),
				dir("code/harbor-api/node_modules", mb(540), 43*day),
				unreadable("code/vault-ui/node_modules"),
				atLeast("code/atlas-docs/node_modules", gb(2.2), 2*month),
				dir("code/playground/vite-spike/node_modules", mb(402), 3*month),
				dir("code/playground/three-demo/node_modules", mb(371), 11*month),
				dir("code/archive/harbor-2023/node_modules", gb(1.3), 14*month),
				unreadable("code/archive/tidepool-spike/node_modules"),
			},
			took: 6400 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "archived-projects", Title: "Archived project folders", Category: "projects", Risk: engine.RiskCaution,
				Paths: darwin("~/code/archive/*"),
				Note:  "Check that each project is pushed somewhere before it goes.",
				Regen: engine.Regen{Story: "Cloned again from its remote, if it has one; work that was never pushed is gone.", Cost: "a clone per project; whatever was never pushed"}},
			items: []engine.Item{
				dir("code/archive/harbor-2023", gb(6.4), 14*month),
				atLeast("code/archive/tidepool-spike", gb(2.9), 25*month),
				dir("code/archive/kite-prototype", mb(730), 19*month),
			},
			took: 2300 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "docker-volumes-named", Title: "Docker dangling named volumes", Category: "docker", Risk: engine.RiskCaution,
				ToolQuery: "docker-volumes-named", NativeCommand: engine.Argv{"docker", "volume", "rm", "{arg}"}, PreAction: engine.PreActionVolumeExport,
				Note:  "Named volumes hold real data. Each one is exported to a tarball in regrow staging before it is removed.",
				Regen: engine.Regen{Story: "Not regenerable: the tarball in regrow staging is the recovery copy (docker volume create, then untar).", Cost: "data is gone for good once staging is cleared"}},
			items: []engine.Item{
				tool("harbor_pgdata (compose project harbor)", "harbor_pgdata", gb(2.3), 5*month),
				tool("tidetable_redis (compose project tidetable)", "tidetable_redis", mb(184), 8*month),
				tool("scratch_minio", "scratch_minio", gb(5.6), 13*month),
			},
			took:     1425 * time.Millisecond,
			fails:    dockerDown,
			failTook: 2 * time.Minute,
		},
		{
			rule: engine.Rule{ID: "trash-empty", Title: "Empty Trash", Category: "macos", Risk: engine.RiskCaution,
				Paths: darwin("~/.Trash"), NativeCommand: engine.Argv{"osascript", "-e", `tell application "Finder" to empty trash`}, EmptiesTrash: true,
				Note:  "Permanent: the point of no return for everything regrow, and you, moved to the Trash before.",
				Regen: engine.Regen{Story: "Not regenerable: the Trash is the undo buffer.", Cost: "contents unrecoverable"}},
			items: []engine.Item{dir(".Trash", gb(6.3), 20)},
			took:  260 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "ios-backups", Title: "Local iOS device backups", Category: "macos", Risk: engine.RiskSurfaceOnly,
				Paths: darwin("~/Library/Application Support/MobileSync/Backup/*"),
				Note:  "A backup may be the only copy of a device. Delete one in Finder ▸ (device) ▸ Manage Backups.",
				Regen: engine.Regen{Story: "A new backup is made the next time the device is connected and backed up.", Cost: "none, while the device still works"}},
			items: []engine.Item{
				dir("Library/Application Support/MobileSync/Backup/00008110-000A1C2E3F4B801E", gb(38.6), 22*day),
				dir("Library/Application Support/MobileSync/Backup/00008030-001D7A9C0E81402E", gb(12.2), 16*month),
			},
			took: 180 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "docker-volumes-kept", Title: "Docker volumes (kept)", Category: "docker", Risk: engine.RiskSurfaceOnly,
				ToolQuery: "docker-volumes-kept",
				Note:      "Each row says why it is kept. Stop the container or edit the keep-list, then scan again.",
				Regen:     engine.Regen{Story: "regrow never deletes these.", Cost: "none"}},
			items: []engine.Item{
				tool("harbor_pgdata_live: mounted by a running container", "harbor_pgdata_live", gb(4.4), 0),
				tool("ledger_backups: on the keep-list", "ledger_backups", gb(1.1), 3*month),
			},
			took:     1420 * time.Millisecond,
			fails:    dockerDown,
			failTook: 2 * time.Minute,
		},
		{
			rule: engine.Rule{ID: "apfs-purgeable", Title: "APFS purgeable space", Category: engine.CategoryPhantomSpace, Risk: engine.RiskSurfaceOnly,
				ToolQuery: "apfs-purgeable",
				Note:      "Finder counts purgeable space as free, df does not. macOS frees it by itself when the disk fills.",
				Regen:     engine.Regen{Story: "Nothing to regenerate: macOS frees purgeable space on its own under pressure.", Cost: "none"}},
			items: []engine.Item{{Label: "Purgeable space on Macintosh HD", Bytes: gb(21.4)}},
			took:  41 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "cargo-registry", Title: "Cargo registry cache", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/.cargo/registry/cache", "~/.cargo/registry/src"),
				Regen: engine.Regen{Story: "cargo downloads crates again on the next build.", Cost: "next builds re-download; network"}},
			took: 2 * time.Millisecond,
		},
		{
			rule: engine.Rule{ID: "playwright-cache", Title: "Playwright browsers", Category: "dev-caches", Risk: engine.RiskSafe,
				Paths: darwin("~/Library/Caches/ms-playwright"),
				Regen: engine.Regen{Story: "npx playwright install downloads the browsers again.", Cost: "a download of a few hundred MiB; network"}},
			took: 3 * time.Millisecond,
		},
	}
}

// pageScan reports the rows in the order their scans end, as a real
// scan does. failing reports the rows that can fail as failed.
func pageScan(rows []pageRow, failing bool) ScanFunc {
	type result struct {
		index int
		f     engine.Finding
		took  time.Duration
	}
	results := make([]result, len(rows))
	for i, r := range rows {
		results[i] = result{i, engine.Finding{Rule: r.rule, Items: r.items}, r.took}
		if failing && r.fails != "" {
			results[i] = result{i, engine.Finding{Rule: r.rule, Err: r.fails}, r.failTook}
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].took < results[j].took })
	return func(_ context.Context, _ []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for _, r := range results {
			emit(r.index, r.f, r.took)
		}
	}
}

// stopwatch is a run's clock: every action takes a fixed time from a
// repeating pattern, so journal times differ from line to line and are
// the same in every recording.
type stopwatch struct {
	mu    sync.Mutex
	at    time.Time
	calls int
}

var actionTimes = []time.Duration{4 * time.Second, time.Second, 2 * time.Second, 9 * time.Second, time.Second, 3 * time.Second, 6 * time.Second}

// Now is called twice per action, for its start line and for its done
// or fail line.
func (w *stopwatch) Now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	at := w.at
	if w.calls%2 == 0 {
		w.at = w.at.Add(actionTimes[(w.calls/2)%len(actionTimes)])
	}
	w.calls++
	return at
}

func (w *stopwatch) peek() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.at
}

// pageFinder stands in for Finder and moves nothing. A second item of
// the same name gets the time in its name, as Finder renames it, and
// the one path Finder "cannot take" goes to staging.
type pageFinder struct {
	clock  *stopwatch
	runID  string
	staged string
	seen   map[string]bool
}

func (f *pageFinder) Move(_ context.Context, path string) (trash.Receipt, error) {
	base := filepath.Base(path)
	if path == f.staged {
		return trash.Receipt{Original: path, To: pageStaging + "/" + f.runID + "/" + base, Method: trash.MethodStaging}, nil
	}
	to := pageHome + "/.Trash/" + base
	if f.seen[base] {
		to += " " + f.clock.peek().Format("15.04.05")
	}
	f.seen[base] = true
	return trash.Receipt{Original: path, To: to, Method: trash.MethodFinder}, nil
}

const pageBrewFailure = `exit status 1: Warning: Skipping harbor-cli: most recent version 2.4.1 not installed
Error: Could not cleanup old kegs! Fix your permissions on:
  /Users/dev/Library/Caches/Homebrew/downloads
Error: Permission denied @ apply2files - /Users/dev/Library/Caches/Homebrew/downloads/3fd1c0a9--harbor-cli-2.4.1.tar.gz`

const pageTrashFailure = "exit status 1: 31:42: execution error: Finder got an error: AppleEvent timed out. (-1712)"

// pageExecutor builds a run's executor with a Finder, a steward command
// runner and a volume export that touch nothing: they answer as the
// real ones would. Only the journal is real, under the test's temp
// state dir. trashFails makes Empty Trash fail.
func pageExecutor(t *testing.T, logPath, runID string, began time.Time, trashFails bool) (*executor.Executor, func(), error) {
	log, err := oplog.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	clock := &stopwatch{at: began}
	ex := &executor.Executor{
		Trash: &pageFinder{clock: clock, runID: runID, staged: pageHome + "/code/playground/three-demo/node_modules", seen: map[string]bool{}},
		Log:   log,
		Now:   clock.Now,
		PreActions: map[string]executor.PreAction{
			engine.PreActionVolumeExport: func(_ context.Context, a engine.Action) (*trash.Receipt, error) {
				return &trash.Receipt{
					Original: "docker volume " + a.ItemKey,
					To:       pageStaging + "/" + runID + "/docker-volume-" + a.ItemKey + ".tar",
					Method:   trash.MethodExport,
				}, nil
			},
		},
		RunNative: func(_ context.Context, argv []string) error {
			switch argv[0] {
			case "brew":
				return errors.New(pageBrewFailure)
			case "osascript":
				if trashFails {
					return errors.New(pageTrashFailure)
				}
			case "sudo", "pnpm":
				t.Errorf("the plan held a command the engine must skip: %v", argv)
			}
			return nil
		},
	}
	return ex, func() { _ = log.Close() }, nil
}

// until reads events up to and including the one that ends request re.
func (p *peer) until(re string) []event {
	p.t.Helper()
	var got []event
	for {
		e := p.next()
		got = append(got, e)
		if e.Re == re && (e.Event == "done" || e.Event == "error") {
			return got
		}
	}
}

func (c *fakeClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// pageRecording is the transcript being put together: sections of raw
// event lines.
type pageRecording struct {
	sections map[string][]string
}

func (r *pageRecording) add(section string, events []event) {
	for _, e := range events {
		r.sections[section] = append(r.sections[section], e.raw)
	}
}

// fix rewrites what differs from run to run.
func (r *pageRecording) fix(rewrite func(section, line string) string) {
	for section, lines := range r.sections {
		for i, line := range lines {
			lines[i] = rewrite(section, line)
		}
	}
}

func (r *pageRecording) lines() []string {
	var out []string
	for _, name := range pageSections {
		header, _ := json.Marshal(map[string]string{"section": name})
		out = append(out, string(header))
		out = append(out, r.sections[name]...)
	}
	return out
}

var (
	elapsedRe = regexp.MustCompile(`"elapsed_ms":\d+`)
	// The last digits of a forecast depend on whether the CPU fuses a
	// multiply and an add, so the transcript keeps six decimals.
	daysRe = regexp.MustCompile(`"days_to_full":(\d+\.\d{6})\d+`)
)

// recordPage plays the page's requests against the Server and returns
// the transcript.
func recordPage(t *testing.T) []string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	rows := pageRows()
	catalog := make([]engine.Rule, len(rows))
	for i, r := range rows {
		catalog[i] = r.rule
	}
	host := engine.Host{OS: "darwin", Version: "15.5", Home: pageHome}
	rec := &pageRecording{sections: map[string][]string{}}

	var (
		mu         sync.Mutex
		failing    bool
		trashFails bool
	)
	set := func(scanFails, emptyTrashFails bool) {
		mu.Lock()
		defer mu.Unlock()
		failing, trashFails = scanFails, emptyTrashFails
	}
	planAt, runAt := pageScanAt.Add(100*time.Second), pageScanAt.Add(125*time.Second)
	clock := &fakeClock{now: pageScanAt}
	srv := &Server{
		Version: "0.5.0",
		Host:    host,
		Catalog: catalog,
		Scan: func(ctx context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
			mu.Lock()
			run := pageScan(rows, failing)
			mu.Unlock()
			run(ctx, rules, emit)
		},
		Account:  engine.Account,
		Headroom: func(context.Context) (headroom.Sample, error) { return pageSample(pageScanAt, gb(24.3)), nil },
		NewExecutor: func(runID string) (*executor.Executor, func(), error) {
			mu.Lock()
			fails := trashFails
			mu.Unlock()
			return pageExecutor(t, logPath, runID, runAt, fails)
		},
		ProbeFDA: func() FDA { return FDAGranted },
		Now:      clock.Now,
		Self:     pageSelf,
		LookPath: func(file string) (string, error) {
			if file == "pnpm" {
				return "", fmt.Errorf("exec: %q: executable file not found in $PATH", file)
			}
			return pageHome + "/bin/" + file, nil
		},
	}
	p := start(t, srv)
	rec.add("hello", p.expect("hello"))

	scan := func() []event {
		p.send(`{"type":"scan","id":"REQ"}`)
		return p.until("REQ")
	}
	plan := func(scanID string, atoms []string) []event {
		req := map[string]any{"type": "plan", "id": "REQ", "scan_id": scanID}
		if atoms != nil {
			req["select"] = atoms
		}
		line, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		p.send(string(line))
		return p.until("REQ")
	}
	execute := func(planID string) []event {
		p.send(`{"type":"execute","id":"REQ","plan_id":"` + planID + `"}`)
		return p.until("REQ")
	}
	scanIDOf := func(events []event) string {
		for _, e := range events {
			if e.Event == "start" {
				return e.ScanID
			}
		}
		t.Fatal("the scan sent no start event")
		return ""
	}
	// offered is every atom of every select a scan handed out.
	offered := func(events []event) []string {
		atoms := []string{}
		for _, e := range events {
			atoms = append(atoms, e.Select...)
		}
		return atoms
	}

	first := scan()
	rec.add("scan", first)
	scanID := scanIDOf(first)
	nonce := strings.TrimSuffix(scanID, "-1")

	clock.set(pageScanAt.Add(25 * time.Second))
	planDefault := plan(scanID, nil)
	rec.add("plan-default", planDefault)

	clock.set(planAt)
	planAll := plan(scanID, offered(first))
	rec.add("plan-all", planAll)
	clock.set(runAt)
	rec.add("execute", execute(planAll[0].PlanID))

	set(true, false)
	clock.set(pageScanAt)
	rec.add("scan-errors", scan())

	// The same plan over a fresh scan, run while Finder fails to empty
	// the Trash: the one way a run skips actions.
	set(false, true)
	third := scan()
	clock.set(planAt)
	planAgain := plan(scanIDOf(third), offered(third))
	clock.set(runAt)
	rec.add("execute-trash-failed", execute(planAgain[0].PlanID))

	history := func(step time.Duration, freeGiB ...float64) []headroom.Sample {
		samples := make([]headroom.Sample, len(freeGiB))
		for i, free := range freeGiB {
			at := pageTickAt.Add(-15*time.Minute - time.Duration(len(freeGiB)-1-i)*step)
			samples[i] = pageSample(at, gb(free))
		}
		return samples
	}
	falling := func(from, perStep float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = from - perStep*float64(i)
		}
		return out
	}
	ticks := []struct {
		section  string
		history  []headroom.Sample
		autotrim bool
		staged   int64
	}{
		// Half a day falling 0.6 GiB an hour, then 12 GiB gone in the
		// last quarter of an hour.
		{"tick", history(time.Hour, falling(43.8, 0.6, 13)...), false, mb(371) + gb(2.3) + mb(184) + gb(5.6)},
		{"tick-short", history(time.Hour, 24.4), false, 0},
		{"tick-steady", history(time.Hour, 24.0, 24.1, 24.1, 24.2, 24.2, 24.3, 24.3, 24.3), false, 0},
		{"tick-locked", history(time.Hour, falling(24.9, 0.06, 10)...), true, 0},
	}
	for _, tk := range ticks {
		rec.add(tk.section, recordPageTick(t, host, tk.history, tk.autotrim, tk.staged))
	}

	planIDs := map[string]string{
		planDefault[0].PlanID: pagePlanDefault,
		planAll[0].PlanID:     pagePlanAll,
		planAgain[0].PlanID:   pagePlanAll,
	}
	fixIDs := func(line string) string {
		line = strings.ReplaceAll(line, `"scan_id":"`+nonce+"-", `"scan_id":"`+pageNonce+"-")
		for id, fixed := range planIDs {
			stamp := id[:len(id)-len(fixed)]
			line = strings.ReplaceAll(line, id, stamp+fixed)
		}
		return line
	}
	rec.fix(func(section, line string) string {
		line = fixIDs(line)
		line = daysRe.ReplaceAllString(line, `"days_to_full":$1`)
		// A scan ends a moment after its slowest rule.
		elapsed := `"elapsed_ms":6412`
		if section == "scan-errors" {
			elapsed = `"elapsed_ms":120004`
		}
		return elapsedRe.ReplaceAllString(line, elapsed)
	})

	// execute-trash-failed replays against the plan the page drew from
	// plan-all, so the two plans must be one.
	if again := fixIDs(planAgain[0].raw); again != rec.sections["plan-all"][0] {
		t.Errorf("the plan execute-trash-failed ran is not plan-all's:\n%s\n%s", again, rec.sections["plan-all"][0])
	}
	for _, dir := range []string{state, os.TempDir()} {
		if text := strings.Join(rec.lines(), "\n"); strings.Contains(text, dir) {
			t.Errorf("the transcript holds a path of this machine under %s", dir)
		}
	}
	return rec.lines()
}

// recordPageTick is one tick of the real watch loop over a seeded
// history in a temp state dir. Its catalog's prune rule has never been
// run by hand, so autotrim is locked; nothing it could run is wired.
func recordPageTick(t *testing.T, host engine.Host, history []headroom.Sample, autotrim bool, staged int64) []event {
	t.Helper()
	catalog := []engine.Rule{{
		ID: engine.RuleGoBuildCache, Title: "Go build cache", Category: "dev-caches", Risk: engine.RiskSafe,
		NativeCommand: engine.Argv{"go", "clean", "-cache"},
		Prune: &engine.Prune{MinAge: engine.Duration(48 * time.Hour), KeepUnder: engine.ByteSize(15 << 30),
			UnlessRunning: []string{"go", "compile", "link"}},
	}}
	state := t.TempDir()
	for _, s := range history {
		if err := headroom.Append(filepath.Join(state, headroom.FileName), s); err != nil {
			t.Fatal(err)
		}
	}
	now := pageSample(pageTickAt, gb(24.3))
	refuse := func(what string) error {
		t.Errorf("the tick reached %s: nothing may run", what)
		return errors.New("refused by the test")
	}
	ap := &autopilot.Autopilot{
		StateDir:  state,
		OplogPath: filepath.Join(state, "oplog.jsonl"),
		Host:      host,
		Catalog:   catalog,
		Take:      func(context.Context) (headroom.Sample, error) { return now, nil },
		FreeSpace: func() (int64, error) { return now.Free, nil },
		Processes: func(context.Context) ([]headroom.Process, error) {
			return []headroom.Process{
				{PID: 391, PPID: 1, Name: "WindowServer", RSS: gb(1.2)},
				{PID: 48211, PPID: 48190, Name: "ld", RSS: gb(14.2)},
				{PID: 5120, PPID: 1, Name: "Simulator", RSS: gb(2.7)},
			}, nil
		},
		GoEnv:     func(context.Context, string) (string, error) { return "", refuse("go env") },
		RunNative: func(_ context.Context, argv []string) error { return refuse(strings.Join(argv, " ")) },
		Now:       func() time.Time { return now.At },
	}
	srv := &Server{
		Host:     host,
		Tick:     ap.Tick,
		ProbeFDA: func() FDA { return FDAGranted },
	}
	if staged > 0 {
		srv.Staged = func(context.Context) int64 { return staged }
	}
	p := start(t, srv)
	p.expect("hello")
	if autotrim {
		p.send(`{"type":"tick","id":"REQ","autotrim":true}`)
	} else {
		p.send(`{"type":"tick","id":"REQ"}`)
	}
	return p.until("REQ")
}

func TestPageTranscript(t *testing.T) {
	got := recordPage(t)
	if t.Failed() {
		return
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(pageTranscriptFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pageTranscriptFile, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(pageTranscriptFile)
	if err != nil {
		t.Fatalf("read the transcript (run with -update to create it): %v", err)
	}
	want := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	for i := 0; i < len(got) || i < len(want); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Fatalf("line %d of %s differs from what the engine sends now (run with -update after a deliberate change):\n got %s\nwant %s", i+1, pageTranscriptFile, g, w)
		}
	}
	checkPageTranscript(t, want)
}

// pageLine is a transcript line decoded for the checks below.
type pageLine struct {
	event
	Section     string           `json:"section"`
	DaysToFull  *float64         `json:"days_to_full"`
	NoForecast  string           `json:"no_forecast"`
	Alerts      []headroom.Alert `json:"alerts"`
	StagedBytes int64            `json:"staged_bytes"`
}

// checkPageTranscript holds the file to what the page has to draw:
// every section, and in them every kind of row. A fixture change that
// drops one fails here, not in a screenshot.
func checkPageTranscript(t *testing.T, lines []string) {
	t.Helper()
	sections := map[string][]pageLine{}
	var order []string
	for i, raw := range lines {
		var l pageLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		l.raw = raw
		if l.Section != "" {
			order = append(order, l.Section)
			continue
		}
		if len(order) == 0 || l.Event == "" {
			t.Fatalf("line %d is neither a section header nor an event of one: %s", i+1, raw)
		}
		if l.Event != "hello" && l.Re != "REQ" {
			t.Errorf("line %d answers request %q, want the literal REQ", i+1, l.Re)
		}
		sections[order[len(order)-1]] = append(sections[order[len(order)-1]], l)
	}
	if strings.Join(order, " ") != strings.Join(pageSections, " ") {
		t.Fatalf("sections = %v, want %v", order, pageSections)
	}
	for _, name := range pageSections {
		if len(sections[name]) == 0 {
			t.Fatalf("section %s is empty", name)
		}
	}
	text := strings.Join(lines, "\n")
	if all, synthetic := strings.Count(text, "/Users/"), strings.Count(text, pageHome+"/"); all != synthetic {
		t.Errorf("%d of %d home paths are outside %s", all-synthetic, all, pageHome)
	}
	for _, outside := range []string{"/var/folders", "/private/", "/tmp/"} {
		if strings.Contains(text, outside) {
			t.Errorf("the transcript holds a path under %s", outside)
		}
	}
	names := func(section string) string {
		var out []string
		for _, l := range sections[section] {
			out = append(out, l.Event)
		}
		return strings.Join(out, " ")
	}
	need := func(section, what string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("section %s: want %s", section, what)
		}
	}

	need("hello", "one hello with the Full Disk Access state", names("hello") == "hello" && sections["hello"][0].FDA == FDAGranted)

	// The scan.
	rows := len(pageRows())
	wantScan := "headroom start" + strings.Repeat(" finding", rows) + " summary done"
	need("scan", "headroom through done with every rule's finding", names("scan") == wantScan)
	need("scan-errors", "headroom through done with every rule's finding", names("scan-errors") == wantScan)
	var summary, errorSummary pageLine
	findings := map[string]pageLine{}
	for _, l := range sections["scan"] {
		switch l.Event {
		case "finding":
			findings[l.Finding.Rule.ID] = l
		case "summary":
			summary = l
		}
	}
	count := func(match func(l pageLine) bool) int {
		n := 0
		for _, l := range findings {
			if match(l) {
				n++
			}
		}
		return n
	}
	wholeRule := func(l pageLine) bool {
		f := l.Finding
		return f.Rule.Risk == engine.RiskSafe && len(f.Rule.NativeCommand) > 0 && !f.Rule.NativeCommand.PerItem() &&
			len(l.Select) == 1 && l.Select[0] == f.Rule.ID
	}
	buckets := map[string]bool{}
	for _, l := range findings {
		buckets[l.Bucket] = true
	}
	need("scan", "a finding in each of the four buckets", len(buckets) == 4)
	need("scan", "a safe whole-rule command over 50 GiB", count(func(l pageLine) bool { return wholeRule(l) && l.Finding.TotalBytes() > 50<<30 }) == 1)
	need("scan", "several safe whole-rule commands under 1 GiB", count(func(l pageLine) bool { return wholeRule(l) && l.Finding.TotalBytes() < 1<<30 }) >= 3)
	need("scan", "a safe rule that needs administrator rights", count(func(l pageLine) bool {
		return l.Finding.Rule.Risk == engine.RiskSafe && l.Finding.Rule.Sudo && len(l.Select) > 0
	}) == 1)
	need("scan", "a safe Trash rule with four items, each with its atom", count(func(l pageLine) bool {
		f := l.Finding
		return f.Rule.Risk == engine.RiskSafe && l.Bucket == engine.BucketAfterTrash && len(f.Items) == 4 && len(l.Select) == 4
	}) == 1)
	need("scan", "a caution Trash rule with a dozen items, two unreadable and one a lower bound", count(func(l pageLine) bool {
		f := l.Finding
		var unreadable, lowerBound int
		for _, it := range f.Items {
			switch {
			case it.Unreadable():
				unreadable++
			case it.Partial:
				lowerBound++
			}
		}
		return f.Rule.Risk == engine.RiskCaution && l.Bucket == engine.BucketAfterTrash && len(f.Items) == 12 &&
			unreadable == 2 && lowerBound == 1 && len(l.Select) == 10
	}) == 1)
	need("scan", "a caution per-item command with a pre_action", count(func(l pageLine) bool {
		r := l.Finding.Rule
		return r.Risk == engine.RiskCaution && r.NativeCommand.PerItem() && r.PreAction != "" && len(l.Select) == len(l.Finding.Items)
	}) == 1)
	need("scan", "a rule that empties the Trash", count(func(l pageLine) bool { return l.Finding.Rule.EmptiesTrash }) == 1)
	need("scan", "two surface-only rules with a note and nothing to tick", count(func(l pageLine) bool {
		return l.Bucket == engine.BucketShownOnly && l.Finding.Rule.Note != "" && l.Select == nil && len(l.Finding.Items) > 0
	}) == 2)
	need("scan", "one phantom-space rule", count(func(l pageLine) bool { return l.Bucket == engine.BucketMacOSManaged }) == 1)
	need("scan", "two rules that found nothing", count(func(l pageLine) bool {
		return len(l.Finding.Items) == 0 && l.Finding.Err == "" && l.Select == nil
	}) == 2)
	nested := 0
	for _, l := range findings {
		for _, it := range l.Finding.Items {
			id := engine.ItemID(l.Finding.Rule.ID, it.Key)
			if own, ok := summary.Exclusive[id]; !ok {
				t.Errorf("section scan: the summary has no exclusive bytes for %s", id)
			} else if own != it.Bytes && l.Finding.Rule.Risk == engine.RiskCaution && l.Bucket == engine.BucketAfterTrash {
				nested++
			}
		}
	}
	need("scan", "a caution Trash item whose own bytes differ from its size", nested > 0)

	failed := 0
	for _, l := range sections["scan-errors"] {
		switch {
		case l.Event == "summary":
			errorSummary = l
		case l.Event == "finding" && l.Finding.Err != "":
			failed++
			need("scan-errors", "a failed rule with no items and nothing to tick", len(l.Finding.Items) == 0 && l.Select == nil)
		}
	}
	need("scan-errors", "three rules failing", failed == 3)
	need("scan-errors", "totals that changed with them", summary.Totals != nil && errorSummary.Totals != nil &&
		errorSummary.Totals.FreesNow < summary.Totals.FreesNow && errorSummary.Totals.ShownOnly < summary.Totals.ShownOnly &&
		errorSummary.Totals.AfterTrash == summary.Totals.AfterTrash)

	// The plans.
	var offered []string
	for _, row := range pageRows() {
		offered = append(offered, findings[row.rule.ID].Select...)
	}
	sort.Strings(offered)
	skipped := func(plan *engine.Plan, part string) int {
		n := 0
		for _, s := range plan.Skipped {
			if strings.Contains(s.Reason, part) {
				n++
			}
		}
		return n
	}
	for _, name := range []string{"plan-default", "plan-all"} {
		if names(name) != "plan done" {
			t.Fatalf("section %s: events are %q, want a plan and its done", name, names(name))
		}
	}
	def, all := sections["plan-default"][0], sections["plan-all"][0]
	need("plan-default", "the default selection echoed: the safe rules that found something",
		strings.Join(def.Select, " ") == "brew-cache go-build-cache npm-cache pip-cache pnpm-store sim-runtimes xcode-derived-data yarn-cache")
	need("plan-default", "an expiry ten minutes after the plan was made", def.ExpiresAt.Equal(pageScanAt.Add(25*time.Second+10*time.Minute)))
	need("plan-default", "the administrator-rights skip naming this engine",
		skipped(def.Plan, "needs administrator rights — run `"+pageSelf+" clean sim-runtimes` in Terminal") == 1)
	need("plan-default", "the skip for a program that is not on the PATH", skipped(def.Plan, "`pnpm` is not on the PATH regrow was started with") == 1)
	need("plan-all", "every atom the scan handed out, sorted", strings.Join(all.Select, "\n") == strings.Join(offered, "\n"))
	need("plan-all", "both engine skips and an item inside another selected one",
		skipped(all.Plan, "needs administrator rights") == 1 && skipped(all.Plan, "is not on the PATH") == 1 && skipped(all.Plan, "inside archived-projects/") == 1)
	var includes, preActions int
	for _, a := range all.Plan.Actions {
		if len(a.Includes) > 0 {
			includes++
		}
		if a.PreAction != "" {
			preActions++
		}
	}
	need("plan-all", "Empty Trash first", len(all.Plan.Actions) > 0 && all.Plan.Actions[0].EmptiesTrash)
	need("plan-all", "an action that takes an unselected item with it", includes == 1)
	need("plan-all", "actions with a pre_action", preActions == 3)
	need("plan-all", "bytes that free now and bytes that go to the Trash", all.Totals != nil && all.Totals.FreesNow > 0 && all.Totals.AfterTrash > 0)

	// The runs.
	type run struct {
		starts, done, failed, multiLine, finder, staging, export, renamed int
		result                                                            *executor.Result
	}
	runOf := func(section string) run {
		var r run
		for _, l := range sections[section] {
			if l.Event == "done" {
				r.result = l.Result
				continue
			}
			e := l.Entry
			if e == nil || e.Run != all.PlanID {
				t.Errorf("section %s: a line that is not a journal entry of plan-all's run: %s", section, l.raw)
				continue
			}
			switch e.Event {
			case oplog.EventStart:
				r.starts++
			case oplog.EventDone:
				r.done++
			case oplog.EventFail:
				r.failed++
				if strings.Count(e.Error, "\n") >= 2 {
					r.multiLine++
				}
			}
			if e.Receipt != nil {
				switch e.Receipt.Method {
				case trash.MethodFinder:
					r.finder++
					if filepath.Base(e.Receipt.To) != filepath.Base(e.Receipt.Original) {
						r.renamed++
					}
				case trash.MethodStaging:
					r.staging++
				case trash.MethodExport:
					r.export++
				}
			}
		}
		return r
	}
	ran := runOf("execute")
	need("execute", "a start and an end for every action of plan-all", ran.starts == len(all.Plan.Actions) && ran.done+ran.failed == ran.starts)
	need("execute", "one steward command failed with a multi-line error", ran.failed == 1 && ran.multiLine == 1)
	need("execute", "Trash moves with a Finder receipt, some renamed by Finder", ran.finder >= 10 && ran.renamed > 0)
	need("execute", "one move into regrow staging", ran.staging == 1)
	need("execute", "volume exports", ran.export == 3)
	if res := ran.result; res == nil {
		t.Error("section execute: want a done with the run's result")
	} else {
		need("execute", "a result under plan-all's id with bytes freed now, in the Trash and in staging", res.RunID == all.PlanID &&
			res.Done == ran.done && res.Failed == 1 && res.TrashBytes > 0 && res.StagedBytes > 0 && res.Bytes > res.TrashBytes+res.StagedBytes &&
			len(res.Failures) == 1 && len(res.Skipped) == 0)
	}
	held := runOf("execute-trash-failed")
	if res := held.result; res == nil {
		t.Error("section execute-trash-failed: want a done with the run's result")
	} else {
		need("execute-trash-failed", "every Trash move skipped, none journaled, the steward commands still run",
			len(res.Skipped) == ran.finder+ran.staging && held.finder+held.staging == 0 && res.TrashBytes == 0 &&
				res.Failed == 2 && res.Done == ran.done-len(res.Skipped)-1 && held.export == 3)
	}

	// The ticks.
	tick := sections["tick"][0]
	acute, withTop := 0, 0
	for _, a := range tick.Alerts {
		if a.Acute {
			acute++
			if a.TopProcess != nil {
				withTop++
			}
		}
	}
	need("tick", "a tick and its done", names("tick") == "tick done")
	need("tick", "days to full and no reason against one", tick.DaysToFull != nil && tick.NoForecast == "")
	need("tick", "two alerts, one of them acute and naming the top process", len(tick.Alerts) == 2 && acute == 1 && withTop == 1)
	need("tick", "staged bytes", tick.StagedBytes > 0)
	for name, why := range map[string]string{"tick-short": headroom.ShortHistory, "tick-steady": headroom.NotFalling} {
		l := sections[name][0]
		need(name, "a tick with no forecast because of "+why, names(name) == "tick done" && l.NoForecast == why && l.DaysToFull == nil && len(l.Alerts) == 0)
	}
	need("tick-locked", "the tick event, then the autotrim_locked error",
		names("tick-locked") == "tick error" && sections["tick-locked"][1].Code == CodeAutotrimLocked)
}
