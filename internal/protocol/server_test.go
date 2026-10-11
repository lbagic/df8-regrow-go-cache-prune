package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ja) == string(jb)
}

// gatedMover holds every move until release is closed, reporting each
// path as its move begins.
type gatedMover struct {
	inner   executor.Mover
	entered chan string
	release chan struct{}
}

func newGatedMover(inner executor.Mover) *gatedMover {
	return &gatedMover{inner: inner, entered: make(chan string, 8), release: make(chan struct{})}
}

func (g *gatedMover) Move(ctx context.Context, path string) (trash.Receipt, error) {
	g.entered <- path
	<-g.release
	return g.inner.Move(ctx, path)
}

func (g *gatedMover) waitEntered(t *testing.T, path string) {
	t.Helper()
	select {
	case got := <-g.entered:
		if got != path {
			t.Fatalf("move of %s began, want %s", got, path)
		}
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for the move of %s to begin", path)
	}
}

const bothRules = `["fixture-cache","fixture-logs"]`

// neverTick is a tick seam for tests in which every tick must be
// refused before it runs.
func neverTick(t *testing.T) func(context.Context, bool) (headroom.Tick, error) {
	return func(context.Context, bool) (headroom.Tick, error) {
		t.Error("a tick ran while another operation was in flight")
		return headroom.Tick{}, nil
	}
}

func TestScanPlanExecuteOverPipes(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.Version = "1.2.3"
	srv.Headroom = func(context.Context) (headroom.Sample, error) {
		return headroom.Sample{At: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), Total: 100, Free: 7, Purgeable: 2, SwapUsed: 3}, nil
	}
	exclusive := map[string]int64{"fixture-cache/~/proj/cache": 100, "fixture-logs/~/proj/logs": 40}
	srv.Account = func(findings []engine.Finding) engine.Ledger {
		if len(findings) != 2 || findings[1].Rule.ID != "fixture-logs" {
			t.Errorf("the summary must see every finding in catalog order, got %+v", findings)
		}
		return engine.Ledger{Exclusive: exclusive, Totals: engine.Totals{AfterTrash: 140, Partial: 1}}
	}
	p := start(t, srv)

	hello := p.expect("hello")[0]
	if hello.Protocol != 1 || hello.Version != "1.2.3" || hello.FDA != FDAGranted {
		t.Fatalf("hello = %s, want protocol 1, version 1.2.3 and fda granted (the fixture Trash is readable)", hello.raw)
	}

	p.send(`{"type":"scan","id":"s"}`)
	scan := p.expect("headroom/s", "start/s", "finding/s", "finding/s", "summary/s", "done/s")
	if scan[0].raw != `{"event":"headroom","re":"s","at":"2026-10-04T12:00:00Z","total":100,"free":7,"purgeable":2,"swap_used":3}` {
		t.Errorf("headroom = %s, want the sample's fields on the event line", scan[0].raw)
	}
	scanID := scan[1].ScanID
	if scanID == "" || scan[1].Rules != 2 {
		t.Fatalf("start = %s, want a scan id and 2 rules", scan[1].raw)
	}
	for i, e := range scan[2:4] {
		want := m.findings()[i]
		if e.ScanID != scanID || e.Index != i || e.TookMS != int64(i+1) || e.Finding.Rule.ID != want.Rule.ID {
			t.Errorf("finding %d = %s", i, e.raw)
		}
		if len(e.Finding.Items) != 1 || e.Finding.Items[0].Bytes != want.Items[0].Bytes {
			t.Errorf("finding %d items = %+v", i, e.Finding.Items)
		}
	}
	if key := scan[2].Finding.Items[0].Key; key != "~/proj/cache" {
		t.Errorf("a scanned item must carry the key plans address it by, got %q", key)
	}
	if scan[4].ScanID != scanID || *scan[4].Totals != (engine.Totals{AfterTrash: 140, Partial: 1}) || !sameJSON(t, scan[4].Exclusive, exclusive) {
		t.Errorf("summary = %s", scan[4].raw)
	}
	if !strings.Contains(scan[4].raw, `"totals":{"frees_now":0,"after_trash":140,"shown_only":0,"macos_managed":0,"partial":1}`) {
		t.Errorf("summary totals keys = %s", scan[4].raw)
	}
	if scan[5].Canceled == nil || *scan[5].Canceled || scan[5].ElapsedMS == nil {
		t.Errorf("scan done = %s, want elapsed_ms and canceled:false", scan[5].raw)
	}

	// No select: the default selection, which is the safe rule only.
	planned := p.planned("p", scanID, "")
	if got := actionRules(planned.Plan); got != "fixture-cache" {
		t.Fatalf("planned %q, want the safe rule alone", got)
	}
	a := planned.Plan.Actions[0]
	if a.Kind != engine.ActionTrash || a.Path != m.cache || a.Bytes != 100 {
		t.Errorf("action = %+v", a)
	}
	if *planned.Totals != (engine.Totals{AfterTrash: 100}) {
		t.Errorf("plan totals = %+v, want 100 bytes after the Trash is emptied", *planned.Totals)
	}
	if planned.PlanID == "" {
		t.Fatal("a plan event must carry its plan id")
	}

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	run := p.expect("journal/x", "journal/x", "done/x")
	res := run[2].Result
	if res.RunID != planned.PlanID || res.Done != 1 || res.Failed != 0 || res.Bytes != 100 || res.TrashBytes != 100 {
		t.Errorf("result = %s", run[2].raw)
	}
	if run[2].Canceled == nil || *run[2].Canceled {
		t.Errorf("execute done = %s, want canceled:false", run[2].raw)
	}
	if exists(m.cache) || !exists(filepath.Join(m.trashDir, "cache", "a.bin")) {
		t.Error("the selected cache was not moved to the Trash")
	}
	if !exists(m.logs) {
		t.Error("the unselected rule's item was touched")
	}

	// The oplog holds exactly what the journal events said, under the
	// plan id as run id.
	entries := m.journal()
	if len(entries) != 2 {
		t.Fatalf("oplog has %d entries, want start and done", len(entries))
	}
	for i, wantEvent := range []string{oplog.EventStart, oplog.EventDone} {
		if entries[i].Event != wantEvent || entries[i].Run != planned.PlanID || entries[i].Seq != 1 || entries[i].RuleID != "fixture-cache" {
			t.Errorf("oplog entry %d = %+v", i, entries[i])
		}
		if !sameJSON(t, entries[i], run[i].Entry) {
			t.Errorf("journal event %d = %s, oplog has %+v", i, run[i].raw, entries[i])
		}
	}
	if r := entries[1].Receipt; r == nil || r.Original != m.cache || r.Method != trash.MethodFinder {
		t.Errorf("done entry receipt = %+v", r)
	}

	rest, err := p.closeAndWait()
	if err != nil || len(rest) != 0 {
		t.Fatalf("after EOF: err %v, stray events %v", err, rest)
	}
}

