package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lbagic/regrow/internal/trash"
)

// Duration is a time span written the Go way in YAML ("48h").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("duration %q: %w", node.Value, err)
	}
	*d = Duration(v)
	return nil
}

// String drops the zero minutes and seconds time.Duration prints:
// "48h", not "48h0m0s".
func (d Duration) String() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// Prune is the policy a cache is trimmed under: delete entries not
// used for MinAge, oldest first, until the cache is down to about
// KeepUnder. Entries go an hour of mtimes at a time, so the last hour
// taken can carry the cache below KeepUnder. A prune deletes directly, with no Trash and no steward
// command, so it exists only for tool-owned caches that regenerate
// (ARCHITECTURE.md invariants 1, 2 and 4 name the exception).
type Prune struct {
	MinAge    Duration `yaml:"min_age" json:"min_age"`
	KeepUnder ByteSize `yaml:"keep_under" json:"keep_under"`
	// UnlessRunning names processes whose presence defers the prune.
	UnlessRunning []string `yaml:"unless_running" json:"unless_running,omitempty"`
}

// RuleGoBuildCache is the one rule PrunePlan can locate and verify a
// cache for.
const RuleGoBuildCache = "go-build-cache"

func (r Rule) validatePrune() []string {
	p := r.Prune
	if p == nil {
		return nil
	}
	var errs []string
	if r.ID != RuleGoBuildCache {
		errs = append(errs, fmt.Sprintf("prune is implemented for %s only", RuleGoBuildCache))
	}
	if r.Risk != RiskSafe {
		errs = append(errs, "prune requires risk safe: it deletes without the Trash")
	}
	// The planner picks its cutoff on hour boundaries.
	if time.Duration(p.MinAge) < time.Hour {
		errs = append(errs, "prune.min_age must be at least 1h")
	}
	// A missing keep_under would read as "keep nothing".
	if p.KeepUnder <= 0 {
		errs = append(errs, "prune.keep_under must be a positive size")
	}
	for _, name := range p.UnlessRunning {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, "prune.unless_running must not contain empty names")
		}
	}
	return errs
}

// TrimToKeepUnder as PruneRequest.Target lifts the headroom bound: the
// prune trims the cache down to keep_under.
const TrimToKeepUnder int64 = math.MaxInt64

// PruneRequest is the headroom a prune has to restore. The bytes to
// delete are fixed here, before anything runs: free space is not
// re-read while deleting, because Time Machine snapshots can hold
// freed blocks and a loop on statfs would then empty the cache.
type PruneRequest struct {
	Now time.Time
	// Free is free space now; Target is the free space to get back to.
	Free   int64
	Target int64
	// Volume is a path on the volume Free was measured on; the cache
	// must be on it, or deleting would not move Free and autotrim would
	// trim it again on every tick. Empty skips the check.
	Volume string
	// GoEnv answers `go env <key>`; nil asks the go command on PATH.
	GoEnv func(ctx context.Context, key string) (string, error)
}

// PruneSurvey is what PrunePlan measured, for the dry-run report.
type PruneSurvey struct {
	Cache      string `json:"cache,omitempty"`
	CacheFiles int    `json:"cache_files"`
	CacheBytes int64  `json:"cache_bytes"`
	// Cutoff: entries last used at or before it are deleted.
	Cutoff time.Time `json:"cutoff,omitzero"`
	Files  int       `json:"files"`
	Bytes  int64     `json:"bytes"`
}

// goCacheHeader opens the README the go command writes into its build
// cache.
const goCacheHeader = "This directory holds cached build artifacts from the Go build system."

// findBin is absolute so a find earlier on PATH cannot stand in for
// the one this command line was written for.
const findBin = "/usr/bin/find"

// shardGlob matches the 256 shard dirs Go keeps its entries in. The
// class is spelled out: a range like [0-9a-f] follows the locale's
// collation in some fnmatch builds.
const shardGlob = "[0123456789abcdef][0123456789abcdef]"

// entryPattern is the -path pattern for cache entries: files ending in
// -a or -d inside a shard dir, as Go's own trim selects them. Anything
// else under GOCACHE (fuzz, another tool's files when GOCACHE is a
// shared dir) never matches.
func entryPattern(cache string) string {
	return globEscape(cache) + "/" + shardGlob + "/*-[ad]"
}

