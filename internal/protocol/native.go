package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	nativeTailBytes = 4 << 10
	// nativeWaitDelay bounds the wait for a command's output pipes
	// after it exits: a daemon it left behind may hold them open.
	nativeWaitDelay = 10 * time.Second
)

// RunNative runs a steward command for the engine. The engine's own
// stdin and stdout carry the protocol, so the command gets /dev/null
// for stdin and its stdout and stderr are captured; a failure carries
// the tail of that output, which is what the journal records.
func RunNative(ctx context.Context, argv []string) error {
	return runCaptured(ctx, argv, nil)
}

// RunNativeTo is RunNative with the command's stdout sent to w, for a
// command whose output is data (a docker volume tarball). Only stderr
// is captured then.
func RunNativeTo(ctx context.Context, argv []string, w io.Writer) error {
	return runCaptured(ctx, argv, w)
}

func runCaptured(ctx context.Context, argv []string, stdout io.Writer) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	if argv[0] == "sudo" {
		return errors.New("needs administrator rights: the engine has no terminal for sudo")
	}
	out := &tail{max: nativeTailBytes}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	if stdout != nil {
		cmd.Stdout = stdout
	}
	cmd.WaitDelay = nativeWaitDelay
	err := cmd.Run()
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	if text := out.String(); text != "" {
		return fmt.Errorf("%w: %s", err, text)
	}
	return err
}

// tail keeps the last max bytes written to it.
type tail struct {
	max     int
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.dropped = true
	}
	return len(p), nil
}

// String returns the kept output, starting at a line boundary when
// the head was dropped and a later line remains.
func (t *tail) String() string {
	s := strings.TrimSpace(string(t.buf))
	if t.dropped {
		if _, rest, cut := strings.Cut(s, "\n"); cut {
			s = strings.TrimSpace(rest)
		}
	}
	return s
}
