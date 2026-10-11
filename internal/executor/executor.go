// Package executor runs a plan: journal, act, journal again. It is a
// thin caller over two seams — the trash mover and the oplog — so the
// mechanisms stay testable and swappable. Execution is opt-in per run
// (invariant 1); the executor never decides what to run, only how.
package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// Mover is the trash seam the executor crosses for trash actions.
type Mover interface {
	Move(ctx context.Context, path string) (trash.Receipt, error)
}

// Journal is the oplog seam. Append must persist before returning.
type Journal interface {
	Append(oplog.Entry) error
}

// PreAction runs before an action's command or Trash move: it makes the
// target recoverable first (docker volume export to staging) or checks
// that it may still go (agent session recheck). A pre-action failure
// fails the whole action. The receipt it returns is journaled like a
// trash receipt; a Trash move's own receipt replaces it.
type PreAction func(ctx context.Context, a engine.Action) (*trash.Receipt, error)

// Executor runs plan actions one at a time, journaling each before and
// after. One failed action never aborts the run: every action is
// independent, and a half-finished run is exactly what the oplog and
// undo exist to make safe.
type Executor struct {
	Trash Mover
	Log   Journal
	// PreActions maps the pre-action names rules declare (validated by
	// the schema) to their implementations, wired up by the caller. An
	// action naming an unregistered hook fails instead of running bare.
	PreActions map[string]PreAction
	// RunNative executes a native steward command. Nil means real
	// exec with inherited stdio (sudo can prompt, docker can stream).
	RunNative func(ctx context.Context, argv []string) error
	// Now is injectable for deterministic journal timestamps in tests.
	Now func() time.Time
	// RunID names the run in the journal; empty means mint one. The
	// caller may pre-mint it to point staging at a per-run directory.
	RunID string
	// Stop is polled before each action; once it reports true the run
	// ends there. Unlike cancelling ctx, which kills the command in
	// flight, a stop lets the current action finish and be journaled.
	Stop func() bool
}

// Result summarises one executed run.
type Result struct {
	RunID  string `json:"run_id"`
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
	Bytes  int64  `json:"bytes"` // reclaimed by successful actions
	// TrashBytes is the part of Bytes Finder moved to the Trash: it
	// frees nothing until the Trash is emptied.
	TrashBytes int64 `json:"trash_bytes"`
	// StagedBytes is the part of Bytes renamed into regrow's staging
	// dir because Finder was unavailable: emptying the Trash never
	// frees it.
	StagedBytes int64    `json:"staged_bytes"`
	Failures    []string `json:"failures,omitempty"`
	// Skipped lists actions never attempted, with why. They are not
	// journaled: nothing ran.
	Skipped []string `json:"skipped,omitempty"`
	// Pruned is what prune actions deleted, as measured, failed ones
	// included: a prune that fails midway has still deleted entries.
	Pruned oplog.Pruned `json:"pruned"`
	// Stopped: Stop ended the run before every action was attempted.
	Stopped bool `json:"stopped,omitempty"`
}

// NewRunID mints a journal run id: sortable timestamp + entropy so
// two runs in the same second never collide.
func NewRunID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Execute runs every action in the plan. The journal line for an
// action is written and synced before the action runs (invariant 6).
func (e *Executor) Execute(ctx context.Context, plan engine.Plan) (Result, error) {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	runNative := e.RunNative
	if runNative == nil {
		runNative = execNative
	}

	res := Result{RunID: e.RunID}
	if res.RunID == "" {
		res.RunID = NewRunID(now())
	}
	trashUnsafe := false
	for seq, a := range plan.Actions {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if e.Stop != nil && e.Stop() {
			res.Stopped = true
			return res, nil
		}
		if trashUnsafe && a.Kind == engine.ActionTrash {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: emptying the Trash failed, and Finder may still be emptying it", actionID(a)))
			continue
		}
		entry := oplog.Entry{
			Time: now(), Run: res.RunID, Seq: seq + 1, Event: oplog.EventStart,
			RuleID: a.RuleID, ItemKey: a.ItemKey, Kind: string(a.Kind), Command: a.Command, Path: a.Path, Bytes: a.Bytes,
		}
		if err := e.Log.Append(entry); err != nil {
			// Journal down = no action: the invariant is not optional.
			return res, fmt.Errorf("oplog append failed, refusing to act: %w", err)
		}

		var receipt *trash.Receipt
		var pruned *oplog.Pruned
		var actErr error
		switch a.Kind {
		case engine.ActionTrash:
			receipt, actErr = e.runPreAction(ctx, a)
			if actErr == nil {
				var r trash.Receipt
				if r, actErr = e.Trash.Move(ctx, a.Path); actErr == nil {
					receipt = &r
				}
			}
		case engine.ActionNative:
			receipt, actErr = e.runPreAction(ctx, a)
			if actErr == nil {
				actErr = runNative(ctx, a.Command)
			}
		case engine.ActionPrune:
			pruned, actErr = e.prune(ctx, a, runNative)
		default:
			actErr = fmt.Errorf("unknown action kind %q", a.Kind)
		}

		// The receipt rides the after-entry even on failure: if the
		// export succeeded but the command then failed, the journal
		// must still say where the backup landed.
		after := oplog.Entry{
			Time: now(), Run: res.RunID, Seq: seq + 1, Event: oplog.EventDone,
			RuleID: a.RuleID, Receipt: receipt, Pruned: pruned,
		}
		freed := a.Bytes
		if pruned != nil {
			freed = pruned.Bytes
			res.Pruned.Files += pruned.Files
			res.Pruned.Bytes += pruned.Bytes
		}
		if actErr != nil {
			after.Event = oplog.EventFail
			after.Error = actErr.Error()
			res.Failed++
			res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", a.RuleID, actErr))
			if a.EmptiesTrash {
				trashUnsafe = true
			}
		} else {
			res.Done++
			res.Bytes += freed
			if a.Kind == engine.ActionTrash && receipt != nil {
				switch receipt.Method {
				case trash.MethodFinder:
					res.TrashBytes += a.Bytes
				case trash.MethodStaging:
					res.StagedBytes += a.Bytes
				}
			}
		}
		if err := e.Log.Append(after); err != nil {
			return res, fmt.Errorf("oplog append failed after acting — journal is incomplete: %w", err)
		}
	}
	return res, nil
}