func TestPlanSelection(t *testing.T) {
	m := newMachine(t)
	sudoRule := engine.Rule{ID: "fixture-sudo", Risk: engine.RiskSafe, NativeCommand: engine.Argv{"fixture-wipe"}, Sudo: true}
	findings := append(m.findings(), engine.Finding{
		Rule: sudoRule, Items: []engine.Item{{Path: filepath.Join(m.home, "proj", "system"), Bytes: 7}},
	})
	srv := m.server()
	srv.Catalog = append(srv.Catalog, sudoRule)
	srv.Scan = scanOf(findings)
	p := start(t, srv)
	p.expect("hello")
	scanID := p.scanned("s", 3)

	const sudoReason = "needs administrator rights — run `regrow clean fixture-sudo` in Terminal"
	tests := []struct {
		name          string
		selectJSON    string
		wantActions   string
		wantSkipped   string
		wantUnmatched string
		wantTotals    engine.Totals
	}{
		{"no select plans the default selection, sudo already skipped", "", "fixture-cache", sudoReason, "", engine.Totals{AfterTrash: 100}},
		{"null select is no select", "null", "fixture-cache", sudoReason, "", engine.Totals{AfterTrash: 100}},
		{"empty select plans nothing", "[]", "", "", "", engine.Totals{}},
		{"a named caution rule", `["fixture-logs"]`, "fixture-logs", "", "", engine.Totals{AfterTrash: 40}},
		{"a named sudo rule is a skip", `["fixture-sudo"]`, "", sudoReason, "", engine.Totals{}},
	}
	// As `regrow clean` refuses: a typo gets no plan id at all.
	p.send(`{"type":"plan","id":"typo","scan_id":"` + scanID + `","select":["fixture-cache","nope","fixture-cash"]}`)
	if e := p.expectError("typo", CodeUnmatched); !strings.Contains(e.raw, `"unmatched":["fixture-cash","nope"]`) {
		t.Fatalf("unmatched plan = %s, want the selectors listed", e.raw)
	}

	// One engine serves every row, so the rows run in order on this
	// test's goroutine rather than as subtests.
	ids := map[string]bool{}
	for _, tt := range tests {
		e := p.planned("p", scanID, tt.selectJSON)
		if got := actionRules(e.Plan); got != tt.wantActions {
			t.Errorf("%s: actions = %q, want %q", tt.name, got, tt.wantActions)
		}
		var skipped []string
		for _, s := range e.Plan.Skipped {
			skipped = append(skipped, s.Reason)
		}
		if got := strings.Join(skipped, "|"); got != tt.wantSkipped {
			t.Errorf("%s: skipped = %q, want %q", tt.name, got, tt.wantSkipped)
		}
		if got := strings.Join(e.Plan.Unmatched, ","); got != tt.wantUnmatched {
			t.Errorf("%s: unmatched = %q, want %q", tt.name, got, tt.wantUnmatched)
		}
		if *e.Totals != tt.wantTotals {
			t.Errorf("%s: totals = %+v, want %+v", tt.name, *e.Totals, tt.wantTotals)
		}
		if tt.wantActions == "" && !strings.Contains(e.raw, `"actions":[]`) {
			t.Errorf("%s: an empty plan must list no actions as [], got %s", tt.name, e.raw)
		}
		if ids[e.PlanID] {
			t.Errorf("%s: plan id %s was issued twice", tt.name, e.PlanID)
		}
		ids[e.PlanID] = true
	}
}

