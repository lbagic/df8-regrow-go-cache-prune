# regrow

Go CLI disk cleaner: knows what everything is and how it regenerates. Module `github.com/lbagic/regrow`.

Docs: `docs/PRODUCT.md` (what/why), `docs/PLAN.md` (build order + Status tracker), `docs/ARCHITECTURE.md` (structure + invariants).

## Session rules

- **End of every session:** tick finished prompts in the Status block at the top of `docs/PLAN.md` and update its "Now:" line. Keep `go build ./... && go vet ./... && go test ./...` green.
- Safety invariants in ARCHITECTURE.md are non-negotiable: dry-run default, trash-not-rm, path guards, oplog before action, surface-only never deletable.
- Cleaning targets are YAML rules in `rules/` — prefer adding/editing rules over adding code. Every rule gets a golden test against a fixture $HOME.
- Record notable decisions in the "Decisions log" section of `docs/ARCHITECTURE.md`, dated.
- The app's scan page follows `DESIGN.md`: one HTML file, no build, no framework, no network at runtime; check every change by a screenshot in light and dark.
