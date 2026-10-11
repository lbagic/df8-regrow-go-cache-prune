package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNameAndMarker(t *testing.T) {
	home := t.TempDir()
	// Real hit: target/ with CACHEDIR.TAG.
	touch(t, filepath.Join(home, "workspace", "proj-a", "target", "CACHEDIR.TAG"))
	// Decoy: target/ without the marker (a non-cargo dir named target).
	touch(t, filepath.Join(home, "workspace", "proj-b", "target", "somefile"))
	// Decoy: marker in a dir not named target.
	touch(t, filepath.Join(home, "workspace", "proj-c", "cache", "CACHEDIR.TAG"))
	// Inside builtin-excluded tree: never found.
	touch(t, filepath.Join(home, "workspace", "node_modules", "dep", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{
		Roots:   []string{"~/workspace", "~/does-not-exist"},
		Name:    "target",
		Markers: []string{"CACHEDIR.TAG"},
	}
	got, _ := discover(context.Background(), testWalker(), host, spec)
	want := []string{filepath.Join(home, "workspace", "proj-a", "target")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

func TestDiscoverByNameMatchesInsideBuiltinExcludeList(t *testing.T) {
	// node_modules is builtin-excluded for descent, but a rule that
	// *targets* node_modules by name must still find it.
	home := t.TempDir()
	touch(t, filepath.Join(home, "app", "node_modules", "left-pad", "index.js"))

	host := engine.Host{OS: "darwin", Home: home}
	got, _ := discover(context.Background(), testWalker(), host, engine.Discover{Roots: []string{"~"}, Name: "node_modules"})
	want := []string{filepath.Join(home, "app", "node_modules")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

func TestDiscoverMaxDepth(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "a", "b", "c", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{Roots: []string{"~"}, Name: "target", Markers: []string{"CACHEDIR.TAG"}, MaxDepth: 2}
	if got, _ := discover(context.Background(), testWalker(), host, spec); len(got) != 0 {
		t.Errorf("depth-limited discover found %v", got)
	}

	spec.MaxDepth = 4
	if got, _ := discover(context.Background(), testWalker(), host, spec); len(got) != 1 {
		t.Errorf("discover at sufficient depth found %v", got)
	}
}

func TestDiscoverRuleExcludeWins(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "vendor", "target", "CACHEDIR.TAG"))
	touch(t, filepath.Join(home, "src", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{Roots: []string{"~"}, Name: "target", Markers: []string{"CACHEDIR.TAG"}, Exclude: []string{"vendor"}}
	got, _ := discover(context.Background(), testWalker(), host, spec)
	want := []string{filepath.Join(home, "src", "target")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

// B6: the shipped node_modules rule must reach monorepo depths.
func TestNodeModulesRuleReachesDepthEight(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var spec *engine.Discover
	for _, r := range catalog {
		if r.ID == "node-modules-dirs" {
			spec = r.Discover
		}
	}
	if spec == nil {
		t.Fatal("node-modules-dirs rule missing from the embedded catalog")
	}
	home := t.TempDir()
	deep := filepath.Join(home, "workspace", "org", "repo", "apps", "web", "packages", "ui", "lib", "node_modules")
	touch(t, filepath.Join(deep, "left-pad", "index.js"))

	got, _ := discover(context.Background(), testWalker(), engine.Host{OS: "darwin", Home: home}, *spec)
	if want := []string{deep}; !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

// A root reached twice (a symlink to another root, or a root inside
// another) must not report its hits twice.
func TestDiscoverRootsReachedTwiceReportOnce(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "workspace", "app", "node_modules", "x", "index.js"))
	if err := os.Symlink(filepath.Join(home, "workspace"), filepath.Join(home, "code")); err != nil {
		t.Fatal(err)
	}
	spec := engine.Discover{Roots: []string{"~/workspace", "~/code", "~/workspace/app"}, Name: "node_modules"}
	got, _ := discover(context.Background(), testWalker(), engine.Host{OS: "darwin", Home: home}, spec)
	if want := []string{filepath.Join(home, "workspace", "app", "node_modules")}; !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

// A root that is itself a symlink is walked through.
func TestDiscoverFollowsASymlinkedRoot(t *testing.T) {
	home := t.TempDir()
	real := t.TempDir()
	touch(t, filepath.Join(real, "app", "node_modules", "x", "index.js"))
	if err := os.Symlink(real, filepath.Join(home, "workspace")); err != nil {
		t.Fatal(err)
	}
	spec := engine.Discover{Roots: []string{"~/workspace"}, Name: "node_modules"}
	got, _ := discover(context.Background(), testWalker(), engine.Host{OS: "darwin", Home: home}, spec)
	if want := []string{filepath.Join(home, "workspace", "app", "node_modules")}; !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

// A root inside another, reached through a symlink, keeps the hits
// that lie deeper than the outer root's depth limit.
func TestDiscoverInnerRootKeepsItsDeepHits(t *testing.T) {
	home := t.TempDir()
	deep := filepath.Join(home, "workspace", "a", "b", "c", "node_modules")
	touch(t, filepath.Join(deep, "x", "index.js"))
	if err := os.Symlink(filepath.Join(home, "workspace", "a"), filepath.Join(home, "code")); err != nil {
		t.Fatal(err)
	}
	spec := engine.Discover{Roots: []string{"~/workspace", "~/code"}, Name: "node_modules", MaxDepth: 3}
	got, _ := discover(context.Background(), testWalker(), engine.Host{OS: "darwin", Home: home}, spec)
	if want := []string{filepath.Join(home, "code", "b", "c", "node_modules")}; !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want the hit through the inner root %v", got, want)
	}
}

// A home written with a trailing slash must not shift every depth.
func TestDiscoverDepthWithTrailingSlashRoot(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "a", "b", "target", "CACHEDIR.TAG"))
	spec := engine.Discover{Roots: []string{"~"}, Name: "target", Markers: []string{"CACHEDIR.TAG"}, MaxDepth: 2}
	host := engine.Host{OS: "darwin", Home: home + "/"}
	if got, _ := discover(context.Background(), testWalker(), host, spec); len(got) != 0 {
		t.Errorf("depth-3 hit found under max_depth 2: %v", got)
	}
	spec.MaxDepth = 3
	if got, _ := discover(context.Background(), testWalker(), host, spec); len(got) != 1 {
		t.Errorf("depth-3 hit missed under max_depth 3: %v", got)
	}
}
