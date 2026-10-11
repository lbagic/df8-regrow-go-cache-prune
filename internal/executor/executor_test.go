package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

type fakeMover struct {
	fail  map[string]error
	moved []string
}

func (m *fakeMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	if err := m.fail[path]; err != nil {
		return trash.Receipt{}, err
	}
	m.moved = append(m.moved, path)
	return trash.Receipt{Original: path, To: "/staging" + path, Method: trash.MethodStaging}, nil
}

type memLog struct {
	entries []oplog.Entry
	fail    bool
}

func (l *memLog) Append(e oplog.Entry) error {
	if l.fail {
		return errors.New("disk full")
	}
	l.entries = append(l.entries, e)
	return nil
}

func fixedNow() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }

func testPlan() engine.Plan {
	return engine.Plan{Actions: []engine.Action{
		{RuleID: "npm-cache", Kind: engine.ActionTrash, Path: "/u/.npm", Bytes: 100, Command: trash.PreviewCommand("/u/.npm")},
		{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: 200},
	}}
}

func TestExecuteJournalsBeforeActing(t *testing.T) {
	log := &memLog{}
	var order []string
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error { order = append(order, "native"); return nil }}

	res, err := e.Execute(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Failed != 0 || res.Bytes != 300 {
		t.Fatalf("result wrong: %+v", res)
	}
	// start(1), done(1), start(2), done(2) — start always precedes its action's outcome line.
	events := []string{}
	for _, en := range log.entries {
		events = append(events, en.Event)
	}
	want := []string{"start", "done", "start", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("journal order = %v, want %v", events, want)
	}
	if log.entries[1].Receipt == nil || log.entries[1].Receipt.Original != "/u/.npm" {
		t.Fatalf("done trash entry must carry the receipt: %+v", log.entries[1])
	}
	if log.entries[3].Receipt != nil {
		t.Fatal("native done entry must not carry a receipt")
	}
}

func TestExecuteRefusesWhenJournalDown(t *testing.T) {
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: &memLog{fail: true}, Now: fixedNow}
	_, err := e.Execute(context.Background(), testPlan())
	if err == nil || !strings.Contains(err.Error(), "refusing to act") {
		t.Fatalf("journal failure must block actions, got %v", err)
	}
	if len(mover.moved) != 0 {
		t.Fatal("nothing may move when the journal cannot be written")
	}
}

func TestExecuteContinuesPastFailures(t *testing.T) {
	log := &memLog{}
	mover := &fakeMover{fail: map[string]error{"/u/.npm": errors.New("vanished since scan")}}
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error { return nil }}

	res, err := e.Execute(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || res.Failed != 1 || res.Bytes != 200 || res.TrashBytes+res.StagedBytes != 0 {
		t.Fatalf("one failure must not abort the run: %+v", res)
	}
	if log.entries[1].Event != oplog.EventFail || !strings.Contains(log.entries[1].Error, "vanished") {
		t.Fatalf("failure must be journaled: %+v", log.entries[1])
	}
}

