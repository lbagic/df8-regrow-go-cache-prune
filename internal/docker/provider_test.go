package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/engine"
)

// testNow anchors the 30d staleness window against the recorded
// daemon JSON: everything dated June 12 or earlier is stale.
var testNow = time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

// recordedExec serves the recorded daemon JSON in testdata — the
// client seam the whole provider is tested behind.
func recordedExec(t *testing.T) Exec {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return func(_ context.Context, args ...string) ([]byte, bool, error) {
		switch strings.Join(args[:2], " ") {
		case "system df":
			return read("df.json"), true, nil
		case "container inspect":
			return read("containers.jsonl"), true, nil
		case "volume inspect":
			return read("volumes.json"), true, nil
		case "image inspect":
			return read("images.json"), true, nil
		}
		t.Fatalf("unexpected docker call: %v", args)
		return nil, false, nil
	}
}

func testProvider(t *testing.T, run Exec) *Provider {
	t.Helper()
	return &Provider{
		Exec:       run,
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		LoadConfig: func() (config.Docker, error) {
			return config.Docker{Keep: []config.KeepEntry{{Name: "keepme_*"}}}, nil
		},
		Now: func() time.Time { return testNow },
	}
}

func keys(items []engine.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Arg
		if out[i] == "" {
			out[i] = it.Label
		}
	}
	return out
}