func TestPlanIsSingleUse(t *testing.T) {
	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)

	execute := `{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`
	p.send(execute)
	p.expect("journal/x", "journal/x", "journal/x", "journal/x", "done/x")
	before := m.journal()

	p.send(execute)
	p.expectError("x", CodeUnknownPlan)
	if after := m.journal(); len(after) != len(before) {
		t.Fatalf("the second execute journaled %d more entries", len(after)-len(before))
	}
	if runs := oplog.Runs(m.journal()); len(runs) != 1 {
		t.Fatalf("want one run in the oplog, got %d", len(runs))
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestPlanExpiresAfterTenMinutes(t *testing.T) {
	m := newMachine(t)
	clock := &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	srv := m.server()
	srv.Now = clock.Now
	p := start(t, srv)
	p.expect("hello")

	// Exactly ten minutes old still runs.
	onTime := p.planned("p1", p.scanned("s1", 2), `["fixture-cache"]`)
	clock.advance(10 * time.Minute)
	p.send(`{"type":"execute","id":"x1","plan_id":"` + onTime.PlanID + `"}`)
	p.expect("journal/x1", "journal/x1", "done/x1")

	// A second older does not.
	late := p.planned("p2", p.scanned("s2", 2), `["fixture-logs"]`)
	clock.advance(10*time.Minute + time.Second)
	p.send(`{"type":"execute","id":"x2","plan_id":"` + late.PlanID + `"}`)
	p.expectError("x2", CodePlanExpired)
	if !exists(m.logs) {
		t.Fatal("an expired plan moved its target")
	}
	for _, e := range m.journal() {
		if e.Run == late.PlanID {
			t.Fatalf("an expired plan was journaled: %+v", e)
		}
	}
	// Expiry consumed the id.
	p.send(`{"type":"execute","id":"x3","plan_id":"` + late.PlanID + `"}`)
	p.expectError("x3", CodeUnknownPlan)

	// A plan pruned for age while another plan is made still answers
	// plan_expired, and its expiry spends nothing else.
	scanID := p.scanned("s3", 2)
	stale := p.planned("p3", scanID, `["fixture-logs"]`)
	clock.advance(11 * time.Minute)
	fresh := p.planned("p4", scanID, `["fixture-logs"]`)
	p.send(`{"type":"execute","id":"x4","plan_id":"` + stale.PlanID + `"}`)
	p.expectError("x4", CodePlanExpired)
	p.send(`{"type":"execute","id":"x5","plan_id":"` + fresh.PlanID + `"}`)
	if done := p.expect("journal/x5", "journal/x5", "done/x5")[2]; done.Result.Done != 1 || exists(m.logs) {
		t.Fatalf("the fresh plan did not run: %s", done.raw)
	}
}

func TestPlanClockIgnoresTheMonotonicReading(t *testing.T) {
	// The monotonic clock stops while a Mac sleeps; a plan made before
	// a night with the lid closed must be measured in wall time.
	for _, srv := range []*Server{{}, {Now: time.Now}} {
		s := &session{srv: srv}
		if got := s.now(); strings.Contains(got.String(), " m=") {
			t.Fatalf("plan clock %s carries a monotonic reading", got)
		}
	}
}

func TestNewScanSupersedesOlderPlans(t *testing.T) {
	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")
	first := p.scanned("s1", 2)
	planned := p.planned("p", first, bothRules)
	second := p.scanned("s2", 2)
	if second == first {
		t.Fatal("two scans shared a scan id")
	}

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expectError("x", CodeUnknownPlan)
	if !exists(m.cache) || len(m.journal()) != 0 {
		t.Fatal("a plan from a replaced scan ran")
	}
	p.send(`{"type":"plan","id":"p2","scan_id":"` + first + `"}`)
	p.expectError("p2", CodeScanSuperseded)
	p.send(`{"type":"plan","id":"p3","scan_id":"never-issued"}`)
	p.expectError("p3", CodeUnknownScan)
}

// Plans die when a newer scan starts, not when it finishes: a canceled
// rescan still ends them.
func TestPlansDieWhenANewScanStarts(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	fixed := srv.Scan
	calls := 0
	srv.Scan = func(ctx context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		calls++
		if calls == 1 {
			fixed(ctx, rules, emit)
			return
		}
		<-ctx.Done()
	}
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s1", 2), bothRules)

	p.send(`{"type":"scan","id":"s2"}`)
	p.expect("start/s2")
	p.send(`{"type":"cancel","id":"c","target":"s2"}`)
	p.expect("done/s2", "done/c")

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expectError("x", CodeUnknownPlan)
	if !exists(m.cache) || len(m.journal()) != 0 {
		t.Fatal("a plan outlived the scan that started after it")
	}
}

func TestCancelThenRescan(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	real := srv.Scan
	calls := 0
	srv.Scan = func(ctx context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		calls++
		if calls > 1 {
			real(ctx, rules, emit)
			return
		}
		emit(0, m.findings()[0], 0)
		<-ctx.Done()
		// A finding that arrives after the cancel must not be sent.
		emit(1, m.findings()[1], 0)
	}
	srv.Tick = neverTick(t)
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"scan","id":"s1"}`)
	first := p.expect("start/s1", "finding/s1")[0].ScanID

	p.send(`{"type":"scan","id":"s2"}`)
	p.expectError("s2", CodeBusy)
	p.send(`{"type":"tick","id":"t"}`)
	p.expectError("t", CodeBusy)
	p.send(`{"type":"plan","id":"p1","scan_id":"` + first + `"}`)
	p.expectError("p1", CodeScanRunning)

	p.send(`{"type":"cancel","id":"c","target":"s1"}`)
	ended := p.expect("done/s1", "done/c")
	if ended[0].Canceled == nil || !*ended[0].Canceled {
		t.Fatalf("the canceled scan ended with %s, want canceled:true", ended[0].raw)
	}
	p.send(`{"type":"plan","id":"p2","scan_id":"` + first + `"}`)
	p.expectError("p2", CodeScanCanceled)

	// The cancel's done is the peer's cue: a rescan sent after it is
	// never busy.
	second := p.scanned("s3", 2)
	if second == first {
		t.Fatal("the rescan reused the canceled scan's id")
	}
	p.send(`{"type":"plan","id":"p3","scan_id":"` + first + `"}`)
	p.expectError("p3", CodeScanSuperseded)
	if got := actionRules(p.planned("p4", second, "").Plan); got != "fixture-cache" {
		t.Fatalf("plan after the rescan = %q", got)
	}
}

func TestBusyWhileExecuting(t *testing.T) {
	m := newMachine(t)
	gate := newGatedMover(m.mover())
	srv := m.server()
	srv.NewExecutor = m.executors(gate)
	srv.Tick = neverTick(t)
	p := start(t, srv)
	p.expect("hello")
	scanID := p.scanned("s", 2)
	cachePlan := p.planned("p1", scanID, `["fixture-cache"]`)
	logsPlan := p.planned("p2", scanID, `["fixture-logs"]`)

	p.send(`{"type":"execute","id":"x1","plan_id":"` + cachePlan.PlanID + `"}`)
	p.expect("journal/x1")
	gate.waitEntered(t, m.cache)

	p.send(`{"type":"scan","id":"s2"}`)
	p.expectError("s2", CodeBusy)
	p.send(`{"type":"execute","id":"x2","plan_id":"` + logsPlan.PlanID + `"}`)
	p.expectError("x2", CodeBusy)
	p.send(`{"type":"tick","id":"t","autotrim":true}`)
	p.expectError("t", CodeBusy)
	// A plan built while the execute runs would replay against
	// findings it is changing.
	p.send(`{"type":"plan","id":"p3","scan_id":"` + scanID + `"}`)
	p.expectError("p3", CodeScanSpent)

	close(gate.release)
	p.expect("journal/x1", "done/x1")

	// The refused scan did not replace the spent one, and the slot is
	// free again.
	p.send(`{"type":"plan","id":"p4","scan_id":"` + scanID + `"}`)
	p.expectError("p4", CodeScanSpent)
	logsPlan = p.planned("p5", p.scanned("s3", 2), `["fixture-logs"]`)
	p.send(`{"type":"execute","id":"x3","plan_id":"` + logsPlan.PlanID + `"}`)
	gate.waitEntered(t, m.logs)
	if done := p.expect("journal/x3", "journal/x3", "done/x3")[2]; done.Result.Done != 1 || exists(m.logs) {
		t.Fatalf("an execute after the busy one did not run: %s", done.raw)
	}
}

// Two plans of one scan, both emptying the Trash: run back to back,
// the second would permanently delete the first run's Trash moves.
func TestExecuteSpendsEveryPlanOfItsScan(t *testing.T) {
	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")
	scanID := p.scanned("s", 2)
	first := p.planned("p1", scanID, `["fixture-cache"]`)
	second := p.planned("p2", scanID, bothRules)

	p.send(`{"type":"execute","id":"x1","plan_id":"` + first.PlanID + `"}`)
	p.expect("journal/x1", "journal/x1", "done/x1")
	journaled := len(m.journal())

	p.send(`{"type":"execute","id":"x2","plan_id":"` + second.PlanID + `"}`)
	p.expectError("x2", CodeUnknownPlan)
	if !exists(m.logs) || len(m.journal()) != journaled {
		t.Fatal("a second plan of an executed scan ran")
	}
	p.send(`{"type":"plan","id":"p3","scan_id":"` + scanID + `"}`)
	p.expectError("p3", CodeScanSpent)
}

func TestCancelStopsAnExecuteBetweenActions(t *testing.T) {
	m := newMachine(t)
	gate := newGatedMover(m.mover())
	srv := m.server()
	srv.NewExecutor = m.executors(gate)
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)
	if got := actionRules(planned.Plan); got != "fixture-cache,fixture-logs" {
		t.Fatalf("planned %q", got)
	}

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expect("journal/x")
	gate.waitEntered(t, m.cache)
	p.send(`{"type":"cancel","id":"c","target":"x"}`)
	// Sent after the cancel and answered before the move is released:
	// the cancel did not wait for the action, and did not kill it.
	p.send(`{"type":"cancel","id":"probe","target":"nothing"}`)
	p.expect("done/probe")
	close(gate.release)

	ended := p.expect("journal/x", "done/x", "done/c")
	if ended[0].Entry.Event != oplog.EventDone || ended[0].Entry.Receipt == nil {
		t.Errorf("the action in flight must finish and journal its receipt, got %s", ended[0].raw)
	}
	res := ended[1].Result
	if !*ended[1].Canceled || res.Done != 1 || res.Failed != 0 || res.Bytes != 100 {
		t.Errorf("execute ended with %s, want canceled:true after one action", ended[1].raw)
	}
	if exists(m.cache) || !exists(m.logs) {
		t.Error("want the first action finished and the second never started")
	}
	for _, e := range m.journal() {
		if e.Seq != 1 {
			t.Errorf("an action after the cancel was journaled: %+v", e)
		}
	}
}

func TestEOFMidExecuteFinishesAndJournalsTheCurrentAction(t *testing.T) {
	m := newMachine(t)
	gate := newGatedMover(m.mover())
	srv := m.server()
	srv.NewExecutor = m.executors(gate)
	stopping := make(chan struct{})
	srv.stopping = func() { close(stopping) }
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expect("journal/x")
	gate.waitEntered(t, m.cache)

	_ = p.stdin.Close()
	waitFor(t, stopping, "shutdown to tell the execute to stop")
	select {
	case <-p.exited:
		t.Fatal("the engine exited with an action in flight")
	default:
	}
	close(gate.release)

	rest, err := p.closeAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].name() != "journal/x" || rest[1].name() != "done/x" {
		t.Fatalf("events after EOF = %v", rest)
	}
	if !*rest[1].Canceled || rest[1].Result.Done != 1 {
		t.Errorf("execute ended with %s, want one action done and canceled:true", rest[1].raw)
	}

	entries := m.journal()
	if len(entries) != 2 || entries[0].Event != oplog.EventStart || entries[1].Event != oplog.EventDone || entries[1].Seq != 1 {
		t.Fatalf("oplog = %+v, want the current action's start and done only", entries)
	}
	if entries[1].Receipt == nil {
		t.Fatal("the finished action lost its receipt: undo could not restore it")
	}
	if exists(m.cache) || !exists(m.logs) {
		t.Error("want the first action finished and the second never started")
	}
}

func TestEOFCancelsAScan(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	canceled := make(chan struct{})
	srv.Scan = func(ctx context.Context, _ []engine.Rule, _ func(int, engine.Finding, time.Duration)) {
		<-ctx.Done()
		close(canceled)
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"scan","id":"s"}`)
	p.expect("start/s")

	rest, err := p.closeAndWait()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, canceled, "the scan to see its context canceled")
	if len(rest) != 1 || rest[0].name() != "done/s" || !*rest[0].Canceled {
		t.Fatalf("events after EOF = %v, want the scan's done with canceled:true", rest)
	}
}