func TestExecuteStopsOnCancel(t *testing.T) {
	log := &memLog{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Executor{Trash: &fakeMover{}, Log: log, Now: fixedNow}
	if _, err := e.Execute(ctx, testPlan()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(log.entries) != 0 {
		t.Fatal("cancelled run must not journal or act")
	}
}

func TestExecuteStopEndsTheRunBeforeTheNextAction(t *testing.T) {
	log := &memLog{}
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error {
			t.Error("an action started after the stop")
			return nil
		},
		// Asked to stop while the first action is in flight.
		Stop: func() bool { return len(mover.moved) > 0 }}

	res, err := e.Execute(context.Background(), testPlan())
	if err != nil {
		t.Fatalf("a stop is not an error, got %v", err)
	}
	if !res.Stopped || res.Done != 1 || res.Failed != 0 || res.Bytes != 100 {
		t.Fatalf("result = %+v, want the first action done and the run stopped", res)
	}
	if len(log.entries) != 2 || log.entries[1].Event != oplog.EventDone || log.entries[1].Receipt == nil {
		t.Fatalf("the action in flight must be journaled to its end, got %+v", log.entries)
	}
}

func exportPlan() engine.Plan {
	return engine.Plan{Actions: []engine.Action{{
		RuleID: "docker-volumes-named", ItemKey: "dune_db", Kind: engine.ActionNative,
		Command: []string{"docker", "volume", "rm", "dune_db"}, Bytes: 500,
		PreAction: engine.PreActionVolumeExport,
	}}}
}

func TestExecutePreActionRunsBeforeCommand(t *testing.T) {
	log := &memLog{}
	var order []string
	e := &Executor{Log: log, Now: fixedNow,
		PreActions: map[string]PreAction{
			engine.PreActionVolumeExport: func(_ context.Context, a engine.Action) (*trash.Receipt, error) {
				order = append(order, "export:"+a.ItemKey)
				return &trash.Receipt{Original: "docker volume " + a.ItemKey, To: "/staging/dune_db.tar", Method: trash.MethodExport}, nil
			},
		},
		RunNative: func(_ context.Context, argv []string) error {
			order = append(order, "native:"+strings.Join(argv, " "))
			return nil
		}}
	res, err := e.Execute(context.Background(), exportPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || res.Failed != 0 {
		t.Fatalf("result wrong: %+v", res)
	}
	want := "export:dune_db,native:docker volume rm dune_db"
	if strings.Join(order, ",") != want {
		t.Fatalf("order = %v, want export before rm", order)
	}
	if log.entries[1].Receipt == nil || log.entries[1].Receipt.Method != trash.MethodExport {
		t.Fatalf("done entry must carry the export receipt: %+v", log.entries[1])
	}
}

func TestExecuteCountsARemovedVolumeAsExported(t *testing.T) {
	e := &Executor{Log: &memLog{}, Now: fixedNow, Trash: &fakeMover{},
		PreActions: map[string]PreAction{
			engine.PreActionVolumeExport: func(_ context.Context, a engine.Action) (*trash.Receipt, error) {
				return &trash.Receipt{Original: "docker volume " + a.ItemKey, To: "/staging/" + a.ItemKey + ".tar", Method: trash.MethodExport}, nil
			},
		},
		RunNative: func(_ context.Context, argv []string) error {
			if argv[len(argv)-1] == "kept_db" {
				return errors.New("volume is in use")
			}
			return nil
		}}
	volume := func(name string, bytes int64) engine.Action {
		return engine.Action{RuleID: "docker-volumes-named", ItemKey: name, Kind: engine.ActionNative,
			Command: []string{"docker", "volume", "rm", name}, Bytes: bytes, PreAction: engine.PreActionVolumeExport}
	}
	plan := engine.Plan{Actions: []engine.Action{
		volume("dune_db", 300), volume("kept_db", 70),
		{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: 50},
		{RuleID: "npm-cache", Kind: engine.ActionTrash, Path: "/u/.npm", Bytes: 20, Command: trash.PreviewCommand("/u/.npm")},
	}}
	res, err := e.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 3 || res.Failed != 1 || res.Bytes != 370 || res.ExportedBytes != 300 || res.StagedBytes != 20 || res.TrashBytes != 0 {
		t.Fatalf("result = %+v, want 370 bytes done, of them 300 an exported volume and 20 staged; the volume whose removal failed counts nowhere", res)
	}
}

func TestExecutePreActionFailureBlocksCommand(t *testing.T) {
	log := &memLog{}
	nativeRan := false
	e := &Executor{Log: log, Now: fixedNow,
		PreActions: map[string]PreAction{
			engine.PreActionVolumeExport: func(context.Context, engine.Action) (*trash.Receipt, error) {
				return nil, errors.New("export failed, refusing to remove")
			},
		},
		RunNative: func(context.Context, []string) error { nativeRan = true; return nil }}
	res, err := e.Execute(context.Background(), exportPlan())
	if err != nil {
		t.Fatal(err)
	}
	if nativeRan {
		t.Fatal("no backup, no deletion: the command must not run after a failed pre-action")
	}
	if res.Failed != 1 || log.entries[1].Event != oplog.EventFail {
		t.Fatalf("failed pre-action must fail the action: %+v, %+v", res, log.entries[1])
	}
}

func recheckPlan() engine.Plan {
	const path = "/private/tmp/claude-1000/-Users-t-proj/11111111-1111-4111-8111-111111111111"
	return engine.Plan{Actions: []engine.Action{{
		RuleID: "agent-scratch", ItemKey: path, Kind: engine.ActionTrash, Path: path, Bytes: 500,
		Command: trash.PreviewCommand(path), PreAction: engine.PreActionAgentScratchRecheck,
	}}}
}

func TestExecutePreActionGatesTrashMove(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		log := &memLog{}
		mover := &fakeMover{}
		var checked []string
		e := &Executor{Trash: mover, Log: log, Now: fixedNow,
			PreActions: map[string]PreAction{
				engine.PreActionAgentScratchRecheck: func(_ context.Context, a engine.Action) (*trash.Receipt, error) {
					checked = append(checked, a.Path)
					if refuse {
						return nil, errors.New("session is running again")
					}
					return nil, nil
				},
			}}
		res, err := e.Execute(context.Background(), recheckPlan())
		if err != nil {
			t.Fatal(err)
		}
		path := recheckPlan().Actions[0].Path
		if len(checked) != 1 || checked[0] != path {
			t.Fatalf("refuse=%v: pre-action saw %v, want the item's path once", refuse, checked)
		}
		if refuse {
			if len(mover.moved) != 0 || res.Failed != 1 || log.entries[1].Event != oplog.EventFail {
				t.Errorf("a refused recheck must fail the action without moving: moved %v, %+v", mover.moved, res)
			}
			continue
		}
		if len(mover.moved) != 1 || res.Done != 1 || log.entries[1].Receipt == nil || log.entries[1].Receipt.To != "/staging"+path {
			t.Errorf("a passed recheck must move and journal the move's receipt: moved %v, %+v, %+v", mover.moved, res, log.entries[1])
		}
	}
}

func TestExecuteTrashWithUnregisteredPreActionDoesNotMove(t *testing.T) {
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: &memLog{}, Now: fixedNow}
	res, err := e.Execute(context.Background(), recheckPlan())
	if err != nil {
		t.Fatal(err)
	}
	if len(mover.moved) != 0 || res.Failed != 1 {
		t.Errorf("a Trash move whose pre-action is not registered must not run: moved %v, %+v", mover.moved, res)
	}
}

