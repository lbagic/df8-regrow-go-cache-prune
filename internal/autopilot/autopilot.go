// Package autopilot runs the headroom loop: a tick samples the disk,
// records the sample and raises the alerts that crossed, and with
// autotrim on it prunes a cache under its rule's policy when headroom
// is low. `regrow prune` runs the same prune by hand.
package autopilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

// Autopilot is the loop wired to one machine. Every func is a seam:
// tests replace them all, so nothing here reaches the real disk,
// process table or go command unless New built it.
type Autopilot struct {
	// StateDir holds headroom.jsonl, the prune state file and, unless
	// OplogPath says otherwise, the oplog.
	StateDir  string
	OplogPath string
	Host      engine.Host
	Catalog   []engine.Rule

	Take      func(context.Context) (headroom.Sample, error)
	FreeSpace func() (int64, error)
	Processes func(context.Context) ([]headroom.Process, error)
	// GoEnv and RunNative default to the real go command and a real
	// exec when nil.
	GoEnv     func(context.Context, string) (string, error)
	RunNative func(context.Context, []string) error
	Now       func() time.Time
	// Self is this process's pid: it and its ancestors never count as
	// a running build.
	Self int
	// Volume is where Take measures free space; a cache elsewhere is
	// never pruned. Empty skips the check.
	Volume string
}

// New wires the loop to the real machine.
func New(host engine.Host, catalog []engine.Rule) (*Autopilot, error) {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return nil, err
	}
	return &Autopilot{
		StateDir:  filepath.Dir(logPath),
		OplogPath: logPath,
		Host:      host,
		Catalog:   catalog,
		Take:      headroom.Take,
		FreeSpace: func() (int64, error) {
			_, free, err := headroom.FreeSpace()
			return free, err
		},
		Processes: headroom.Processes,
		Now:       time.Now,
		Self:      os.Getpid(),
		Volume:    headroom.Volume(),
	}, nil
}

// Name is how `regrow prune` addresses a rule: its id without the
// "-cache" suffix.
func Name(r engine.Rule) string { return strings.TrimSuffix(r.ID, "-cache") }

// FindRule resolves `regrow prune <name>` to a rule with a prune
// policy, by Name or by id.
func FindRule(catalog []engine.Rule, name string) (engine.Rule, bool) {
	for _, r := range catalog {
		if r.Prune != nil && (r.ID == name || Name(r) == name) {
			return r, true
		}
	}
	return engine.Rule{}, false
}

// Names lists what `regrow prune` accepts.
func Names(catalog []engine.Rule) []string {
	var out []string
	for _, r := range catalog {
		if r.Prune != nil {
			out = append(out, Name(r))
		}
	}
	return out
}

// ErrAutotrimLocked is the gate's refusal.
var ErrAutotrimLocked = errors.New("autotrim is locked")

// Gate refuses autotrim for a rule until the oplog holds a completed
// prune of it. Autotrim cannot produce the first one, so "on only
// after one manual run" is a fact in the journal, not a setting.
func Gate(entries []oplog.Entry, r engine.Rule) error {
	type action struct {
		run string
		seq int
	}
	started := map[action]bool{}
	for _, e := range entries {
		k := action{e.Run, e.Seq}
		switch {
		case e.Event == oplog.EventStart && e.Kind == string(engine.ActionPrune) && e.RuleID == r.ID:
			started[k] = true
		case e.Event == oplog.EventDone && started[k]:
			return nil
		}
	}
	return fmt.Errorf("%w for %s until `regrow prune %s --yes` has completed once", ErrAutotrimLocked, r.ID, Name(r))
}

// Target is the free space autotrim works back to, and whether free
// space is under it. It is the top free-space band, raised to what
// LowDays at the forecast rate needs when the forecast is shorter.
func Target(s headroom.Sample, days float64, forecast bool) (target int64, low bool) {
	target = headroom.Bands[0]
	if forecast && days > 0 && days < headroom.LowDays {
		target = max(target, int64(float64(s.Free)/days*headroom.LowDays))
	}
	return target, s.Free < target
}

