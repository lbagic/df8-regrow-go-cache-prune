package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// withAVolume is a plan with a steward command, a Trash move and a
// docker volume removed after its export.
func withAVolume() engine.Plan {
	return engine.Plan{Actions: []engine.Action{
		{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: 3 * gib},
		{RuleID: "dev-caches", Kind: engine.ActionTrash, Path: "/Users/fixture/.cache/x", Bytes: 1 * gib},
		{RuleID: "docker-volumes-named", Kind: engine.ActionNative, Command: []string{"docker", "volume", "rm", "v"}, Bytes: 5 * gib, PreAction: engine.PreActionVolumeExport},
	}}
}

func stdoutOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = real }()
	f()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestPlanPrintsReclaimWithoutVolumeExports(t *testing.T) {
	out := stdoutOf(t, func() {
		if err := printPlan(withAVolume(), false); err != nil {
			t.Fatal(err)
		}
	})
	if !lineWith(out, "Would reclaim:", "4.0 GiB") || strings.Contains(out, "9.0 GiB") {
		t.Errorf("a volume's export keeps its bytes on disk; Would reclaim is the 3 GiB steward command and the 1 GiB Trash move, got:\n%s", out)
	}
	if !lineWith(out, "Volume exports", "5.0 GiB") {
		t.Errorf("the volume's bytes stay visible on their own line, got:\n%s", out)
	}
}

func TestCleanPromptPrintsReclaimWithoutVolumeExports(t *testing.T) {
	out := stdoutOf(t, func() { printPlanActions(withAVolume()) })
	if !lineWith(out, "Would reclaim:", "4.0 GiB") || strings.Contains(out, "9.0 GiB") {
		t.Errorf("Would reclaim before the clean prompt leaves out the volume's export, got:\n%s", out)
	}
	if !lineWith(out, "Volume exports", "5.0 GiB") {
		t.Errorf("the volume's bytes stay visible on their own line, got:\n%s", out)
	}
}

func TestPlanReclaimFigureMatchesTheGrossWhenNothingIsExported(t *testing.T) {
	plan := withAVolume()
	plan.Actions = plan.Actions[:2]
	if out := stdoutOf(t, func() { _ = printPlan(plan, false) }); !lineWith(out, "Would reclaim:", "4.0 GiB") {
		t.Errorf("without a volume the figure is the plan's whole sum, got:\n%s", out)
	}
	if out := stdoutOf(t, func() { printPlanActions(plan) }); !lineWith(out, "Would reclaim:", "4.0 GiB") {
		t.Errorf("without a volume the figure is the plan's whole sum, got:\n%s", out)
	}
}

func TestPlanOfVolumesAlonePrintsZeroReclaim(t *testing.T) {
	plan := withAVolume()
	plan.Actions = plan.Actions[2:]
	out := stdoutOf(t, func() { _ = printPlan(plan, false) })
	if !lineWith(out, "Would reclaim:", "0 B") || !lineWith(out, "Volume exports", "5.0 GiB") {
		t.Errorf("a plan of volume exports alone reclaims nothing until the tarballs go, got:\n%s", out)
	}
}
