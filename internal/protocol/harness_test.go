package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

const waitLimit = 10 * time.Second

// event is any line the engine wrote, decoded loosely.
type event struct {
	Event     string           `json:"event"`
	Re        string           `json:"re"`
	Code      string           `json:"code"`
	Message   string           `json:"message"`
	Protocol  int              `json:"protocol"`
	Version   string           `json:"version"`
	FDA       FDA              `json:"fda"`
	ScanID    string           `json:"scan_id"`
	Rules     int              `json:"rules"`
	Index     int              `json:"index"`
	TookMS    int64            `json:"took_ms"`
	Finding   *engine.Finding  `json:"finding"`
	Totals    *engine.Totals   `json:"totals"`
	Exclusive map[string]int64 `json:"exclusive"`
	PlanID    string           `json:"plan_id"`
	Plan      *engine.Plan     `json:"plan"`
	Entry     *oplog.Entry     `json:"entry"`
	Result    *executor.Result `json:"result"`
	Canceled  *bool            `json:"canceled"`
	ElapsedMS *int64           `json:"elapsed_ms"`

	raw string
}

// name is the event as sequence assertions spell it: "finding/s1".
func (e event) name() string {
	if e.Re == "" {
		return e.Event
	}
	return e.Event + "/" + e.Re
}

// peer is the shell's end of the pipes.
type peer struct {
	t      *testing.T
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	events chan event
	// exited closes when Serve returns; serveErr is what it returned.
	exited   chan struct{}
	serveErr error
	draining bool
}

// start serves srv over in-process pipes and reads its events.
func start(t *testing.T, srv *Server) *peer {
	t.Helper()
	return startWith(t, context.Background(), srv, true)
}

// startUndrained serves srv with nobody reading its output: every
// write blocks until drain is called.
func startUndrained(t *testing.T, srv *Server) *peer {
	t.Helper()
	return startWith(t, context.Background(), srv, false)
}

func startWith(t *testing.T, ctx context.Context, srv *Server, drain bool) *peer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &peer{t: t, stdin: inW, stdout: outR, events: make(chan event, 4096), exited: make(chan struct{})}
	go func() {
		p.serveErr = srv.Serve(ctx, inR, outW)
		_ = outW.Close()
		close(p.exited)
	}()
	// No goroutine of this engine outlives its test.
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-p.exited:
		case <-time.After(waitLimit):
			t.Error("Serve did not return after its input ended")
		}
		_ = outR.Close()
		if p.draining {
			for range p.events {
			}
		}
	})
	if drain {
		p.drain()
	}
	return p
}

func (p *peer) drain() {
	p.draining = true
	go func() {
		defer close(p.events)
		sc := bufio.NewScanner(p.stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			var e event
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Event == "" {
				p.t.Errorf("the engine wrote a line that is not an event: %q (%v)", sc.Text(), err)
				continue
			}
			e.raw = sc.Text()
			p.events <- e
		}
	}()
}

// send writes one request line.
func (p *peer) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
		p.t.Fatalf("send %s: %v", line, err)
	}
}

func (p *peer) next() event {
	p.t.Helper()
	select {
	case e, ok := <-p.events:
		if !ok {
			p.t.Fatal("the engine closed its output while an event was expected")
		}
		return e
	case <-time.After(waitLimit):
		p.t.Fatal("timed out waiting for an event")
	}
	return event{}
}

// expect reads exactly these events, in order.
func (p *peer) expect(names ...string) []event {
	p.t.Helper()
	got := make([]event, 0, len(names))
	for i, want := range names {
		e := p.next()
		if e.name() != want {
			p.t.Fatalf("event %d = %s, want %s\nline: %s", i, e.name(), want, e.raw)
		}
		got = append(got, e)
	}
	return got
}

// expectError reads one error event answering re with the given code.
func (p *peer) expectError(re, code string) event {
	p.t.Helper()
	e := p.next()
	want := "error"
	if re != "" {
		want += "/" + re
	}
	if e.name() != want || e.Code != code {
		p.t.Fatalf("got %s, want %s with code %s", e.raw, want, code)
	}
	return e
}

