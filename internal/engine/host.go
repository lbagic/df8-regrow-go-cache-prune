package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Host is the machine rules are resolved against. Injectable so tests
// and fixture homes never depend on the real machine.
type Host struct {
	OS      string // GOOS: darwin, linux
	Version string // OS release, e.g. "15.5" (sw_vers) or "" if unknown
	Home    string // home directory; ~ in rules expands to this
	// Root re-anchors absolute rule paths (/Library, /Applications)
	// so fixture tests can fake the system tree. Empty or "/" means
	// the real filesystem. Home is never re-anchored: it is already
	// a concrete path.
	Root string
}

// DetectHost inspects the running machine.
func DetectHost() Host {
	home, _ := os.UserHomeDir()
	h := Host{OS: runtime.GOOS, Home: home}
	if h.OS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			h.Version = strings.TrimSpace(string(out))
		}
	}
	return h
}

// ExpandPath resolves a leading ~ against the host home and anchors
// absolute paths under Root when one is set.
func (h Host) ExpandPath(p string) string {
	if p == "~" {
		return h.Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(h.Home, p[2:])
	}
	if h.Root != "" && h.Root != "/" && filepath.IsAbs(p) {
		return filepath.Join(h.Root, p)
	}
	return p
}

// ResolvePaths returns the rule's known paths that apply to this host:
// the entries under the host OS whose version constraints match, with
// ~ expanded. Existence is the scanner's concern, not resolution's.
func (h Host) ResolvePaths(r Rule) []string {
	var out []string
	for _, e := range r.Paths[h.OS] {
		if !e.matches(h.Version) {
			continue
		}
		out = append(out, h.ExpandPath(e.Path))
	}
	return out
}

// Causes returns the rule's fix-the-cause rows that apply to this
// host's version.
func (h Host) Causes(r Rule) []Cause {
	var out []Cause
	for _, c := range r.Causes {
		if (PathEntry{OSMin: c.OSMin, OSMax: c.OSMax}).matches(h.Version) {
			out = append(out, c)
		}
	}
	return out
}
