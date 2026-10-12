// Package rulesdoc writes docs/RULES.md, the reader's catalog of what
// regrow scans, from the rules themselves, so the page lists exactly
// the catalog `regrow rules --beta-rules` lists. A rule's scans: and
// keeps: lines and the preActions sentences are prose: no test holds
// them to the code they describe. Regenerate the page with:
//
//	go test ./internal/rulesdoc -update
package rulesdoc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
)

// Command regenerates the page; the page names it at the top.
const Command = "go test ./internal/rulesdoc -update"

// group is a category as the page heads it. Groups print in this
// order, wedge first (PRODUCT.md §2).
type group struct{ category, name, blurb string }

var groups = []group{
	{"ai", "AI models", "Model caches and libraries of AI tools."},
	{"dev-caches", "Developer caches and build output", "Package caches, build caches and build output of developer tools, and what coding agents leave behind."},
	{"toolchains", "Toolchains", "Installed SDKs and tool versions."},
	{"xcode", "Xcode and simulators", ""},
	{"docker", "Docker", "Docker objects, listed through the `docker` command; regrow never touches Docker's files directly."},
	{"macos", "macOS", "System caches, logs, downloads and the Trash."},
	{"phantom-space", "Phantom space", "Space your files do not add up to: snapshots, sparse disk images and purgeable space. These rows explain the number; most are never deleted."},
}

// GroupName is the heading of a category, and false when the page has
// none for it.
func GroupName(category string) (string, bool) {
	for _, g := range groups {
		if g.category == category {
			return g.name, true
		}
	}
	return "", false
}

// preActions says what each executor hook does, as a sentence that
// follows "Right before each removal, ".
var preActions = map[string]string{
	engine.PreActionVolumeExport:               "regrow exports the volume to a tarball in regrow staging, and leaves the volume in place if the export fails. That tarball is the only way back. A volume bigger than the export cap is never offered.",
	engine.PreActionAgentScratchRecheck:        "regrow judges the session again, and leaves the folder in place if the session is kept now or regrow cannot tell.",
	engine.PreActionWorktreeRecheck:            "regrow checks the worktree again, and leaves it in place if it is no longer clean or its HEAD is detached at a commit no branch holds. git itself refuses a worktree with modified or untracked files.",
	engine.PreActionBuildOutputRecheck:         "regrow judges the folder again, as the scan did, and leaves it in place if any reason to keep it holds now.",
	engine.PreActionNpxRecheck:                 "regrow judges the folder again, as the scan did, and leaves it in place if any reason to keep it holds now or your processes cannot be listed.",
	engine.PreActionDownloadedInstallerRecheck: "regrow judges the download against the installed apps again, as the scan did, and leaves it in place if any reason to keep it holds now.",
	engine.PreActionFnmVersionRecheck:          "regrow reads every source again, as the scan did, and leaves the version installed if anything names it now or a source cannot be read.",
}

// PreActionText is the sentence for a hook, and false when the page has
// none for it.
func PreActionText(name string) (string, bool) {
	s, ok := preActions[name]
	return s, ok
}

