// Package config loads the user's optional config file:
// $XDG_CONFIG_HOME/regrow/config.yaml (default ~/.config/regrow/).
// A missing file is the defaults; a file with unknown keys is an
// error — a typo'd key must never silently mean "no config".
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// KeepEntry protects docker objects from regrow by name glob or
// compose project. In YAML an entry is a bare string (a name glob) or
// a map with `name:` and/or `project:`.
type KeepEntry struct {
	Name    string `yaml:"name"`
	Project string `yaml:"project"`
}

func (k *KeepEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		k.Name = node.Value
		return nil
	}
	type plain KeepEntry // drop the method, avoid recursion
	return node.Decode((*plain)(k))
}

func (k KeepEntry) validate() error {
	if k.Name == "" && k.Project == "" {
		return errors.New("keep entry needs a name glob or a project")
	}
	if k.Name != "" {
		if _, err := path.Match(k.Name, "probe"); err != nil {
			return fmt.Errorf("keep name %q: bad glob: %w", k.Name, err)
		}
	}
	return nil
}

// matches reports whether the entry protects the given object.
func (k KeepEntry) matches(name, project string) bool {
	if k.Name != "" {
		ok, _ := path.Match(k.Name, name)
		if !ok {
			return false
		}
	}
	if k.Project != "" && k.Project != project {
		return false
	}
	return true
}

// Docker is the docker provider's config (Prompt G2). Keep-list
// exists because docker labels are immutable after creation — the only
// way to durably say "never touch this volume" is regrow's own config.
type Docker struct {
	Keep []KeepEntry `yaml:"keep"`
	// VolumeExportCap bounds the pre-delete tarball export ("10GiB").
	// Volumes larger than the cap are never offered for deletion —
	// no backup, no removal. Empty means the 10GiB default.
	VolumeExportCap string `yaml:"volume_export_cap"`
}

// Keeps reports whether any keep entry protects the object.
func (d Docker) Keeps(name, project string) bool {
	for _, k := range d.Keep {
		if k.matches(name, project) {
			return true
		}
	}
	return false
}

// ExportCapBytes resolves the export cap.
func (d Docker) ExportCapBytes() (int64, error) {
	if d.VolumeExportCap == "" {
		return DefaultVolumeExportCap, nil
	}
	return ParseSize(d.VolumeExportCap)
}

// DefaultVolumeExportCap is 10 GiB: big enough for real project
// databases, small enough that the export cannot fill a disk the user
// is trying to free (decision log 2026-07-13).
const DefaultVolumeExportCap = 10 << 30

// Config is the whole file.
type Config struct {
	Docker Docker `yaml:"docker"`
}

func (c Config) validate() error {
	for _, k := range c.Docker.Keep {
		if err := k.validate(); err != nil {
			return err
		}
	}
	_, err := c.Docker.ExportCapBytes()
	return err
}

// DefaultPath is $XDG_CONFIG_HOME/regrow/config.yaml, defaulting to
// ~/.config/regrow/config.yaml.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "regrow", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "regrow", "config.yaml"), nil
}

// Load reads the default config file; missing file means defaults.
func Load() (Config, error) {
	p, err := DefaultPath()
	if err != nil {
		return Config{}, err
	}
	return LoadFile(p)
}

// LoadFile reads one config file. Unknown keys are an error.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ParseSize parses human sizes: SI (kB, MB, GB, TB — powers of 1000)
// and binary (KiB, MiB, GiB, TiB), or a bare byte count.
func ParseSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	i := strings.IndexFunc(t, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if i < 0 {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("size %q: %w", s, err)
		}
		return n, nil
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(t[:i]), 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	mult, ok := map[string]float64{
		"B":  1,
		"kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	}[strings.TrimSpace(t[i:])]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit (use B, kB, MB, GB, TB, KiB, MiB, GiB, TiB)", s)
	}
	return int64(val * mult), nil
}
