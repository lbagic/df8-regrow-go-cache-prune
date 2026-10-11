package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// CoreSimulator is simctl-only territory (docs/research/02 §6): raw
// deletion desyncs the simdiskimaged DB. These queries enumerate what
// `simctl delete` / `simctl runtime delete` can address. simctl
// failing (no Xcode, only CLT) means the rules do not apply.

func querySimctlDevices(ctx context.Context) ([]engine.Item, error) {
	available, _, err := simctlDevices(ctx)
	return available, err
}

func querySimctlDevicesUnavailable(ctx context.Context) ([]engine.Item, error) {
	_, unavailable, err := simctlDevices(ctx)
	return unavailable, err
}

func simctlDevices(ctx context.Context) (available, unavailable []engine.Item, err error) {
	out, ok, runErr := runTool(ctx, "xcrun", "simctl", "list", "devices", "-j")
	if !ok || runErr != nil {
		return nil, nil, nil
	}
	return parseSimDevices(out)
}

func querySimctlRuntimes(ctx context.Context) ([]engine.Item, error) {
	out, ok, err := runTool(ctx, "xcrun", "simctl", "runtime", "list", "-j")
	if !ok || err != nil {
		return nil, nil
	}
	return parseSimRuntimes(out)
}

type simDevice struct {
	Name         string `json:"name"`
	UDID         string `json:"udid"`
	IsAvailable  bool   `json:"isAvailable"`
	DataPath     string `json:"dataPath"`
	DataPathSize int64  `json:"dataPathSize"`
	LastBootedAt string `json:"lastBootedAt"`
}

func parseSimDevices(data []byte) (available, unavailable []engine.Item, err error) {
	var raw struct {
		Devices map[string][]simDevice `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("simctl list devices: %w", err)
	}
	runtimes := make([]string, 0, len(raw.Devices))
	for rt := range raw.Devices {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	for _, rt := range runtimes {
		for _, d := range raw.Devices[rt] {
			item := engine.Item{
				Label:    fmt.Sprintf("%s (%s)", d.Name, runtimeLabel(rt)),
				Arg:      d.UDID,
				Bytes:    d.DataPathSize,
				LastUsed: parseSimTime(d.LastBootedAt),
			}
			if d.IsAvailable {
				available = append(available, item)
			} else {
				unavailable = append(unavailable, item)
			}
		}
	}
	return available, unavailable, nil
}

type simRuntime struct {
	Identifier        string `json:"identifier"`
	RuntimeIdentifier string `json:"runtimeIdentifier"`
	Build             string `json:"build"`
	SizeBytes         int64  `json:"sizeBytes"`
	Deletable         bool   `json:"deletable"`
	LastUsedAt        string `json:"lastUsedAt"`
}

func parseSimRuntimes(data []byte) ([]engine.Item, error) {
	var raw map[string]simRuntime
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("simctl runtime list: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var items []engine.Item
	for _, k := range keys {
		rt := raw[k]
		if !rt.Deletable {
			continue
		}
		items = append(items, engine.Item{
			Label:    fmt.Sprintf("%s (%s)", runtimeLabel(rt.RuntimeIdentifier), rt.Build),
			Arg:      rt.Identifier,
			Bytes:    rt.SizeBytes,
			LastUsed: parseSimTime(rt.LastUsedAt),
		})
	}
	return items, nil
}

// runtimeLabel turns "com.apple.CoreSimulator.SimRuntime.iOS-17-0"
// into "iOS 17.0".
func runtimeLabel(id string) string {
	id = strings.TrimPrefix(id, "com.apple.CoreSimulator.SimRuntime.")
	platform, version, found := strings.Cut(id, "-")
	if !found {
		return id
	}
	return platform + " " + strings.ReplaceAll(version, "-", ".")
}

func parseSimTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
