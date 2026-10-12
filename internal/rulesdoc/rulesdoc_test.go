package rulesdoc

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/rules"
)

var update = flag.Bool("update", false, "rewrite docs/RULES.md")

var page = filepath.Join("..", "..", "docs", "RULES.md")

func catalog(t *testing.T) []engine.Rule {
	t.Helper()
	c, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The committed page is what Render writes for the embedded catalog: a
// rule edited without regenerating, or the page edited by hand, fails.
func TestPageIsCurrent(t *testing.T) {
	want := Render(catalog(t))
	if *update {
		if err := os.WriteFile(page, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read %s (run `%s` to create it): %v", page, Command, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the rules; run `%s` and commit the result", page, Command)
	}
}

// A tool query's targets are invisible in the rule's fields, so the
// rule has to say in words what its scan walks and runs.
func TestEveryQueryRuleSaysWhatItScans(t *testing.T) {
	for _, r := range catalog(t) {
		if r.ToolQuery != "" && strings.TrimSpace(r.Scans) == "" {
			t.Errorf("%s: tool_query %s needs a `scans:` line for docs/RULES.md", r.ID, r.ToolQuery)
		}
	}
}

func TestEveryPreActionHasText(t *testing.T) {
	for _, name := range engine.KnownPreActions() {
		if _, ok := PreActionText(name); !ok {
			t.Errorf("pre_action %s has no sentence in preActions", name)
		}
	}
}

func TestEveryCategoryHasAGroup(t *testing.T) {
	for _, r := range catalog(t) {
		if _, ok := GroupName(r.Category); !ok {
			t.Errorf("%s: category %s has no heading in groups", r.ID, r.Category)
		}
	}
}

// Each entry links rules/<id>.yaml, so a rule's file is named by its id.
func TestRuleFilesAreNamedByID(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range catalog(t) {
		ids[r.ID] = true
	}
	files, err := fs.Glob(rules.FS, "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if id := strings.TrimSuffix(f, ".yaml"); !ids[id] {
			t.Errorf("rules/%s holds no rule with id %s", f, id)
		}
	}
}

func TestProseEscapesMarkupOutsideCode(t *testing.T) {
	cases := map[string]string{
		"git worktree add <path> <branch>":    "git worktree add &lt;path&gt; &lt;branch&gt;",
		"run `git worktree add <path>` again": "run `git worktree add <path>` again",
		"~/.lmstudio/models/*/*":              `~/.lmstudio/models/\*/\*`,
		"`*.mov` files":                       "`*.mov` files",
		"plain text stays":                    "plain text stays",
	}
	for in, want := range cases {
		if got := prose(in); got != want {
			t.Errorf("prose(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandQuotesAsAShellReads(t *testing.T) {
	r := engine.Rule{Sudo: true, NativeCommand: engine.Argv{"find", "{path}", "-name", "*.mov", "-delete"}}
	if got, want := command(r), "sudo find {path} -name '*.mov' -delete"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	r = engine.Rule{NativeCommand: engine.Argv{"osascript", "-e", `tell application "Finder" to empty trash`}}
	if got, want := command(r), `osascript -e 'tell application "Finder" to empty trash'`; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestVersionBoundsNameWhatTheyAdmit(t *testing.T) {
	cases := []struct{ min, max, want string }{
		{"", "", ""},
		{"", "25", " (macOS 15.x and earlier)"},
		{"", "25.9", " (macOS 15.x and earlier)"},
		{"", "26", " (macOS 26.0.0 and earlier)"},
		{"", "15", " (macOS 15.0.0 and earlier)"},
		{"", "14.4", " (macOS 14.4.0 and earlier)"},
		{"", "26.0.1", " (macOS 26.0.1 and earlier)"},
		{"26", "", " (macOS 26 and later)"},
		{"16", "", " (macOS 26 and later)"},
		{"14.4", "", " (macOS 14.4 and later)"},
		{"14", "15.7", " (macOS 14 to 15.7.0)"},
		{"16", "26.1", " (macOS 26 to 26.1.0)"},
		{"15.5", "25", " (macOS 15.5 to 15.x)"},
		{"14", "25.9", " (macOS 14 to 15.x)"},
		{"16", "25", " (no macOS release)"},
		{"26", "15", " (no macOS release)"},
	}
	for _, c := range cases {
		if got := versionText(c.min, c.max); got != c.want {
			t.Errorf("versionText(%q, %q) = %q, want %q", c.min, c.max, got, c.want)
		}
	}
}