func TestExecutePreActionReceiptJournaledOnCommandFailure(t *testing.T) {
	// Export succeeded, rm then failed: the journal must still say
	// where the backup landed.
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow,
		PreActions: map[string]PreAction{
			engine.PreActionVolumeExport: func(context.Context, engine.Action) (*trash.Receipt, error) {
				return &trash.Receipt{Original: "docker volume dune_db", To: "/staging/dune_db.tar", Method: trash.MethodExport}, nil
			},
		},
		RunNative: func(context.Context, []string) error { return errors.New("daemon died") }}
	res, err := e.Execute(context.Background(), exportPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Fatalf("result wrong: %+v", res)
	}
	fail := log.entries[1]
	if fail.Event != oplog.EventFail || fail.Receipt == nil || fail.Receipt.To != "/staging/dune_db.tar" {
		t.Fatalf("fail entry must keep the export receipt: %+v", fail)
	}
}

func TestExecuteUnregisteredPreActionFails(t *testing.T) {
	log := &memLog{}
	nativeRan := false
	e := &Executor{Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error { nativeRan = true; return nil }}
	res, err := e.Execute(context.Background(), exportPlan())
	if err != nil {
		t.Fatal(err)
	}
	if nativeRan || res.Failed != 1 {
		t.Fatalf("an unregistered pre-action must never run the command bare: %+v", res)
	}
}

func TestUndoCountsExportReceipts(t *testing.T) {
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow}
	run := oplog.Run{ID: "r1", Entries: []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "docker-volumes-named",
			Receipt: &trash.Receipt{Original: "docker volume dune_db", To: "/staging/dune_db.tar", Method: trash.MethodExport}},
	}}
	res, err := e.Undo(run)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExportSkipped != 1 || res.Restored != 0 || res.Failed != 0 {
		t.Fatalf("export receipts are counted, never restored: %+v", res)
	}
	if len(log.entries) != 0 {
		t.Fatal("nothing to journal when nothing was restored")
	}
}

