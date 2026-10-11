package protocol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/autopilot"
	"github.com/lbagic/regrow/internal/headroom"
)

var tickAt = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func TestTickReportsTheWatchLoop(t *testing.T) {
	days := 2.5
	tick := headroom.Tick{
		Sample:     headroom.Sample{At: tickAt, Total: 100, Free: 7, Purgeable: 2, SwapUsed: 3},
		DaysToFull: &days,
		Alerts:     []headroom.Alert{{Kind: headroom.AlertFreeBelow, Message: "Free space is below 10.0 GiB"}},
		Pruned:     &headroom.PruneResult{RuleID: "fixture-prune", Run: "r1", Files: 4, Bytes: 9},
	}
	var asked []bool
	m := newMachine(t)
	srv := m.server()
	srv.Tick = func(_ context.Context, autotrim bool) (headroom.Tick, error) {
		asked = append(asked, autotrim)
		return tick, nil
	}
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"tick","id":"t1","autotrim":true}`)
	got := p.expect("tick/t1", "done/t1")[0]
	for _, want := range []string{
		`"event":"tick","re":"t1","at":"2026-10-04T12:00:00Z","total":100,"free":7,"purgeable":2,"swap_used":3`,
		`"days_to_full":2.5`,
		`"alerts":[{`, `Free space is below 10.0 GiB`,
		`"pruned":{"rule_id":"fixture-prune","run":"r1","files":4,"bytes":9`,
	} {
		if !strings.Contains(got.raw, want) {
			t.Errorf("tick = %s, missing %s", got.raw, want)
		}
	}
	p.send(`{"type":"tick","id":"t2"}`)
	p.expect("tick/t2", "done/t2")
	if fmt.Sprint(asked) != "[true false]" {
		t.Fatalf("autotrim passed as %v, want [true false]", asked)
	}
}

func TestTickFailures(t *testing.T) {
	sampled := headroom.Tick{Sample: headroom.Sample{At: tickAt, Free: 7}}
	tests := []struct {
		name     string
		tick     headroom.Tick
		err      error
		wantTick bool
		wantCode string
	}{
		{"a locked autotrim still reports the sample", sampled,
			fmt.Errorf("%w for fixture-prune until `regrow prune fixture --yes` has completed once", autopilot.ErrAutotrimLocked), true, CodeAutotrimLocked},
		{"a history write failure still reports the sample", sampled, errors.New("append headroom.jsonl: disk full"), true, CodeTickFailed},
		{"a failed sample reports only the error", headroom.Tick{}, errors.New("statfs: no such volume"), false, CodeTickFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			srv := m.server()
			srv.Tick = func(context.Context, bool) (headroom.Tick, error) { return tt.tick, tt.err }
			p := start(t, srv)
			p.expect("hello")
			p.send(`{"type":"tick","id":"t","autotrim":true}`)
			if tt.wantTick {
				p.expect("tick/t")
			}
			if e := p.expectError("t", tt.wantCode); !strings.Contains(e.Message, tt.err.Error()) {
				t.Fatalf("error = %s, want %q", e.raw, tt.err)
			}
		})
	}

	m := newMachine(t)
	p := start(t, m.server())
	p.expect("hello")
	p.send(`{"type":"tick","id":"t"}`)
	p.expectError("t", CodeTickFailed)
}

// An unreadable headroom history leaves the tick without a forecast or
// alerts. The tick says so and still succeeds, unless something else
// failed with it.
func TestTickWithAnUnreadableHistory(t *testing.T) {
	unreadable := fmt.Errorf("%w: headroom.jsonl: permission denied", autopilot.ErrHistoryUnreadable)
	pruned := &headroom.PruneResult{RuleID: "fixture-prune", Run: "r1", Files: 1, Bytes: 2}
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"alone, the tick is done", errors.Join(unreadable), ""},
		{"with a failed prune, that failure ends it", errors.Join(nil, unreadable, errors.New("prune fixture-prune failed: exit status 1")), CodeTickFailed},
		{"with a locked autotrim, the lock ends it", errors.Join(unreadable, fmt.Errorf("%w for fixture-prune", autopilot.ErrAutotrimLocked)), CodeAutotrimLocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			srv := m.server()
			srv.Tick = func(context.Context, bool) (headroom.Tick, error) {
				return headroom.Tick{Sample: headroom.Sample{At: tickAt, Free: 7}, Pruned: pruned}, tt.err
			}
			p := start(t, srv)
			p.expect("hello")
			p.send(`{"type":"tick","id":"t","autotrim":true}`)
			got := p.expect("tick/t")[0]
			if !strings.Contains(got.raw, `"history_unreadable":true`) || !strings.Contains(got.raw, `"pruned":{"rule_id":"fixture-prune"`) {
				t.Fatalf("tick = %s, want history_unreadable and the prune", got.raw)
			}
			if tt.wantCode == "" {
				p.expect("done/t")
				return
			}
			if e := p.expectError("t", tt.wantCode); strings.Contains(e.Message, "headroom.jsonl") {
				t.Fatalf("error = %s, the unreadable history is not the failure", e.raw)
			}
		})
	}

	// A readable history leaves the field out.
	m := newMachine(t)
	srv := m.server()
	srv.Tick = func(context.Context, bool) (headroom.Tick, error) {
		return headroom.Tick{Sample: headroom.Sample{At: tickAt}}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"tick","id":"t"}`)
	if got := p.expect("tick/t", "done/t")[0]; strings.Contains(got.raw, "history_unreadable") {
		t.Fatalf("tick = %s", got.raw)
	}
}

// A tick is an operation like a scan or an execute: one at a time, and
// shutdown waits for it, since its autotrim prune is never cut short.
func TestTickIsAnOperation(t *testing.T) {
	m := newMachine(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var ctxErr atomic.Value
	srv := m.server()
	srv.Tick = func(ctx context.Context, _ bool) (headroom.Tick, error) {
		close(entered)
		<-release
		ctxErr.Store(fmt.Sprint(ctx.Err()))
		return headroom.Tick{Sample: headroom.Sample{At: tickAt}}, nil
	}
	stopping := make(chan struct{})
	srv.stopping = func() { close(stopping) }
	// A scan would be given up on after this; a tick must not be.
	srv.exitGrace = 20 * time.Millisecond
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"tick","id":"t1"}`)
	waitFor(t, entered, "the tick to start")
	p.send(`{"type":"scan","id":"s"}`)
	p.expectError("s", CodeBusy)
	p.send(`{"type":"tick","id":"t2"}`)
	p.expectError("t2", CodeBusy)
	// A cancel does not cut a tick short; it is answered once the tick
	// has ended.
	p.send(`{"type":"cancel","id":"c","target":"t1"}`)

	_ = p.stdin.Close()
	waitFor(t, stopping, "shutdown to begin")
	select {
	case <-p.exited:
		t.Fatal("the engine exited with a tick in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	rest, err := p.closeAndWait()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range rest {
		names = append(names, e.name())
	}
	if strings.Join(names, ",") != "tick/t1,done/t1,done/c" {
		t.Fatalf("events after EOF = %v", names)
	}
	if ctxErr.Load() != "<nil>" {
		t.Fatalf("the tick's context ended (%v): a prune in flight would be killed", ctxErr.Load())
	}
}
