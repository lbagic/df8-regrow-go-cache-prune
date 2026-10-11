package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Scanner measures every rule against a host. Host and Queries are
// injectable so tests run against fixture homes and fake tools.
type Scanner struct {
	Host    engine.Host
	Queries map[string]ToolQuery
	// fs bounds the scan's filesystem calls; nil means defaultWalker.
	fs *walker
	// queryTimeout bounds each tool query; 0 means toolQueryTimeout.
	queryTimeout time.Duration
	// CauseQueries are the fix-the-cause checks, by name.
	CauseQueries map[string]CauseQuery
}

// toolQueryTimeout bounds one tool query. A wedged Docker VM or
// CoreSimulatorService leaves its CLI waiting on a socket forever;
// `docker system df -v` sizes every volume, so the bound is generous.
const toolQueryTimeout = 2 * time.Minute

// New builds a scanner for the host with the built-in tool queries.
func New(host engine.Host) *Scanner {
	return &Scanner{Host: host, Queries: DefaultQueries(), CauseQueries: DefaultCauseQueries(host)}
}

func (s *Scanner) walker() *walker {
	if s.fs != nil {
		return s.fs
	}
	return defaultWalker
}

// Scan measures all rules and returns one finding per rule, catalog
// order preserved. Rules whose targets are absent return a finding
// with no items; rule-level failures land in Finding.Err instead of
// aborting the run.
func (s *Scanner) Scan(ctx context.Context, rules []engine.Rule) []engine.Finding {
	findings := make([]engine.Finding, len(rules))
	s.ScanStream(ctx, rules, func(i int, f engine.Finding, _ time.Duration) {
		findings[i] = f
	})
	return findings
}

