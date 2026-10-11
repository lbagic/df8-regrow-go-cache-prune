package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, root string, segs []string, json string) {
	t.Helper()
	p := filepath.Join(append([]string{root}, segs...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanOllamaModels(t *testing.T) {
	dir := t.TempDir()
	// llama3: one exclusive 4GB layer + one 274MB layer shared with
	// nomic-embed-text; both share nothing else.
	writeManifest(t, dir, []string{"registry.ollama.ai", "library", "llama3", "latest"},
		`{"config":{"digest":"sha256-c1","size":500},
		  "layers":[{"digest":"sha256-big","size":4000000000},{"digest":"sha256-shared","size":274000000}]}`)
	writeManifest(t, dir, []string{"registry.ollama.ai", "library", "nomic-embed-text", "latest"},
		`{"config":{"digest":"sha256-c2","size":500},
		  "layers":[{"digest":"sha256-shared","size":274000000}]}`)
	writeManifest(t, dir, []string{"hf.co", "org", "model", "Q4"},
		`{"config":{"digest":"sha256-c3","size":500},
		  "layers":[{"digest":"sha256-hf","size":1000000}]}`)
	writeManifest(t, dir, []string{"registry.ollama.ai", "library", "broken", "latest"}, `{not json`)

	items, err := scanOllamaModels(context.Background(), testWalker(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 models (broken manifest skipped), got %d: %+v", len(items), items)
	}
	byArg := map[string]int{}
	for i, it := range items {
		byArg[it.Arg] = i
	}

	llama := items[byArg["llama3:latest"]]
	if llama.Bytes != 4000000500 {
		t.Errorf("llama3 exclusive bytes = %d, want 4000000500 (shared layer excluded)", llama.Bytes)
	}
	if llama.Label != "llama3:latest (+274.0 MB shared)" {
		t.Errorf("llama3 label = %q", llama.Label)
	}

	nomic := items[byArg["nomic-embed-text:latest"]]
	if nomic.Bytes != 500 {
		t.Errorf("nomic exclusive bytes = %d, want 500 (its only layer is shared)", nomic.Bytes)
	}

	hf := items[byArg["hf.co/org/model:Q4"]]
	if hf.Bytes != 1000500 {
		t.Errorf("hf.co model bytes = %d", hf.Bytes)
	}
	if hf.LastUsed.IsZero() {
		t.Error("manifest mtime should supply LastUsed")
	}
}

func TestOllamaModelName(t *testing.T) {
	cases := []struct {
		registry, ns, model, tag, want string
	}{
		{"registry.ollama.ai", "library", "llama3", "latest", "llama3:latest"},
		{"registry.ollama.ai", "someuser", "custom", "v1", "someuser/custom:v1"},
		{"hf.co", "org", "model", "Q4", "hf.co/org/model:Q4"},
	}
	for _, c := range cases {
		if got := ollamaModelName(c.registry, c.ns, c.model, c.tag); got != c.want {
			t.Errorf("ollamaModelName(%q,%q,%q,%q) = %q, want %q", c.registry, c.ns, c.model, c.tag, got, c.want)
		}
	}
}

func TestOllamaManifestsDirEnvOverride(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "manifests"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_MODELS", base)
	got, ok := ollamaManifestsDir(context.Background(), testWalker())
	if !ok || got != filepath.Join(base, "manifests") {
		t.Fatalf("ollamaManifestsDir() = (%q, %v)", got, ok)
	}
}
