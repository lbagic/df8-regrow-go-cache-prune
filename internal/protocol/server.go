package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

const (
	defaultPlanTTL = 10 * time.Minute
	// defaultMoveLimit bounds one Trash move. Without Full Disk Access
	// a syscall on another app's folder can block forever, and no
	// cancel reaches a goroutine stuck in one.
	defaultMoveLimit = time.Minute
	// defaultExitGrace bounds what shutdown waits for that cannot lose
	// data: a canceled scan winding down, and a reader that stopped
	// draining events. An execute or a tick is always waited for in
	// full.
	defaultExitGrace = 5 * time.Second
)

// ScanFunc is the scan seam, in the shape of scanner.ScanStream: emit
// is called once per rule as it completes, never concurrently, with
// the rule's catalog index and its scan time. ScanFunc returns after
// the last emit, and promptly once ctx is canceled.
type ScanFunc func(ctx context.Context, rules []engine.Rule, emit func(index int, f engine.Finding, took time.Duration))

// Server speaks the engine protocol for one peer.
type Server struct {
	// Version is the binary's version, announced in hello.
	Version string
	Host    engine.Host
	Catalog []engine.Rule
	Scan    ScanFunc
	// Account computes a finished scan's ledger, in the shape of
	// engine.Account. Nil omits the summary event.
	Account func(findings []engine.Finding) engine.Ledger
	// Headroom samples free space for a scan's first event, in the
	// shape of headroom.Take. Nil omits the event.
	Headroom func(ctx context.Context) (headroom.Sample, error)
	// Tick is one pass of the watch loop, in the shape of
	// autopilot.Autopilot.Tick, autotrim gate included. Nil refuses
	// tick requests.
	Tick func(ctx context.Context, autotrim bool) (headroom.Tick, error)
	// NewExecutor builds the executor of one run: journal open, mover
	// and pre-actions pointed at the run's staging directory. release
	// is called once the run has ended. The server sets the run id,
	// the stop hook, the journal tee, and RunNative when it is nil.
	NewExecutor func(runID string) (ex *executor.Executor, release func(), err error)
	// ProbeFDA answers hello's fda field; nil means ProbeFDA(Host).
	ProbeFDA func() FDA
	// Now is injectable for plan expiry in tests.
	Now func() time.Time
	// PlanTTL is how long a plan id stays executable; zero means 10
	// minutes.
	PlanTTL time.Duration
	// CleanFlags are the flags `regrow clean` needs in Terminal to see
	// this engine's catalog (--rules-dir, --beta-rules); sudo skips
	// name them.
	CleanFlags []string

	exitGrace time.Duration
	moveLimit time.Duration
	// stopping, when set, runs in shutdown once the operation in
	// flight has been told to stop.
	stopping func()
}

type scanStatus int

const (
	scanRunning scanStatus = iota
	scanDone
	scanCanceled
	// scanSpent: an execute ran against the scan's findings, so they
	// no longer describe the disk.
	scanSpent
)

type scanState struct {
	id       string
	seq      int
	status   scanStatus
	findings []engine.Finding
}

type heldPlan struct {
	plan    engine.Plan
	scan    *scanState
	created time.Time
	// expired marks a plan pruned for age; its body is dropped, its id
	// kept so an execute of it still hears plan_expired.
	expired bool
}

// operation is the one scan, execute or tick in flight.
type operation struct {
	kind string
	re   string
	// stop asks the operation to end early: a scan's context is
	// canceled, an execute stops before its next action.
	stop func()
	// cancels are the cancel requests answered once it has ended.
	cancels  []string
	finished chan struct{}
}

type session struct {
	srv   *Server
	out   *outbox
	nonce string

	mu       sync.Mutex
	scanSeq  int
	scan     *scanState
	plans    map[string]heldPlan
	inflight *operation
}

