package autopilot

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

const gib = headroom.GiB

var start = time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)

// harness is one fake machine: a state dir under XDG_STATE_HOME, a Go
// build cache in a temp home, and every Autopilot seam under the
// test's control. The only command it will run is the planned find,
// and only against the fixture cache.
type harness struct {
	t     *testing.T
	ap    *Autopilot
	cache string
	size  int64 // physical bytes of one cache entry

	now   time.Time
	free  int64
	swap  int64
	procs []headroom.Process
	ran   [][]string
}

// Ages of the fixture's entries at start; min_age is 48h.
var entryAges = map[string]time.Duration{
	"00/e100-a": 100 * time.Hour,
	"3f/e72-d":  72 * time.Hour,
	"a7/e50-a":  50 * time.Hour,
	"a7/e47-d":  47 * time.Hour,
	"ff/e01-a":  time.Hour,
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "ho[me] *?")
	h := &harness{t: t, now: start, free: 40 * gib, cache: filepath.Join(home, "Library", "Caches", "go-build")}

	h.plant("README", "This directory holds cached build artifacts from the Go build system.\n", 200*time.Hour)
	h.plant("trim.txt", "1790000000\n", 200*time.Hour)
	for _, rel := range decoys {
		h.plant(rel, strings.Repeat("x", 4096), 200*time.Hour)
	}
	for rel, age := range entryAges {
		h.plant(rel, strings.Repeat("x", 4096), age)
	}
	usage, err := engine.MeasureCache(context.Background(), h.cache)
	if err != nil || usage.Files != len(entryAges) {
		t.Fatalf("fixture cache measures %+v, err %v", usage, err)
	}
	h.size = usage.Bytes / int64(usage.Files)

	h.ap = &Autopilot{
		StateDir:  filepath.Dir(logPath),
		OplogPath: logPath,
		Host:      engine.Host{OS: "darwin", Home: home},
		Volume:    root,
		Catalog: []engine.Rule{{
			ID: engine.RuleGoBuildCache, Title: "Go build cache", Category: "dev-caches", Risk: engine.RiskSafe,
			Paths:         map[string][]engine.PathEntry{"darwin": {{Path: "~/Library/Caches/go-build"}}},
			NativeCommand: engine.Argv{"go", "clean", "-cache"},
			Prune: &engine.Prune{
				MinAge: engine.Duration(48 * time.Hour), KeepUnder: engine.ByteSize(h.size),
				UnlessRunning: []string{"go", "compile", "link"},
			},
		}},
		Take: func(context.Context) (headroom.Sample, error) {
			return headroom.Sample{At: h.now, Total: 460 * gib, Free: h.free, SwapUsed: h.swap}, nil
		},
		FreeSpace: func() (int64, error) { return h.free, nil },
		Processes: func(context.Context) ([]headroom.Process, error) { return h.procs, nil },
		GoEnv:     func(context.Context, string) (string, error) { return h.cache, nil },
		Now:       func() time.Time { return h.now },
		RunNative: func(_ context.Context, argv []string) error {
			if len(argv) < 2 || argv[0] != "/usr/bin/find" || argv[1] != h.cache || !strings.HasPrefix(h.cache, root) {
				t.Fatalf("refusing to run %q: only the planned find on the fixture cache may run", argv)
			}
			h.ran = append(h.ran, argv)
			return exec.Command(argv[0], argv[1:]...).Run()
		},
	}
	return h
}

func (h *harness) rule() engine.Rule { return h.ap.Catalog[0] }

func (h *harness) plant(rel, body string, age time.Duration) {
	h.t.Helper()
	p := filepath.Join(h.cache, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	at := start.Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		h.t.Fatal(err)
	}
}

