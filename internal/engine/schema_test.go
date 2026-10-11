package engine

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPathEntryUnmarshalScalarAndMap(t *testing.T) {
	var got struct {
		Paths []PathEntry `yaml:"paths"`
	}
	src := `
paths:
  - ~/Library/Caches/example
  - path: /Library/Application Support/com.apple.idleassetsd/Customer
    os_min: "13"
    os_max: "15"
`
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got.Paths))
	}
	if got.Paths[0].Path != "~/Library/Caches/example" || got.Paths[0].OSMin != "" {
		t.Errorf("scalar entry parsed wrong: %+v", got.Paths[0])
	}
	if got.Paths[1].OSMin != "13" || got.Paths[1].OSMax != "15" {
		t.Errorf("map entry parsed wrong: %+v", got.Paths[1])
	}
}

func TestPathEntryVersionMatch(t *testing.T) {
	tests := []struct {
		entry   PathEntry
		version string
		want    bool
	}{
		{PathEntry{Path: "p"}, "15.5", true},
		{PathEntry{Path: "p", OSMin: "26"}, "26.0", true},
		{PathEntry{Path: "p", OSMin: "26"}, "15.5", false},
		{PathEntry{Path: "p", OSMax: "15"}, "15.2", false}, // 15.2 > 15
		{PathEntry{Path: "p", OSMax: "15.4"}, "15.2", true},
		{PathEntry{Path: "p", OSMin: "14", OSMax: "15.9"}, "15.5", true},
		// Unknown host version: constrained entries do not apply.
		{PathEntry{Path: "p", OSMin: "26"}, "", false},
		{PathEntry{Path: "p"}, "", true},
	}
	for _, tt := range tests {
		if got := tt.entry.matches(tt.version); got != tt.want {
			t.Errorf("entry %+v vs version %q: got %v want %v", tt.entry, tt.version, got, tt.want)
		}
	}
}

