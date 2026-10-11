package trash

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Method says which mechanism made a path recoverable-gone.
type Method string

const (
	MethodFinder  Method = "finder"  // OS Trash, "Put Back" works
	MethodStaging Method = "staging" // rename into the staging dir
	// MethodExport is a copy, not a move: a docker volume tarball'd
	// into staging before `docker volume rm` (Prompt G2). The original
	// is not a host path, so undo cannot rename it back — recovery is
	// manual (docker volume create + untar), pointed at by the receipt.
	MethodExport Method = "export"
)

// Receipt records where a trashed path actually landed so undo can
// rename it back. Receipts are what the oplog journals for trash
// actions; they are the undo contract.
type Receipt struct {
	Original string `json:"original"`
	To       string `json:"to"`
	Method   Method `json:"method"`
}

// Restorable reports whether Restore can undo this receipt. Export
// receipts cannot be renamed back — Original names a docker volume,
// not a path.
func (r Receipt) Restorable() bool { return r.Method != MethodExport }

// Mover makes paths recoverable-gone (ARCHITECTURE.md invariant 2):
// Finder move to the OS Trash first, rename into StagingDir when
// Finder is unavailable (SSH session, CI). Never rm.
type Mover struct {
	// Home feeds the path guard — the guard runs here too, not just in
	// the planner: defense in depth on the last hop before action.
	Home string
	// StagingDir receives fallback moves; created on first use.
	StagingDir string
	// RunFinder executes the Finder move and returns the trashed
	// item's POSIX path. Nil means real osascript; tests inject fakes.
	RunFinder func(ctx context.Context, path string) (string, error)
}

// Move trashes one path and returns the receipt undo needs.
func (m *Mover) Move(ctx context.Context, path string) (Receipt, error) {
	if err := GuardPath(path, m.Home); err != nil {
		return Receipt{}, err
	}
	if _, err := os.Lstat(path); err != nil {
		return Receipt{}, err // vanished since scan, or never existed
	}

	runFinder := m.RunFinder
	if runFinder == nil {
		runFinder = finderMove
	}
	if to, err := runFinder(ctx, path); err == nil {
		return Receipt{Original: path, To: to, Method: MethodFinder}, nil
	} else if ctx.Err() != nil {
		return Receipt{}, err // cancelled: don't escalate to the fallback
	}

	to, err := m.stage(path)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Original: path, To: to, Method: MethodStaging}, nil
}

// stage renames path into the staging directory under a name that
// never collides. Rename works regardless of permissions inside the
// tree (read-only go mod cache): only the parent directories matter.
func (m *Mover) stage(path string) (string, error) {
	if m.StagingDir == "" {
		return "", fmt.Errorf("trash: Finder unavailable and no staging dir configured")
	}
	if err := os.MkdirAll(m.StagingDir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	to := filepath.Join(m.StagingDir, base)
	for n := 2; ; n++ {
		if _, err := os.Lstat(to); os.IsNotExist(err) {
			break
		}
		to = filepath.Join(m.StagingDir, fmt.Sprintf("%s-%d", base, n))
	}
	if err := os.Rename(path, to); err != nil {
		return "", fmt.Errorf("trash: staging move failed (cross-volume paths are not supported yet): %w", err)
	}
	return to, nil
}

// Restore undoes one receipt: renames the trashed item back to its
// original path. It refuses to overwrite anything that reappeared at
// the original location.
func Restore(r Receipt) error {
	if !r.Restorable() {
		return fmt.Errorf("restore %s: %s receipts are not auto-restorable (backup kept at %s)", r.Original, r.Method, r.To)
	}
	if _, err := os.Lstat(r.To); err != nil {
		return fmt.Errorf("restore %s: trashed copy is gone (Trash emptied?): %w", r.Original, err)
	}
	if _, err := os.Lstat(r.Original); err == nil {
		return fmt.Errorf("restore %s: something already exists there again", r.Original)
	}
	if err := os.MkdirAll(filepath.Dir(r.Original), 0o755); err != nil {
		return err
	}
	return os.Rename(r.To, r.Original)
}

// finderMove runs the real osascript from PreviewCommand — execution
// and preview are the same argv by construction.
func finderMove(ctx context.Context, path string) (string, error) {
	argv := PreviewCommand(path)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("osascript: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	// POSIX path of a folder alias carries a trailing slash.
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}
