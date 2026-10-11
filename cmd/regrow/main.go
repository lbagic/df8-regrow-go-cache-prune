// Command regrow scans the disk for regenerable caches and junk,
// explains what everything is and how it comes back, and reclaims
// space. `regrow help` lists the subcommands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/tui"
)

// version is overridden at release time via -ldflags.
var version = "0.0.0-dev"

const usageText = `regrow: what is on your disk, and how it comes back.

Usage:
  regrow [scan] [--json]          interactive checklist on a terminal;
                                  plain listing when piped; --json prints
                                  the engine's scan events (docs/ENGINE.md)
  regrow plan [id ...] [--json]   dry run: the exact commands that would run
  regrow clean [id ...] [--yes]   show the plan, confirm, execute
  regrow doctor [--json]          runaway bugs, causes to fix, phantom space; read-only
  regrow tick [--autotrim]        one headroom sample: free space, forecast, alerts
  regrow prune go-build [--yes]   delete old Go build cache entries; dry run without --yes
  regrow undo [run-id]            restore the newest (or given) run's Trash moves
  regrow history [--json]         past runs from the oplog
  regrow rules [--json]           list the rule catalog
  regrow engine                   JSON-lines protocol on stdin/stdout for a
                                  shell app (docs/ENGINE.md)
  regrow version                  print the version
  regrow help                     print this help

Ids are rule ids ("sim-devices") or item ids ("sim-devices/AAA-111"), as
scan output and the TUI footer list them. Without ids, plan and clean use
the default selection: every safe rule that found something.

Flags go before ids:
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "regrow:", err)
		os.Exit(1)
	}
}

type options struct {
	rulesDir  string
	asJSON    bool
	betaRules bool
	yes       bool
	autotrim  bool
}

func newFlagSet(name string, o *options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.rulesDir, "rules-dir", "", "load rules from a directory instead of the embedded catalog")
	fs.BoolVar(&o.asJSON, "json", false, "machine-readable output")
	fs.BoolVar(&o.betaRules, "beta-rules", false, "include rules still in staged rollout")
	fs.BoolVar(&o.yes, "yes", false, "clean: skip the confirmation prompt; prune: execute")
	fs.BoolVar(&o.autotrim, "autotrim", false, "tick: prune when headroom is low (refused until one manual prune has completed)")
	return fs
}

// parseArgs splits the subcommand from its flags and ids. help is set
// by `regrow help` and by -h/--help after any subcommand.
func parseArgs(args []string) (cmd string, opts options, ids []string, help bool, err error) {
	cmd = "scan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := newFlagSet(cmd, &opts)
	err = fs.Parse(args)
	ids = fs.Args()
	if err == nil && cmd == "prune" {
		ids, err = interspersedArgs(fs, ids)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cmd, opts, nil, true, nil
		}
		return cmd, opts, nil, false, fmt.Errorf("%w (`regrow help` lists commands and flags)", err)
	}
	return cmd, opts, ids, cmd == "help", nil
}

func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, usageText)
	fs := newFlagSet("regrow", &options{})
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func run(args []string) error {
	cmd, opts, ids, help, err := parseArgs(args)
	if err != nil {
		return err
	}
	if help {
		printUsage(os.Stdout)
		return nil
	}
	if cmd == "version" {
		fmt.Println("regrow", version)
		return nil
	}

	catalog, err := loadCatalog(opts.rulesDir)
	if err != nil {
		return err
	}
	if !opts.betaRules {
		catalog = engine.WithoutBeta(catalog)
	}
	host := engine.DetectHost()

	switch cmd {
	case "rules":
		return printRules(catalog, opts.asJSON)
	case "scan":
		if opts.asJSON {
			return newEngineServer(host, catalog, nil).ScanOnce(context.Background(), os.Stdout)
		}
		if isTTY() {
			plan, confirmed, err := tui.Run(host, version, func(ctx context.Context) []engine.Finding {
				return scanner.New(host).Scan(ctx, catalog)
			})
			if err != nil || !confirmed {
				return err
			}
			// Confirmed twice in the TUI (plan → x → confirm → y);
			// execute here so native commands get real terminal stdio.
			printPlanActions(plan)
			return executePlan(host, plan)
		}
		findings := scanner.New(host).Scan(context.Background(), catalog)
		writeFindings(os.Stdout, findings, engine.Account(findings))
		return nil
	case "plan":
		findings := scanner.New(host).Scan(context.Background(), catalog)
		plan := engine.BuildPlan(host, findings, selectionFor(ids, findings))
		return printPlan(plan, opts.asJSON)
	case "clean":
		return runClean(host, catalog, ids, opts.yes)
	case "doctor":
		return runDoctor(os.Stdout, host, catalog, opts.asJSON)
	case "tick":
		return runTick(host, catalog, opts)
	case "prune":
		return runPrune(host, catalog, ids, opts)
	case "engine":
		return runEngine(host, catalog, opts)
	case "undo":
		return runUndo(ids)
	case "history":
		return runHistory(opts.asJSON)
	default:
		return fmt.Errorf("unknown command %q (`regrow help` lists commands)", cmd)
	}
}

func loadCatalog(dir string) ([]engine.Rule, error) {
	if dir != "" {
		return engine.LoadDir(dir)
	}
	return engine.LoadEmbedded()
}

// selectionFor turns command-line ids into selection atoms. No ids
// means the default selection; BuildPlan itself plans nothing for an
// empty selection.
func selectionFor(ids []string, findings []engine.Finding) map[string]bool {
	if len(ids) == 0 {
		return engine.DefaultSelection(findings)
	}
	sel := make(map[string]bool, len(ids))
	for _, id := range ids {
		sel[id] = true
	}
	return sel
}

func printRules(catalog []engine.Rule, asJSON bool) error {
	if asJSON {
		return emitJSON(catalog)
	}
	for _, r := range catalog {
		fmt.Printf("%-24s %-12s %-10s %s\n", r.ID, r.Category, r.Risk, r.Title)
	}
	return nil
}

// writeFindings prints the scan grouped by category, largest first
// inside each (PRODUCT.md §4). Rows show each rule's whole size; the
// totals at the end count every byte once, in the bucket of the
// innermost row that holds it.
func writeFindings(w io.Writer, findings []engine.Finding, ledger engine.Ledger) {
	byCategory := map[string][]engine.Finding{}
	for _, f := range findings {
		byCategory[f.Rule.Category] = append(byCategory[f.Rule.Category], f)
	}
	categories := make([]string, 0, len(byCategory))
	for c := range byCategory {
		categories = append(categories, c)
	}
	categoryOwn := func(c string) int64 {
		var n int64
		for _, f := range byCategory[c] {
			n += ledger.Share(f)
		}
		return n
	}
	sort.Slice(categories, func(i, j int) bool {
		if oi, oj := categoryOwn(categories[i]), categoryOwn(categories[j]); oi != oj {
			return oi > oj
		}
		return categories[i] < categories[j]
	})

	for _, c := range categories {
		group := byCategory[c]
		sort.SliceStable(group, func(i, j int) bool { return group[i].TotalBytes() > group[j].TotalBytes() })
		_, _ = fmt.Fprintln(w, strings.ToUpper(strings.ReplaceAll(c, "-", " ")))
		for _, f := range group {
			size := engine.SizeText(f.TotalBytes(), f.Partial())
			switch {
			case f.Err != "":
				_, _ = fmt.Fprintf(w, "  ! %-32s %12s  %s (%s)\n", f.Rule.Title, size, f.Rule.Risk, f.Err)
			case len(f.Items) == 0:
				_, _ = fmt.Fprintf(w, "  - %-32s %12s  not found\n", f.Rule.Title, "")
			default:
				_, _ = fmt.Fprintf(w, "  • %-32s %12s  %-12s %s\n", f.Rule.Title, size, f.Rule.Risk, f.Rule.Regen.Story)
				for _, it := range f.Items {
					id := engine.ItemID(f.Rule.ID, it.Key)
					line := fmt.Sprintf("      %12s  %s", engine.SizeText(it.Bytes, it.Partial), id)
					var notes []string
					if x := ledger.Exclusive[id]; x != it.Bytes {
						notes = append(notes, engine.HumanBytes(x)+" outside other rows")
					}
					if note := engine.PartialText(it.Bytes, it.Partial); note != "" {
						notes = append(notes, note)
					}
					if len(notes) > 0 {
						line += "  (" + strings.Join(notes, "; ") + ")"
					}
					_, _ = fmt.Fprintln(w, line)
				}
			}
		}
	}
	_, _ = fmt.Fprintln(w)
	for _, line := range tui.LedgerLines(ledger.Totals) {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintln(w, "Dry-run: `regrow plan` shows the exact commands; nothing was deleted.")
}

func printPlan(plan engine.Plan, asJSON bool) error {
	if asJSON {
		return emitJSON(plan)
	}
	if len(plan.Actions) == 0 && len(plan.Skipped) == 0 && len(plan.Unmatched) == 0 {
		fmt.Println("Nothing to plan: no selected rule found anything.")
		return nil
	}
	fmt.Println("DRY RUN — commands that WOULD run (nothing executed):")
	for _, a := range plan.Actions {
		fmt.Println("  " + tui.ActionLine(a))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	for _, u := range plan.Unmatched {
		fmt.Printf("  [unmatched] %-17s selector matched nothing in this scan\n", u)
	}
	fmt.Println()
	for _, line := range tui.TotalsLines(plan) {
		fmt.Println(line)
	}
	fmt.Printf("Would reclaim: %s\n", tui.HumanBytes(plan.TotalBytes()))
	return nil
}

// isTTY: the interactive UI needs a terminal on both ends.
func isTTY() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func emitJSON(v any) error { return writeJSON(os.Stdout, v) }

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
