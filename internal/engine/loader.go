package engine

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/lbagic/regrow/rules"
)

// LoadFS parses every *.yaml in fsys as one rule per file, validates,
// rejects duplicate ids, and returns the catalog sorted by id.
// Decoding is strict: unknown fields fail so schema typos surface in
// tests instead of silently dropping data.
func LoadFS(fsys fs.FS) ([]Rule, error) {
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.yaml rules found")
	}
	sort.Strings(files)
	seen := make(map[string]string, len(files))
	catalog := make([]Rule, 0, len(files))
	for _, name := range files {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var r Rule
		if err := unmarshalStrict(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if prev, dup := seen[r.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate rule id %q (also in %s)", name, r.ID, prev)
		}
		seen[r.ID] = name
		catalog = append(catalog, r)
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	return catalog, nil
}

// LoadDir loads a rules directory (--rules-dir override).
func LoadDir(dir string) ([]Rule, error) {
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", dir)
	}
	return LoadFS(os.DirFS(dir))
}

// LoadEmbedded loads the catalog compiled into the binary.
func LoadEmbedded() ([]Rule, error) {
	return LoadFS(rules.FS)
}

// WithoutBeta drops beta rules from the catalog — the default view.
// --beta-rules keeps them (staged rollout of new rules, PRODUCT.md).
func WithoutBeta(catalog []Rule) []Rule {
	out := make([]Rule, 0, len(catalog))
	for _, r := range catalog {
		if !r.Beta {
			out = append(out, r)
		}
	}
	return out
}

func unmarshalStrict(data []byte, out *Rule) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}
