package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// Docker Desktop keeps its own settings in its group container
// (settings-store.json since 4.35, settings.json before) and the
// engine's in ~/.docker/daemon.json. Both are plain files: neither
// check talks to the daemon.
const (
	dockerSettingsDir  = "~/Library/Group Containers/group.com.docker"
	dockerDaemonConfig = "~/.docker/daemon.json"
)

// dockerVMMemory reads the VM's memory limit. Docker Desktop writes
// only settings that differ from its defaults, so a missing key means
// the default: up to half of the machine's memory. A limit that high
// or higher caps nothing, whoever wrote it.
func (c *causeChecks) dockerVMMemory(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
	for _, name := range []string{"settings-store.json", "settings.json"} {
		path := c.host.ExpandPath(dockerSettingsDir + "/" + name)
		data, err := c.read(ctx, path)
		if absent(err) {
			continue
		}
		shown := tilde(c.host, path)
		if err != nil {
			return engine.VerdictUnknown, shown + " is " + engine.UnreadableNote
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(data, &settings); err != nil {
			return engine.VerdictUnknown, shown + " could not be read as Docker Desktop's settings"
		}
		total := c.totalMemory()
		for key, raw := range settings {
			// MemoryMiB in settings-store.json, memoryMiB in settings.json.
			if !strings.EqualFold(key, "memoryMiB") {
				continue
			}
			var mib float64
			if err := json.Unmarshal(raw, &mib); err != nil {
				return engine.VerdictUnknown, fmt.Sprintf("%s in %s is not a number", key, shown)
			}
			if mib <= 0 {
				break
			}
			limit := int64(mib) << 20
			switch {
			case total == 0:
				return engine.VerdictNormal, "capped at " + engine.HumanBytes(limit)
			case limit >= total/2:
				return engine.VerdictFlagged, fmt.Sprintf("the memory limit is %s, half or more of this Mac's %s", engine.HumanBytes(limit), engine.HumanBytes(total))
			}
			return engine.VerdictNormal, fmt.Sprintf("capped at %s of this Mac's %s", engine.HumanBytes(limit), engine.HumanBytes(total))
		}
		detail := "no memory limit in Docker Desktop's settings, so its default applies: half of this Mac's memory"
		if total > 0 {
			detail += fmt.Sprintf(" (%s of %s)", engine.HumanBytes(total/2), engine.HumanBytes(total))
		}
		return engine.VerdictFlagged, detail
	}
	return engine.VerdictNormal, "does not apply: no Docker Desktop settings on this machine"
}

func (c *causeChecks) totalMemory() int64 {
	if c.memory != nil {
		return c.memory()
	}
	return hostMemory()
}

// dockerBuildCacheLimit reads builder.gc from the engine's config, as
// Docker Engine 29.8 reads it (moby daemon/config/builder.go): the
// collection is on unless enabled is false, and that has been the
// default since Engine 28.2; a policy replaces the default sizes; and
// defaultKeepStorage is the older name of defaultReservedSpace. So
// only enabled: false, or a policy with no rule in it, leaves the
// cache without a limit.
func (c *causeChecks) dockerBuildCacheLimit(ctx context.Context, _ engine.Rule) (engine.Verdict, string) {
	path := c.host.ExpandPath(dockerDaemonConfig)
	shown := tilde(c.host, path)
	data, err := c.read(ctx, path)
	switch {
	case absent(err):
		return engine.VerdictNormal, "does not apply: no " + shown + " on this machine"
	case err != nil:
		return engine.VerdictUnknown, shown + " is " + engine.UnreadableNote
	}
	var cfg struct {
		Builder struct {
			GC struct {
				Enabled              *bool              `json:"enabled"`
				Policy               *[]json.RawMessage `json:"policy"`
				DefaultReservedSpace string             `json:"defaultReservedSpace"`
				DefaultKeepStorage   string             `json:"defaultKeepStorage"`
				DefaultMaxUsedSpace  string             `json:"defaultMaxUsedSpace"`
				DefaultMinFreeSpace  string             `json:"defaultMinFreeSpace"`
			} `json:"gc"`
		} `json:"builder"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return engine.VerdictUnknown, shown + " could not be read as Docker Engine's settings"
	}
	gc := cfg.Builder.GC
	if gc.Enabled != nil && !*gc.Enabled {
		return engine.VerdictFlagged, shown + " switches build-cache garbage collection off (builder.gc.enabled is false), so nothing limits the cache"
	}
	if gc.Policy != nil && len(*gc.Policy) == 0 {
		return engine.VerdictFlagged, shown + " gives build-cache garbage collection a policy with no rule (builder.gc.policy), so nothing limits the cache"
	}

	detail := "garbage collection on, under Docker's default limits: shares of the engine's own disk, which in Docker Desktop is the VM disk"
	if gc.Policy != nil {
		detail = "garbage collection on, under the policy in builder.gc.policy"
	} else {
		// The engine reads the older name only when the newer is unset.
		if gc.DefaultReservedSpace != "" {
			gc.DefaultKeepStorage = ""
		}
		var sizes []string
		for _, size := range []struct{ key, value string }{
			{"defaultReservedSpace", gc.DefaultReservedSpace},
			{"defaultKeepStorage", gc.DefaultKeepStorage},
			{"defaultMaxUsedSpace", gc.DefaultMaxUsedSpace},
			{"defaultMinFreeSpace", gc.DefaultMinFreeSpace},
		} {
			if size.value != "" {
				sizes = append(sizes, fmt.Sprintf("builder.gc.%s is %s", size.key, size.value))
			}
		}
		if len(sizes) > 0 {
			detail = "garbage collection on: " + strings.Join(sizes, ", ")
		}
	}
	if gc.Enabled == nil {
		detail += "; builder.gc.enabled is not set, which means on since Docker Engine 28.2"
	}
	return engine.VerdictNormal, detail
}