func TestServeStopsWhenItsContextEnds(t *testing.T) {
	m := newMachine(t)
	gate := newGatedMover(m.mover())
	srv := m.server()
	srv.NewExecutor = m.executors(gate)
	stopping := make(chan struct{})
	srv.stopping = func() { close(stopping) }
	ctx, cancel := context.WithCancel(context.Background())
	p := startWith(t, ctx, srv, true)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)
	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expect("journal/x")
	gate.waitEntered(t, m.cache)

	// A termination signal ends ctx with stdin still open. The move is
	// released only once the execute has been told to stop.
	cancel()
	waitFor(t, stopping, "shutdown to tell the execute to stop")
	close(gate.release)
	waitFor(t, p.exited, "Serve to return once its context ended")
	if entries := m.journal(); len(entries) != 2 || entries[1].Event != oplog.EventDone {
		t.Fatalf("oplog = %+v, want the current action finished and journaled", entries)
	}
	if !exists(m.logs) {
		t.Error("an action started after the engine was told to stop")
	}
}

func TestUndrainedReaderBlocksNeitherScanNorCancel(t *testing.T) {
	const rules = 3000
	m := newMachine(t)
	srv := m.server()
	srv.Catalog = make([]engine.Rule, rules)
	emitted := make(chan struct{})
	canceled := make(chan struct{})
	srv.Scan = func(ctx context.Context, catalog []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for i := range catalog {
			emit(i, engine.Finding{Rule: engine.Rule{ID: "fixture-rule"}, Items: []engine.Item{{Path: "/fixture/item", Bytes: int64(i)}}}, 0)
		}
		close(emitted)
		<-ctx.Done()
		close(canceled)
	}
	p := startUndrained(t, srv)

	p.send(`{"type":"scan","id":"s"}`)
	waitFor(t, emitted, "the scan to emit every finding with nobody reading")
	p.send(`{"type":"cancel","id":"c","target":"s"}`)
	waitFor(t, canceled, "the cancel to reach the scan with nobody reading")

	// Nothing was dropped or reordered while the reader was away.
	p.drain()
	p.expect("hello", "start/s")
	for i := range rules {
		if e := p.next(); e.name() != "finding/s" || e.Index != i {
			t.Fatalf("event %d of the backlog = %s", i, e.raw)
		}
	}
	p.expect("done/s", "done/c")
}

