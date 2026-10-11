package engine

import (
	"reflect"
	"testing"
)

func TestResolvePathsVersionAware(t *testing.T) {
	// Modeled on the aerial-wallpapers path move (research/02 §14):
	// same rule, different path per macOS release.
	rule := Rule{
		ID: "aerial", Title: "Aerial", Category: "system", Risk: RiskSafe,
		Paths: map[string][]PathEntry{
			"darwin": {
				{Path: "/Library/Application Support/com.apple.idleassetsd/Customer", OSMax: "15.99"},
				{Path: "~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches", OSMin: "26"},
			},
			"linux": {{Path: "~/.cache/never-here"}},
		},
	}

	sequoia := Host{OS: "darwin", Version: "15.5", Home: "/Users/t"}
	if got := sequoia.ResolvePaths(rule); !reflect.DeepEqual(got, []string{
		"/Library/Application Support/com.apple.idleassetsd/Customer",
	}) {
		t.Errorf("sequoia resolved %v", got)
	}

	tahoe := Host{OS: "darwin", Version: "26.1", Home: "/Users/t"}
	if got := tahoe.ResolvePaths(rule); !reflect.DeepEqual(got, []string{
		"/Users/t/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches",
	}) {
		t.Errorf("tahoe resolved %v", got)
	}

	linux := Host{OS: "linux", Home: "/home/t"}
	if got := linux.ResolvePaths(rule); !reflect.DeepEqual(got, []string{"/home/t/.cache/never-here"}) {
		t.Errorf("linux resolved %v", got)
	}
}

func TestExpandPath(t *testing.T) {
	h := Host{Home: "/Users/t"}
	tests := map[string]string{
		"~":                "/Users/t",
		"~/Library/Caches": "/Users/t/Library/Caches",
		"/absolute/path":   "/absolute/path",
		"~user/other":      "~user/other", // ~user syntax unsupported, left as-is
	}
	for in, want := range tests {
		if got := h.ExpandPath(in); got != want {
			t.Errorf("ExpandPath(%q) = %q, want %q", in, got, want)
		}
	}
}