// ScanStream measures all rules like Scan, handing each finding to
// emit as soon as its rule completes, with the rule's own scan time.
// emit is called from scan goroutines but never concurrently, and
// ScanStream returns only after the last emit.
func (s *Scanner) ScanStream(ctx context.Context, rules []engine.Rule, emit func(i int, f engine.Finding, took time.Duration)) {
	sem := make(chan struct{}, runtime.NumCPU())
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, r := range rules {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			f := s.scanRule(ctx, r)
			mu.Lock()
			emit(i, f, time.Since(start))
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func (s *Scanner) scanRule(ctx context.Context, r engine.Rule) engine.Finding {
	f := engine.Finding{Rule: r}
	w := s.walker()

	for _, path := range s.Host.ResolvePaths(r) {
		matches, partial := w.glob(ctx, path)
		if partial {
			f.Items = append(f.Items, unreadableMarker(tilde(s.Host, path)))
		}
		for _, p := range matches {
			if item, ok := s.measure(ctx, p); ok {
				f.Items = append(f.Items, item)
			}
		}
	}

	if r.Discover != nil {
		hits, partialRoots := discover(ctx, w, s.Host, *r.Discover)
		for _, root := range partialRoots {
			f.Items = append(f.Items, unreadableMarker("folders under "+tilde(s.Host, root)))
		}
		for _, path := range hits {
			if item, ok := s.measure(ctx, path); ok {
				f.Items = append(f.Items, item)
			}
		}
	}

	if r.ToolQuery != "" {
		query, known := s.Queries[r.ToolQuery]
		if !known {
			f.Err = joinErr(f.Err, fmt.Errorf("unknown tool query %q", r.ToolQuery))
		} else if items, err := s.runQuery(ctx, query); err != nil {
			f.Err = joinErr(f.Err, err)
		} else {
			f.Items = append(f.Items, items...)
		}
	}

	// A cancelled scan's findings are incomplete: the error keeps
	// them out of every default selection.
	if err := ctx.Err(); err != nil && !strings.Contains(f.Err, err.Error()) {
		f.Err = joinErr(f.Err, err)
	}

	// Every item leaves the scanner with its stable key (Prompt G0):
	// "ruleID/key" is how selection atoms and --json address it.
	f.FillItemKeys(s.Host.Home)
	return f
}

// runQuery runs a tool query under the query deadline. It stops
// waiting at the deadline even for a query that ignores its ctx (one
// parked on a lock another query holds), and a query that answers
// after its deadline passed reports the deadline, not its own outcome:
// a killed CLI often reads as "tool not available".
func (s *Scanner) runQuery(ctx context.Context, query ToolQuery) ([]engine.Item, error) {
	return withDeadline(ctx, s.timeout(), "tool", query)
}

func (s *Scanner) timeout() time.Duration {
	if s.queryTimeout > 0 {
		return s.queryTimeout
	}
	return toolQueryTimeout
}

// withDeadline is runQuery's rule for any query: a tool query's items
// or a cause check's verdict. who is the subject of the deadline's
// error: what gave no answer.
func withDeadline[T any](ctx context.Context, timeout time.Duration, who string, query func(context.Context) (T, error)) (T, error) {
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := query(qctx)
		ch <- result{v, err}
	}()
	var r result
	var zero T
	select {
	case r = <-ch:
	case <-qctx.Done():
	}
	if ctx.Err() == nil && (errors.Is(qctx.Err(), context.DeadlineExceeded) || errors.Is(r.err, context.DeadlineExceeded)) {
		return zero, fmt.Errorf("%s gave no answer within %s", who, timeout)
	}
	if r.err == nil && ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return r.v, r.err
}

// measure sizes one path. Absent paths are not an item: most rules
// simply do not apply to a given machine. A path that refuses or
// blocks is an item with Partial set.
func (s *Scanner) measure(ctx context.Context, path string) (engine.Item, bool) {
	u, found, _ := s.walker().usage(ctx, path)
	if !found {
		return engine.Item{}, false
	}
	return engine.Item{Path: path, Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}, true
}

// unreadableMarker stands for targets the scan could not enumerate
// (behind a glob parent, a discover folder or a tool's store that
// blocked or refused). It has no path, so no plan can act on it.
func unreadableMarker(label string) engine.Item {
	return engine.Item{Label: label, Partial: true}
}

// tilde abbreviates the host home in a label.
func tilde(host engine.Host, path string) string {
	if rest, ok := strings.CutPrefix(path, host.Home+string(filepath.Separator)); ok && host.Home != "" {
		return "~/" + rest
	}
	return path
}

// homeTilde abbreviates the process's home, for tool queries that
// resolve their own locations.
func homeTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return tilde(engine.Host{Home: home}, path)
}

// glob resolves metacharacters against the filesystem so rules can
// target patterns like /Applications/Install macOS*.app, listing each
// parent through the walker. Literal paths pass through untouched; a
// pattern with no matches yields nothing. partial is true when a
// parent on the pattern's way blocked or refused.
func (w *walker) glob(ctx context.Context, pattern string) (matches []string, partial bool) {
	if !hasMeta(pattern) {
		return []string{pattern}, false
	}
	dir, file := filepath.Split(pattern)
	if _, err := filepath.Match(file, ""); err != nil {
		return nil, false // malformed pattern: matches nothing, like filepath.Glob
	}
	dir = filepath.Clean(dir)
	parents := []string{dir}
	if hasMeta(dir) {
		parents, partial = w.glob(ctx, dir)
	}
	for _, d := range parents {
		entries, incomplete, err := w.list(ctx, d)
		if err != nil {
			return matches, true
		}
		partial = partial || incomplete
		for _, e := range entries {
			if ok, _ := filepath.Match(file, e.Name()); ok {
				matches = append(matches, filepath.Join(d, e.Name()))
			}
		}
	}
	slices.Sort(matches)
	return matches, partial
}

func hasMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func joinErr(existing string, err error) string {
	if existing == "" {
		return err.Error()
	}
	return existing + "; " + err.Error()
}
