package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// One owner per byte: the containment forest can only nest what it
// sees as nested, and it compares paths byte for byte. So no two rules
// may name the same path, even in another case or through a macOS
// system symlink, and two rules that share a prefix must spell it the
// same way. Two rules discovering the same directory name would both
// claim every hit.
func ownerConflicts(catalog []Rule) []string {
	type spelling struct{ rule, path string }
	var conflicts []string
	for _, osName := range []string{"darwin", "linux"} {
		owner := map[string]spelling{}
		var all []spelling
		for _, r := range catalog {
			for _, e := range r.Paths[osName] {
				p := filepath.Clean(e.Path)
				all = append(all, spelling{r.ID, p})
				key := ownerKey(osName, p)
				if prev, ok := owner[key]; ok && prev.rule != r.ID {
					conflicts = append(conflicts, fmt.Sprintf("%s: %s (%s) and %s (%s) name one path", osName, prev.rule, prev.path, r.ID, p))
					continue
				}
				owner[key] = spelling{r.ID, p}
			}
		}
		for _, outer := range all {
			for _, inner := range all {
				if outer.rule == inner.rule {
					continue
				}
				nested := strings.HasPrefix(ownerKey(osName, inner.path), ownerKey(osName, outer.path)+"/")
				if nested && !strings.HasPrefix(inner.path, outer.path+"/") {
					conflicts = append(conflicts, fmt.Sprintf("%s: %s (%s) is inside %s (%s) but spells the prefix differently", osName, inner.rule, inner.path, outer.rule, outer.path))
				}
			}
		}
	}
	names := map[string]string{}
	for _, r := range catalog {
		if r.Discover == nil || r.Discover.Name == "" {
			continue
		}
		if prev, ok := names[r.Discover.Name]; ok {
			conflicts = append(conflicts, fmt.Sprintf("%s and %s both discover %q", prev, r.ID, r.Discover.Name))
			continue
		}
		names[r.Discover.Name] = r.ID
	}
	slices.Sort(conflicts)
	return conflicts
}

// ownerKey is how the filesystem compares a path: APFS is
// case-insensitive by default, and /tmp, /var and /etc are symlinks
// into /private on macOS.
func ownerKey(osName, path string) string {
	if osName == "darwin" {
		for _, link := range []string{"/tmp", "/var", "/etc"} {
			if path == link || strings.HasPrefix(path, link+"/") {
				path = "/private" + path
				break
			}
		}
	}
	return strings.ToLower(path)
}

func TestCatalogOneOwnerPerByte(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ownerConflicts(catalog) {
		t.Error(c)
	}
}

func TestOwnerConflicts(t *testing.T) {
	pathRule := func(id string, paths ...string) Rule {
		entries := make([]PathEntry, len(paths))
		for i, p := range paths {
			entries[i] = PathEntry{Path: p}
		}
		return Rule{ID: id, Paths: map[string][]PathEntry{"darwin": entries}}
	}
	discoverRule := func(id, name string) Rule {
		return Rule{ID: id, Discover: &Discover{Roots: []string{"~"}, Name: name}}
	}
	cases := []struct {
		name    string
		catalog []Rule
		want    int
	}{
		{"same literal path", []Rule{pathRule("a", "~/Library/Caches/x"), pathRule("b", "~/Library/Caches/x")}, 1},
		{"trailing slash", []Rule{pathRule("a", "~/.npm"), pathRule("b", "~/.npm/")}, 1},
		{"other case", []Rule{pathRule("a", "~/Library/Caches/Yarn"), pathRule("b", "~/Library/Caches/yarn")}, 1},
		{"through /tmp", []Rule{pathRule("a", "/tmp/x"), pathRule("b", "/private/tmp/x")}, 1},
		{"prefix in other case", []Rule{pathRule("a", "~/Library/Caches"), pathRule("b", "~/library/caches/go-build")}, 1},
		{"same discover name", []Rule{discoverRule("a", "dist"), discoverRule("b", "dist")}, 1},
		{"nested, same spelling", []Rule{pathRule("a", "~/Library/Caches"), pathRule("b", "~/Library/Caches/go-build")}, 0},
		{"one rule, two OS", []Rule{{ID: "a", Paths: map[string][]PathEntry{"darwin": {{Path: "~/.npm"}}, "linux": {{Path: "~/.npm"}}}}}, 0},
		{"distinct names", []Rule{discoverRule("a", "target"), discoverRule("b", "node_modules")}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerConflicts(tc.catalog); len(got) != tc.want {
				t.Errorf("got %d conflicts, want %d: %q", len(got), tc.want, got)
			}
		})
	}
}

// The aerial row's fix is the owner's instructions for a download that
// eats tens of GB: the lines that stop it must not drop out of the
// catalog unnoticed.
func TestAerialCauseFixText(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var fix []string
	for _, r := range catalog {
		for _, c := range r.Causes {
			if r.ID == "aerial-wallpapers" && c.Check == "aerial-downloads" {
				fix = c.Fix
			}
		}
	}
	if fix == nil {
		t.Fatal("aerial-wallpapers declares no aerial-downloads cause")
	}
	line := func(parts ...string) bool {
		return slices.ContainsFunc(fix, func(l string) bool {
			for _, p := range parts {
				if !strings.Contains(l, p) {
					return false
				}
			}
			return true
		})
	}
	for _, want := range [][]string{
		{"Quit System Settings", "Wallpaper"},
		{"come back", "the pane is opened"},
		// The disable is untested against SIP; the same line must say so.
		{"sudo launchctl disable system/com.apple.idleassetsd", "untested", "System Integrity Protection"},
		{"sudo killall idleassetsd"},
		{"regrow clean aerial-wallpapers"},
	} {
		if !line(want...) {
			t.Errorf("no fix line with %q in:\n%s", want, strings.Join(fix, "\n"))
		}
	}
}
