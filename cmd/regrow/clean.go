package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/docker"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/scanner"
	"github.com/lbagic/regrow/internal/trash"
	"github.com/lbagic/regrow/internal/tui"
)

// runClean is the execution opt-in (invariant 1: dry-run is the
// default; this command IS the opt-in). Without ids the default
// selection runs, the set the TUI pre-ticks; caution rules must be
// named explicitly. The plan is shown and confirmed before anything
// moves.
func runClean(host engine.Host, catalog []engine.Rule, ids []string, yes bool) error {
	findings := scanner.New(host).Scan(context.Background(), catalog)
	plan := engine.BuildPlan(host, findings, selectionFor(ids, findings))
	if len(plan.Unmatched) > 0 {
		// A typo'd selector must never quietly execute less (or, for a
		// rule atom, more) than the user meant.
		hint := "`regrow scan` lists rules and item ids"
		for _, u := range plan.Unmatched {
			if strings.HasPrefix(u, "-") {
				hint = "flags go before ids: `regrow clean --yes id ...`"
				break
			}
		}
		return fmt.Errorf("selector(s) matched nothing in this scan: %s\n(%s)",
			strings.Join(plan.Unmatched, ", "), hint)
	}
	if len(plan.Actions) == 0 {
		for _, line := range nothingToClean(plan) {
			fmt.Println(line)
		}
		return nil
	}

	printPlanActions(plan)
	if !yes {
		if !isTTY() {
			return fmt.Errorf("refusing to execute without a terminal; pass --yes to confirm")
		}
		fmt.Print("Proceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Println("Aborted; nothing was executed.")
			return nil
		}
	}
	return executePlan(host, plan)
}

// printPlanActions prints the about-to-run action list — before the
// clean prompt, and after a TUI-confirmed run so the terminal
// scrollback keeps a record of what executed once the alt-screen is
// gone.
func printPlanActions(plan engine.Plan) {
	fmt.Println("About to execute:")
	for _, a := range plan.Actions {
		fmt.Println("  " + tui.ActionLine(a))
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  [skip] %-22s %s\n", s.RuleID, s.Reason)
	}
	for _, line := range tui.TotalsLines(plan) {
		fmt.Println(line)
	}
	fmt.Printf("Total: %s\n", tui.HumanBytes(plan.TotalBytes()))
}

// executePlan runs an already-confirmed plan: oplog first, then the
// executor. Both confirmation paths (clean prompt, TUI plan → x → y)
// land here.
func executePlan(host engine.Host, plan engine.Plan) error {
	exec, release, err := newRunExecutor(host, executor.NewRunID(time.Now()), nil)
	if err != nil {
		return err
	}
	defer release()
	res, err := exec.Execute(context.Background(), plan)
	if err != nil {
		return err
	}

	fmt.Println()
	for _, line := range runSummary(res) {
		fmt.Println(line)
	}
	return nil
}

// newRunExecutor wires the executor of one run: the journal, and a
// mover and volume exporter pointed at the run's staging directory.
// stream runs the export's docker command; nil streams docker's
// stderr to the terminal. release closes the journal.
func newRunExecutor(host engine.Host, runID string, stream func(context.Context, []string, io.Writer) error) (*executor.Executor, func(), error) {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	// The volume export cap comes from user config; a broken config
	// must block execution, not silently fall back to defaults.
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	capBytes, err := cfg.Docker.ExportCapBytes()
	if err != nil {
		return nil, nil, err
	}
	log, err := oplog.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	stagingDir := filepath.Join(stagingRoot(logPath), runID)
	exec := &executor.Executor{
		Trash: &trash.Mover{Home: host.Home, StagingDir: stagingDir},
		Log:   log,
		PreActions: map[string]executor.PreAction{
			engine.PreActionVolumeExport: (&docker.Exporter{StagingDir: stagingDir, CapBytes: capBytes, Stream: stream}).PreAction,
			engine.PreActionAgentScratchRecheck: func(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
				return nil, scanner.RecheckAgentScratch(ctx, a.Path)
			},
			engine.PreActionWorktreeRecheck: func(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
				return nil, scanner.CheckWorktreeClean(ctx, a.Path)
			},
			engine.PreActionBuildOutputRecheck: func(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
				return nil, scanner.RecheckBuildOutput(ctx, a.Path)
			},
			engine.PreActionNpxRecheck: func(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
				return nil, scanner.RecheckNpxEntry(ctx, a.Path)
			},
			engine.PreActionDownloadedInstallerRecheck: func(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
				return nil, scanner.RecheckDownloadedInstaller(ctx, a.Path)
			},
		},
		RunID: runID,
	}
	// Journal entries fsync per Append (invariant 6); nothing left to
	// lose at close.
	return exec, func() { _ = log.Close() }, nil
}

// stagingRoot is the directory beside the oplog that holds every run's
// staging directory.
func stagingRoot(logPath string) string {
	return filepath.Join(filepath.Dir(logPath), "staging")
}