func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PrunePlan plans one prune of the rule's cache: a single action whose
// command deletes the entries last used at or before a cutoff, or a
// skip saying why not. The cutoff is the end of the earliest hour by
// which the oldest entries add up to min(cache − keep_under, target −
// free), and never later than now − min_age. find stats each file
// again when it runs, so entries used since planning survive. The
// error is ctx only.
func PrunePlan(ctx context.Context, host Host, r Rule, req PruneRequest) (Plan, PruneSurvey, error) {
	var survey PruneSurvey
	skip := func(format string, args ...any) (Plan, PruneSurvey, error) {
		return Plan{Skipped: []Skip{{RuleID: r.ID, Reason: fmt.Sprintf(format, args...)}}}, survey, nil
	}
	if r.Prune == nil {
		return skip("the rule has no prune policy")
	}
	cache, err := goCacheDir(ctx, req.GoEnv)
	if err != nil {
		if ctx.Err() != nil {
			return Plan{}, survey, ctx.Err()
		}
		return skip("%v", err)
	}
	if err := trash.GuardPath(cache, host.Home); err != nil {
		return skip("%v", err)
	}
	if err := VerifyGoCache(cache); err != nil {
		return skip("%v", err)
	}
	if req.Volume != "" {
		same, err := sameVolume(cache, req.Volume)
		if err != nil {
			return skip("%v", err)
		}
		if !same {
			return skip("%s is not on the volume of %s, whose free space was measured, so pruning it would not bring that space back", cache, req.Volume)
		}
	}
	survey.Cache = cache

	hist, err := surveyCache(ctx, cache)
	if err != nil {
		if ctx.Err() != nil {
			return Plan{}, survey, ctx.Err()
		}
		return skip("cannot read %s: %v", cache, err)
	}
	total := hist.total()
	survey.CacheFiles, survey.CacheBytes = total.Files, total.Bytes

	keep := int64(r.Prune.KeepUnder)
	if total.Bytes <= keep {
		return skip("the cache is %s, within the %s it may keep", HumanBytes(total.Bytes), HumanBytes(keep))
	}
	if req.Free >= req.Target {
		return skip("free space is %s, at or above the %s target", HumanBytes(req.Free), HumanBytes(req.Target))
	}
	want := min(total.Bytes-keep, req.Target-req.Free)

	cutoff, doomed := hist.cutoff(want, req.Now.Add(-time.Duration(r.Prune.MinAge)))
	if doomed.Files == 0 {
		return skip("no entry has been unused for %s", r.Prune.MinAge)
	}
	survey.Cutoff, survey.Files, survey.Bytes = cutoff, doomed.Files, doomed.Bytes

	return Plan{Actions: []Action{{
		RuleID:  r.ID,
		Kind:    ActionPrune,
		Command: PruneCommand(cache, cutoff),
		Path:    cache,
		Bytes:   doomed.Bytes,
	}}}, survey, nil
}

// PruneCommand deletes the cache entries last used at or before cutoff.
// -ignore_readdir_race: Go trims its cache and `go clean -cache`
// removes whole shard dirs, either of which can run while find walks;
// without it a vanished entry or shard makes find exit 1, and the
// prune is journaled as failed though it deleted what it could.
func PruneCommand(cache string, cutoff time.Time) []string {
	return []string{findBin, cache, "-ignore_readdir_race", "-mindepth", "2", "-maxdepth", "2", "-type", "f",
		"-path", entryPattern(cache), "!", "-newermt", cutoff.UTC().Format("2006-01-02 15:04:05") + " UTC", "-delete"}
}

// GoEnv asks the go command on PATH. An app launched outside a login
// shell has no go on PATH; the prune is then refused, never guessed.
// GOTOOLCHAIN=local: run from inside a module that asks for a newer
// Go, the go command would otherwise download a toolchain first.
func GoEnv(ctx context.Context, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", key)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// goCacheDir takes the cache location from the go command alone, never
// from a rule path: a moved GOCACHE must not leave regrow deleting in
// the default location.
func goCacheDir(ctx context.Context, goEnv func(context.Context, string) (string, error)) (string, error) {
	if goEnv == nil {
		goEnv = GoEnv
	}
	out, err := goEnv(ctx, "GOCACHE")
	if err != nil {
		return "", fmt.Errorf("`go env GOCACHE` failed, so the cache cannot be located: %v", err)
	}
	dir := strings.TrimSpace(out)
	if dir == "" || dir == "off" || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("`go env GOCACHE` reports %q, which is not a cache directory", dir)
	}
	// find does not descend into a start path that is a symlink.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("go build cache: %v", err)
	}
	return resolved, nil
}

