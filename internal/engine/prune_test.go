package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var pruneNow = time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)

func pruneRule() Rule {
	return Rule{
		ID: RuleGoBuildCache, Title: "Go build cache", Category: "dev-caches", Risk: RiskSafe,
		Paths:         map[string][]PathEntry{"darwin": {{Path: "~/Library/Caches/go-build"}}},
		NativeCommand: Argv{"go", "clean", "-cache"},
		Prune:         &Prune{MinAge: Duration(48 * time.Hour), KeepUnder: 1, UnlessRunning: []string{"go"}},
	}
}

// cacheFixture is a Go build cache under a temp home: five entries of
// one size at known ages, and everything a prune must leave alone.
type cacheFixture struct {
	host  Host
	cache string
	size  int64 // physical bytes of one entry
}

var (
	fixtureEntries = map[string]time.Duration{
		"00/e100-a": 100 * time.Hour,
		"3f/e72-d":  72 * time.Hour,
		"a7/e50-a":  50 * time.Hour,
		"a7/e47-d":  47 * time.Hour,
		"ff/e01-a":  time.Hour,
	}
	// Every decoy is older than every entry: age alone would take it.
	// other/ stands for another tool's files when GOCACHE is a shared
	// dir; AB/ is not a shard, since Go writes lowercase hex.
	fixtureDecoys = []string{
		"README", "trim.txt", "testexpire.txt", "stray-a",
		"00/notes.txt", "00/e-b", "fuzz/pkg/seed-a", "3f/dir-a/inner-a",
		"other/x-a", "fuzz/x-a", "AB/x-a", "abc/x-d",
	}
)

func newCacheFixture(t *testing.T) cacheFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Glob characters in the path: the command must match them literally.
	fx := cacheFixture{host: Host{OS: "darwin", Home: filepath.Join(root, "ho[me] *?")}}
	fx.cache = filepath.Join(fx.host.Home, "Library", "Caches", "go-build")
	plant := func(rel string, body []byte, age time.Duration) {
		p := filepath.Join(fx.cache, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		at := pruneNow.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	body := make([]byte, 4096)
	for rel, age := range fixtureEntries {
		plant(rel, body, age)
	}
	for _, rel := range fixtureDecoys {
		plant(rel, body, 200*time.Hour)
	}
	plant("README", []byte(goCacheHeader+"\nRun \"go clean -cache\" if the directory is getting too large.\n"), 200*time.Hour)
	if err := os.Symlink(filepath.Join(fx.cache, "README"), filepath.Join(fx.cache, "3f", "link-a")); err != nil {
		t.Fatal(err)
	}
	old := pruneNow.Add(-200 * time.Hour)
	if err := os.Chtimes(filepath.Join(fx.cache, "3f", "dir-a"), old, old); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(fx.cache, "00/e100-a"))
	if err != nil {
		t.Fatal(err)
	}
	fx.size = physicalBytes(info)
	if fx.size <= 0 {
		t.Fatalf("fixture entry reports %d physical bytes", fx.size)
	}
	return fx
}

func (fx cacheFixture) goEnv(context.Context, string) (string, error) { return fx.cache, nil }

func (fx cacheFixture) request(free, target int64) PruneRequest {
	return PruneRequest{Now: pruneNow, Free: free, Target: target, GoEnv: fx.goEnv}
}

