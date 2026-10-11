package scanner

import (
	"context"
	"os/exec"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// ToolQuery enumerates targets only a steward tool can see (docker
// system df, simctl list, hf scan-cache, ollama list). Queries live
// in code because their output parsing is per-tool; rules reference
// them by name via tool_query.
type ToolQuery func(ctx context.Context) ([]engine.Item, error)

// DefaultQueries returns the built-in query registry.
func DefaultQueries() map[string]ToolQuery {
	queries := map[string]ToolQuery{
		"apfs-purgeable":             queryAPFSPurgeable,
		"build-output":               queryBuildOutput,
		"downloaded-installers":      queryDownloadedInstallers,
		"git-worktrees":              queryGitWorktrees,
		"hf-hub":                     queryHFHub,
		"npx-cache":                  queryNpxCache,
		"ollama-models":              queryOllamaModels,
		"simctl-devices":             querySimctlDevices,
		"simctl-devices-unavailable": querySimctlDevicesUnavailable,
		"simctl-runtimes":            querySimctlRuntimes,
		"tm-snapshots":               queryTMSnapshots,
	}
	sessions := &agentSessions{}
	queries["agent-scratch"] = sessions.Ended
	queries["agent-scratch-kept"] = sessions.Kept
	for name, q := range dockerQueries() {
		queries[name] = q
	}
	return queries
}

// toolWaitDelay bounds how long a killed tool may keep its output
// pipes open (a child it spawned can hold them) before Wait gives up.
const toolWaitDelay = 5 * time.Second

// runTool executes a tool and returns stdout. A tool missing from
// PATH means the rule does not apply to this machine: (nil, false)
// with no error, so laptops without docker or Xcode stay quiet.
func runTool(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, false, nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = toolWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
