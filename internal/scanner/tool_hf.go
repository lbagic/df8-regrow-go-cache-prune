package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// HuggingFace hub cache (docs/research/02 §9). Enumeration walks the
// documented cache layout directly instead of shelling out to
// `hf`/`huggingface-cli`: the CLI is usually venv-bound (a machine with
// 100GB of models often has no `hf` on PATH), and the cache is plain
// filesystem — no DB to desync. Blobs live *per repo* (snapshots
// symlink into the sibling blobs/ dir), so trashing a whole repo dir is
// exactly what `hf cache rm` does, with undo on top. Sizes come from
// DirSize (physical blocks; symlinks not followed), so a blob shared by
// several snapshots of one repo counts once — dedup-aware by
// construction.

func queryHFHub(ctx context.Context) ([]engine.Item, error) {
	dir, ok := hfHubDir(ctx, defaultWalker)
	if !ok {
		return nil, nil
	}
	return scanHFHub(ctx, defaultWalker, dir)
}

// hfHubDir resolves the hub cache location the way huggingface_hub
// does: HF_HUB_CACHE > HUGGINGFACE_HUB_CACHE (legacy) > $HF_HOME/hub >
// ~/.cache/huggingface/hub. A missing dir means the rule does not
// apply to this machine.
func hfHubDir(ctx context.Context, w *walker) (string, bool) {
	for _, env := range []string{"HF_HUB_CACHE", "HUGGINGFACE_HUB_CACHE"} {
		if v := os.Getenv(env); v != "" {
			return v, w.isDir(ctx, v)
		}
	}
	base := os.Getenv("HF_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, ".cache", "huggingface")
	}
	p := filepath.Join(base, "hub")
	return p, w.isDir(ctx, p)
}

// scanHFHub lists one item per repo. Recency is the repo's newest
// mtime (Usage.Newest): the download or refresh of any snapshot. A hub
// directory that refuses or blocks adds an unreadable marker item.
func scanHFHub(ctx context.Context, w *walker, dir string) ([]engine.Item, error) {
	entries, partial, err := w.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	var items []engine.Item
	if partial {
		items = append(items, unreadableMarker(homeTilde(dir)))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoType, repoID, ok := decodeHFRepoDir(e.Name())
		if !ok {
			continue // version.txt, .locks, unknown layouts
		}
		repoPath := filepath.Join(dir, e.Name())
		u, _, err := w.usage(ctx, repoPath)
		if err != nil {
			return items, err // only ctx cancellation reaches here
		}
		label := repoID
		if repoType != "model" {
			label += " (" + repoType + ")"
		}
		items = append(items, engine.Item{
			Path:     repoPath,
			Label:    label,
			Arg:      repoType + "/" + repoID,
			Bytes:    u.Bytes,
			LastUsed: u.Newest,
			Partial:  u.Partial,
		})
	}
	return items, nil
}

// decodeHFRepoDir reverses huggingface_hub's folder naming:
// "models--meta-llama--Llama-3-8B" → ("model", "meta-llama/Llama-3-8B").
// Repo names cannot contain "--", so the split is unambiguous.
func decodeHFRepoDir(name string) (repoType, repoID string, ok bool) {
	segs := strings.Split(name, "--")
	if len(segs) < 2 {
		return "", "", false
	}
	switch segs[0] {
	case "models", "datasets", "spaces":
		return strings.TrimSuffix(segs[0], "s"), strings.Join(segs[1:], "/"), true
	}
	return "", "", false
}
