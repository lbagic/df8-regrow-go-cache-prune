package scanner

import (
	"context"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// queryTMSnapshots lists local Time Machine snapshots. APFS does not
// expose per-snapshot sizes cheaply, so items carry zero bytes: the
// win shows up as purgeable space after deletion, and the rule's
// regen story explains that. The snapshot's embedded date becomes the
// item Arg (what `tmutil deletelocalsnapshots` targets) and LastUsed
// (when the snapshot was taken).
func queryTMSnapshots(ctx context.Context) ([]engine.Item, error) {
	out, ok, err := runTool(ctx, "tmutil", "listlocalsnapshots", "/")
	if !ok || err != nil {
		return nil, nil
	}
	return parseTMSnapshots(string(out)), nil
}

const tmSnapshotPrefix = "com.apple.TimeMachine."

func parseTMSnapshots(out string) []engine.Item {
	var items []engine.Item
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, tmSnapshotPrefix) {
			continue
		}
		// com.apple.TimeMachine.2026-07-13-090000.local → 2026-07-13-090000
		date := strings.TrimSuffix(strings.TrimPrefix(line, tmSnapshotPrefix), ".local")
		taken, err := time.ParseInLocation("2006-01-02-150405", date, time.Local)
		if err != nil {
			// Unrecognized name shape: list it, but without a delete
			// handle a per-item command can never form (ExpandItem
			// refuses empty {arg}) — safer than guessing a date.
			items = append(items, engine.Item{Label: line})
			continue
		}
		items = append(items, engine.Item{Label: line, Arg: date, LastUsed: taken})
	}
	return items
}