func TestClassifyTiers(t *testing.T) {
	p := testProvider(t, recordedExec(t))
	ctx := context.Background()

	named, err := p.VolumesNamed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || named[0].Arg != "dune_timescaledb_data" {
		t.Fatalf("named tier = %+v, want the dune timescale volume", keys(named))
	}
	if !strings.Contains(named[0].Label, "project dune") {
		t.Errorf("named label must show the compose project, got %q", named[0].Label)
	}
	// No referencing container and an empty ledger: last-used unknown.
	if !named[0].LastUsed.IsZero() {
		t.Errorf("orphaned volume without ledger history must have zero LastUsed, got %v", named[0].LastUsed)
	}

	anon, err := p.VolumesAnon(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantAnon := "3333333333333333333333333333333333333333333333333333333333333333"
	if len(anon) != 1 || anon[0].Arg != wantAnon {
		t.Fatalf("anon tier = %v, want the stale anonymous volume", keys(anon))
	}
	// A dangling volume by definition has no referencing container, so
	// its last-used can only come from the ledger (empty here): the
	// classifier falls back to CreatedAt for the age gate.
	if !anon[0].LastUsed.IsZero() {
		t.Errorf("dangling anon volume LastUsed = %v, want zero without ledger history", anon[0].LastUsed)
	}

	kept, err := p.VolumesKept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKept := map[string]string{
		"aabbccddcccccccccccccccccccccccccccccccccccccccccccccccccccccccc": "within 30d",
		"bigvol": "export cap",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc": "in use by lumen-e2e-redis",
		"dune_chroma_data": "in use by (unknown container)", // df Links>0 backs up a missed join
		"keepme_data":      "keep-list",
	}
	if len(kept) != len(wantKept) {
		t.Fatalf("kept tier = %v, want %d entries", keys(kept), len(wantKept))
	}
	for _, it := range kept {
		reason, ok := wantKept[it.Arg]
		if !ok {
			t.Errorf("unexpected kept volume %s (%s)", it.Arg, it.Label)
			continue
		}
		if !strings.Contains(it.Label, reason) {
			t.Errorf("kept %s label %q must carry reason %q", it.Arg, it.Label, reason)
		}
	}

	containers, err := p.ContainersStopped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Only the container stopped >30d ago: running and fresh ones stay.
	if len(containers) != 1 || containers[0].Arg != "30123456789a" {
		t.Fatalf("stopped tier = %v, want only pebble-postgres", keys(containers))
	}
	if containers[0].Bytes != 63000000 {
		t.Errorf("container bytes = %d, want the df writable-layer size", containers[0].Bytes)
	}

	images, err := p.ImagesDangling(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The referenced dangling image and the tagged image are excluded.
	if len(images) != 1 || images[0].Arg != "aaa111000000" {
		t.Fatalf("dangling tier = %v, want only the unreferenced dangling image", keys(images))
	}
	if images[0].LastUsed.Format("2006-01-02") != "2026-05-20" {
		t.Errorf("image LastUsed = %v, want LastTagTime", images[0].LastUsed)
	}

	unused, err := p.ImagesUnused(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// postgres:16 is as old and as pulled, but a container runs it.
	if len(unused) != 1 || unused[0].Arg != "ddd444000000" || unused[0].Bytes != 847100000 {
		t.Fatalf("unused tier = %+v, want only the pulled image no container runs, at its unique size", unused)
	}

	cache, err := p.BuildCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Only the stale 25MB record counts. In-use and recently-used
	// records are excluded, and so is the stale 139MB record shared
	// with an image layer: pruning it frees nothing.
	if len(cache) != 1 || cache[0].Bytes != 25000000 {
		t.Fatalf("build cache = %+v, want one 25MB aggregate", cache)
	}
}

func TestDaemonDownMeansNoFindingsNoError(t *testing.T) {
	for name, run := range map[string]Exec{
		"binary missing": func(context.Context, ...string) ([]byte, bool, error) {
			return nil, false, nil
		},
		"daemon down": func(context.Context, ...string) ([]byte, bool, error) {
			return nil, true, errors.New("Cannot connect to the Docker daemon")
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := testProvider(t, run)
			items, err := p.VolumesNamed(context.Background())
			if err != nil || items != nil {
				t.Fatalf("daemon-down must be silent: items=%v err=%v", items, err)
			}
		})
	}
}

func TestHalfVisibleDaemonIsAnError(t *testing.T) {
	// df succeeds but inspect dies (daemon stopped mid-scan): volumes
	// must NOT silently classify as dangling.
	rec := recordedExec(t)
	run := func(ctx context.Context, args ...string) ([]byte, bool, error) {
		if strings.Join(args[:2], " ") == "container inspect" {
			return nil, true, errors.New("Cannot connect to the Docker daemon")
		}
		return rec(ctx, args...)
	}
	p := testProvider(t, run)
	if _, err := p.VolumesNamed(context.Background()); err == nil {
		t.Fatal("inspect failure after a successful df must be an error")
	}
}

func TestLedgerSurvivesContainerRemoval(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
	mkProvider := func(df, containers, volumes string) *Provider {
		return &Provider{
			Exec: func(_ context.Context, args ...string) ([]byte, bool, error) {
				switch strings.Join(args[:2], " ") {
				case "system df":
					return []byte(df), true, nil
				case "container inspect":
					return []byte(containers), true, nil
				case "volume inspect":
					return []byte(volumes), true, nil
				}
				return nil, true, fmt.Errorf("unexpected call %v", args)
			},
			LedgerPath: ledgerPath,
			LoadConfig: func() (config.Docker, error) { return config.Docker{}, nil },
			Now:        func() time.Time { return testNow },
		}
	}

	// Scan 1: a stopped container still references the volume — the
	// join sees FinishedAt, the volume is protected, the ledger learns.
	p1 := mkProvider(
		`{"Containers":[{"ID":"c1","Size":"1kB"}],"Volumes":[{"Name":"projdb","Links":"1","Size":"5MB"}]}`,
		`{"ID":"c1","Name":"/projdb-pg","Created":"2026-03-01T00:00:00Z","Status":"exited","StartedAt":"2026-03-01T00:00:01Z","FinishedAt":"2026-04-02T00:00:00Z","Mounts":[{"Type":"volume","Name":"projdb"}],"Labels":{},"Image":"postgres"}`,
		`[{"Name":"projdb","CreatedAt":"2026-03-01T00:00:00Z","Labels":{"com.docker.compose.project":"proj"}}]`,
	)
	kept, err := p1.VolumesKept(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("scan1: volume must be protected, got %v", keys(kept))
	}

	// Scan 2: `docker rm` erased the container — the join is gone, but
	// the ledger still knows the volume was last used on April 2.
	p2 := mkProvider(
		`{"Volumes":[{"Name":"projdb","Links":"0","Size":"5MB"}]}`,
		``,
		`[{"Name":"projdb","CreatedAt":"2026-03-01T00:00:00Z","Labels":{"com.docker.compose.project":"proj"}}]`,
	)
	named, err := p2.VolumesNamed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || named[0].LastUsed.Format("2006-01-02") != "2026-04-02" {
		t.Fatalf("scan2: ledger must supply last-used after container removal, got %+v", named)
	}

	// Name reuse with a different CreatedAt must NOT inherit history.
	p3 := mkProvider(
		`{"Volumes":[{"Name":"projdb","Links":"0","Size":"5MB"}]}`,
		``,
		`[{"Name":"projdb","CreatedAt":"2026-07-01T00:00:00Z","Labels":{"com.docker.compose.project":"proj"}}]`,
	)
	named, err = p3.VolumesNamed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || !named[0].LastUsed.IsZero() {
		t.Fatalf("recreated volume must start with no history, got %+v", named)
	}
}

func TestLedgerPrunesRemovedVolumes(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
	if err := saveLedger(ledgerPath, ledger{
		"gone@2026-01-01T00:00:00Z": {LastUsed: testNow},
	}); err != nil {
		t.Fatal(err)
	}
	p := testProvider(t, recordedExec(t))
	p.LedgerPath = ledgerPath
	if _, err := p.VolumesNamed(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	if _, stale := l["gone@2026-01-01T00:00:00Z"]; stale {
		t.Error("removed volume must be pruned from the ledger")
	}
	if len(l) != 7 {
		t.Errorf("ledger must hold exactly the 7 current volumes, got %d", len(l))
	}
}

func TestLedgerFeedsNamedTier(t *testing.T) {
	// Pre-seeded history for the orphaned named volume shows up as its
	// last-used even though no container references it anymore.
	ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
	if err := saveLedger(ledgerPath, ledger{
		"dune_timescaledb_data@2026-03-01T10:00:00Z": {LastUsed: time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatal(err)
	}
	p := testProvider(t, recordedExec(t))
	p.LedgerPath = ledgerPath
	named, err := p.VolumesNamed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || named[0].LastUsed.Format("2006-01-02") != "2026-06-12" {
		t.Fatalf("ledger history must feed the named tier, got %+v", named)
	}
}

func TestCorruptLedgerFailsLoudly(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(ledgerPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := testProvider(t, recordedExec(t))
	p.LedgerPath = ledgerPath
	if _, err := p.VolumesNamed(context.Background()); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("corrupt ledger must fail loudly, got %v", err)
	}
}

func TestParseSizeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "GB", "1.5XB", "-"} {
		if _, ok := parseSize(s); ok {
			t.Errorf("parseSize(%q) should fail", s)
		}
	}
}

func TestParseDockerTime(t *testing.T) {
	cases := map[string]string{
		"2026-07-07T15:55:46.101992375Z":          "2026-07-07", // inspect RFC3339Nano
		"2026-06-12T09:03:11Z":                    "2026-06-12", // volume inspect RFC3339
		"2026-06-12 11:02:25 +0200 CEST":          "2026-06-12", // df CreatedAt
		"2026-06-12 09:00:25.123456789 +0000 UTC": "2026-06-12", // df LastUsedAt
	}
	for in, day := range cases {
		if got := parseDockerTime(in); got.IsZero() || got.UTC().Format("2006-01-02") != day {
			t.Errorf("parseDockerTime(%q) = %v, want day %s", in, got, day)
		}
	}
	for _, zero := range []string{"0001-01-01T00:00:00Z", "", "garbage"} {
		if got := parseDockerTime(zero); !got.IsZero() {
			t.Errorf("parseDockerTime(%q) = %v, want zero", zero, got)
		}
	}
}

// A docker CLI killed at the query deadline is not a stopped daemon:
// every docker rule must report the deadline instead of "not found",
// including rules that read the memoized snapshot afterwards.
func TestSnapshotDeadlineIsAnError(t *testing.T) {
	wedged := func(ctx context.Context, _ ...string) ([]byte, bool, error) {
		<-ctx.Done()
		return nil, true, errors.New("signal: killed")
	}
	p := testProvider(t, wedged)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.BuildCache(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wedged daemon = %v, want the deadline", err)
	}
	if _, err := p.ImagesDangling(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a later rule = %v, want the same deadline, not an empty result", err)
	}
}