// nothingToClean explains an empty plan: refusals and skips are why a
// named rule did nothing, so they are never hidden.
func nothingToClean(plan engine.Plan) []string {
	if len(plan.Skipped) == 0 {
		return []string{"Nothing to clean: no selected rule found anything."}
	}
	lines := []string{"Nothing to clean: everything selected was skipped."}
	for _, s := range plan.Skipped {
		id := s.RuleID
		if s.ItemKey != "" {
			id = engine.ItemID(s.RuleID, s.ItemKey)
		}
		lines = append(lines, fmt.Sprintf("  [skip] %-22s %s", id, s.Reason))
	}
	return lines
}

// runSummary reports an executed run by where the space went: freed,
// in the Trash, or in regrow staging (Finder unavailable), which
// emptying the Trash never frees.
func runSummary(res executor.Result) []string {
	lines := []string{
		fmt.Sprintf("Done: %d ok, %d failed. Run %s.", res.Done, res.Failed, res.RunID),
		fmt.Sprintf("  Freed now          %10s", tui.HumanBytes(res.Bytes-res.TrashBytes-res.StagedBytes)),
		fmt.Sprintf("  In the Trash       %10s  freed once it is emptied; `regrow undo` restores", tui.HumanBytes(res.TrashBytes)),
	}
	if res.StagedBytes > 0 {
		lines = append(lines, fmt.Sprintf("  In regrow staging  %10s  Finder was unavailable; emptying the Trash does not free it; `regrow undo` restores", tui.HumanBytes(res.StagedBytes)))
	}
	for _, f := range res.Failures {
		lines = append(lines, "  failed: "+f)
	}
	for _, s := range res.Skipped {
		lines = append(lines, "  skipped: "+s)
	}
	return lines
}

// runUndo restores the newest run that still has something to restore,
// or the given run id.
func runUndo(args []string) error {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		return err
	}
	runs := oplog.Runs(entries)

	var target *oplog.Run
	if len(args) > 0 {
		for i := range runs {
			if runs[i].ID == args[0] {
				target = &runs[i]
			}
		}
		if target == nil {
			return fmt.Errorf("run %q not in the oplog (see `regrow history`)", args[0])
		}
	} else {
		for i := len(runs) - 1; i >= 0; i-- {
			if len(runs[i].Undoable()) > 0 {
				target = &runs[i]
				break
			}
		}
		if target == nil {
			fmt.Println("Nothing to undo: no run has restorable trash moves.")
			return nil
		}
	}

	log, err := oplog.Open(logPath)
	if err != nil {
		return err
	}
	// Journal entries fsync per Append (invariant 6); nothing left to
	// lose at close.
	defer func() { _ = log.Close() }()

	exec := &executor.Executor{Log: log}
	res, err := exec.Undo(*target)
	if err != nil {
		return err
	}
	fmt.Printf("Undo of run %s: %d restored, %d failed.\n", target.ID, res.Restored, res.Failed)
	for _, f := range res.Failures {
		fmt.Println("  failed:", f)
	}
	if res.NativeSkipped > 0 {
		fmt.Printf("  %d native command(s) are not undoable — their data comes back via the regen story (`regrow rules`).\n", res.NativeSkipped)
	}
	if res.ExportSkipped > 0 {
		fmt.Printf("  %d docker volume(s) are not auto-restorable — the tarball in regrow staging is the recovery copy (`regrow history` shows where).\n", res.ExportSkipped)
	}
	return nil
}

func runHistory(asJSON bool) error {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	entries, err := oplog.Read(logPath)
	if err != nil {
		return err
	}
	runs := oplog.Runs(entries)
	if asJSON {
		return emitJSON(runs)
	}
	if len(runs) == 0 {
		fmt.Println("No history yet: nothing has been executed on this machine.")
		return nil
	}
	for _, line := range historyLines(runs) {
		fmt.Println(line)
	}
	return nil
}

// historyLines is one line per run, then what its size counts.
func historyLines(runs []oplog.Run) []string {
	lines := make([]string, 0, len(runs)+len(historyNote))
	for _, r := range runs {
		var done, failed, undone int
		for _, e := range r.Entries {
			switch e.Event {
			case oplog.EventDone:
				done++
			case oplog.EventFail:
				failed++
			case oplog.EventUndo:
				if e.Error == "" {
					undone++
				}
			}
		}
		status := fmt.Sprintf("%d ok, %d failed", done, failed)
		if undone > 0 {
			status += fmt.Sprintf(", %d undone", undone)
		}
		if rest := len(r.Undoable()); rest > 0 {
			status += fmt.Sprintf(" (%d restorable)", rest)
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %10s cleaned  %s", r.ID, r.Start.Local().Format("2006-01-02 15:04"), tui.HumanBytes(runBytes(r)), status))
	}
	return append(lines, historyNote...)
}

var historyNote = []string{
	"Cleaned counts finished actions only: a prune at what it deleted, anything else at its planned size; a failed or cut-short action counts nothing.",
	"A Trash move frees its space only once the Trash is emptied; a move to regrow staging (Finder unavailable) is not freed by that.",
}