// Render writes the page for the whole catalog, beta rules included.
func Render(catalog []engine.Rule) []byte {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	var beta int
	risks := map[engine.Risk]int{}
	for _, r := range catalog {
		risks[r.Risk]++
		if r.Beta {
			beta++
		}
	}

	p("<!-- Generated from rules/*.yaml by `%s`. Edit the rules, not this file. -->\n\n", Command)
	p("# What regrow scans\n\n")
	p("regrow's catalog holds %d rules: %d safe, %d caution and %d surface-only. ", len(catalog), risks[engine.RiskSafe], risks[engine.RiskCaution], risks[engine.RiskSurfaceOnly])
	p("%d of them are beta, and a scan includes them only with `--beta-rules`. ", beta)
	p("Every entry below is written from the rule's file in [`rules/`](../rules), the catalog `regrow rules --beta-rules` lists. ")
	p("Paths are the macOS ones; the rules also carry Linux paths, for a Linux build that has not shipped. ")
	p("[SCAN.md](SCAN.md) says what a scan reads, which programs it runs, and what regrow writes.\n\n")
	p("How to read an entry:\n\n")
	p("- **safe**: ticked from the start. `regrow clean` with no ids, the checklist and the app begin with every safe rule that found something without an error. When some of a rule's items could not be read at all, only the rest are ticked, and a rule whose command acts on all its items at once is not ticked.\n")
	p("- **caution**: acted on only when you name it or tick it.\n")
	p("- **surface-only**: shown with its size, never deleted. regrow refuses it even when you name it, and refuses anything that holds it.\n")
	p("- **beta**: in a scan only with `--beta-rules`.\n")
	p("- **Looks at**: the folders the rule measures, or what its scan walks and runs.\n")
	p("- **Removed by**: a move to the Trash, which `regrow undo` puts back (into regrow staging when Finder fails); or the command the entry names, mostly the tool's own cleanup command, which deletes for good; or never. Nothing is removed until you confirm a plan (`--yes` confirms it in advance), except by the Go build cache prune: `regrow prune go-build --yes` deletes without showing a plan first, and autotrim, which deletes the same kind of entries by itself when free space is low, stays off until you turn it on.\n")
	p("- **Keeps**: what the rule leaves alone, where that is not plain from what it looks at. The list names the main reasons; the code has narrower ones, mostly an item it cannot read or judge, and each of them keeps more, never less.\n")
	p("- **Comes back**: what recreates it, and what that costs.\n")

	for _, g := range orderedGroups(catalog) {
		rules := inGroup(catalog, g.category)
		p("\n## %s\n\n", g.name)
		if g.blurb != "" {
			p("%s\n\n", g.blurb)
		}
		p("| Rule | Risk | Removed by |\n|---|---|---|\n")
		for _, r := range rules {
			p("| [%s](#%s) | %s | %s |\n", cell(r.Title), r.ID, riskText(r), cell(removedShort(r)))
		}
		for _, r := range rules {
			writeRule(&b, r)
		}
	}
	return []byte(b.String())
}

// orderedGroups lists the groups the catalog uses, in page order; a
// category with no heading of its own comes last under its own name.
func orderedGroups(catalog []engine.Rule) []group {
	used := map[string]bool{}
	for _, r := range catalog {
		used[r.Category] = true
	}
	var out []group
	for _, g := range groups {
		if used[g.category] {
			out = append(out, g)
			delete(used, g.category)
		}
	}
	var rest []string
	for c := range used {
		rest = append(rest, c)
	}
	slices.Sort(rest)
	for _, c := range rest {
		out = append(out, group{category: c, name: c})
	}
	return out
}