func sameVolume(a, b string) (bool, error) {
	sa, err := statT(a)
	if err != nil {
		return false, err
	}
	sb, err := statT(b)
	if err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev, nil
}

func statT(path string) (*syscall.Stat_t, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: no device number", path)
	}
	return st, nil
}

// VerifyGoCache refuses any directory the go command did not set up as
// its build cache.
func VerifyGoCache(dir string) error {
	f, err := os.Open(filepath.Join(dir, "README"))
	if err != nil {
		return fmt.Errorf("%s has no README, so it is not a Go build cache", dir)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(goCacheHeader))
	if _, err := io.ReadFull(f, head); err != nil || string(head) != goCacheHeader {
		return fmt.Errorf("%s/README does not carry Go's cache header, so it is not a Go build cache", dir)
	}
	return nil
}

// CacheUsage counts cache entries and their physical bytes.
type CacheUsage struct {
	Files int
	Bytes int64
}

// MeasureCache totals the entries a prune command can match.
func MeasureCache(ctx context.Context, cache string) (CacheUsage, error) {
	hist, err := surveyCache(ctx, cache)
	if err != nil {
		return CacheUsage{}, err
	}
	return hist.total(), nil
}

// cacheHist buckets entries by the hour their mtime falls in. Memory
// is one bucket per hour of cache age, not per file.
type cacheHist map[int64]*CacheUsage

func hourBucket(t time.Time) int64 {
	sec := t.Unix()
	h := sec / 3600
	if sec%3600 < 0 {
		h--
	}
	return h
}

func (h cacheHist) total() CacheUsage {
	var u CacheUsage
	for _, b := range h {
		u.Files += b.Files
		u.Bytes += b.Bytes
	}
	return u
}

// cutoff takes whole hours oldest first until they hold want bytes,
// and only hours that end at or before limit. The time it returns is
// the last second of the last hour taken: find's `! -newermt` keeps or
// drops the fraction of a second after it depending on the build, and
// either way nothing from a later hour is matched.
func (h cacheHist) cutoff(want int64, limit time.Time) (time.Time, CacheUsage) {
	last := hourBucket(limit) - 1
	hours := make([]int64, 0, len(h))
	for k := range h {
		if k <= last {
			hours = append(hours, k)
		}
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i] < hours[j] })

	var at time.Time
	var doomed CacheUsage
	for _, k := range hours {
		doomed.Files += h[k].Files
		doomed.Bytes += h[k].Bytes
		at = time.Unix((k+1)*3600-1, 0)
		if doomed.Bytes >= want {
			break
		}
	}
	return at, doomed
}

// surveyCache reads the cache the way the prune command does: regular
// files directly inside a shard dir (two lowercase hex digits) whose
// name ends in -a or -d. The README, trim.txt, testexpire.txt, other
// dirs and anything deeper are not entries.
func surveyCache(ctx context.Context, cache string) (cacheHist, error) {
	shards, err := os.ReadDir(cache)
	if err != nil {
		return nil, err
	}
	hist := cacheHist{}
	for _, shard := range shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !shard.IsDir() || !isShard(shard.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(cache, shard.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !isCacheEntry(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			k := hourBucket(info.ModTime())
			b := hist[k]
			if b == nil {
				b = &CacheUsage{}
				hist[k] = b
			}
			b.Files++
			b.Bytes += physicalBytes(info)
		}
	}
	return hist, nil
}

func isShard(name string) bool {
	if len(name) != 2 {
		return false
	}
	for _, c := range []byte(name) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isCacheEntry(name string) bool {
	return strings.HasSuffix(name, "-a") || strings.HasSuffix(name, "-d")
}

// physicalBytes is allocated blocks, the size deletion gives back.
func physicalBytes(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