// closeAndWait ends the peer's input and returns what Serve returned
// plus every event still unread.
func (p *peer) closeAndWait() (rest []event, err error) {
	p.t.Helper()
	_ = p.stdin.Close()
	waitFor(p.t, p.exited, "Serve to return after its input ended")
	for e := range p.events {
		rest = append(rest, e)
	}
	return rest, p.serveErr
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// machine is a fake home for one test: two trash-only rules with one
// directory each, a Trash the fake Finder renames into, and a journal
// under a temp state dir. Nothing in it can reach the real home.
type machine struct {
	t        *testing.T
	home     string
	trashDir string
	logPath  string
	cache    string // the safe rule's item
	logs     string // the caution rule's item
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	m := &machine{
		t:        t,
		home:     home,
		trashDir: filepath.Join(home, ".Trash"),
		logPath:  logPath,
		cache:    filepath.Join(home, "proj", "cache"),
		logs:     filepath.Join(home, "proj", "logs"),
	}
	for _, f := range []string{filepath.Join(m.cache, "a.bin"), filepath.Join(m.logs, "b.log")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(m.trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machine) host() engine.Host {
	return engine.Host{OS: "darwin", Version: "15.5", Home: m.home}
}

func (m *machine) catalog() []engine.Rule {
	return []engine.Rule{
		{ID: "fixture-cache", Title: "Fixture cache", Category: "fixture", Risk: engine.RiskSafe},
		{ID: "fixture-logs", Title: "Fixture logs", Category: "fixture", Risk: engine.RiskCaution},
	}
}

func (m *machine) findings() []engine.Finding {
	rules := m.catalog()
	return []engine.Finding{
		{Rule: rules[0], Items: []engine.Item{{Path: m.cache, Bytes: 100}}},
		{Rule: rules[1], Items: []engine.Item{{Path: m.logs, Bytes: 40}}},
	}
}

// scanOf is a scan seam that reports fixed findings, one per rule.
func scanOf(findings []engine.Finding) ScanFunc {
	return func(_ context.Context, _ []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for i, f := range findings {
			emit(i, f, time.Duration(i+1)*time.Millisecond)
		}
	}
}

// executors builds each run's executor the way cmd does, with a
// Finder that renames into the fixture Trash and a steward-command
// runner that fails the test: these catalogs are trash-only.
func (m *machine) executors(mover executor.Mover) func(string) (*executor.Executor, func(), error) {
	return func(runID string) (*executor.Executor, func(), error) {
		log, err := oplog.Open(m.logPath)
		if err != nil {
			return nil, nil, err
		}
		if mover == nil {
			mover = m.mover()
		}
		ex := &executor.Executor{
			Trash: mover,
			Log:   log,
			RunNative: func(_ context.Context, argv []string) error {
				m.t.Errorf("a trash-only catalog ran a steward command: %v", argv)
				return errors.New("refused by the test")
			},
		}
		return ex, func() { _ = log.Close() }, nil
	}
}

func (m *machine) mover() *trash.Mover {
	return &trash.Mover{
		Home: m.home,
		RunFinder: func(_ context.Context, path string) (string, error) {
			to := filepath.Join(m.trashDir, filepath.Base(path))
			return to, os.Rename(path, to)
		},
	}
}

// server is a Server over the machine with a scan that reports its
// findings.
func (m *machine) server() *Server {
	return &Server{
		Host:        m.host(),
		Catalog:     m.catalog(),
		Scan:        scanOf(m.findings()),
		NewExecutor: m.executors(nil),
	}
}

func (m *machine) journal() []oplog.Entry {
	m.t.Helper()
	entries, err := oplog.Read(m.logPath)
	if err != nil {
		m.t.Fatal(err)
	}
	return entries
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// scanned runs one scan to completion and returns its scan id.
func (p *peer) scanned(re string, rules int) string {
	p.t.Helper()
	p.send(`{"type":"scan","id":"` + re + `"}`)
	names := []string{"start/" + re}
	for range rules {
		names = append(names, "finding/"+re)
	}
	names = append(names, "done/"+re)
	return p.expect(names...)[0].ScanID
}

// planned requests a plan and returns its plan event. selectJSON is
// the raw value of `select`, or "" to leave the field out.
func (p *peer) planned(re, scanID, selectJSON string) event {
	p.t.Helper()
	req := `{"type":"plan","id":"` + re + `","scan_id":"` + scanID + `"`
	if selectJSON != "" {
		req += `,"select":` + selectJSON
	}
	p.send(req + "}")
	return p.expect("plan/"+re, "done/"+re)[0]
}

func actionRules(plan *engine.Plan) string {
	ids := make([]string, len(plan.Actions))
	for i, a := range plan.Actions {
		ids[i] = a.RuleID
	}
	return strings.Join(ids, ",")
}