// Serve runs the protocol until in ends or ctx is canceled, then shuts
// down: a scan in flight is canceled, an execute finishes and journals
// its current action and stops there, a tick runs to its end. It returns in's read error, nil
// at EOF.
func (srv *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s := srv.newSession(out)

	probe := srv.ProbeFDA
	if probe == nil {
		probe = func() FDA { return ProbeFDA(srv.Host) }
	}
	s.emit(helloEvent{head{Event: "hello"}, Version, srv.Version, probe()})

	lines := make(chan requestLine)
	quit := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		readErr <- readLines(in, MaxLine, func(l requestLine) bool {
			select {
			case lines <- l:
				return true
			case <-quit:
				return false
			}
		})
		close(lines)
	}()

	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case l, ok := <-lines:
			if !ok {
				err = <-readErr
				break loop
			}
			s.handle(l)
		}
	}
	close(quit)

	grace := srv.exitGrace
	if grace == 0 {
		grace = defaultExitGrace
	}
	s.shutdown(grace)
	s.out.close(grace)
	return err
}

// ScanOnce writes the events of one scan to out, as a scan request
// would, without hello and without re: `regrow scan --json`. It
// returns once every event is written, or with the write error that
// stopped them: a stream cut short has no done line, and its reader
// must hear so.
func (srv *Server) ScanOnce(ctx context.Context, out io.Writer) error {
	s := srv.newSession(out)
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	op, sc := s.beginScan(request{Type: reqScan}, cancel)
	s.mu.Unlock()
	s.runScan(ctx, cancel, op, sc)
	s.out.close(0)
	return s.out.writeErr()
}

func (srv *Server) newSession(out io.Writer) *session {
	return &session{srv: srv, out: newOutbox(out), nonce: newNonce(), plans: map[string]heldPlan{}}
}

func (s *session) emit(event any) bool { return s.out.send(event) }

// now is wall-clock time. Round(0) drops the monotonic reading, which
// on macOS stops while the machine sleeps: a plan held overnight must
// expire.
func (s *session) now() time.Time {
	if s.srv.Now != nil {
		return s.srv.Now().Round(0)
	}
	return time.Now().Round(0)
}

func (s *session) handle(l requestLine) {
	if l.tooLong {
		s.emit(failure("", CodeLineTooLong, fmt.Sprintf("request line exceeds %d bytes", MaxLine)))
		return
	}
	if len(bytes.TrimSpace(l.data)) == 0 {
		return
	}
	var req request
	if err := json.Unmarshal(l.data, &req); err != nil {
		// A well-formed line with a mistyped field still names its
		// request, so the peer can tell which one failed.
		var named struct {
			ID any `json:"id"`
		}
		_ = json.Unmarshal(l.data, &named)
		re, _ := named.ID.(string)
		s.emit(failure(re, CodeBadRequest, "request is not a JSON object of the documented shape: "+err.Error()))
		return
	}
	if req.ID == "" {
		s.emit(failure("", CodeBadRequest, "request needs a non-empty string id"))
		return
	}
	switch req.Type {
	case reqScan:
		s.startScan(req)
	case reqPlan:
		s.plan(req)
	case reqExecute:
		s.startExecute(req)
	case reqCancel:
		s.cancel(req)
	case reqTick:
		s.startTick(req)
	default:
		s.emit(failure(req.ID, CodeUnknownRequest, fmt.Sprintf("unknown request type %q", req.Type)))
	}
}

// busy refuses req while a scan, execute or tick is in flight. The caller
// holds s.mu.
func (s *session) busy(req request) bool {
	cur := s.inflight
	if cur == nil {
		return false
	}
	article := "a"
	if cur.kind == reqExecute {
		article = "an"
	}
	s.emit(failure(req.ID, CodeBusy, fmt.Sprintf("%s %s is in flight (request %q)", article, cur.kind, cur.re)))
	return true
}

// begin claims the in-flight slot. The caller holds s.mu and has
// checked busy.
func (s *session) begin(req request, stop func()) *operation {
	op := &operation{kind: req.Type, re: req.ID, stop: stop, finished: make(chan struct{})}
	s.inflight = op
	return op
}

// end releases the slot and emits the operation's terminal event,
// then answers the cancels that waited for it. settle, if any, runs
// first. All under s.mu, so a peer that has read the terminal event is
// never told busy and never sees the state from before it.
func (s *session) end(op *operation, terminal any, settle func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if settle != nil {
		settle()
	}
	s.inflight = nil
	s.emit(terminal)
	for _, re := range op.cancels {
		s.emit(done(re))
	}
	close(op.finished)
}

