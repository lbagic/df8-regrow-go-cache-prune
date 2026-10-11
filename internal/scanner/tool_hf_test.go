package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeHFRepo lays out one repo the way huggingface_hub does: blobs/,
// snapshots/<rev>/ with symlinks into blobs, refs/main.
func fakeHFRepo(t *testing.T, hub, dirName string, revisions int) {
	t.Helper()
	repo := filepath.Join(hub, dirName)
	blob := filepath.Join(repo, "blobs", "aabbcc")
	writeFile(t, blob, 64*1024)
	for i := 0; i < revisions; i++ {
		snap := filepath.Join(repo, "snapshots", string(rune('a'+i))+"rev")
		if err := os.MkdirAll(snap, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../blobs/aabbcc", filepath.Join(snap, "model.bin")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(repo, "refs", "main"), 10)
}

func TestScanHFHub(t *testing.T) {
	hub := t.TempDir()
	fakeHFRepo(t, hub, "models--meta-llama--Llama-3-8B", 2) // 2 snapshots, 1 blob
	fakeHFRepo(t, hub, "datasets--squad", 1)
	fakeHFRepo(t, hub, "spaces--org--demo", 1)
	writeFile(t, filepath.Join(hub, "version.txt"), 1) // not a repo
	if err := os.MkdirAll(filepath.Join(hub, ".locks"), 0o755); err != nil {
		t.Fatal(err)
	}

	items, err := scanHFHub(context.Background(), testWalker(), hub)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 repos, got %d: %+v", len(items), items)
	}
	// ReadDir order: datasets--squad, models--..., spaces--org--demo
	if items[0].Label != "squad (dataset)" || items[0].Arg != "dataset/squad" {
		t.Errorf("dataset item: %+v", items[0])
	}
	if items[1].Label != "meta-llama/Llama-3-8B" || items[1].Arg != "model/meta-llama/Llama-3-8B" {
		t.Errorf("model item: %+v", items[1])
	}
	if items[2].Arg != "space/org/demo" {
		t.Errorf("space item: %+v", items[2])
	}
	for _, it := range items {
		if it.Path == "" {
			t.Errorf("item %q has no path — planner cannot trash it", it.Label)
		}
		if it.LastUsed.IsZero() {
			t.Errorf("item %q has no last-used", it.Label)
		}
	}
	// Dedup-aware size: the model repo's blob is symlinked from two
	// snapshots but must count once — well under twice its 64KB.
	if items[1].Bytes <= 0 || items[1].Bytes >= 2*64*1024 {
		t.Errorf("model bytes = %d, want one blob's worth (dedup)", items[1].Bytes)
	}
}

func TestDecodeHFRepoDir(t *testing.T) {
	cases := []struct {
		in, typ, id string
		ok          bool
	}{
		{"models--gpt2", "model", "gpt2", true},
		{"models--meta-llama--Llama-3-8B", "model", "meta-llama/Llama-3-8B", true},
		{"datasets--squad", "dataset", "squad", true},
		{"spaces--org--demo", "space", "org/demo", true},
		{"version.txt", "", "", false},
		{".locks", "", "", false},
		{"tmp--something", "", "", false},
	}
	for _, c := range cases {
		typ, id, ok := decodeHFRepoDir(c.in)
		if typ != c.typ || id != c.id || ok != c.ok {
			t.Errorf("decodeHFRepoDir(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, typ, id, ok, c.typ, c.id, c.ok)
		}
	}
}

func TestHFHubDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HF_HUB_CACHE", dir)
	got, ok := hfHubDir(context.Background(), testWalker())
	if !ok || got != dir {
		t.Fatalf("hfHubDir() = (%q, %v), want (%q, true)", got, ok, dir)
	}

	base := t.TempDir()
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("HF_HOME", base)
	if err := os.MkdirAll(filepath.Join(base, "hub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok = hfHubDir(context.Background(), testWalker())
	if !ok || got != filepath.Join(base, "hub") {
		t.Fatalf("hfHubDir() with HF_HOME = (%q, %v)", got, ok)
	}
}