// present lists which fixture paths still exist.
func (fx cacheFixture) present(t *testing.T, rels []string) []string {
	t.Helper()
	var out []string
	for _, rel := range rels {
		if _, err := os.Lstat(filepath.Join(fx.cache, rel)); err == nil {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// runFind executes a planned prune command, and only inside the
// fixture.
func (fx cacheFixture) runFind(t *testing.T, argv []string) {
	t.Helper()
	if len(argv) < 2 || argv[0] != findBin || argv[1] != fx.cache || !strings.Contains(fx.cache, "TestPrune") {
		t.Fatalf("refusing to run %q: not a prune of the fixture cache %s", argv, fx.cache)
	}
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("%q: %v\n%s", argv, err, out)
	}
}

func hourEnd(age time.Duration) time.Time {
	return time.Unix((hourBucket(pruneNow.Add(-age))+1)*3600-1, 0)
}

func TestPrunePlanTakesOldestHoursUpToTheNeed(t *testing.T) {
	tests := []struct {
		name string
		// keepLess is how far under the cache size keep_under sits, and
		// short how far free space is under its target, in entries.
		keepLess, short float64
		unbounded       bool
		wantGone        []string
		wantCutoffAge   time.Duration
	}{
		{"no headroom bound: every entry past min_age", 4, 0, true,
			[]string{"00/e100-a", "3f/e72-d", "a7/e50-a"}, 50 * time.Hour},
		{"keep_under stops it first", 1.5, 0, true,
			[]string{"00/e100-a", "3f/e72-d"}, 72 * time.Hour},
		{"the headroom target stops it first", 4, 0.5, false,
			[]string{"00/e100-a"}, 100 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCacheFixture(t)
			r := pruneRule()
			r.Prune.KeepUnder = ByteSize(float64(5*fx.size) - tt.keepLess*float64(fx.size))
			req := fx.request(10<<30, TrimToKeepUnder)
			req.Volume = fx.host.Home
			if !tt.unbounded {
				req.Target = req.Free + int64(tt.short*float64(fx.size))
			}

			plan, survey, err := PrunePlan(context.Background(), fx.host, r, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 1 || len(plan.Skipped) != 0 {
				t.Fatalf("want exactly one action, got %+v", plan)
			}
			a := plan.Actions[0]
			cutoff := hourEnd(tt.wantCutoffAge)
			wantCmd := []string{"/usr/bin/find", fx.cache, "-ignore_readdir_race", "-mindepth", "2", "-maxdepth", "2", "-type", "f",
				"-path", globEscape(fx.cache) + "/[0123456789abcdef][0123456789abcdef]/*-[ad]",
				"!", "-newermt", cutoff.UTC().Format("2006-01-02 15:04:05") + " UTC", "-delete"}
			if a.Kind != ActionPrune || a.RuleID != RuleGoBuildCache || a.Path != fx.cache || !reflect.DeepEqual(a.Command, wantCmd) {
				t.Errorf("action = %+v\nwant command %q", a, wantCmd)
			}
			wantBytes := int64(len(tt.wantGone)) * fx.size
			if a.Bytes != wantBytes || survey.Bytes != wantBytes || survey.Files != len(tt.wantGone) {
				t.Errorf("estimate = %d bytes (survey %d in %d files), want %d in %d",
					a.Bytes, survey.Bytes, survey.Files, wantBytes, len(tt.wantGone))
			}
			if survey.CacheFiles != 5 || survey.CacheBytes != 5*fx.size {
				t.Errorf("the survey must count the five entries and no decoy, got %d files, %d bytes", survey.CacheFiles, survey.CacheBytes)
			}
			if !survey.Cutoff.Equal(cutoff) || cutoff.After(pruneNow.Add(-48*time.Hour)) {
				t.Errorf("cutoff = %v, want %v, at or before now − min_age", survey.Cutoff, cutoff)
			}
			if got := plan.Totals(); got.FreesNow != wantBytes || got.AfterTrash != 0 {
				t.Errorf("a prune frees now, got totals %+v", got)
			}

			fx.runFind(t, a.Command)

			all := make([]string, 0, len(fixtureEntries))
			for rel := range fixtureEntries {
				all = append(all, rel)
			}
			var wantLeft []string
			for _, rel := range all {
				gone := false
				for _, g := range tt.wantGone {
					gone = gone || g == rel
				}
				if !gone {
					wantLeft = append(wantLeft, rel)
				}
			}
			sort.Strings(wantLeft)
			if got := fx.present(t, all); !reflect.DeepEqual(got, wantLeft) {
				t.Errorf("entries left after the command = %v, want %v", got, wantLeft)
			}
			decoys := append([]string{"3f/link-a", "3f/dir-a"}, fixtureDecoys...)
			sort.Strings(decoys)
			if got := fx.present(t, decoys); !reflect.DeepEqual(got, decoys) {
				t.Errorf("the command deleted a decoy: left %v, want all of %v", got, decoys)
			}
		})
	}
}

func TestPrunePlanRefusesWhatIsNotAGoCache(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fx cacheFixture, req *PruneRequest, r *Rule)
		want  string
	}{
		{"README missing", func(t *testing.T, fx cacheFixture, _ *PruneRequest, _ *Rule) {
			if err := os.Remove(filepath.Join(fx.cache, "README")); err != nil {
				t.Fatal(err)
			}
		}, "has no README"},
		{"README without Go's header", func(t *testing.T, fx cacheFixture, _ *PruneRequest, _ *Rule) {
			if err := os.WriteFile(filepath.Join(fx.cache, "README"), []byte("My notes.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "does not carry Go's cache header"},
		{"go env fails", func(_ *testing.T, _ cacheFixture, req *PruneRequest, _ *Rule) {
			req.GoEnv = func(context.Context, string) (string, error) { return "", errors.New("exec: \"go\": not found") }
		}, "`go env GOCACHE` failed"},
		{"GOCACHE off", func(_ *testing.T, _ cacheFixture, req *PruneRequest, _ *Rule) {
			req.GoEnv = func(context.Context, string) (string, error) { return "off", nil }
		}, "not a cache directory"},
		{"GOCACHE relative", func(_ *testing.T, _ cacheFixture, req *PruneRequest, _ *Rule) {
			req.GoEnv = func(context.Context, string) (string, error) { return "Library/Caches/go-build", nil }
		}, "not a cache directory"},
		{"GOCACHE does not exist", func(_ *testing.T, fx cacheFixture, req *PruneRequest, _ *Rule) {
			req.GoEnv = func(context.Context, string) (string, error) { return filepath.Join(fx.cache, "nope"), nil }
		}, "go build cache:"},
		{"GOCACHE is the home directory", func(t *testing.T, fx cacheFixture, req *PruneRequest, _ *Rule) {
			if err := os.WriteFile(filepath.Join(fx.host.Home, "README"), []byte(goCacheHeader+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			req.GoEnv = func(context.Context, string) (string, error) { return fx.host.Home, nil }
		}, "refusing home directory"},
		{"rule without a prune block", func(_ *testing.T, _ cacheFixture, _ *PruneRequest, r *Rule) {
			r.Prune = nil
		}, "no prune policy"},
		// /dev is its own file system on macOS and Linux: a stand-in
		// for a GOCACHE on an external disk.
		{"free space measured on another volume", func(_ *testing.T, _ cacheFixture, req *PruneRequest, _ *Rule) {
			req.Volume = "/dev"
		}, "is not on the volume of /dev, whose free space was measured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCacheFixture(t)
			req := fx.request(10<<30, TrimToKeepUnder)
			r := pruneRule()
			tt.setup(t, fx, &req, &r)
			plan, _, err := PrunePlan(context.Background(), fx.host, r, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 0 {
				t.Fatalf("must refuse, planned %+v", plan.Actions)
			}
			if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, tt.want) {
				t.Fatalf("skip = %+v, want a reason containing %q", plan.Skipped, tt.want)
			}
		})
	}
}

func TestPrunePlanSkipsWhenThereIsNothingToDo(t *testing.T) {
	tests := []struct {
		name  string
		setup func(fx cacheFixture, req *PruneRequest, r *Rule)
		want  string
	}{
		{"cache within keep_under", func(fx cacheFixture, _ *PruneRequest, r *Rule) {
			r.Prune.KeepUnder = ByteSize(5 * fx.size)
		}, "within the"},
		{"free space at the target", func(_ cacheFixture, req *PruneRequest, _ *Rule) {
			req.Free, req.Target = 50<<30, 50<<30
		}, "at or above the"},
		{"nothing unused for min_age", func(_ cacheFixture, _ *PruneRequest, r *Rule) {
			r.Prune.MinAge = Duration(150 * time.Hour)
		}, "no entry has been unused for 150h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCacheFixture(t)
			req := fx.request(10<<30, TrimToKeepUnder)
			r := pruneRule()
			tt.setup(fx, &req, &r)
			plan, survey, err := PrunePlan(context.Background(), fx.host, r, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, tt.want) {
				t.Fatalf("plan = %+v, want one skip containing %q", plan, tt.want)
			}
			if survey.CacheFiles != 5 {
				t.Errorf("the survey still reports the cache, got %+v", survey)
			}
		})
	}
}

func TestPrunePlanStopsOnCancel(t *testing.T) {
	fx := newCacheFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, _, err := PrunePlan(ctx, fx.host, pruneRule(), fx.request(10<<30, TrimToKeepUnder))
	if !errors.Is(err, context.Canceled) || len(plan.Actions) != 0 {
		t.Fatalf("a canceled plan = %+v, err %v; want no action and context.Canceled", plan, err)
	}
}

func TestCutoffTakesOnlyHoursThatEndByTheLimit(t *testing.T) {
	hour := func(h int64) time.Time { return time.Unix(h*3600, 0) }
	hist := cacheHist{
		10: {Files: 1, Bytes: 100},
		11: {Files: 2, Bytes: 200},
		12: {Files: 4, Bytes: 400},
	}
	tests := []struct {
		name      string
		want      int64
		limit     time.Time
		wantAt    time.Time
		wantFiles int
		wantBytes int64
	}{
		{"stops at the hour that reaches the need", 250, hour(20), hour(12).Add(-time.Second), 3, 300},
		{"an hour ending exactly at the limit counts", 1000, hour(12), hour(12).Add(-time.Second), 3, 300},
		{"an hour the limit falls inside does not", 1000, hour(12).Add(-time.Second), hour(11).Add(-time.Second), 1, 100},
		{"nothing old enough", 1000, hour(10).Add(30 * time.Minute), time.Time{}, 0, 0},
	}
	for _, tt := range tests {
		at, doomed := hist.cutoff(tt.want, tt.limit)
		if !at.Equal(tt.wantAt) || doomed.Files != tt.wantFiles || doomed.Bytes != tt.wantBytes {
			t.Errorf("%s: cutoff = %v, %+v; want %v, %d files, %d bytes", tt.name, at, doomed, tt.wantAt, tt.wantFiles, tt.wantBytes)
		}
	}
	// An mtime on the hour belongs to the hour it starts.
	if hourBucket(hour(12)) != 12 || hourBucket(hour(12).Add(-time.Nanosecond)) != 11 {
		t.Errorf("hourBucket splits hours at the wrong instant")
	}
}

func TestDurationPrintsWithoutZeroUnits(t *testing.T) {
	for d, want := range map[time.Duration]string{
		48 * time.Hour:                "48h",
		90 * time.Minute:              "1h30m",
		time.Hour + 30*time.Second:    "1h0m30s",
		45 * time.Minute:              "45m",
		150*time.Hour + 2*time.Minute: "150h2m",
	} {
		if got := Duration(d).String(); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
		if back, err := time.ParseDuration(Duration(d).String()); err != nil || back != d {
			t.Errorf("%q must parse back to %v, got %v, %v", Duration(d).String(), d, back, err)
		}
	}
}

func TestValidatePruneBlock(t *testing.T) {
	if err := pruneRule().Validate(); err != nil {
		t.Fatalf("valid prune rule rejected: %v", err)
	}
	tests := []struct {
		name    string
		mutate  func(*Rule)
		wantErr string
	}{
		{"another rule", func(r *Rule) { r.ID = "npm-cache" }, "implemented for go-build-cache only"},
		{"not safe", func(r *Rule) { r.Risk = RiskCaution }, "requires risk safe"},
		{"min_age under an hour", func(r *Rule) { r.Prune.MinAge = Duration(30 * time.Minute) }, "min_age must be at least 1h"},
		{"keep_under missing", func(r *Rule) { r.Prune.KeepUnder = 0 }, "keep_under must be a positive size"},
		{"empty process name", func(r *Rule) { r.Prune.UnlessRunning = []string{"go", " "} }, "unless_running"},
	}
	for _, tt := range tests {
		r := pruneRule()
		tt.mutate(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: err = %v, want it to contain %q", tt.name, err, tt.wantErr)
		}
	}
}

func TestEmbeddedGoBuildRuleCarriesThePolicy(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		if r.ID != RuleGoBuildCache {
			if r.Prune != nil {
				t.Errorf("%s carries a prune block", r.ID)
			}
			continue
		}
		want := &Prune{MinAge: Duration(48 * time.Hour), KeepUnder: 15 << 30, UnlessRunning: []string{"go", "compile", "link"}}
		if !reflect.DeepEqual(r.Prune, want) {
			t.Errorf("go-build-cache prune = %+v, want %+v", r.Prune, want)
		}
		return
	}
	t.Fatal("no go-build-cache rule in the embedded catalog")
}

func TestGlobEscape(t *testing.T) {
	if got, want := globEscape(`/u/ho[me] *?\x`), `/u/ho\[me\] \*\?\\x`; got != want {
		t.Errorf("globEscape = %q, want %q", got, want)
	}
	for _, name := range []string{"00", "3f", "a7", "ff"} {
		if !isShard(name) {
			t.Errorf("isShard(%q) = false", name)
		}
	}
	for _, name := range []string{"AB", "0g", "abc", "a", "fuzz", ""} {
		if isShard(name) {
			t.Errorf("isShard(%q) = true", name)
		}
	}
}

func TestPruneRuleBytesFreeNowInTheLedger(t *testing.T) {
	withCommand := pruneRule()
	pruneOnly := pruneRule()
	pruneOnly.ID, pruneOnly.NativeCommand = "prune-only", nil
	led := Account([]Finding{
		{Rule: withCommand, Items: []Item{{Path: "/u/a", Bytes: 3}}},
		{Rule: pruneOnly, Items: []Item{{Path: "/u/b", Bytes: 4}}},
	})
	if led.Totals.FreesNow != 7 || led.Totals.AfterTrash != 0 {
		t.Errorf("a rule with a prune policy frees now, got %+v", led.Totals)
	}
}

// TestPruneCommandSurvivesAConcurrentCacheClean runs the prune command
// while shard dirs vanish under it, as when `go clean -cache` runs
// during a prune. Once find is deleting in one shard, the others are
// moved out of the cache, so find meets them gone; whether it gets
// there first depends on timing, so the scenario repeats until the race
// has happened once.
func TestPruneCommandSurvivesAConcurrentCacheClean(t *testing.T) {
	if _, err := os.Stat(findBin); err != nil {
		t.Skipf("%s: %v", findBin, err)
	}
	shards := []string{"00", "3f", "a7", "ff"}
	const perShard = 600
	for attempt := 1; attempt <= 5; attempt++ {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cache, gone := filepath.Join(root, "go-build"), filepath.Join(root, "gone")
		if err := os.MkdirAll(gone, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, sh := range shards {
			if err := os.MkdirAll(filepath.Join(cache, sh), 0o755); err != nil {
				t.Fatal(err)
			}
			for i := range perShard {
				if err := os.WriteFile(filepath.Join(cache, sh, fmt.Sprintf("%04x-a", i)), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}

		// A cutoff an hour ahead matches every entry.
		cmd := exec.Command(findBin, PruneCommand(cache, time.Now().Add(time.Hour))[1:]...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		moved, stop := make(chan []string, 1), make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					moved <- nil
					return
				default:
				}
				for _, sh := range shards {
					names, _ := os.ReadDir(filepath.Join(cache, sh))
					if len(names) == perShard {
						continue
					}
					var out []string
					for _, other := range shards {
						if other != sh && os.Rename(filepath.Join(cache, other), filepath.Join(gone, other)) == nil {
							out = append(out, other)
						}
					}
					moved <- out
					return
				}
			}
		}()
		err = cmd.Wait()
		close(stop)
		away := <-moved
		if err != nil {
			t.Fatalf("attempt %d: find must not fail when shards vanish under it: %v\n%s", attempt, err, stderr.String())
		}
		for _, sh := range away {
			if left, _ := os.ReadDir(filepath.Join(gone, sh)); len(left) > 0 {
				return // find met a vanished shard and still exited 0
			}
		}
	}
	t.Skip("find finished before any shard vanished in 5 attempts; the race was not exercised")
}
