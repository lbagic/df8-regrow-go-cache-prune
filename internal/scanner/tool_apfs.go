package scanner

import (
	"context"
	"fmt"
	"runtime"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/headroom"
)

// APFS purgeable space (Prompt H, plans/2026-07-13-doctor-phantom.md
// D3): the Finder-vs-df gap. The probe lives in internal/headroom,
// which samples the same number.
func queryAPFSPurgeable(ctx context.Context) ([]engine.Item, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	volume := headroom.Volume()
	purgeable, err := headroom.Purgeable(ctx, volume)
	if err != nil || purgeable <= 0 {
		return nil, err
	}
	return []engine.Item{{
		Label: fmt.Sprintf("purgeable on %s — Finder counts it as free, df does not", volume),
		Arg:   volume,
		Bytes: purgeable,
	}}, nil
}
