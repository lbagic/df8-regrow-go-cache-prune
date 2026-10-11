package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func TestAerialDownloads(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	video := func(t *testing.T, path string, age time.Duration) {
		t.Helper()
		writeFile(t, path, 1<<20)
		at := causeNow.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// What idleassetsd keeps beside the videos; it is rewritten without
	// any download, so it must never read as one.
	catalogue := func(t *testing.T, customer string) {
		t.Helper()
		writeFile(t, filepath.Join(customer, "entries.json"), 130_000)
		writeFile(t, filepath.Join(customer, "resources.tar"), 2_700_000)
		writeFile(t, filepath.Join(customer, "4KSDR240FPS", "notes.txt"), 10)
	}

	tests := []struct {
		name    string
		plant   func(t *testing.T, customer string)
		want    engine.Verdict
		detail  []string
		without string
	}{
		{
			name: "videos left by an earlier batch",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 80*time.Hour)
				video(t, filepath.Join(customer, "4KSDR240FPS", "b.MOV"), 72*time.Hour)
			},
			want:    engine.VerdictFlagged,
			detail:  []string{"2 videos, 2.0 MiB", "newest written 3 d ago"},
			without: "downloading",
		},
		{
			name: "a batch coming in",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 2*time.Hour)
				video(t, filepath.Join(customer, "4KSDR", "b.mov"), 3*time.Minute)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"2 videos, 2.0 MiB", "newest written 3 min ago, so a batch is downloading now"},
		},
		{
			name: "one video",
			plant: func(t *testing.T, customer string) {
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), time.Hour)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"1 video, 1.0 MiB"},
		},
		{
			name:   "only the catalogue files",
			plant:  catalogue,
			want:   engine.VerdictNormal,
			detail: []string{"no aerial videos on disk"},
		},
		{
			name:   "no folder",
			plant:  func(*testing.T, string) {},
			want:   engine.VerdictNormal,
			detail: []string{"does not apply"},
		},
		{
			name: "a folder that cannot be read, no video seen",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR", "hidden.mov"), time.Hour)
				chmod(t, filepath.Join(customer, "4KSDR"), 0)
			},
			want:   engine.VerdictUnknown,
			detail: []string{"Full Disk Access may be needed"},
		},
		{
			name: "a folder that cannot be read beside a video",
			plant: func(t *testing.T, customer string) {
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 80*time.Hour)
				video(t, filepath.Join(customer, "4KSDR", "hidden.mov"), time.Minute)
				chmod(t, filepath.Join(customer, "4KSDR"), 0)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"1 video, ≥ 1.0 MiB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Geteuid() == 0 && strings.Contains(tt.name, "cannot be read") {
				t.Skip("root reads mode-000 folders")
			}
			c := causeFixture(t)
			tt.plant(t, filepath.Join(c.host.Root, "Library/Application Support/com.apple.idleassetsd/Customer"))
			verdict, detail := c.aerialDownloads(context.Background(), rule)
			wantCause(t, verdict, detail, tt.want, tt.detail...)
			if tt.without != "" && strings.Contains(detail, tt.without) {
				t.Errorf("detail %q must not say %q", detail, tt.without)
			}
		})
	}
}

// The videos are only shown: removing them is a raw delete as root,
// which regrow never runs (invariants 2 and 4). On both of the rule's
// paths a found video is never pre-selected, and naming the rule by id
// plans nothing for it.
func TestAerialWallpapersAreShownOnly(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	for _, tc := range []struct {
		version, video string
	}{
		{"15.5", "/Library/Application Support/com.apple.idleassetsd/Customer/4KSDR240FPS/a.mov"},
		{"26.0", "~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches/a.mov"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			host := fixtureHost(t, rule)
			host.Version = tc.version
			writeFile(t, host.ExpandPath(tc.video), 1<<20)
			s := New(host)
			s.Queries = nil
			findings := s.Scan(context.Background(), []engine.Rule{rule})
			if f := findings[0]; f.Err != "" || len(f.Items) == 0 {
				t.Fatalf("finding %+v, want the video's folder found", f)
			}
			if sel := engine.DefaultSelection(findings); len(sel) != 0 {
				t.Errorf("default selection %v, want nothing", sel)
			}
			plan := engine.BuildPlan(host, findings, map[string]bool{rule.ID: true})
			if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "surface-only: report, never delete" {
				t.Errorf("plan by id: actions %+v, skipped %+v; want no action and one surface-only skip", plan.Actions, plan.Skipped)
			}
		})
	}
}