func TestUndoRestoresReverseAndJournals(t *testing.T) {
	// Build a real staged move so Restore has something to rename.
	staging := t.TempDir()
	target := t.TempDir() + "/cache"
	if err := writeFile(target+"/a.bin", "x"); err != nil {
		t.Fatal(err)
	}
	m := &trash.Mover{Home: "/nope", StagingDir: staging,
		RunFinder: func(context.Context, string) (string, error) { return "", errors.New("no Finder") }}
	receipt, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}

	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow}
	run := oplog.Run{ID: "r1", Entries: []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "x", Receipt: &receipt},
		{Run: "r1", Seq: 2, Event: oplog.EventDone, RuleID: "go-build-cache"}, // native
	}}
	res, err := e.Undo(run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.Failed != 0 || res.NativeSkipped != 1 {
		t.Fatalf("undo result wrong: %+v", res)
	}
	if len(log.entries) != 1 || log.entries[0].Event != oplog.EventUndo || log.entries[0].Error != "" {
		t.Fatalf("undo must journal the restore: %+v", log.entries)
	}
	if !exists(target + "/a.bin") {
		t.Fatal("undo did not bring the tree back")
	}
}

func TestUndoJournalsFailedRestore(t *testing.T) {
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow}
	run := oplog.Run{ID: "r1", Entries: []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "x",
			Receipt: &trash.Receipt{Original: "/u/x", To: "/gone/x", Method: trash.MethodStaging}},
	}}
	res, err := e.Undo(run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Restored != 0 {
		t.Fatalf("want failed restore counted: %+v", res)
	}
	if len(log.entries) != 1 || log.entries[0].Error == "" {
		t.Fatalf("failed restore must be journaled with its error: %+v", log.entries)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestExecuteSplitsTrashFromStaging(t *testing.T) {
	tests := []struct {
		name               string
		finder             func(context.Context, string) (string, error)
		wantTrash, wantStg int64
	}{
		{"Finder moves to the Trash", func(_ context.Context, path string) (string, error) {
			to := filepath.Join(filepath.Dir(path), ".Trash-fixture")
			return to, os.Rename(path, to)
		}, 100, 0},
		{"Finder unavailable stages", func(context.Context, string) (string, error) {
			return "", errors.New("no Finder over ssh")
		}, 0, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			target := filepath.Join(home, "proj", "cache")
			if err := writeFile(filepath.Join(target, "a.bin"), "x"); err != nil {
				t.Fatal(err)
			}
			e := &Executor{
				Trash: &trash.Mover{Home: home, StagingDir: t.TempDir(), RunFinder: tt.finder},
				Log:   &memLog{}, Now: fixedNow,
				RunNative: func(context.Context, []string) error { t.Fatal("no native action planned"); return nil },
			}
			plan := engine.Plan{Actions: []engine.Action{
				{RuleID: "cache", Kind: engine.ActionTrash, Path: target, Bytes: 100, Command: trash.PreviewCommand(target)},
			}}
			res, err := e.Execute(context.Background(), plan)
			if err != nil || res.Done != 1 {
				t.Fatalf("execute: %+v, %v", res, err)
			}
			if res.TrashBytes != tt.wantTrash || res.StagedBytes != tt.wantStg {
				t.Fatalf("TrashBytes %d StagedBytes %d, want %d %d", res.TrashBytes, res.StagedBytes, tt.wantTrash, tt.wantStg)
			}
		})
	}
}

func TestExecuteSkipsTrashMovesAfterFailedEmpty(t *testing.T) {
	log := &memLog{}
	mover := &fakeMover{}
	var ran []string
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(_ context.Context, argv []string) error {
			ran = append(ran, argv[0])
			if argv[0] == "empty-trash" {
				return errors.New("AppleEvent timed out")
			}
			return nil
		}}
	plan := engine.Plan{Actions: []engine.Action{
		{RuleID: "trash-empty", Kind: engine.ActionNative, Command: []string{"empty-trash"}, Bytes: 10, EmptiesTrash: true},
		{RuleID: "npm-cache", Kind: engine.ActionTrash, Path: "/u/.npm", Bytes: 100, Command: trash.PreviewCommand("/u/.npm")},
		{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: 200},
	}}
	res, err := e.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(mover.moved) != 0 {
		t.Fatalf("no Trash move may run after a failed empty, moved %v", mover.moved)
	}
	if want := []string{"empty-trash", "go"}; strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Fatalf("native commands run = %v, want %v (steward commands do not touch the Trash)", ran, want)
	}
	if res.Failed != 1 || res.Done != 1 || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0], "npm-cache: emptying the Trash failed") {
		t.Fatalf("result = %+v", res)
	}
	for _, en := range log.entries {
		if en.RuleID == "npm-cache" {
			t.Fatalf("a skipped action must not be journaled: %+v", en)
		}
	}
}
