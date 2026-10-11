package main

import (
	"context"
	"fmt"
	"io"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/tui"
)

// runDoctor is the hero-bug scan (PRODUCT.md §3): a fast pass over
// only the rules that carry a doctor block, the fix-the-cause checks,
// and the phantom-space explainers. Read-only by construction — doctor
// never plans and never executes; hero fix lines point at `regrow
// clean`, cause fix lines are the owner's to run.
func runDoctor(w io.Writer, host engine.Host, catalog []engine.Rule, asJSON bool) error {
	report := doctorReport(context.Background(), scanner.New(host), catalog)
	if asJSON {
		return writeJSON(w, report)
	}
	writeDoctorReport(w, report)
	return nil
}

// doctorReport scans the doctor rules and checks the causes of every
// rule in the catalog: a rule that carries only causes is checked
// without being scanned.
func doctorReport(ctx context.Context, s *scanner.Scanner, catalog []engine.Rule) engine.DoctorReport {
	findings := s.Scan(ctx, engine.DoctorRules(catalog))
	return engine.BuildDoctorReport(findings, s.Causes(ctx, catalog))
}

func writeDoctorReport(w io.Writer, rep engine.DoctorReport) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("regrow doctor — known runaway bugs, what keeps filling the disk, and where the \"missing\" space hides\n\n")

	p("HERO BUGS\n")
	var flagged, unknown int
	for _, c := range rep.Hero {
		f := c.Finding
		size := engine.SizeText(f.TotalBytes(), f.Partial())
		line := tui.HumanBytes(int64(f.Rule.Doctor.FlagAbove))
		switch {
		case c.Verdict == engine.VerdictFlagged:
			flagged++
			p("  🚩 %-33s %12s  RUNAWAY — healthy is under %s\n", f.Rule.Title, size, line)
			p("       %s\n", f.Rule.Doctor.Story)
			p("       fix: regrow clean %s%s\n", f.Rule.ID, nativeHint(f.Rule))
		case f.Err != "":
			unknown++
			p("  ? %-34s %12s  unknown — scan failed: %s\n", f.Rule.Title, "", f.Err)
		case c.Verdict == engine.VerdictUnknown:
			unknown++
			p("  ? %-34s %12s  unknown — flags above %s; %s\n", f.Rule.Title, size, line, engine.PartialText(f.TotalBytes(), true))
		case len(f.Items) == 0:
			p("  ✓ %-34s %12s  not present\n", f.Rule.Title, "")
		default:
			p("  ✓ %-34s %12s  normal (flags above %s)\n", f.Rule.Title, size, line)
		}
	}

	var causes, causesUnknown int
	if len(rep.Causes) > 0 {
		p("\nFIX THE CAUSE — what keeps filling the disk; regrow prints the fix and changes nothing\n")
	}
	for _, c := range rep.Causes {
		switch c.Verdict {
		case engine.VerdictFlagged:
			causes++
			p("  🚩 %-33s %s\n", c.Cause.Title, c.Detail)
			p("       %s\n", c.Cause.Story)
			for i, line := range c.Cause.Fix {
				label := "fix:"
				if i > 0 {
					label = ""
				}
				p("       %-4s %s\n", label, line)
			}
		case engine.VerdictUnknown:
			causesUnknown++
			p("  ? %-34s unknown — %s\n", c.Cause.Title, c.Detail)
		default:
			p("  ✓ %-34s %s\n", c.Cause.Title, c.Detail)
		}
	}

	p("\nPHANTOM SPACE — why Finder shows more used than your files add up to\n")
	for _, f := range rep.Phantom {
		switch {
		case f.Err != "":
			p("  ! %-34s %12s  scan failed: %s\n", f.Rule.Title, "", f.Err)
		case len(f.Items) == 0:
			p("  • %-34s %12s  not present\n", f.Rule.Title, "")
		default:
			p("  • %-34s %12s\n", f.Rule.Title, engine.SizeText(f.TotalBytes(), f.Partial()))
			for _, it := range f.Items {
				p("      %s\n", itemLine(it))
			}
			if f.Rule.Note != "" {
				p("      %s\n", f.Rule.Note)
			}
		}
	}

	p("\n")
	switch {
	case flagged > 0:
		p("%d runaway bug(s) found. Fixes above are dry-run first: `regrow plan <id>` shows the exact commands.\n", flagged)
	case unknown > 0:
		p("No runaway bug in what could be read; %d check(s) unknown above. Phantom space is informational.\n", unknown)
	case causes > 0 || causesUnknown > 0:
		p("No runaway bugs on this machine. Phantom space above is informational.\n")
	default:
		p("No runaway bugs on this machine. Phantom space above is informational — nothing needs fixing.\n")
	}
	switch {
	case causes > 0 && causesUnknown > 0:
		p("%d cause(s) to fix above, %d more could not be checked. The fixes are yours to run: regrow changes no setting.\n", causes, causesUnknown)
	case causes > 0:
		p("%d cause(s) to fix above. The fixes are yours to run: regrow changes no setting.\n", causes)
	case causesUnknown > 0:
		p("%d cause check(s) unknown above.\n", causesUnknown)
	}
}

// itemLine renders a phantom item label; the size already prints on
// the rule's title line, and labels carry their own story
// ("Docker.raw — 35.2 GiB real of 64.0 GiB sparse").
func itemLine(it engine.Item) string {
	if it.Label != "" {
		return it.Label
	}
	return it.Path
}

// nativeHint spells out the steward command a fix runs, so the
// screenshot alone tells the reader what would execute.
func nativeHint(r engine.Rule) string {
	if len(r.NativeCommand) == 0 {
		return "  (moves to Trash, undoable)"
	}
	cmd := tui.ShellJoin(r.NativeCommand)
	if r.Sudo {
		cmd = "sudo " + cmd
	}
	return fmt.Sprintf("  (runs: %s)", cmd)
}
