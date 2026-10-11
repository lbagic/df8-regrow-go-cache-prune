package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/trash"
)

// Exporter is the docker-volume-export pre-action: before `docker
// volume rm` runs, the volume's contents are tarballed into the run's
// staging directory. Volume data lives inside the VM where Finder
// trash cannot reach, so the export is how volume deletion honors
// trash-not-rm (invariant 2). The receipt is a recovery pointer
// (docker volume create + untar), not an auto-restorable move.
type Exporter struct {
	// StagingDir is the run's staging directory (the same family the
	// trash fallback uses).
	StagingDir string
	// CapBytes refuses exports of volumes bigger than this: the
	// tarball would eat the very disk regrow is freeing.
	CapBytes int64
	// Exec probes that the volume still exists; nil = real docker CLI.
	Exec Exec
	// Stream runs the export command with stdout attached to the
	// tarball; nil = real docker CLI. Separate from Exec because a
	// volume tarball must never be buffered in memory.
	Stream func(ctx context.Context, args []string, stdout io.Writer) error
}

// PreAction matches the executor's pre-action hook signature.
func (e *Exporter) PreAction(ctx context.Context, a engine.Action) (*trash.Receipt, error) {
	name := a.ItemKey
	if name == "" {
		return nil, fmt.Errorf("volume export: action has no item key")
	}
	if e.CapBytes > 0 && a.Bytes > e.CapBytes {
		// The planner should never have offered this (over-cap volumes
		// are kept-tier); defense in depth on the last hop.
		return nil, fmt.Errorf("volume %s: %d bytes exceeds the %s export cap", name, a.Bytes, humanGiB(e.CapBytes))
	}

	run := e.Exec
	if run == nil {
		run = realExec
	}
	if _, found, err := run(ctx, "volume", "inspect", name); !found || err != nil {
		return nil, fmt.Errorf("volume %s vanished since scan (or docker stopped): refusing to remove", name)
	}

	if err := os.MkdirAll(e.StagingDir, 0o700); err != nil {
		return nil, err
	}
	tarPath := stagePath(e.StagingDir, "docker-volume-"+name+".tar")
	f, err := os.OpenFile(tarPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	stream := e.Stream
	if stream == nil {
		stream = realStream
	}
	// busybox (~4MB) pulls on first use; read-only mount, tar to stdout.
	args := []string{"run", "--rm", "-v", name + ":/volume:ro", "busybox", "tar", "-cf", "-", "-C", "/volume", "."}
	streamErr := stream(ctx, args, f)
	if closeErr := f.Close(); streamErr == nil {
		streamErr = closeErr
	}
	if streamErr != nil {
		_ = os.Remove(tarPath) // a partial tarball is not a backup
		return nil, fmt.Errorf("volume %s: export failed, refusing to remove: %w", name, streamErr)
	}
	return &trash.Receipt{
		Original: "docker volume " + name,
		To:       tarPath,
		Method:   trash.MethodExport,
	}, nil
}

// stagePath picks a name that never collides, like the trash stager.
func stagePath(dir, base string) string {
	p := filepath.Join(dir, base)
	for n := 2; ; n++ {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d", base, n))
	}
}

// realStream runs docker with stdout attached to the tarball; stderr
// stays on the terminal so a busybox pull is visible, not a hang.
func realStream(ctx context.Context, args []string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
