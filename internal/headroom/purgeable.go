package headroom

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
)

// Purgeable is the Finder-vs-df gap. Foundation's
// NSURLVolumeAvailableCapacityForImportantUsageKey is the
// purgeable-inclusive number Finder shows;
// NSURLVolumeAvailableCapacityKey is the statfs number df shows. The
// JXA ObjC bridge reads both in-process: no Apple events (so no TCC
// automation prompt) and no CGo. diskutil and system_profiler expose
// only container-free and cannot see purgeable.
const purgeableJXA = `function run(argv) {
	ObjC.import("Foundation");
	const url = $.NSURL.fileURLWithPath(argv[0]);
	const vals = url.resourceValuesForKeysError(
		$([$.NSURLVolumeAvailableCapacityForImportantUsageKey, $.NSURLVolumeAvailableCapacityKey]), null);
	if (vals.isNil()) return "{}";
	return JSON.stringify({
		important: ObjC.unwrap(vals.objectForKey($.NSURLVolumeAvailableCapacityForImportantUsageKey)),
		available: ObjC.unwrap(vals.objectForKey($.NSURLVolumeAvailableCapacityKey)),
	});
}`

// Purgeable returns the volume's purgeable bytes, 0 where the probe
// does not exist (not macOS, osascript missing).
func Purgeable(ctx context.Context, volume string) (int64, error) {
	if runtime.GOOS != "darwin" {
		return 0, nil
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		return 0, nil
	}
	out, err := probeOutput(ctx, probeWaitDelay, "osascript", "-l", "JavaScript", "-e", purgeableJXA, volume)
	if err != nil {
		return 0, fmt.Errorf("purgeable probe: %w", err)
	}
	return parsePurgeable(out)
}

func parsePurgeable(out []byte) (int64, error) {
	var v struct {
		Important int64 `json:"important"`
		Available int64 `json:"available"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return 0, fmt.Errorf("purgeable probe output: %w", err)
	}
	return max(v.Important-v.Available, 0), nil
}