func TestCauseYAMLAndHostVersion(t *testing.T) {
	src := `
id: example-rule
title: Example
category: macos
risk: safe
paths:
  darwin:
    - ~/Library/Caches/example
causes:
  - check: seen-everywhere
    title: Everywhere
    story: why it refills
    fix:
      - "first step"
      - "second step"
  - check: seen-on-15
    os_max: "25"
    title: Sequoia
    story: why it refills there
    fix: ["only step"]
`
	var r Rule
	if err := unmarshalStrict([]byte(src), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(r.Causes) != 2 || !reflect.DeepEqual(r.Causes[0].Fix, []string{"first step", "second step"}) || r.Causes[1].OSMax != "25" {
		t.Fatalf("causes parsed wrong: %+v", r.Causes)
	}

	checks := func(version string) []string {
		var out []string
		for _, c := range (Host{OS: "darwin", Version: version}).Causes(r) {
			out = append(out, c.Check)
		}
		return out
	}
	if got := checks("15.7.7"); !reflect.DeepEqual(got, []string{"seen-everywhere", "seen-on-15"}) {
		t.Errorf("macOS 15.7.7 gets %v, want both causes", got)
	}
	if got := checks("26.0"); !reflect.DeepEqual(got, []string{"seen-everywhere"}) {
		t.Errorf("macOS 26.0 gets %v, want only the unbounded cause", got)
	}
}

func validRule() Rule {
	return Rule{
		ID:       "example-rule",
		Title:    "Example",
		Category: "dev-caches",
		Risk:     RiskSafe,
		Paths:    map[string][]PathEntry{"darwin": {{Path: "~/Library/Caches/example"}}},
	}
}

func TestValidate(t *testing.T) {
	if err := validRule().Validate(); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	trashWithHook := validRule()
	trashWithHook.PreAction = PreActionAgentScratchRecheck
	if err := trashWithHook.Validate(); err != nil {
		t.Fatalf("a pre_action on a Trash rule must be valid, the hook gates each move: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Rule)
		wantErr string
	}{
		{"bad id", func(r *Rule) { r.ID = "Bad_ID" }, "kebab-case"},
		{"missing title", func(r *Rule) { r.Title = "" }, "title"},
		{"missing category", func(r *Rule) { r.Category = "" }, "category"},
		{"bad risk", func(r *Rule) { r.Risk = "expert" }, "risk"},
		{"no source", func(r *Rule) { r.Paths = nil }, "at least one of"},
		{"unknown os", func(r *Rule) { r.Paths = map[string][]PathEntry{"windows": {{Path: "C:"}}} }, "unknown paths os"},
		{"empty path", func(r *Rule) { r.Paths = map[string][]PathEntry{"darwin": {{Path: ""}}} }, "empty path"},
		{"discover without roots", func(r *Rule) { r.Discover = &Discover{Name: "target"} }, "roots"},
		{"discover without matcher", func(r *Rule) { r.Discover = &Discover{Roots: []string{"~"}} }, "name or markers"},
		{
			"surface-only with native command",
			func(r *Rule) { r.Risk = RiskSurfaceOnly; r.NativeCommand = Argv{"rm", "-rf"} },
			"surface-only",
		},
		{
			"placeholder typo",
			func(r *Rule) { r.ToolQuery = "q"; r.NativeCommand = Argv{"tool", "rm", "{id}"} },
			"unknown placeholder {id}",
		},
		{
			"arg placeholder without tool query",
			func(r *Rule) { r.NativeCommand = Argv{"tool", "rm", "{arg}"} },
			"only tool_query items supply args",
		},
		{
			"unknown pre_action",
			func(r *Rule) {
				r.ToolQuery = "q"
				r.NativeCommand = Argv{"docker", "volume", "rm", "{arg}"}
				r.PreAction = "docker-volume-exprot"
			},
			"unknown pre_action",
		},
		{
			"pre_action on whole-rule command",
			func(r *Rule) {
				r.NativeCommand = Argv{"docker", "system", "prune"}
				r.PreAction = PreActionVolumeExport
			},
			"per-item action",
		},
		{
			"empties_trash without native command",
			func(r *Rule) { r.Risk = RiskCaution; r.EmptiesTrash = true },
			"whole-rule native_command",
		},
		{
			"empties_trash on per-item command",
			func(r *Rule) {
				r.Risk = RiskCaution
				r.NativeCommand = Argv{"trash-tool", "purge", "{path}"}
				r.EmptiesTrash = true
			},
			"whole-rule native_command",
		},
		{
			"empties_trash on safe rule",
			func(r *Rule) {
				r.NativeCommand = Argv{"osascript", "-e", "empty"}
				r.EmptiesTrash = true
			},
			"must not be safe",
		},
		{
			"doctor without threshold",
			func(r *Rule) { r.Doctor = &Doctor{Story: "known bug"} },
			"flag_above",
		},
		{
			"doctor without story",
			func(r *Rule) { r.Doctor = &Doctor{FlagAbove: 1 << 30} },
			"doctor.story",
		},
		{
			"cause without a check name",
			func(r *Rule) { r.Causes = []Cause{{Title: "t", Story: "s", Fix: []string{"f"}}} },
			"kebab-case",
		},
		{
			"cause without story",
			func(r *Rule) { r.Causes = []Cause{{Check: "some-check", Title: "t", Fix: []string{"f"}}} },
			"some-check needs a title and a story",
		},
		{
			"cause without fix",
			func(r *Rule) { r.Causes = []Cause{{Check: "some-check", Title: "t", Story: "s"}} },
			"some-check needs fix lines",
		},
		{
			"cause with an empty fix line",
			func(r *Rule) {
				r.Causes = []Cause{{Check: "some-check", Title: "t", Story: "s", Fix: []string{"f", ""}}}
			},
			"some-check needs fix lines",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRule()
			tt.mutate(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestArgvPlaceholders(t *testing.T) {
	a := Argv{"tool", "{arg}", "cp {path} {path}", "{}"}
	got := a.Placeholders()
	want := []string{"{arg}", "{path}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Placeholders = %v, want %v", got, want)
	}
	if !a.PerItem() {
		t.Fatal("command with placeholders must be per-item")
	}
	if (Argv{"go", "clean"}).PerItem() {
		t.Fatal("command without placeholders must not be per-item")
	}
}

func TestArgvExpandItem(t *testing.T) {
	a := Argv{"tool", "rm", "{arg}", "--from", "{path}"}
	got, err := a.ExpandItem(Item{Path: "/x/y", Arg: "m1", Label: "model"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tool", "rm", "m1", "--from", "/x/y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandItem = %v, want %v", got, want)
	}
}

func TestArgvExpandItemRefusesEmptyValue(t *testing.T) {
	a := Argv{"tool", "rm", "{arg}"}
	if _, err := a.ExpandItem(Item{Label: "no-arg-item"}); err == nil {
		t.Fatal("empty {arg} substitution must fail, not produce a blank argument")
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		in   string
		want ByteSize
	}{
		{"20GB", 20 << 30},
		{"20GiB", 20 << 30},
		{"5 gb", 5 << 30},
		{"500MB", 500 << 20},
		{"2TB", 2 << 40},
		{"1.5KB", 1536},
		{"512B", 512},
		{"512", 512},
	}
	for _, tt := range tests {
		got, err := ParseByteSize(tt.in)
		if err != nil {
			t.Errorf("ParseByteSize(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"", "GB", "-5GB", "5XB", "lots"} {
		if got, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, want error", bad, got)
		}
	}
}

func TestByteSizeYAML(t *testing.T) {
	var got struct {
		Doctor Doctor `yaml:"doctor"`
	}
	src := "doctor:\n  flag_above: 20GB\n  story: runaway index\n"
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatal(err)
	}
	if got.Doctor.FlagAbove != 20<<30 || got.Doctor.Story != "runaway index" {
		t.Fatalf("parsed wrong: %+v", got.Doctor)
	}
	if err := yaml.Unmarshal([]byte("doctor:\n  flag_above: soon\n"), &got); err == nil {
		t.Fatal("want error for unparseable flag_above")
	}
}
