# Prompt H — `regrow doctor` + phantom space

Status: **shipped** (2026-07-13). Deviations from plan: D4's query landed
in `internal/scanner/tool_docker.go` (same file as the provider wiring)
rather than `internal/docker` — it is a file stat with no daemon
conversation, and the provider's Exec seam would have been dead weight.
M3's "dramatic on ≥1 real machine" is still open: the dev machine is
healthy (flagged rendering verified via a lowered threshold).

## Goal (PLAN.md Prompt H, PRODUCT.md §4/§5)

`regrow doctor`: hero-bug scanners (~/.claude/debug, mediaanalysisd,
.Spotlight-V100, Playwright transform cache) + phantom-space category
(TM snapshots, Docker VM real-vs-logical, APFS purgeable) with
explainer copy. M3 check: doctor finds something dramatic on ≥1 real
machine; screenshot-worthy.

## Design decisions

**D1 — hero bugs are rule metadata, not code.** New optional `doctor:`
block on a rule: `flag_above: 20GB` (threshold) + `story:` (what the
bug is, one sentence). Doctor = scan rules that carry the block,
compare total bytes against threshold. Rules-first (CLAUDE.md); a new
hero bug is a YAML PR. Rejected: a separate scanner registry in code —
duplicates paths/fixtures the rules already have.

**D2 — phantom space is a category, not a flag.** `category:
phantom-space` (renders "PHANTOM SPACE" via the existing header
formatter). `tm-snapshots` and `docker-vm-disk` move there;
new `apfs-purgeable` joins them. Category is already the only display
grouping; a second `phantom: true` axis would mean two grouping
notions. PRODUCT.md §4 mock shows PHANTOM SPACE as a top-level
category, so the main scan moves too, not just doctor.

**D3 — purgeable via JXA/ObjC bridge.** Verified live 2026-07-13:
`osascript -l JavaScript` + `ObjC.import("Foundation")`,
`NSURLVolumeAvailableCapacityForImportantUsageKey` (Finder-style,
purgeable-inclusive) minus `NSURLVolumeAvailableCapacityKey` (statfs)
= purgeable bytes. This machine: 119.07 − 91.92 = 27.1 GB. No TCC
prompt (no Apple events to other apps), no CGo (keeps CGO_ENABLED=0
cross-builds). Rejected: `tell application "Finder"` (automation TCC
prompt), `diskutil`/`system_profiler` (container-free only, verified
no purgeable key), CGo Foundation call (breaks release cross-build).

**D4 — Docker VM real-vs-logical from file stat, exposed via the
docker provider.** Scanner already reports physical blocks (real);
logical = sparse apparent size from the same stat. New provider query
`docker-vm-disk` labels the item "X real (Y sparse)" — works with the
daemon down (it's a file stat), lives in internal/docker because
that's where docker knowledge lives. Rule switches from `paths:` to
`tool_query:`.

**D5 — new hero rules.**
- `claude-code-cache`: ~/.claude/{debug,cache,shell-snapshots,paste-cache},
  safe, per-path items. `~/.claude/projects` is history — never listed.
  Hero: debug log-loop bug, 100–200GB observed. flag_above: 5GB.
- `playwright-cache`: ~/Library/Caches/ms-playwright, safe, trash-path
  (regen: `npx playwright install`). Hero: transform-cache bug 26GB+.
  flag_above: 10GB.
- `media-analysis-cache` (exists): flag_above: 2GB (normal ≈ 100MB;
  leak hit 15–143GB, fixed 15.2+).
- `spotlight-index` (exists): flag_above: 20GB (normal 1–10GB;
  233GB observed).

**D6 — doctor output.** Plain printed report (no TUI): HERO BUGS
section — ✓ normal / 🚩 flagged with story + exact fix command;
PHANTOM SPACE section — always shown with "why Finder still shows
full" copy. `--json` emits the same structure. Report assembly is a
pure function in `internal/engine` (testable); cmd prints it.

## Slices

1. engine: `ByteSize` (parse "20GB") + `Doctor` schema + validation + tests
2. rules: two new YAMLs, doctor blocks on two existing, category moves; goldens
3. scanner: `apfs-purgeable` query (JXA); docker provider `docker-vm-disk` query
4. engine: doctor report builder + tests
5. cmd: `regrow doctor [--json]` + docs (PLAN status, CHANGELOG, ARCHITECTURE decisions)
6. dogfood on this machine; golden refresh
