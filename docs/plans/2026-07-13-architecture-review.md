# Architecture review — 2026-07-13

Deepening opportunities surfaced by an architecture review (vocabulary: module / interface / depth / seam / adapter / leverage / locality, per the deep-module school). Status of each candidate is tracked here so a future session can pick up without redoing the review.

**Decision (2026-07-13): candidates 1 and 2 accepted and implemented same day** (grilled decisions: C1 seam-only, Action.Command kept and built at the seam, no Move/Receipt stubs; C2 fixture in rule YAML, isolated per-rule scan + per-rule goldens, hard-fail coverage with fixture.host override). 3–5 are parked.

## Status

- [x] Candidate 1 — trash seam (implemented 2026-07-13)
- [x] Candidate 2 — golden harness owns rule coverage (implemented 2026-07-13)
- [ ] Candidate 3 — inventory view (parked, unrouted)
- [x] Candidate 4 — placeholder validation (implemented 2026-07-13 in Prompt F: schema owns `Placeholders/PerItem/ExpandItem`, Validate rejects unknown tokens and unsuppliable `{arg}`, empty values skip the item)
- [ ] Candidate 5 — per-item selection (deferred by design; noted at Prompt G)
- [x] Friction: scan ctx cancellation in `DirSize`/`discover` (fixed 2026-07-13)
- [x] Friction: `Risk.Actionable()` replaces three inline checks (fixed 2026-07-13)
- [x] Friction: TUI scroll window tested at short terminal size (fixed 2026-07-13)
- [ ] Friction: tool-test colocation (won't do — churn, no behavior gain)

## Candidate 1 — Put the trash mechanism behind the trash seam ✅ implemented 2026-07-13

**Strength:** Strong. **Files:** `internal/engine/plan.go:142-148`, `internal/trash/guard.go`, `internal/trash/doc.go`, `internal/oplog/` (empty).

**Problem.** The planner knows the trash mechanism: `trashCommand` builds the Finder osascript inside `engine/plan.go`, while `internal/trash` owns only the 36-line `GuardPath` — `trash/doc.go` describes move-to-trash + staging-fallback behavior that exists nowhere. Phase 2 (Prompt F, next) adds execution, staging fallback, oplog and undo; written against today's seam, mechanism knowledge spreads across planner, executor and undo.

**Solution.** Deepen `trash` into the module that owns "make this path recoverable-gone": Finder move, staging-dir fallback, receipts that oplog/undo consume. Plan actions carry intent (kind + path); the exact-command preview for the plan screen derives at the seam; the Phase 2 executor is a thin caller crossing one interface. Path guard stays enforced by the planner (existing ARCHITECTURE.md decision holds).

**Wins.** Locality: mechanism + fallback + undo layout in one module. Interface is the test surface: executor tests fake trash. Dry-run and execute cross the same seam. `trash/doc.go` stops describing vaporware.

## Candidate 2 — Make the golden harness own rule coverage ✅ implemented 2026-07-13

**Strength:** Strong. **Files:** `internal/scanner/golden_test.go:29-75`, `rules/*.yaml`, `internal/scanner/testdata/golden/plan.golden`.

**Problem.** A rule's fixture is a hardcoded Go path list in `buildFixture`, linked to its YAML only by trailing `// rule-id` comments. Nothing asserts a path rule matched ≥1 fixture item — path rot (PRODUCT.md's named top risk) regresses silently: edit a YAML path, the rule quietly vanishes from `plan.golden`, `-update` accepts the loss. And "community PRs a rule, not code" (pillar 5) is false today: every new rule edits Go. One shared golden file is also a merge-conflict magnet for concurrent rule PRs.

**Solution.** Fixture becomes data the rule owns (per-rule manifest under testdata, or a fixture field in the rule YAML — grilling decision). Harness deepens: builds the fixture $HOME from rule data, fails when a path rule produces zero items, snapshots per rule instead of one shared `plan.golden`. Keeps Host.Root re-anchoring and fake tool queries.

**Wins.** Leverage: one harness × 30+ rules. Locality: rule + fixture + golden co-located. Silent coverage loss becomes a red test. Rule PRs stop touching Go.

## Candidate 3 — One inventory view, two adapters (parked)

**Strength:** Worth exploring. **Files:** `internal/tui/tui.go:226-268`, `cmd/regrow/main.go:110-146`, `internal/tui/format.go`.

Grouping/ranking/totals implemented twice (`tui.buildRows`, `main.printFindings`), already drifting: TUI tie-breaks deterministically (bytes desc, then rule ID), plain printer has no tie-break (equal-size categories reorder run to run); TUI hides empty findings, printer shows them. `HumanBytes`/`ShellJoin` live in tui, so the piped/`--json` path imports bubbletea for two string functions. Fix: one inventory-view module; checklist and plain printer become two adapters at that seam; display helpers move out of tui.

## Candidate 4 — Own the {path}/{arg} placeholder convention (parked)

**Strength:** Worth exploring. **Files:** `internal/engine/plan.go:99-133`, `internal/engine/schema.go:142-189`.

Placeholder convention is part of the Rule interface but owned by no module: literal `"{path}"`/`"{arg}"` strings appear three times in plan.go; `Rule.Validate` never checks them. Concrete failure: a typo (`{id}` for `{arg}`) silently downgrades a per-item steward command to run-once-for-the-whole-rule with the literal `{id}` in the argv — nothing fails until execution. Fix: schema owns placeholders — named tokens validated at load against what the rule can supply, expansion in one place.

## Candidate 5 — Per-item selection (parked, deliberately deferred)

**Strength:** Speculative. **Files:** `internal/engine/plan.go:58-61`, `internal/tui/tui.go:187-198`, PRODUCT.md §4.

Selection interface is `map[ruleID]bool`; items have no identity, so the Phase 3 wedge (per-model rows with per-model toggles — "ollama llama3:70b, unused 84d") cannot be expressed. Do nothing now (one caller = hypothetical seam); when Phase 3G nears, item identity enters the selection interface at one seam, keeping `BuildPlan` the enforcement chokepoint. This entry exists so the wall is chosen, not hit.

## Smaller frictions (no card warranted)

- ~~`Scanner.Scan` promises ctx cancellation but `DirSize` and `discover` ignore it~~ — fixed 2026-07-13: both honor ctx; cancelled walks return partial totals with the ctx error.
- ~~Risk actionability re-derived at three call sites~~ — fixed 2026-07-13: `Risk.Actionable()` owns it (planner, Validate, TUI toggleable).
- ~~`window()`/`truncate()` never see a non-default terminal size in tests~~ — fixed 2026-07-13: `TestWindowFollowsCursor` drives a WindowSizeMsg-shrunk terminal.
- Tool-adapter parser tests all live in one `tool_test.go` while adapters span three files — mild locality tax. Deliberately left: pure churn, no behavior gain.

## Already deep — protect while refactoring

`engine.BuildPlan` (surface-only enforcement + guard + command expansion behind one call, two trusting callers). Real seams with two adapters each: `engine.Host` (DetectHost vs fixture), `scanner.ToolQuery` registry (exec adapters vs test fakes), tui's injected scan closure, `engine.LoadFS(fs.FS)` (embed / DirFS / MapFS). Don't shallow these.

## Top recommendation rationale

Candidate 1 first: Phase 2 is the next session and will be written against whatever seam exists that day — placing the seam before the executor costs one small refactor; after, it costs unwinding mechanism knowledge from three callers. Candidate 2 second; it also lands inside Prompt F's "golden tests for every rule" scope.

HTML version of this review (diagrams): generated 2026-07-13, temp file — this doc is the durable copy.
