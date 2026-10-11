package headroom

import (
	"context"
	"testing"
	"time"
)

func TestProbeOutputReturnsWhileAChildHoldsThePipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The shell is killed at the deadline; the sleep it started keeps
	// stdout open for three seconds more.
	_, err := probeOutput(ctx, 200*time.Millisecond, "/bin/sh", "-c", "sleep 3 & wait")
	if err == nil {
		t.Fatal("a probe killed at its deadline must report an error")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the probe waited %v for its child to let go of the pipe", took)
	}
}
