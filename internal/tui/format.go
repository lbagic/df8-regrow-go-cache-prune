package tui

import (
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// HumanBytes renders a byte count the way the sketch does: "20.1 GiB".
// The implementation lives in engine (scanner labels use it too);
// this alias keeps the TUI/CLI call sites stable.
func HumanBytes(n int64) string { return engine.HumanBytes(n) }

// ShellJoin renders argv for display, quoting args with spaces or
// glob characters, so a pasted line means what the argv means.
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"'*?[") {
			parts[i] = fmt.Sprintf("%q", a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// ActionCommand renders an action's command for plan output, spelling
// out the pre-action so the export-then-remove sequence is visible on
// every plan surface, not just in the rule's note.
func ActionCommand(a engine.Action) string {
	s := ShellJoin(a.Command)
	switch a.PreAction {
	case "":
	case engine.PreActionVolumeExport:
		s += "  (tarball to staging first)"
	case engine.PreActionAgentScratchRecheck:
		s += "  (session checked again first)"
	case engine.PreActionWorktreeRecheck:
		s += "  (worktree rechecked first)"
	case engine.PreActionBuildOutputRecheck:
		s += "  (project checked again first)"
	case engine.PreActionNpxRecheck:
		s += "  (running processes checked again first)"
	case engine.PreActionDownloadedInstallerRecheck:
		s += "  (download and app checked again first)"
	case engine.PreActionFnmVersionRecheck:
		s += "  (version checked unreferenced again first)"
	default:
		s += "  (pre: " + a.PreAction + ")"
	}
	return s
}

// Reversibility says what `regrow undo` can do for an action once it
// has run: only Trash moves come back.
func Reversibility(a engine.Action) string {
	if a.Kind == engine.ActionTrash {
		return "undo restores"
	}
	return "no undo"
}

// ActionLine renders one plan action identically on every plan surface,
// with one more line per item the action deletes along with its target.
// Every surface indents the action by two spaces; the extra lines carry
// that indent themselves, so they start under the rule id.
func ActionLine(a engine.Action) string {
	line := fmt.Sprintf("%-8s %-24s %10s  %-13s  %s",
		"["+string(a.Kind)+"]", a.RuleID, HumanBytes(a.Bytes), Reversibility(a), ActionCommand(a))
	for _, in := range a.Includes {
		line += fmt.Sprintf("\n  %-8s also removes %s (%s)", "", in.ID, HumanBytes(in.Bytes))
	}
	return line
}

// TotalsLines splits a plan's total by when the space comes back.
func TotalsLines(p engine.Plan) []string {
	t := p.Totals()
	lines := []string{
		fmt.Sprintf("Frees now          %10s  steward commands, no undo", HumanBytes(t.FreesNow)),
		fmt.Sprintf("Frees after Trash  %10s  moved to the Trash; `regrow undo` restores until it is emptied", HumanBytes(t.AfterTrash)),
	}
	if t.Exported > 0 {
		lines = append(lines, fmt.Sprintf("Volume exports     %10s  docker volumes, exported to staging first; the tarballs keep the room until you delete them", HumanBytes(t.Exported)))
	}
	return lines
}

// LedgerLines prints a scan's buckets: every byte once, in the bucket
// of the innermost item that holds it.
func LedgerLines(t engine.Totals) []string {
	lines := []string{
		"Each byte counted once within the rows; macOS-managed space can overlap them:",
		fmt.Sprintf("  Frees now          %10s  steward commands", HumanBytes(t.FreesNow)),
		fmt.Sprintf("  Frees after Trash  %10s  moved to the Trash; freed once it is emptied", HumanBytes(t.AfterTrash)),
	}
	if t.Exported > 0 {
		lines = append(lines, fmt.Sprintf("  Volume exports     %10s  docker volumes removed after an export; the tarball keeps the room until it is deleted", HumanBytes(t.Exported)))
	}
	lines = append(lines,
		fmt.Sprintf("  Shown only         %10s  surface-only; regrow never deletes it", HumanBytes(t.ShownOnly)),
		fmt.Sprintf("  macOS-managed      %10s  phantom space macOS reclaims on its own", HumanBytes(t.MacOSManaged)),
	)
	if t.Partial > 0 {
		lines = append(lines, fmt.Sprintf("  %d item(s) partly or wholly %s; their sizes are lower bounds.", t.Partial, engine.UnreadableNote))
	}
	return lines
}