func TestServeExitsWhenNobodyReads(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.exitGrace = 50 * time.Millisecond
	p := startUndrained(t, srv)
	p.send(`{"type":"scan","id":"s"}`)
	_ = p.stdin.Close()
	waitFor(t, p.exited, "Serve to give up on a reader that never drains")
}

func TestOverLongLine(t *testing.T) {
	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")

	p.send(`{"type":"scan","id":"big","pad":"` + strings.Repeat("x", MaxLine) + `"}`)
	if e := p.expectError("", CodeLineTooLong); e.Re != "" {
		t.Fatalf("an unparsed line cannot name a request, got %s", e.raw)
	}
	// The oversized line started no scan and the stream is still in
	// step: the next request is answered.
	p.scanned("s", 2)

	edge := `{"type":"cancel","id":"edge","target":"none"`
	edge += strings.Repeat(" ", MaxLine-len(edge)-1) + "}"
	if len(edge) != MaxLine {
		t.Fatalf("test line is %d bytes", len(edge))
	}
	p.send(edge)
	p.expect("done/edge")
}

func TestMalformedRequests(t *testing.T) {
	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")
	tests := []struct {
		line     string
		re, code string
	}{
		{`this is not json`, "", CodeBadRequest},
		{`{"type":"scan"}`, "", CodeBadRequest},
		{`{"type":"scan","id":7}`, "", CodeBadRequest},
		{`{"type":"plan","id":"q","scan_id":"x","select":"go-build-cache"}`, "q", CodeBadRequest},
		{`{"type":"execute","id":"q"}`, "q", CodeBadRequest},
		{`{"type":"frobnicate","id":"q"}`, "q", CodeUnknownRequest},
		{`{"type":"plan","id":"q"}`, "q", CodeBadRequest},
		{`{"type":"cancel","id":"q"}`, "q", CodeBadRequest},
		{`{"type":"execute","id":"q","plan_id":"never-issued"}`, "q", CodeUnknownPlan},
	}
	for _, tt := range tests {
		p.send(tt.line)
		p.expectError(tt.re, tt.code)
	}
	// A blank line is not a request, and a cancel of something that is
	// not in flight has nothing left to do.
	p.send("")
	p.send(`{"type":"cancel","id":"c","target":"gone"}`)
	p.expect("done/c")
}

