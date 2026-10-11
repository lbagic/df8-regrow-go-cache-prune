package headroom

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleAt(at time.Time, freeGiB int64) Sample {
	return Sample{At: at, Total: 460 * GiB, Free: freeGiB * GiB, Purgeable: GiB, SwapUsed: 2 * GiB}
}

func TestAppendThenTailRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "regrow", FileName)
	if got, err := Tail(path, time.Time{}); err != nil || got != nil {
		t.Fatalf("a missing file is an empty history, got %v, %v", got, err)
	}
	want := []Sample{sampleAt(t0, 90), sampleAt(t0.Add(15*time.Minute), 89), sampleAt(t0.Add(30*time.Minute), 88)}
	for _, s := range want {
		if err := Append(path, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Tail(path, time.Time{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Tail = %+v, %v; want %+v", got, err, want)
	}
	since, err := Tail(path, t0.Add(15*time.Minute))
	if err != nil || !reflect.DeepEqual(since, want[1:]) {
		t.Fatalf("Tail(since) = %+v, %v; want %+v", since, err, want[1:])
	}
}

func TestTailSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	lines := []string{
		`{"at":"2026-10-01T00:00:00Z","total":493921239040,"free":96636764160,"purgeable":0,"swap_used":0}`,
		`{"at":"2026-10-01T00:15:00Z","total":4939212`, // a write cut short by a full disk
		`not json at all`,
		``,
		`{}`,
		`{"at":"2026-10-01T00:30:00Z","total":"460"}`,
		`{"at":"2026-10-01T00:45:00Z","total":493921239040,"free":95563022336,"purgeable":0,"swap_used":0}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Free != 90*GiB || got[1].Free != 89*GiB || !got[1].At.Equal(t0.Add(45*time.Minute)) {
		t.Fatalf("Tail must return the two whole lines and nothing else, got %+v", got)
	}
}

func TestAppendAfterATruncatedLineStaysReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Append(path, sampleAt(t0, 90)); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// No trailing newline: the next append would otherwise glue its
	// line onto this fragment and lose both.
	if _, err := f.WriteString(`{"at":"2026-10-01T00:15:00Z","tot`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	next := sampleAt(t0.Add(30*time.Minute), 88)
	if err := Append(path, next); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(path, time.Time{})
	if err != nil || len(got) != 2 || !reflect.DeepEqual(got[1], next) {
		t.Fatalf("the sample appended after a fragment must read back, got %+v, %v", got, err)
	}
}

func TestAppendDropsHistoryPastRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	now := t0.Add(40 * 24 * time.Hour)
	for day := 0; day < 40; day++ {
		if err := Append(path, sampleAt(t0.Add(time.Duration(day)*24*time.Hour), 90)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Append(path, sampleAt(now, 80)); err != nil {
		t.Fatal(err)
	}
	got, err := Tail(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !got[len(got)-1].At.Equal(now) {
		t.Fatalf("the newest sample must survive, got %+v", got)
	}
	// Retention plus the slack before a rewrite: nothing older stays.
	oldest := now.Add(-Retention - compactSlack)
	if got[0].At.Before(oldest) {
		t.Errorf("oldest sample kept is %v, want nothing before %v", got[0].At, oldest)
	}
	if len(got) < 30 {
		t.Errorf("the last 30 days must survive, kept %d samples", len(got))
	}
}
