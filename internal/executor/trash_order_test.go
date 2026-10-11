package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// trashFixture is a fake machine for one run: a home with a Trash that
// already holds something, a cache to move, a Finder that renames into
// that Trash, and an "empty the Trash" command that wipes it.
type trashFixture struct {
	home, trashDir, target, logPath string
}

const emptyTrashCmd = "fixture-empty-trash"

func newTrashFixture(t *testing.T) trashFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	fx := trashFixture{
		home:     home,
		trashDir: filepath.Join(home, ".Trash"),
		target:   filepath.Join(home, "proj", "cache"),
		logPath:  logPath,
	}
	for _, f := range []string{filepath.Join(fx.trashDir, "older-junk"), filepath.Join(fx.target, "a.bin")} {
		if err := writeFile(f, "x"); err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

func (fx trashFixture) findings() []engine.Finding {
	return []engine.Finding{
		{
			Rule:  engine.Rule{ID: "fixture-cache", Risk: engine.RiskSafe},
			Items: []engine.Item{{Path: fx.target, Bytes: 1}},
		},
		{
			Rule: engine.Rule{
				ID: "fixture-trash-empty", Risk: engine.RiskCaution,
				NativeCommand: engine.Argv{emptyTrashCmd}, EmptiesTrash: true,
			},
			Items: []engine.Item{{Path: fx.trashDir, Bytes: 1}},
		},
	}
}

// run executes the plan against the fixture, then undoes the run from
// the journal as `regrow undo` would.
func (fx trashFixture) run(t *testing.T, plan engine.Plan) UndoResult {
	t.Helper()
	log, err := oplog.Open(fx.logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	e := &Executor{
		Trash: &trash.Mover{
			Home: fx.home,
			RunFinder: func(_ context.Context, path string) (string, error) {
				to := filepath.Join(fx.trashDir, filepath.Base(path))
				return to, os.Rename(path, to)
			},
		},
		Log: log,
		Now: fixedNow,
		RunNative: func(_ context.Context, argv []string) error {
			if !reflect.DeepEqual(argv, []string{emptyTrashCmd}) {
				t.Fatalf("unexpected native command %v", argv)
			}
			entries, err := os.ReadDir(fx.trashDir)
			if err != nil {
				return err
			}
			for _, en := range entries {
				if err := os.RemoveAll(filepath.Join(fx.trashDir, en.Name())); err != nil {
					return err
				}
			}
			return nil
		},
	}
	res, err := e.Execute(context.Background(), plan)
	if err != nil || res.Done != 2 {
		t.Fatalf("execute: %+v, %v", res, err)
	}
	if exists(filepath.Join(fx.trashDir, "older-junk")) {
		t.Fatal("the Trash was not emptied")
	}
	entries, err := oplog.Read(fx.logPath)
	if err != nil {
		t.Fatal(err)
	}
	runs := oplog.Runs(entries)
	if len(runs) != 1 {
		t.Fatalf("want one journaled run, got %d", len(runs))
	}
	undo, err := e.Undo(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	return undo
}

func TestEmptyTrashPlannedFirstKeepsTheRunsMoveRestorable(t *testing.T) {
	fx := newTrashFixture(t)
	plan := engine.BuildPlan(engine.Host{Home: fx.home}, fx.findings(),
		map[string]bool{"fixture-cache": true, "fixture-trash-empty": true})
	if len(plan.Actions) != 2 || plan.Actions[0].RuleID != "fixture-trash-empty" {
		t.Fatalf("want the Trash emptied first, got %+v", plan.Actions)
	}

	undo := fx.run(t, plan)
	if undo.Restored != 1 || undo.Failed != 0 {
		t.Fatalf("undo = %+v, want the move restored", undo)
	}
	if !exists(filepath.Join(fx.target, "a.bin")) {
		t.Fatal("undo did not bring the moved cache back")
	}
}

// The fixture can tell the orders apart: with the move first, emptying
// the Trash destroys it and undo has nothing to restore.
func TestEmptyTrashAfterTheMoveLosesIt(t *testing.T) {
	fx := newTrashFixture(t)
	plan := engine.BuildPlan(engine.Host{Home: fx.home}, fx.findings(),
		map[string]bool{"fixture-cache": true, "fixture-trash-empty": true})
	plan.Actions[0], plan.Actions[1] = plan.Actions[1], plan.Actions[0]

	undo := fx.run(t, plan)
	if undo.Restored != 0 || undo.Failed != 1 {
		t.Fatalf("undo = %+v, want the restore to fail", undo)
	}
}