// ErrHistoryUnreadable marks a tick that sampled but could not read
// headroom.jsonl: it has no forecast and no alerts, and autotrim judged
// headroom by the top free-space band alone.
var ErrHistoryUnreadable = errors.New("the headroom history cannot be read")

// Tick takes one sample, records it, and reports it with the forecast
// and the alerts that crossed. With autotrim it also prunes when
// headroom is low. The Tick is good whenever its sample time is set,
// error or not: a refused autotrim or a history that could not be
// written must not hide the sample and its alerts.
func (a *Autopilot) Tick(ctx context.Context, autotrim bool) (headroom.Tick, error) {
	s, err := a.Take(ctx)
	if err != nil {
		return headroom.Tick{}, err
	}
	tick := headroom.Tick{Sample: s}
	path := filepath.Join(a.StateDir, headroom.FileName)
	prev, herr := headroom.Tail(path, s.At.Add(-headroom.Window))
	errs := []error{headroom.Append(path, s)}

	var days float64
	var forecast bool
	if herr != nil {
		// Without the history every condition would read as a fresh
		// crossing on every tick.
		errs = append(errs, fmt.Errorf("%w: %s: %v", ErrHistoryUnreadable, path, herr))
	} else {
		cur := append(prev[:len(prev):len(prev)], s)
		days, forecast = headroom.Forecast(cur)
		if forecast {
			tick.DaysToFull = &days
		}
		tick.Alerts = a.nameTopProcess(ctx, headroom.Alerts(prev, cur))
	}

	if autotrim {
		pruned, err := a.autotrim(ctx, s, days, forecast)
		tick.Pruned = pruned
		errs = append(errs, err)
	}
	return tick, errors.Join(errs...)
}

func (a *Autopilot) nameTopProcess(ctx context.Context, alerts []headroom.Alert) []headroom.Alert {
	acute := false
	for _, al := range alerts {
		acute = acute || al.Acute
	}
	if !acute {
		return alerts
	}
	ps, err := a.Processes(ctx)
	if err != nil {
		return alerts
	}
	top, ok := headroom.Top(ps)
	if !ok {
		return alerts
	}
	for i, al := range alerts {
		if al.Acute {
			alerts[i] = al.WithTop(top)
		}
	}
	return alerts
}

// autotrim reports a shut gate, locked or unreadable, as an error only
// when headroom is low enough that a prune would have run; otherwise
// the tick succeeds and says so. A prune that ran and failed is an
// error too.
func (a *Autopilot) autotrim(ctx context.Context, s headroom.Sample, days float64, forecast bool) (*headroom.PruneResult, error) {
	var rule *engine.Rule
	for i := range a.Catalog {
		if a.Catalog[i].Prune != nil {
			rule = &a.Catalog[i]
			break
		}
	}
	if rule == nil {
		return nil, nil
	}
	target, low := Target(s, days, forecast)
	gate := a.Gate(*rule)
	if !low {
		switch {
		case gate == nil:
			return nil, nil
		case errors.Is(gate, ErrAutotrimLocked):
			return &headroom.PruneResult{RuleID: rule.ID, Skipped: gate.Error()}, nil
		default:
			return &headroom.PruneResult{RuleID: rule.ID, Skipped: fmt.Sprintf("the oplog cannot be read, so autotrim would refuse: %v", gate)}, nil
		}
	}
	if gate != nil {
		return nil, gate
	}
	res, err := a.prune(ctx, *rule, s.Free, target, true)
	if err != nil && res.Run == "" {
		return nil, err
	}
	if err == nil && res.Error != "" {
		err = fmt.Errorf("prune %s failed: %s", rule.ID, res.Error)
	}
	return &res, err
}

// Gate reads the oplog and applies the package-level Gate to it.
func (a *Autopilot) Gate(r engine.Rule) error {
	entries, err := oplog.Read(a.OplogPath)
	if err != nil {
		return err
	}
	return Gate(entries, r)
}

// Preview is the dry run of a manual prune.
type Preview struct {
	Plan   engine.Plan        `json:"plan"`
	Survey engine.PruneSurvey `json:"survey"`
	// Running lists the rule's unless_running processes alive now; a
	// real run would refuse while any is.
	Running []string `json:"running,omitempty"`
}

