package headroom

import (
	"context"
	"os/exec"
	"time"
)

// probeWaitDelay bounds how long a killed probe may keep its output
// pipe open (a child it spawned can hold it) before Wait gives up;
// without it a canceled probe can block the tick for as long as the
// child lives.
const probeWaitDelay = 5 * time.Second

func probeOutput(ctx context.Context, waitDelay time.Duration, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	return cmd.Output()
}