func TestFindingWithAnUnencodableTimeArrivesAsUnknown(t *testing.T) {
	m := newMachine(t)
	findings := m.findings()
	findings[0].Items[0].LastUsed = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	srv := m.server()
	srv.Scan = scanOf(findings)
	var accounted []engine.Finding
	srv.Account = func(f []engine.Finding) engine.Ledger {
		accounted = f
		return engine.Ledger{}
	}
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"scan","id":"s"}`)
	scan := p.expect("start/s", "finding/s", "finding/s", "summary/s", "done/s")
	f := scan[1].Finding
	if f.Err != "" || len(f.Items) != 1 || f.Items[0].Bytes != 100 || !f.Items[0].LastUsed.IsZero() {
		t.Fatalf("finding = %s, want the item with last_used unknown", scan[1].raw)
	}
	// The engine holds what the peer saw, and plans from it.
	if !accounted[0].Items[0].LastUsed.IsZero() {
		t.Fatalf("the engine kept last_used %s the peer never saw", accounted[0].Items[0].LastUsed)
	}
	if got := actionRules(p.planned("p", scan[0].ScanID, "").Plan); got != "fixture-cache" {
		t.Fatalf("plan = %q", got)
	}
}

// blockedMover never returns a move of block, as a Finder move or
// Lstat stuck on a folder without Full Disk Access would.
type blockedMover struct {
	inner   executor.Mover
	block   string
	release chan struct{}
}

func (b blockedMover) Move(ctx context.Context, path string) (trash.Receipt, error) {
	if path == b.block {
		<-b.release
		return trash.Receipt{}, errors.New("released")
	}
	return b.inner.Move(ctx, path)
}

func TestABlockedTrashMoveFailsAfterItsLimit(t *testing.T) {
	m := newMachine(t)
	stuck := blockedMover{inner: m.mover(), block: m.cache, release: make(chan struct{})}
	t.Cleanup(func() { close(stuck.release) })
	srv := m.server()
	srv.NewExecutor = m.executors(stuck)
	srv.moveLimit = 50 * time.Millisecond
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	run := p.expect("journal/x", "journal/x", "journal/x", "journal/x", "done/x")
	if e := run[1].Entry; e.Event != oplog.EventFail || !strings.Contains(e.Error, "did not finish within 50ms") {
		t.Fatalf("the blocked move journaled %s", run[1].raw)
	}
	if res := run[4].Result; res.Failed != 1 || res.Done != 1 || exists(m.logs) {
		t.Fatalf("result = %s, want the blocked move failed and the next one run", run[4].raw)
	}
}

// failingJournal persists its first succeed entries, then fails.
type failingJournal struct {
	succeed int
	entries []oplog.Entry
}

func (j *failingJournal) Append(e oplog.Entry) error {
	if len(j.entries) >= j.succeed {
		return errors.New("disk full")
	}
	j.entries = append(j.entries, e)
	return nil
}

func TestJournalEventOnlyAfterTheEntryIsPersisted(t *testing.T) {
	m := newMachine(t)
	journal := &failingJournal{succeed: 2}
	srv := m.server()
	srv.NewExecutor = func(string) (*executor.Executor, func(), error) {
		return &executor.Executor{Trash: m.mover(), Log: journal,
			RunNative: func(_ context.Context, argv []string) error {
				t.Errorf("a trash-only plan ran a steward command: %v", argv)
				return errors.New("refused by the test")
			}}, func() {}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)
	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)

	// The first action's two lines were persisted and announced; the
	// second action's start was not persisted, so it is neither
	// announced nor run.
	p.expect("journal/x", "journal/x")
	e := p.expectError("x", CodeExecuteFailed)
	if !strings.Contains(e.Message, "disk full") {
		t.Fatalf("error = %s, want the journal's failure", e.raw)
	}
	if !exists(m.logs) || len(journal.entries) != 2 {
		t.Fatalf("an action ran without its journal entry (journal %+v)", journal.entries)
	}
	// What ran before the failure travels with it.
	var failed struct {
		Result *executor.Result `json:"result"`
	}
	if err := json.Unmarshal([]byte(e.raw), &failed); err != nil || failed.Result == nil {
		t.Fatalf("error = %s, want the partial result", e.raw)
	}
	if r := failed.Result; r.RunID != planned.PlanID || r.Done != 1 || r.Bytes != 100 || r.TrashBytes != 100 {
		t.Fatalf("partial result = %+v, want the first move", r)
	}
}

func TestExecuteReportsAnExecutorThatCannotBeBuilt(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	var broken atomic.Bool
	broken.Store(true)
	working := srv.NewExecutor
	srv.NewExecutor = func(runID string) (*executor.Executor, func(), error) {
		if broken.Load() {
			return nil, nil, os.ErrPermission
		}
		return working(runID)
	}
	p := start(t, srv)
	p.expect("hello")
	scanID := p.scanned("s", 2)
	first := p.planned("p1", scanID, `["fixture-cache"]`)
	second := p.planned("p2", scanID, `["fixture-cache"]`)
	p.send(`{"type":"execute","id":"x1","plan_id":"` + first.PlanID + `"}`)
	if e := p.expectError("x1", CodeExecuteFailed); strings.Contains(e.raw, `"result"`) {
		t.Fatalf("nothing ran, so no result: %s", e.raw)
	}

	// Nothing ran, so nothing was spent but the plan id: the scan's
	// other plan still executes.
	broken.Store(false)
	p.send(`{"type":"execute","id":"x2","plan_id":"` + second.PlanID + `"}`)
	if done := p.expect("journal/x2", "journal/x2", "done/x2")[2]; done.Result.Done != 1 || exists(m.cache) {
		t.Fatalf("the scan's other plan did not run: %s", done.raw)
	}
}

func TestPlanIDSortsByTimeAndCarries128Bits(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	a, b := newPlanID(now), newPlanID(now)
	if a == b {
		t.Fatal("two plan ids minted in the same second are equal")
	}
	stamp := "20261004-120000-"
	if !strings.HasPrefix(a, stamp) {
		t.Fatalf("plan id %q must sort by time like other run ids", a)
	}
	if got := len(a) - len(stamp); got != 32 {
		t.Fatalf("plan id carries %d hex digits of entropy, want 32 (128 bits)", got)
	}
}

// A Trash rule's pre_action hook runs before the move, and a hook that
// refuses journals a fail and moves nothing. The move's time limit
// starts after the hook: the hook is not inside it.
func TestTrashPreActionRunsBeforeTheMoveAndCanRefuse(t *testing.T) {
	m := newMachine(t)
	rules := m.catalog()
	findings := m.findings()
	for i := range rules {
		rules[i].PreAction = engine.PreActionAgentScratchRecheck
		findings[i].Rule = rules[i]
	}
	var order []string
	mover := m.mover()
	srv := m.server()
	srv.Catalog = rules
	srv.Scan = scanOf(findings)
	// The hook outlasts the move limit: were it inside the limit, the
	// first action would time out.
	srv.moveLimit = 50 * time.Millisecond
	srv.NewExecutor = func(string) (*executor.Executor, func(), error) {
		log, err := oplog.Open(m.logPath)
		if err != nil {
			return nil, nil, err
		}
		ex := &executor.Executor{
			Log: log,
			Trash: moverFunc(func(ctx context.Context, path string) (trash.Receipt, error) {
				order = append(order, "move:"+filepath.Base(path))
				return mover.Move(ctx, path)
			}),
			PreActions: map[string]executor.PreAction{
				engine.PreActionAgentScratchRecheck: func(_ context.Context, a engine.Action) (*trash.Receipt, error) {
					order = append(order, "hook:"+filepath.Base(a.Path))
					if a.Path == m.logs {
						return nil, errors.New("the session is live again")
					}
					time.Sleep(120 * time.Millisecond)
					return nil, nil
				},
			},
			RunNative: func(_ context.Context, argv []string) error {
				t.Errorf("a trash-only plan ran a steward command: %v", argv)
				return errors.New("refused by the test")
			},
		}
		return ex, func() { _ = log.Close() }, nil
	}
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 2), bothRules)
	for _, a := range planned.Plan.Actions {
		if a.Kind != engine.ActionTrash || a.PreAction != engine.PreActionAgentScratchRecheck {
			t.Fatalf("action = %+v, want a Trash move carrying the hook", a)
		}
	}

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	run := p.expect("journal/x", "journal/x", "journal/x", "journal/x", "done/x")
	if got := strings.Join(order, ","); got != "hook:cache,move:cache,hook:logs" {
		t.Fatalf("order = %s, want each hook before its move and no move after a refusal", got)
	}
	if e := run[1].Entry; e.Event != oplog.EventDone || e.Receipt == nil || e.Receipt.Original != m.cache {
		t.Errorf("the accepted move journaled %s", run[1].raw)
	}
	if e := run[3].Entry; e.Event != oplog.EventFail || !strings.Contains(e.Error, "the session is live again") || e.Receipt != nil {
		t.Errorf("the refused move journaled %s, want a fail with the hook's reason", run[3].raw)
	}
	if res := run[4].Result; res.Done != 1 || res.Failed != 1 {
		t.Errorf("result = %s", run[4].raw)
	}
	if exists(m.cache) || !exists(m.logs) {
		t.Error("want the accepted item moved and the refused one left in place")
	}
}

type moverFunc func(ctx context.Context, path string) (trash.Receipt, error)

func (f moverFunc) Move(ctx context.Context, path string) (trash.Receipt, error) {
	return f(ctx, path)
}