// entries lists the cache entries still on disk.
func (h *harness) entries() []string {
	h.t.Helper()
	var out []string
	for rel := range entryAges {
		if _, err := os.Lstat(filepath.Join(h.cache, rel)); err == nil {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func (h *harness) journal() []oplog.Entry {
	h.t.Helper()
	entries, err := oplog.Read(h.ap.OplogPath)
	if err != nil {
		h.t.Fatal(err)
	}
	return entries
}

func (h *harness) history() []headroom.Sample {
	h.t.Helper()
	s, err := headroom.Tail(filepath.Join(h.ap.StateDir, headroom.FileName), time.Time{})
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// openGate journals a completed prune, as one manual run would.
func (h *harness) openGate() {
	h.t.Helper()
	log, err := oplog.Open(h.ap.OplogPath)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, e := range []oplog.Entry{
		{Run: "manual", Seq: 1, Event: oplog.EventStart, RuleID: engine.RuleGoBuildCache, Kind: "prune"},
		{Run: "manual", Seq: 1, Event: oplog.EventDone, RuleID: engine.RuleGoBuildCache, Pruned: &oplog.Pruned{}},
	} {
		if err := log.Append(e); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		h.t.Fatal(err)
	}
}

var allEntries = []string{"00/e100-a", "3f/e72-d", "a7/e47-d", "a7/e50-a", "ff/e01-a"}

// decoys are older than every entry and must outlive every prune:
// another tool's files under a shared GOCACHE, and Go's fuzz cache.
var decoys = []string{"other/x-a", "fuzz/x-a"}

func (h *harness) decoysLeft() bool {
	for _, rel := range decoys {
		if _, err := os.Lstat(filepath.Join(h.cache, rel)); err != nil {
			return false
		}
	}
	return true
}

func TestGate(t *testing.T) {
	rule := engine.Rule{ID: engine.RuleGoBuildCache}
	prune := func(run, event string) oplog.Entry {
		e := oplog.Entry{Run: run, Seq: 1, Event: event, RuleID: rule.ID}
		if event == oplog.EventStart {
			e.Kind = "prune"
		}
		return e
	}
	tests := []struct {
		name    string
		entries []oplog.Entry
		open    bool
	}{
		{"empty journal", nil, false},
		{"prune started, never finished", []oplog.Entry{prune("r1", oplog.EventStart)}, false},
		{"prune failed", []oplog.Entry{prune("r1", oplog.EventStart), prune("r1", oplog.EventFail)}, false},
		{"only `go clean -cache` completed", []oplog.Entry{
			{Run: "r1", Seq: 1, Event: oplog.EventStart, RuleID: rule.ID, Kind: "native"},
			{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: rule.ID},
		}, false},
		{"a prune of another rule completed", []oplog.Entry{
			{Run: "r1", Seq: 1, Event: oplog.EventStart, RuleID: "other-cache", Kind: "prune"},
			{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "other-cache"},
		}, false},
		{"another run's done line", []oplog.Entry{prune("r1", oplog.EventStart), prune("r2", oplog.EventDone)}, false},
		{"prune completed", []oplog.Entry{prune("r1", oplog.EventStart), prune("r1", oplog.EventDone)}, true},
		{"failed once, then completed", []oplog.Entry{
			prune("r1", oplog.EventStart), prune("r1", oplog.EventFail),
			prune("r2", oplog.EventStart), prune("r2", oplog.EventDone),
		}, true},
	}
	for _, tt := range tests {
		err := Gate(tt.entries, rule)
		if tt.open && err != nil {
			t.Errorf("%s: gate must be open, got %v", tt.name, err)
		}
		if !tt.open && (!errors.Is(err, ErrAutotrimLocked) || !strings.Contains(err.Error(), "regrow prune go-build --yes")) {
			t.Errorf("%s: gate must refuse and name the manual command, got %v", tt.name, err)
		}
	}
}

func TestDeferralNext(t *testing.T) {
	at := func(min int) time.Time { return start.Add(time.Duration(min) * time.Minute) }
	type tick struct {
		min      int
		building bool
		freeGiB  int64
		wantRun  bool
	}
	tests := []struct {
		name  string
		ticks []tick
	}{
		{"no build: prune", []tick{{0, false, 40, true}}},
		{"a build defers, and the prune runs once it is gone",
			[]tick{{0, true, 40, false}, {15, true, 40, false}, {30, false, 40, true}}},
		{"two hours of builds with the disk critical: prune anyway",
			[]tick{{0, true, 8, false}, {45, true, 8, false}, {90, true, 8, false}, {105, true, 8, false}, {120, true, 8, true}}},
		{"two hours of builds, disk not critical: keep deferring",
			[]tick{{0, true, 12, false}, {60, true, 12, false}, {120, true, 12, false}, {180, true, 12, false}}},
		{"past two hours, and only then critical",
			[]tick{{0, true, 12, false}, {60, true, 12, false}, {120, true, 12, false}, {150, true, 9, true}}},
		{"a break in the builds restarts the clock",
			[]tick{{0, true, 8, false}, {60, true, 8, false}, {90, false, 8, true}, {105, true, 8, false}, {165, true, 8, false}, {225, true, 8, true}}},
		{"a long gap between ticks is not continuous deferral",
			[]tick{{0, true, 8, false}, {45, true, 8, false}, {240, true, 8, false}, {300, true, 8, false}, {360, true, 8, true}}},
	}
	for _, tt := range tests {
		var d Deferral
		for i, tk := range tt.ticks {
			var run bool
			d, run = d.Next(at(tk.min), tk.building, tk.freeGiB*gib)
			if run != tk.wantRun {
				t.Errorf("%s: tick %d (+%dm): run = %v, want %v", tt.name, i, tk.min, run, tk.wantRun)
			}
			if run && (!d.Since.IsZero() || !d.Last.IsZero()) {
				t.Errorf("%s: tick %d: a prune that runs must clear the deferral, got %+v", tt.name, i, d)
			}
		}
	}
}

func TestTarget(t *testing.T) {
	tests := []struct {
		name     string
		freeGiB  int64
		days     float64
		forecast bool
		wantGiB  int64
		wantLow  bool
	}{
		{"plenty free, no forecast", 60, 0, false, 50, false},
		{"under the top band", 46, 0, false, 50, true},
		{"free above the band but filling in two days", 60, 2, true, 90, true},
		{"filling, but not within three days", 60, 3.5, true, 50, false},
		{"low and filling fast: the larger target", 30, 1, true, 90, true},
		{"low and filling slowly: the band", 30, 2.5, true, 50, true},
	}
	for _, tt := range tests {
		target, low := Target(headroom.Sample{Free: tt.freeGiB * gib}, tt.days, tt.forecast)
		if target != tt.wantGiB*gib || low != tt.wantLow {
			t.Errorf("%s: Target = %s, %v; want %d GiB, %v", tt.name, headroom.Human(target), low, tt.wantGiB, tt.wantLow)
		}
	}
}

func TestTickRecordsTheSampleAndReportsCrossings(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.ap.StateDir, headroom.FileName)
	// 29 hourly samples, 100 GiB falling 1 GiB an hour: 72 GiB an hour ago.
	for i := 0; i < 29; i++ {
		s := headroom.Sample{At: start.Add(time.Duration(i-29) * time.Hour), Total: 460 * gib, Free: int64(100-i) * gib}
		if err := headroom.Append(path, s); err != nil {
			t.Fatal(err)
		}
	}
	h.free = 71 * gib

	tick, err := h.ap.Tick(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !tick.At.Equal(start) || tick.Free != 71*gib {
		t.Errorf("tick sample = %+v", tick.Sample)
	}
	if tick.DaysToFull == nil || math.Abs(*tick.DaysToFull-71.0/24) > 1e-6 {
		t.Errorf("days to full = %v, want %v", tick.DaysToFull, 71.0/24)
	}
	if len(tick.Alerts) != 1 || tick.Alerts[0].Kind != headroom.AlertDaysToFull {
		t.Errorf("alerts = %+v, want the days-to-full crossing alone", tick.Alerts)
	}
	if tick.Pruned != nil || len(h.ran) != 0 {
		t.Errorf("a tick without autotrim must not prune: %+v, ran %v", tick.Pruned, h.ran)
	}
	hist := h.history()
	if len(hist) != 30 || !hist[29].At.Equal(start) || hist[29].Free != 71*gib {
		t.Fatalf("the sample must be appended to the history, got %d samples, last %+v", len(hist), hist[len(hist)-1])
	}

	// The same reading again is no crossing.
	h.now = start.Add(15 * time.Minute)
	again, err := h.ap.Tick(context.Background(), false)
	if err != nil || len(again.Alerts) != 0 {
		t.Errorf("second tick: alerts %+v, err %v; want none", again.Alerts, err)
	}
}

func TestTickNamesTheTopProcessOnAcuteAlerts(t *testing.T) {
	h := newHarness(t)
	h.free = 90 * gib
	if _, err := h.ap.Tick(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	h.now = start.Add(15 * time.Minute)
	h.free = 83 * gib
	h.procs = []headroom.Process{{PID: 7, Name: "small", RSS: gib}, {PID: 412, Name: "runaway", RSS: 28 * gib}}

	tick, err := h.ap.Tick(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Alerts) != 1 || tick.Alerts[0].Kind != headroom.AlertFreeFall {
		t.Fatalf("alerts = %+v, want one free-fall", tick.Alerts)
	}
	a := tick.Alerts[0]
	if a.TopProcess == nil || a.TopProcess.PID != 412 || !strings.Contains(a.Message, "runaway (pid 412, 28.0 GiB resident)") {
		t.Errorf("the acute alert must name the largest process, got %+v", a)
	}
}

func TestLockedAutotrimIsNoErrorWhileHeadroomIsFine(t *testing.T) {
	h := newHarness(t)
	h.free = 80 * gib
	tick, err := h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatalf("a locked autotrim with headroom to spare must not fail the tick: %v", err)
	}
	if tick.Pruned == nil || !strings.Contains(tick.Pruned.Skipped, "autotrim is locked") || len(h.ran) != 0 {
		t.Errorf("the tick must say autotrim is locked and run nothing: %+v, ran %v", tick.Pruned, h.ran)
	}
}

func TestAutotrimIsRefusedUntilOneManualPruneCompleted(t *testing.T) {
	h := newHarness(t)

	tick, err := h.ap.Tick(context.Background(), true)
	if !errors.Is(err, ErrAutotrimLocked) {
		t.Fatalf("autotrim on an empty oplog: err = %v, want ErrAutotrimLocked", err)
	}
	if tick.At.IsZero() || len(tick.Alerts) == 0 || len(h.history()) != 1 {
		t.Errorf("a refused autotrim must still sample and alert: %+v", tick)
	}
	if tick.Pruned != nil || len(h.ran) != 0 || !reflect.DeepEqual(h.entries(), allEntries) {
		t.Fatalf("a refused autotrim must delete nothing: pruned %+v, ran %v, left %v", tick.Pruned, h.ran, h.entries())
	}

	// The manual run trims to keep_under: every entry past min_age.
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil || res.Skipped != "" || res.Error != "" {
		t.Fatalf("manual prune: %+v, %v", res, err)
	}
	if res.Files != 3 || res.Bytes != 3*h.size || res.Run == "" {
		t.Errorf("manual prune result = %+v, want 3 files, %d bytes", res, 3*h.size)
	}
	if got, want := h.entries(), []string{"a7/e47-d", "ff/e01-a"}; !reflect.DeepEqual(got, want) || !h.decoysLeft() {
		t.Fatalf("entries left = %v, want %v and every decoy", got, want)
	}

	// Three days on, the 47 h entry is past min_age and the gate is open.
	h.now = start.Add(72 * time.Hour)
	tick, err = h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatalf("autotrim after a manual prune: %v", err)
	}
	if tick.Pruned == nil || tick.Pruned.Files != 1 || tick.Pruned.Bytes != h.size || tick.Pruned.Skipped != "" {
		t.Fatalf("autotrim result = %+v, want one entry pruned", tick.Pruned)
	}
	if got, want := h.entries(), []string{"ff/e01-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entries left = %v, want %v", got, want)
	}
}

func TestPruneJournalsStartThenWhatItDeleted(t *testing.T) {
	h := newHarness(t)
	// statfs does not move: a snapshot holds the freed blocks.
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 3*h.size || res.FreeBefore != 40*gib || res.FreeAfter != 40*gib {
		t.Errorf("deleted bytes and statfs are reported apart: %+v", res)
	}
	j := h.journal()
	if len(j) != 2 {
		t.Fatalf("journal = %+v, want a start and a done line", j)
	}
	st, done := j[0], j[1]
	if st.Event != oplog.EventStart || st.Kind != "prune" || st.RuleID != engine.RuleGoBuildCache ||
		st.Run != res.Run || st.Path != h.cache || st.Bytes != 3*h.size || !reflect.DeepEqual(st.Command, h.ran[0]) {
		t.Errorf("start line = %+v; ran %q", st, h.ran)
	}
	if done.Event != oplog.EventDone || done.Run != res.Run || done.Pruned == nil ||
		*done.Pruned != (oplog.Pruned{Files: 3, Bytes: 3 * h.size}) {
		t.Errorf("done line = %+v (pruned %+v)", done, done.Pruned)
	}
}

func TestAutotrimStopsAtTheHeadroomTarget(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	// Half an entry short of the 50 GiB band: one hour of the cache is
	// enough, though three entries are past min_age.
	h.free = 50*gib - h.size/2

	tick, err := h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Pruned == nil || tick.Pruned.Files != 1 || tick.Pruned.Bytes != h.size {
		t.Fatalf("pruned = %+v, want exactly the oldest entry", tick.Pruned)
	}
	if got, want := h.entries(), []string{"3f/e72-d", "a7/e47-d", "a7/e50-a", "ff/e01-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entries left = %v, want %v", got, want)
	}
}

func TestAutotrimLeavesTheCacheAloneWhenHeadroomIsFine(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	h.free = 80 * gib

	tick, err := h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Pruned != nil || len(h.ran) != 0 || !reflect.DeepEqual(h.entries(), allEntries) {
		t.Errorf("80 GiB free must not prune: %+v, ran %v", tick.Pruned, h.ran)
	}
}

func TestAutotrimDefersWhileABuildRuns(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	h.procs = []headroom.Process{{PID: 9, Name: "compile", RSS: gib}, {PID: 10, Name: "gopls", RSS: gib}}

	// Low but not critical: three hours of builds never prune.
	h.free = 30 * gib
	for min := 0; min <= 180; min += 15 {
		h.now = start.Add(time.Duration(min) * time.Minute)
		tick, err := h.ap.Tick(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		if tick.Pruned == nil || !strings.Contains(tick.Pruned.Skipped, "a build is running (compile); deferred since") {
			t.Fatalf("+%dm: pruned = %+v, want a deferral naming the build", min, tick.Pruned)
		}
	}
	if len(h.ran) != 0 || !reflect.DeepEqual(h.entries(), allEntries) {
		t.Fatalf("a deferred prune must delete nothing: ran %v, left %v", h.ran, h.entries())
	}

	// Under 10 GiB the prune stops waiting: the deferral is already
	// past two unbroken hours.
	h.now = start.Add(195 * time.Minute)
	h.free = 8 * gib
	tick, err := h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	// By now the 47 h entry is past min_age too: four entries go.
	if tick.Pruned == nil || tick.Pruned.Skipped != "" || tick.Pruned.Files != 4 {
		t.Fatalf("critical after two hours of deferral: pruned = %+v, want the prune to run", tick.Pruned)
	}
	// An entry used within min_age is spared even then.
	if got, want := h.entries(), []string{"ff/e01-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entries left = %v, want %v", got, want)
	}
}

func TestAutotrimDoesNotOverrideEarly(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	h.procs = []headroom.Process{{PID: 9, Name: "link", RSS: gib}}
	h.free = 8 * gib
	for min := 0; min < 120; min += 15 {
		h.now = start.Add(time.Duration(min) * time.Minute)
		tick, err := h.ap.Tick(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		if tick.Pruned == nil || tick.Pruned.Skipped == "" {
			t.Fatalf("+%dm: critical but under two hours of deferral must still defer, got %+v", min, tick.Pruned)
		}
	}
	if len(h.ran) != 0 {
		t.Fatalf("nothing may run before two hours of deferral, ran %v", h.ran)
	}
}

func TestManualPruneRefusesWhileABuildRuns(t *testing.T) {
	h := newHarness(t)
	h.procs = []headroom.Process{{PID: 9, Name: "go", RSS: gib}}
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Skipped, "a build is running (go)") || len(h.ran) != 0 || len(h.journal()) != 0 {
		t.Errorf("result = %+v, ran %v, journal %v; want a skip and nothing else", res, h.ran, h.journal())
	}
}

func TestPruneIsSingleFlight(t *testing.T) {
	h := newHarness(t)
	held, err := lockState(filepath.Join(h.ap.StateDir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "another prune is running" || len(h.ran) != 0 || len(h.journal()) != 0 {
		t.Fatalf("while the lock is held: %+v, ran %v; want a skip", res, h.ran)
	}

	held.unlock()
	res, err = h.ap.Prune(context.Background(), h.rule())
	if err != nil || res.Skipped != "" || res.Files != 3 {
		t.Errorf("after the lock is released: %+v, %v; want the prune to run", res, err)
	}
}

func TestPreviewDeletesNothing(t *testing.T) {
	h := newHarness(t)
	h.procs = []headroom.Process{{PID: 9, Name: "compile", RSS: gib}}
	p, err := h.ap.Preview(context.Background(), h.rule())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Plan.Actions) != 1 || p.Plan.Actions[0].Kind != engine.ActionPrune || p.Survey.Files != 3 || p.Survey.CacheFiles != 5 {
		t.Errorf("preview = %+v", p)
	}
	if !reflect.DeepEqual(p.Running, []string{"compile"}) {
		t.Errorf("running = %v, want [compile]", p.Running)
	}
	if len(h.ran) != 0 || !reflect.DeepEqual(h.entries(), allEntries) {
		t.Errorf("a preview must run nothing: ran %v, left %v", h.ran, h.entries())
	}
	if _, err := os.Stat(h.ap.OplogPath); !os.IsNotExist(err) {
		t.Errorf("a preview must not touch the oplog: %v", err)
	}
}

func TestPruneReportsARefusedCache(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.cache, "README"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Skipped, "does not carry Go's cache header") || len(h.ran) != 0 || len(h.journal()) != 0 {
		t.Errorf("result = %+v, ran %v; want the refusal as the skip reason", res, h.ran)
	}
}

func TestFindRule(t *testing.T) {
	catalog := []engine.Rule{
		{ID: "npm-cache"},
		{ID: engine.RuleGoBuildCache, Prune: &engine.Prune{}},
	}
	for _, name := range []string{"go-build", "go-build-cache"} {
		if r, ok := FindRule(catalog, name); !ok || r.ID != engine.RuleGoBuildCache {
			t.Errorf("FindRule(%q) = %q, %v", name, r.ID, ok)
		}
	}
	for _, name := range []string{"npm", "npm-cache", "go", ""} {
		if r, ok := FindRule(catalog, name); ok {
			t.Errorf("FindRule(%q) = %q, want no rule: only rules with a prune policy", name, r.ID)
		}
	}
	if got := Names(catalog); !reflect.DeepEqual(got, []string{"go-build"}) {
		t.Errorf("Names = %v", got)
	}
}

func TestAutotrimFailedPruneFailsTheTick(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	h.ap.RunNative = func(context.Context, []string) error { return errors.New("exit status 1") }

	tick, err := h.ap.Tick(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "prune go-build-cache failed: go-build-cache: exit status 1") {
		t.Fatalf("a failed find must fail the tick, got %v", err)
	}
	if tick.Pruned == nil || tick.Pruned.Run == "" || tick.Pruned.Error == "" {
		t.Errorf("the failed prune is still reported: %+v", tick.Pruned)
	}
}

func TestPruneErrorBeforeExecutingReportsNoRun(t *testing.T) {
	h := newHarness(t)
	h.openGate()
	// The state file cannot be opened, so the prune stops at the lock.
	if err := os.MkdirAll(filepath.Join(h.ap.StateDir, stateFileName), 0o700); err != nil {
		t.Fatal(err)
	}

	tick, err := h.ap.Tick(context.Background(), true)
	if err == nil || tick.Pruned != nil {
		t.Errorf("autotrim: pruned = %+v, err %v; want no result and the error", tick.Pruned, err)
	}
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err == nil || res.Run != "" || res.Files != 0 {
		t.Errorf("manual: %+v, err %v; want no run and the error", res, err)
	}
	if len(h.ran) != 0 {
		t.Errorf("nothing may run, ran %v", h.ran)
	}
}

func TestPruneUnderGoRunIsNotItsOwnBuild(t *testing.T) {
	h := newHarness(t)
	h.ap.Self = 300
	h.procs = []headroom.Process{
		{PID: 100, PPID: 1, Name: "zsh"},
		{PID: 200, PPID: 100, Name: "go"},
		{PID: 300, PPID: 200, Name: "regrow"},
	}
	res, err := h.ap.Prune(context.Background(), h.rule())
	if err != nil || res.Skipped != "" || res.Files != 3 {
		t.Fatalf("`go run ./cmd/regrow prune go-build --yes` must not defer to its own go: %+v, %v", res, err)
	}
}

func TestUnreadableOplogFailsOnlyTheTicksThatWouldPrune(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Dir(h.ap.OplogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ap.OplogPath, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.free = 80 * gib
	tick, err := h.ap.Tick(context.Background(), true)
	if err != nil {
		t.Fatalf("with headroom to spare an unreadable oplog must not fail the tick: %v", err)
	}
	if tick.Pruned == nil || !strings.Contains(tick.Pruned.Skipped, "the oplog cannot be read") {
		t.Errorf("the tick must say why autotrim would refuse, got %+v", tick.Pruned)
	}

	h.now = start.Add(15 * time.Minute)
	h.free = 30 * gib
	if _, err := h.ap.Tick(context.Background(), true); err == nil || !strings.Contains(err.Error(), "corrupt oplog line") {
		t.Errorf("a tick that would prune must fail on the unreadable oplog, got %v", err)
	}
	if len(h.ran) != 0 {
		t.Errorf("nothing may run, ran %v", h.ran)
	}
}

func TestUnreadableHistoryStillAutotrimsAgainstTheTopBand(t *testing.T) {
	tests := []struct {
		name       string
		freeGiB    int64
		wantPruned bool
	}{
		{"under the top band", 30, true},
		{"over the top band", 60, false},
	}
	for _, tt := range tests {
		h := newHarness(t)
		h.openGate()
		// A directory where the history file should be cannot be read.
		if err := os.MkdirAll(filepath.Join(h.ap.StateDir, headroom.FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		h.free = tt.freeGiB * gib

		tick, err := h.ap.Tick(context.Background(), true)
		if !errors.Is(err, ErrHistoryUnreadable) {
			t.Fatalf("%s: err = %v, want ErrHistoryUnreadable", tt.name, err)
		}
		if tick.At.IsZero() || tick.DaysToFull != nil || len(tick.Alerts) != 0 {
			t.Errorf("%s: the sample stands, with no forecast or alerts: %+v", tt.name, tick)
		}
		pruned := tick.Pruned != nil && tick.Pruned.Files == 3
		if pruned != tt.wantPruned {
			t.Errorf("%s: pruned = %+v, want a prune: %v", tt.name, tick.Pruned, tt.wantPruned)
		}
	}
}
