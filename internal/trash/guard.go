package trash

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GuardPath rejects paths that must never become deletion targets:
// empty or relative paths, the filesystem root and its direct
// children, the home directory itself, and mount roots under
// /Volumes. Deeper paths inside those trees are fine.
func GuardPath(path, home string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path guard: empty path")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path guard: %q is not absolute", path)
	}
	clean := filepath.Clean(path)
	if clean == "/" {
		return fmt.Errorf("path guard: refusing filesystem root")
	}
	if home != "" && clean == filepath.Clean(home) {
		return fmt.Errorf("path guard: refusing home directory %q", clean)
	}
	rel := strings.TrimPrefix(clean, "/")
	depth := len(strings.Split(rel, string(filepath.Separator)))
	if depth == 1 {
		return fmt.Errorf("path guard: refusing top-level directory %q", clean)
	}
	if parts := strings.Split(rel, string(filepath.Separator)); parts[0] == "Volumes" && depth <= 2 {
		return fmt.Errorf("path guard: refusing mount root %q", clean)
	}
	return nil
}