func inGroup(catalog []engine.Rule, category string) []engine.Rule {
	var out []engine.Rule
	for _, r := range catalog {
		if r.Category == category {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b engine.Rule) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func riskText(r engine.Rule) string {
	if r.Beta {
		return string(r.Risk) + ", beta"
	}
	return string(r.Risk)
}

func removedShort(r engine.Rule) string {
	switch {
	case !r.Risk.Actionable():
		return "never"
	case len(r.NativeCommand) > 0:
		return "`" + command(r) + "`"
	default:
		return "Trash"
	}
}

func writeRule(b *strings.Builder, r engine.Rule) {
	p := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	p("\n### %s\n\n", r.ID)
	head := []string{"**" + prose(r.Title) + "**", string(r.Risk)}
	if r.Beta {
		head = append(head, "beta")
	}
	p("%s · [`rules/%s.yaml`](../rules/%s.yaml)\n\n", strings.Join(head, " · "), r.ID, r.ID)

	p("- **Looks at:** %s\n", looksAt(r))
	p("- **Removed by:** %s\n", removedBy(r))
	if len(r.Keeps) > 0 {
		p("- **Keeps:**\n")
		for _, k := range r.Keeps {
			p("  - %s\n", sentence(capitalize(prose(k))))
		}
	}
	p("- **Comes back:** %s", sentence(prose(r.Regen.Story)))
	if r.Regen.Cost != "" {
		p(" Cost: %s", sentence(prose(r.Regen.Cost)))
	}
	p("\n")
	if r.Prune != nil {
		p("- **Prune:** %s\n", pruneText(r))
	}
	if r.Doctor != nil {
		p("- **Doctor:** `regrow doctor` flags it above %s. %s\n", size(int64(r.Doctor.FlagAbove)), sentence(prose(r.Doctor.Story)))
	}
	for _, c := range r.Causes {
		p("- **Fix the cause:** %s%s. `regrow doctor` says whether it is in effect here and prints the fix; regrow changes nothing.\n", prose(c.Title), versionText(c.OSMin, c.OSMax))
	}
	if r.Note != "" {
		p("- **Note:** %s\n", sentence(prose(r.Note)))
	}
}

func looksAt(r engine.Rule) string {
	var parts []string
	paths := r.Paths["darwin"]
	prefix := ""
	if len(paths) == 0 && len(r.Paths["linux"]) > 0 {
		paths, prefix = r.Paths["linux"], "on Linux only, "
	}
	if len(paths) > 0 {
		list := make([]string, len(paths))
		for i, e := range paths {
			list[i] = "`" + e.Path + "`" + versionText(e.OSMin, e.OSMax)
		}
		parts = append(parts, prefix+strings.Join(list, ", "))
	}
	if d := r.Discover; d != nil {
		parts = append(parts, discoverText(*d))
	}
	if r.Scans != "" {
		parts = append(parts, strings.TrimSuffix(strings.TrimSpace(prose(r.Scans)), "."))
	} else if r.ToolQuery != "" {
		parts = append(parts, "the `"+r.ToolQuery+"` query")
	}
	return sentence(strings.Join(parts, "; "))
}

func discoverText(d engine.Discover) string {
	defaultDepth, builtin := scanner.DiscoverDefaults()
	depth := d.MaxDepth
	if depth <= 0 {
		depth = defaultDepth
	}
	var what string
	switch {
	case d.Name != "" && len(d.Markers) > 0:
		what = "folders named `" + d.Name + "` that hold " + codeList(d.Markers)
	case d.Name != "":
		what = "folders named `" + d.Name + "`"
	default:
		what = "folders that hold " + codeList(d.Markers)
	}
	var skip []string
	for _, name := range append(slices.Clone(d.Exclude), builtin...) {
		if name != d.Name && !slices.Contains(skip, name) {
			skip = append(skip, name)
		}
	}
	return fmt.Sprintf("%s, up to %d levels below %s, those that exist; the walk does not enter %s folders, or a folder it matched",
		what, depth, codeList(d.Roots), orList(skip))
}

func removedBy(r engine.Rule) string {
	if !r.Risk.Actionable() {
		return "never: surface-only."
	}
	var s string
	if len(r.NativeCommand) > 0 {
		s = "`" + command(r) + "`"
		switch {
		case r.NativeCommand.PerItem():
			s += ", once per item"
			for _, ph := range r.NativeCommand.Placeholders() {
				switch ph {
				case "{path}":
					s += "; `{path}` is the item's path"
				case "{arg}":
					s += "; `{arg}` is the item as the tool names it"
				}
			}
			s += "."
		default:
			s += ", once for the whole rule: all of it or nothing."
		}
		if r.Sudo {
			s += " It runs as root through `sudo`, which asks for your password in a terminal; the app leaves it for you to run there."
		}
		s += " It deletes for good: `regrow undo` cannot bring it back."
		if r.EmptiesTrash {
			s += " It runs before every other action of the plan, so the plan's own Trash moves stay restorable."
		}
	} else {
		s = "a move to the Trash, one per item; `regrow undo` puts it back."
	}
	if r.PreAction != "" {
		if text, ok := preActions[r.PreAction]; ok {
			s += " Right before each removal, " + text
		} else {
			s += " Right before each removal, the `" + r.PreAction + "` check runs."
		}
	}
	return s
}

func pruneText(r engine.Rule) string {
	pr := r.Prune
	s := fmt.Sprintf("`regrow prune go-build --yes` deletes entries unused for %s directly, oldest first, an hour of entries at a time, until the cache is down to about %s. Autotrim, once you turn it on, does the same while free space is low, and stops when it is back at its target. No Trash and no undo.", pr.MinAge, size(int64(pr.KeepUnder)))
	if len(pr.UnlessRunning) > 0 {
		s += " A running " + orList(pr.UnlessRunning) + " refuses a prune by hand, and puts autotrim off unless builds have kept it waiting for hours and free space is critically low."
	}
	return s
}

// command is the steward command as a shell would read it, with sudo
// when the rule asks for it, as the plan shows it.
func command(r engine.Rule) string {
	words := make([]string, 0, len(r.NativeCommand)+1)
	if r.Sudo {
		words = append(words, "sudo")
	}
	for _, w := range r.NativeCommand {
		words = append(words, shellWord(w))
	}
	return strings.Join(words, " ")
}

// shellWord quotes w when a shell would split or expand it. Braces
// stay bare: {path} and {arg} are the catalog's placeholders.
func shellWord(w string) string {
	if !strings.ContainsAny(w, " \t\n'\"\\$`*?[]()<>|&;!#~") {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// versionText names the macOS releases a path's or a cause's bounds
// admit, read as engine.CompareVersion reads them.
func versionText(min, max string) string {
	switch {
	case min != "" && max != "":
		first := macOSMin(min)
		if engine.CompareVersion(first, max) > 0 {
			return " (no macOS release)"
		}
		return " (macOS " + first + " to " + macOSMax(max) + ")"
	case min != "":
		return " (macOS " + macOSMin(min) + " and later)"
	case max != "":
		return " (macOS " + macOSMax(max) + " and earlier)"
	}
	return ""
}

// macOSMax names the last release an os_max admits. A missing part
// compares as 0, so "26" admits 26.0.0 but not 26.0.1, and is written
// out to three parts; a bound in the release gap, such as "25", admits
// every 15.x and is written so (TestVersionBoundsNameWhatTheyAdmit).
func macOSMax(max string) string {
	if inReleaseGap(max) {
		return "15.x"
	}
	for strings.Count(max, ".") < 2 {
		max += ".0"
	}
	return max
}

// macOSMin names the first release an os_min admits: a bound in the
// release gap admits 26 onwards.
func macOSMin(min string) string {
	if inReleaseGap(min) {
		return "26"
	}
	return min
}

// inReleaseGap reports a version whose major number no macOS release
// has: macOS went from 15 to 26.
func inReleaseGap(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n > 15 && n < 26
}

func codeList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	return andList(quoted, "and")
}

func orList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	return andList(quoted, "or")
}

func andList(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " " + conj + " " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

// size prints a byte count in whole binary units where it divides.
func size(n int64) string {
	for _, u := range []struct {
		shift uint
		name  string
	}{{40, "TiB"}, {30, "GiB"}, {20, "MiB"}, {10, "KiB"}} {
		if n >= 1<<u.shift && n%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d %s", n>>u.shift, u.name)
		}
	}
	return engine.HumanBytes(n)
}

// capitalize upper-cases a leading lowercase letter; text that opens
// with a code span stays as it is.
func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".!?") {
		return s
	}
	return s + "."
}

// prose escapes what Markdown would read as markup in catalog text:
// angle brackets (an HTML tag to GitHub) and asterisks, outside code
// spans only, where they print as they are.
func prose(s string) string {
	var b strings.Builder
	inCode := false
	for _, c := range s {
		switch {
		case c == '`':
			inCode = !inCode
			b.WriteRune(c)
		case inCode:
			b.WriteRune(c)
		case c == '<':
			b.WriteString("&lt;")
		case c == '>':
			b.WriteString("&gt;")
		case c == '*':
			b.WriteString(`\*`)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// cell makes text safe inside a table cell.
func cell(s string) string {
	return strings.ReplaceAll(prose(s), "|", `\|`)
}
