package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Ollama models (docs/research/02 §9). Enumeration reads the manifest
// store directly (~/.ollama/models/manifests) instead of `ollama list`:
// the CLI needs the server running — a stopped Ollama.app would hide
// 40GB of models from the scan. Deletion stays with `ollama rm` (the
// rule's native command): blobs are refcounted across models and only
// the steward may GC them.
//
// Sizes are dedup-aware (research §15): Item.Bytes is the model's
// *exclusive* bytes — blobs no other model references — because that is
// what `ollama rm` actually frees. Bytes parked in shared blobs are
// surfaced in the label ("+4.2 GB shared") and free up only when the
// last referencing model goes.

func queryOllamaModels(ctx context.Context) ([]engine.Item, error) {
	dir, ok := ollamaManifestsDir(ctx, defaultWalker)
	if !ok {
		return nil, nil
	}
	return scanOllamaModels(ctx, defaultWalker, dir)
}

// ollamaManifestsDir resolves the store the way ollama does:
// $OLLAMA_MODELS else ~/.ollama/models. Missing dir = no ollama here.
func ollamaManifestsDir(ctx context.Context, w *walker) (string, bool) {
	base := os.Getenv("OLLAMA_MODELS")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, ".ollama", "models")
	}
	p := filepath.Join(base, "manifests")
	return p, w.isDir(ctx, p)
}

// ollamaManifest is the OCI-style manifest ollama writes per model tag.
type ollamaManifest struct {
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
}

type ollamaModel struct {
	name     string
	blobs    map[string]int64 // digest → size
	lastUsed time.Time
}

// scanOllamaModels reads every manifest under manifestsDir
// (registry/namespace/model/tag). A manifest folder that refuses or
// blocks adds an unreadable marker item: models behind it are missing,
// and the shared/exclusive split of the others may be off.
func scanOllamaModels(ctx context.Context, w *walker, manifestsDir string) ([]engine.Item, error) {
	var mu sync.Mutex
	closed := false
	var models []ollamaModel
	partial, err := w.walk(ctx, manifestsDir, func(dir string, batch []fs.DirEntry) ([]string, bool) {
		rel, err := filepath.Rel(manifestsDir, dir)
		if err != nil {
			return nil, true
		}
		depth := 0 // segments of dir below manifestsDir
		if rel != "." {
			depth = len(strings.Split(rel, string(filepath.Separator)))
		}
		var subdirs []string
		var found []ollamaModel
		for _, e := range batch {
			p := childPath(dir, e.Name())
			if e.IsDir() {
				if depth < 3 {
					subdirs = append(subdirs, p)
				}
				continue
			}
			if depth != 3 {
				continue // registry/namespace/model/tag — anything else is not a manifest
			}
			m, ok := readOllamaManifest(p)
			if !ok {
				continue // half-written manifest: skip, keep scanning
			}
			segs := strings.Split(filepath.Join(rel, e.Name()), string(filepath.Separator))
			m.name = ollamaModelName(segs[0], segs[1], segs[2], segs[3])
			if fi, err := e.Info(); err == nil {
				m.lastUsed = fi.ModTime()
			}
			found = append(found, m)
		}
		mu.Lock()
		if !closed {
			models = append(models, found...)
		}
		mu.Unlock()
		return subdirs, true
	})
	if err != nil {
		return nil, err
	}
	mu.Lock()
	closed = true
	models = slices.Clone(models)
	mu.Unlock()

	refs := map[string]int{} // digest → number of models referencing it
	for _, m := range models {
		for digest := range m.blobs {
			refs[digest]++
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].name < models[j].name })
	items := make([]engine.Item, 0, len(models)+1)
	for _, m := range models {
		var exclusive, shared int64
		for digest, size := range m.blobs {
			if refs[digest] > 1 {
				shared += size
			} else {
				exclusive += size
			}
		}
		label := m.name
		if shared > 0 {
			label += fmt.Sprintf(" (+%s shared)", siBytes(shared))
		}
		items = append(items, engine.Item{Label: label, Arg: m.name, Bytes: exclusive, LastUsed: m.lastUsed})
	}
	if partial {
		items = append(items, unreadableMarker("manifests under "+homeTilde(manifestsDir)))
	}
	return items, nil
}

func readOllamaManifest(path string) (ollamaModel, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ollamaModel{}, false
	}
	var m ollamaManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ollamaModel{}, false
	}
	blobs := map[string]int64{}
	if m.Config.Digest != "" {
		blobs[m.Config.Digest] = m.Config.Size
	}
	for _, l := range m.Layers {
		blobs[l.Digest] = l.Size
	}
	return ollamaModel{blobs: blobs}, true
}

// ollamaModelName renders the name `ollama rm` accepts, dropping the
// default registry and namespace the way `ollama list` does:
// (registry.ollama.ai, library, llama3, latest) → "llama3:latest";
// (hf.co, org, model, Q4) → "hf.co/org/model:Q4".
func ollamaModelName(registry, namespace, model, tag string) string {
	name := model + ":" + tag
	if namespace != "library" || registry != "registry.ollama.ai" {
		name = namespace + "/" + name
	}
	if registry != "registry.ollama.ai" {
		name = registry + "/" + name
	}
	return name
}

// siBytes formats like ollama does: SI units, one decimal.
func siBytes(n int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