// Preview plans a manual prune and deletes nothing.
func (a *Autopilot) Preview(ctx context.Context, r engine.Rule) (Preview, error) {
	free, err := a.FreeSpace()
	if err != nil {
		return Preview{}, err
	}
	plan, survey, err := engine.PrunePlan(ctx, a.Host, r, a.request(free, engine.TrimToKeepUnder))
	if err != nil {
		return Preview{}, err
	}
	p := Preview{Plan: plan, Survey: survey}
	if r.Prune != nil {
		if ps, err := a.Processes(ctx); err == nil {
			p.Running = headroom.Running(ps, r.Prune.UnlessRunning, a.Self)
		}
	}
	return p, nil
}

// Prune runs the rule's prune by hand: no headroom bound, the cache is
// trimmed down to keep_under.
func (a *Autopilot) Prune(ctx context.Context, r engine.Rule) (headroom.PruneResult, error) {
	free, err := a.FreeSpace()
	if err != nil {
		return headroom.PruneResult{RuleID: r.ID}, err
	}
	return a.prune(ctx, r, free, engine.TrimToKeepUnder, false)
}

func (a *Autopilot) request(free, target int64) engine.PruneRequest {
	return engine.PruneRequest{Now: a.Now(), Free: free, Target: target, Volume: a.Volume, GoEnv: a.GoEnv}
}

// prune is one single-flight prune: lock, check for builds, plan,
// execute through the ordinary executor, report. A skip is a result,
// not an error. Run is set only when the executor attempted the
// action, so an error with no Run means nothing was deleted.
func (a *Autopilot) prune(ctx context.Context, r engine.Rule, free, target int64, auto bool) (headroom.PruneResult, error) {
	res := headroom.PruneResult{RuleID: r.ID}
	skip := func(format string, args ...any) (headroom.PruneResult, error) {
		res.Skipped = fmt.Sprintf(format, args...)
		return res, nil
	}
	if r.Prune == nil {
		return skip("the rule has no prune policy")
	}

	state, err := lockState(filepath.Join(a.StateDir, stateFileName))
	if errors.Is(err, errBusy) {
		return skip("another prune is running")
	}
	if err != nil {
		return res, err
	}
	defer state.unlock()

	ps, err := a.Processes(ctx)
	if err != nil {
		return skip("cannot tell whether a build is running: %v", err)
	}
	running := headroom.Running(ps, r.Prune.UnlessRunning, a.Self)
	now := a.Now()
	if auto {
		next, run := state.deferrals[r.ID].Next(now, len(running) > 0, free)
		state.deferrals[r.ID] = next
		if err := state.save(); err != nil {
			return res, err
		}
		if !run {
			return skip("a build is running (%s); deferred since %s", strings.Join(running, ", "), next.Since.Local().Format("15:04"))
		}
	} else if len(running) > 0 {
		return skip("a build is running (%s); run it again when the build has finished", strings.Join(running, ", "))
	}

	plan, _, err := engine.PrunePlan(ctx, a.Host, r, a.request(free, target))
	if err != nil {
		return res, err
	}
	if len(plan.Actions) == 0 {
		if len(plan.Skipped) > 0 {
			return skip("%s", plan.Skipped[0].Reason)
		}
		return skip("nothing to prune")
	}

	log, err := oplog.Open(a.OplogPath)
	if err != nil {
		return res, err
	}
	// Every Append syncs; nothing is left to lose at close.
	defer func() { _ = log.Close() }()

	exec := &executor.Executor{Log: log, RunNative: a.RunNative, Now: a.Now, RunID: executor.NewRunID(now)}
	out, err := exec.Execute(ctx, plan)
	if out.Done+out.Failed == 0 {
		if err == nil {
			err = fmt.Errorf("the prune of %s did not run", r.ID)
		}
		return res, err
	}
	res.Run, res.FreeBefore = out.RunID, free
	res.Files, res.Bytes = out.Pruned.Files, out.Pruned.Bytes
	res.Error = strings.Join(out.Failures, "; ")
	if after, ferr := a.FreeSpace(); ferr == nil {
		res.FreeAfter = after
	}
	return res, err
}