func (s *session) startScan(req request) {
	s.mu.Lock()
	if s.busy(req) {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	op, sc := s.beginScan(req, cancel)
	s.mu.Unlock()

	go s.runScan(ctx, cancel, op, sc)
}

// beginScan makes a new scan the current one. The caller holds s.mu
// and has checked busy.
func (s *session) beginScan(req request, cancel context.CancelFunc) (*operation, *scanState) {
	op := s.begin(req, cancel)
	s.scanSeq++
	sc := &scanState{id: fmt.Sprintf("%s-%d", s.nonce, s.scanSeq), seq: s.scanSeq, status: scanRunning}
	// The previous scan's findings and every plan built from them end
	// here: a plan must never run against findings a newer scan
	// replaced.
	s.scan = sc
	clear(s.plans)
	return op, sc
}

func (s *session) runScan(ctx context.Context, cancel context.CancelFunc, op *operation, sc *scanState) {
	defer cancel()
	began := time.Now()
	if s.srv.Headroom != nil {
		if sample, err := s.srv.Headroom(ctx); err == nil {
			s.emit(inlineEvent{head{"headroom", op.re}, sample})
		}
	}
	rules := s.srv.Catalog
	s.emit(startEvent{head{"start", op.re}, sc.id, len(rules)})

	findings := make([]engine.Finding, len(rules))
	s.srv.Scan(ctx, rules, func(i int, f engine.Finding, took time.Duration) {
		if ctx.Err() != nil {
			return
		}
		f = encodable(f)
		f.FillItemKeys(s.srv.Host.Home)
		ev := findingEvent{head{"finding", op.re}, sc.id, i, took.Milliseconds(), f}
		if !s.emit(ev) {
			// The row still has to arrive, or the peer waits on this
			// rule forever; the engine keeps what the peer saw.
			f = engine.Finding{Rule: f.Rule, Err: "finding could not be encoded"}
			ev.Finding = f
			s.emit(ev)
		}
		findings[i] = f
	})
	canceled := ctx.Err() != nil
	if !canceled && s.srv.Account != nil {
		ledger := s.srv.Account(findings)
		s.emit(summaryEvent{head{"summary", op.re}, sc.id, ledger.Totals, ledger.Exclusive})
	}

	s.end(op, scanDoneEvent{head{"done", op.re}, time.Since(began).Milliseconds(), canceled}, func() {
		if canceled {
			sc.status = scanCanceled
		} else {
			sc.status, sc.findings = scanDone, findings
		}
	})
}

// scanFor resolves a plan request's scan id to a finished scan, or to
// the error that says why it cannot be planned. The caller holds s.mu.
func (s *session) scanFor(id string) (*scanState, string, string) {
	if id == "" {
		return nil, CodeBadRequest, "plan needs a scan_id"
	}
	cur := s.scan
	if cur != nil && id == cur.id {
		switch cur.status {
		case scanRunning:
			return nil, CodeScanRunning, fmt.Sprintf("scan %s is still running", id)
		case scanCanceled:
			return nil, CodeScanCanceled, fmt.Sprintf("scan %s was canceled: scan again", id)
		case scanSpent:
			return nil, CodeScanSpent, fmt.Sprintf("an execute already ran against scan %s: scan again", id)
		}
		return cur, "", ""
	}
	if rest, ok := strings.CutPrefix(id, s.nonce+"-"); ok && cur != nil {
		if seq, err := strconv.Atoi(rest); err == nil && seq >= 1 && seq < cur.seq {
			return nil, CodeScanSuperseded, fmt.Sprintf("scan %s was superseded by scan %s", id, cur.id)
		}
	}
	return nil, CodeUnknownScan, fmt.Sprintf("unknown scan %q", id)
}

func (s *session) plan(req request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, code, message := s.scanFor(req.ScanID)
	if code != "" {
		s.emit(failure(req.ID, code, message))
		return
	}
	var selected map[string]bool
	if req.Select == nil {
		selected = engine.DefaultSelection(sc.findings)
	} else {
		selected = make(map[string]bool, len(*req.Select))
		for _, atom := range *req.Select {
			selected[atom] = true
		}
	}
	plan := engine.BuildPlanWith(s.srv.Host, sc.findings, selected, engine.PlanOptions{NoSudo: true, CleanFlags: s.srv.CleanFlags})
	if len(plan.Unmatched) > 0 {
		// As `regrow clean` does: a typo must never run less, or for a
		// rule atom more, than the peer meant.
		ev := failure(req.ID, CodeUnmatched, "selectors matched nothing in this scan: "+strings.Join(plan.Unmatched, ", "))
		ev.Unmatched = plan.Unmatched
		s.emit(ev)
		return
	}
	if plan.Actions == nil {
		plan.Actions = []engine.Action{}
	}

	now := s.now()
	for id, held := range s.plans {
		if !held.expired && s.expired(held, now) {
			s.plans[id] = heldPlan{created: held.created, expired: true}
		}
	}
	id := newPlanID(now)
	s.plans[id] = heldPlan{plan: plan, scan: sc, created: now}
	s.emit(planEvent{head{"plan", req.ID}, id, plan, plan.Totals()})
	s.emit(done(req.ID))
}

func (s *session) expired(held heldPlan, now time.Time) bool {
	ttl := s.srv.PlanTTL
	if ttl == 0 {
		ttl = defaultPlanTTL
	}
	return held.expired || now.Sub(held.created) > ttl
}

func (s *session) startExecute(req request) {
	if req.PlanID == "" {
		s.emit(failure(req.ID, CodeBadRequest, "execute needs a plan_id"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy(req) {
		return
	}
	held, ok := s.plans[req.PlanID]
	if !ok {
		s.emit(failure(req.ID, CodeUnknownPlan, fmt.Sprintf("unknown plan %q: never issued, or dropped when an execute or a newer scan started", req.PlanID)))
		return
	}
	delete(s.plans, req.PlanID)
	if s.expired(held, s.now()) {
		s.emit(failure(req.ID, CodePlanExpired, fmt.Sprintf("plan %s expired: plan again", req.PlanID)))
		return
	}
	stopped := new(atomic.Bool)
	op := s.begin(req, func() { stopped.Store(true) })
	go s.runExecute(op, req.PlanID, held, stopped)
}

func (s *session) runExecute(op *operation, runID string, held heldPlan, stopped *atomic.Bool) {
	if s.srv.NewExecutor == nil {
		s.end(op, failure(op.re, CodeExecuteFailed, "this engine cannot execute"), nil)
		return
	}
	ex, release, err := s.srv.NewExecutor(runID)
	if err != nil {
		s.end(op, failure(op.re, CodeExecuteFailed, err.Error()), nil)
		return
	}
	defer release()
	// From here the run changes what the scan measured, and another
	// plan of it could replay against that: a second Empty Trash would
	// destroy this run's Trash moves.
	s.mu.Lock()
	clear(s.plans)
	held.scan.status = scanSpent
	s.mu.Unlock()
	ex.RunID = runID
	ex.Stop = stopped.Load
	if ex.Trash != nil {
		limit := s.srv.moveLimit
		if limit == 0 {
			limit = defaultMoveLimit
		}
		ex.Trash = boundedMover{ex.Trash, limit}
	}
	ex.Log = journalTee{ex.Log, func(e oplog.Entry) { s.emit(journalEvent{head{"journal", op.re}, e}) }}
	if ex.RunNative == nil {
		ex.RunNative = RunNative
	}
	// Never canceled: cancel and shutdown go through Stop, so the
	// action in flight always finishes and is journaled.
	res, err := ex.Execute(context.Background(), held.plan)
	if err != nil {
		ev := failure(op.re, CodeExecuteFailed, err.Error())
		ev.Result = &res
		s.end(op, ev, nil)
		return
	}
	s.end(op, executeDoneEvent{head{"done", op.re}, res, res.Stopped}, nil)
}

func (s *session) cancel(req request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Target == "" {
		s.emit(failure(req.ID, CodeBadRequest, "cancel needs a target request id"))
		return
	}
	op := s.inflight
	if op == nil || op.re != req.Target {
		// Not in flight means already ended, which is what a cancel
		// asks for.
		s.emit(done(req.ID))
		return
	}
	op.stop()
	op.cancels = append(op.cancels, req.ID)
}

// startTick runs one pass of the watch loop. A tick is an operation
// like a scan or an execute, and like an execute it is never cut
// short: its autotrim prune is one action, journaled to its end.
func (s *session) startTick(req request) {
	if s.srv.Tick == nil {
		s.emit(failure(req.ID, CodeTickFailed, "this engine cannot tick"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy(req) {
		return
	}
	op := s.begin(req, func() {})
	go func() {
		tick, err := s.srv.Tick(context.Background(), req.Autotrim)
		unreadable, err := splitHistoryUnreadable(err)
		if !tick.At.IsZero() {
			// A sampled tick is reported even when it also failed: a
			// refused autotrim must not hide the alerts.
			s.emit(inlineEvent{head{"tick", op.re}, tickBody{tick, unreadable}})
		}
		var terminal any = done(op.re)
		switch {
		case errors.Is(err, autopilot.ErrAutotrimLocked):
			terminal = failure(op.re, CodeAutotrimLocked, err.Error())
		case err != nil:
			terminal = failure(op.re, CodeTickFailed, err.Error())
		}
		s.end(op, terminal, nil)
	}()
}

// tickBody is a tick event's fields: the tick's own, and whether the
// headroom history could not be read, which leaves the tick without a
// forecast or alerts.
type tickBody struct {
	headroom.Tick
	HistoryUnreadable bool `json:"history_unreadable,omitempty"`
}

// splitHistoryUnreadable takes autopilot.ErrHistoryUnreadable out of a
// tick's error: an unreadable history is a fact about the tick, not a
// failure of it.
func splitHistoryUnreadable(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	parts := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts = joined.Unwrap()
	}
	unreadable := false
	var rest []error
	for _, e := range parts {
		if errors.Is(e, autopilot.ErrHistoryUnreadable) {
			unreadable = true
			continue
		}
		rest = append(rest, e)
	}
	return unreadable, errors.Join(rest...)
}

func (s *session) shutdown(grace time.Duration) {
	s.mu.Lock()
	op := s.inflight
	if op != nil {
		op.stop()
	}
	s.mu.Unlock()
	if s.srv.stopping != nil {
		s.srv.stopping()
	}
	if op == nil {
		return
	}
	if op.kind != reqScan {
		<-op.finished
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-op.finished:
	case <-timer.C:
	}
}

// boundedMover gives up on a Trash move after limit. A move stuck in a
// syscall is abandoned, not stopped: its goroutine stays blocked, and
// a Finder move already requested may still land in the Trash.
type boundedMover struct {
	inner executor.Mover
	limit time.Duration
}

func (b boundedMover) Move(ctx context.Context, path string) (trash.Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, b.limit)
	defer cancel()
	type outcome struct {
		receipt trash.Receipt
		err     error
	}
	result := make(chan outcome, 1)
	go func() {
		r, err := b.inner.Move(ctx, path)
		result <- outcome{r, err}
	}()
	select {
	case o := <-result:
		return o.receipt, o.err
	case <-ctx.Done():
	}
	select {
	case o := <-result:
		return o.receipt, o.err
	default:
		return trash.Receipt{}, fmt.Errorf("moving %s to the Trash did not finish within %s (a blocked or unreadable folder?); if the abandoned move completes, the item is in the Trash, where Finder's Put Back restores it, or in regrow staging if Finder failed first, and `regrow undo` has no receipt for either", path, b.limit)
	}
}

// encodable clears what JSON cannot carry: a time outside years
// 0–9999, which reads as unknown instead.
func encodable(f engine.Finding) engine.Finding {
	var items []engine.Item
	for i, it := range f.Items {
		if y := it.LastUsed.Year(); y >= 0 && y <= 9999 {
			continue
		}
		if items == nil {
			items = append([]engine.Item(nil), f.Items...)
		}
		items[i].LastUsed = time.Time{}
	}
	if items != nil {
		f.Items = items
	}
	return f
}

// journalTee forwards an entry to the peer once the journal has
// persisted it.
type journalTee struct {
	inner executor.Journal
	after func(oplog.Entry)
}

func (j journalTee) Append(e oplog.Entry) error {
	if err := j.inner.Append(e); err != nil {
		return err
	}
	j.after(e)
	return nil
}

// newPlanID mints a plan id, which is also the run id its execution
// journals under: a sortable timestamp like executor.NewRunID, with
// 128 random bits so the id cannot be guessed.
func newPlanID(now time.Time) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// newNonce makes scan ids unique to this process, so an id a peer
// kept from an earlier engine can never name a scan of this one.
func newNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