func actionID(a engine.Action) string {
	if a.ItemKey == "" {
		return a.RuleID
	}
	return engine.ItemID(a.RuleID, a.ItemKey)
}

// runPreAction runs the action's pre-action hook, if it names one.
func (e *Executor) runPreAction(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
	if a.PreAction == "" {
		return nil, nil
	}
	hook := e.PreActions[a.PreAction]
	if hook == nil {
		return nil, fmt.Errorf("pre-action %q is not registered, refusing to run %s bare", a.PreAction, a.RuleID)
	}
	return hook(ctx, a)
}

// prune runs a prune action and measures what it deleted as the cache
// before minus after, since find reports nothing itself. This is the
// one action that deletes without the Trash, so the target is checked
// again right before it runs.
func (e *Executor) prune(ctx context.Context, a engine.Action, runNative func(context.Context, []string) error) (*oplog.Pruned, error) {
	if err := engine.VerifyGoCache(a.Path); err != nil {
		return nil, err
	}
	before, err := engine.MeasureCache(ctx, a.Path)
	if err != nil {
		return nil, fmt.Errorf("measure the cache before pruning: %w", err)
	}
	cmdErr := runNative(ctx, a.Command)
	// A canceled prune has still deleted entries; the journal says how many.
	after, err := engine.MeasureCache(context.WithoutCancel(ctx), a.Path)
	if err != nil {
		return nil, errors.Join(cmdErr, fmt.Errorf("measure the cache after pruning: %w", err))
	}
	return &oplog.Pruned{
		Files: max(before.Files-after.Files, 0),
		Bytes: max(before.Bytes-after.Bytes, 0),
	}, cmdErr
}

// UndoResult summarises one undo pass.
type UndoResult struct {
	Restored int
	Failed   int
	Failures []string
	// NativeSkipped counts the run's native actions, which are not
	// undoable — their regen story is the recovery path.
	NativeSkipped int
	// ExportSkipped counts export receipts (docker volume tarballs):
	// not auto-restorable, recovery is manual from the staging file.
	ExportSkipped int
}

// Undo restores a run's undoable trash receipts, last moved first,
// journaling every attempt under the same run id.
func (e *Executor) Undo(run oplog.Run) (UndoResult, error) {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	var res UndoResult
	for _, entry := range run.Entries {
		if entry.Event != oplog.EventDone {
			continue
		}
		switch {
		case entry.Receipt == nil:
			res.NativeSkipped++
		case !entry.Receipt.Restorable():
			res.ExportSkipped++
		}
	}
	for _, entry := range run.Undoable() {
		err := trash.Restore(*entry.Receipt)
		undoEntry := oplog.Entry{
			Time: now(), Run: run.ID, Seq: entry.Seq, Event: oplog.EventUndo,
			RuleID: entry.RuleID, Path: entry.Receipt.Original,
		}
		if err != nil {
			undoEntry.Error = err.Error()
			res.Failed++
			res.Failures = append(res.Failures, err.Error())
		} else {
			res.Restored++
		}
		if logErr := e.Log.Append(undoEntry); logErr != nil {
			return res, fmt.Errorf("oplog append failed during undo: %w", logErr)
		}
	}
	return res, nil
}

// execNative runs a steward command with inherited stdio: sudo can
// prompt, docker can stream progress, the user sees what runs.
func execNative(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
