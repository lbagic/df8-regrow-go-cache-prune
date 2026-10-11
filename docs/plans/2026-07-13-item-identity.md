# Item identity (Prompt G0) — design

Status: implemented 2026-07-13. Groundwork for Prompt G (ML models) and G2 (docker); candidate 5 in the [architecture review](2026-07-13-architecture-review.md).

## Goal

Lift selection from per-rule (`map[ruleID]bool`) to per-item without breaking the per-rule default UX. Items get stable IDs; the TUI grows opt-in expandable item rows; planner/executor accept item-scoped actions; `--json` carries item identity. No new data sources — proven by refitting sim devices, Xcode archives, TM snapshots.

## Identity

- **Item key** — stable per item within its rule, derived (in `engine`, one place):
  1. `Arg` (the tool's own handle: sim UDID, runtime identifier, snapshot date) — the string the delete command targets, so it is the identity;
  2. else `Path`, `$HOME` abbreviated to `~` (stable on a machine, shell-safe to type since `~` mid-word never expands);
  3. else `Label`;
  4. else positional `#n` (unstable across scans; only for anonymous items, none in the current catalog).
- **Item ID** — `ruleID/key`. Rule IDs are kebab-case (`[a-z0-9-]`, no `/`), so splitting on the *first* `/` is unambiguous even when the key itself contains slashes (paths do).
- Keys are filled once after scan (`Finding.FillItemKeys`); `BuildPlan` re-derives as a fallback so hand-built findings (tests, library callers) still match.
- Duplicate keys within a rule (e.g. `paths` + `discover` finding the same dir) select together — matching is by key equality, deliberately not uniquified.

## Selection atoms

`BuildPlan`'s signature is unchanged: `map[string]bool` now holds **atoms** — `"rule-id"` (whole rule, existing behavior) or `"rule-id/key"` (one item). `nil` still means everything; an empty map still means nothing. The planner stays the single enforcement chokepoint:

- surface-only rules skip regardless of atom shape (invariant 5);
- **whole-rule native commands** (`go clean -cache`) cannot honor a partial selection → planned only when the selection covers *all* items, otherwise a `Skip` with reason. Never over-delete relative to selection.
- per-item native commands and trash rules plan exactly the selected items.
- selectors that match nothing land in `Plan.Unmatched` (new field): `clean` refuses to run, `plan` warns — a typo'd UDID must not silently degrade to "nothing to clean".

`Rule.PerItemActionable()` (actionable ∧ (no native command ∨ per-item placeholders)) is the one derivation the planner and the TUI both consult for "can items be toggled individually".

## TUI

Collapsed per-rule rows stay the default. `l`/`→` expands the rule under the cursor, `h`/`←` collapses (from an item row too). Item rows: checkbox (only when `PerItemActionable` and the key is non-empty), label, size, risk badge (rule's — per-item risk arrives with G2's classifier), last-used. Footer on an item row shows the full item ID (copy-paste into `regrow clean`), size, unused days, and the rule's note/regen.

Internal selection state is **item atoms only**; the rule checkbox is derived (`[x]` all / `[~]` partial / `[ ]` none). Space on a rule row = all↔none. Surface-only rules expand for inspection but nothing is toggleable.

## Refits (the proof)

- **sim-devices / sim-runtimes** — already per-item (`{arg}`); gain keys + TUI rows for free.
- **xcode-archives** — path becomes glob `~/Library/Developer/Xcode/Archives/*/*.xcarchive`: per-archive rows with real sizes and mtimes, per-archive trash actions. Empty date directories left behind are cosmetic. (Glob expansion is an existing mechanism — no new data source.)
- **tm-snapshots** — `tmutil thinlocalsnapshots / N 4` (whole-rule, system picks) → `tmutil deletelocalsnapshots {arg}` with the date parsed from the snapshot name; the date also supplies `LastUsed`. Semantic change thin→delete-named, logged in ARCHITECTURE.md. Sizes stay unknowable pre-delete (0 bytes).

## Holes considered

| Hole | Answer |
|---|---|
| Partial selection on whole-rule command over-deletes | Planner skips with reason; TUI disables item toggles there |
| Typo'd selector silently plans nothing | `Plan.Unmatched`; `clean` errors, `plan` warns |
| Item vanished between scan and clean | Executor already records per-action failure and continues |
| Keys with spaces/slashes | First-slash ID split; shell quoting is the user's, `--json`/TUI footer give exact strings |
| Empty key (all of Arg/Path/Label empty) | Positional `#n` fallback; none in current catalog |
| Duplicate keys in one rule | Toggle/plan together; documented, not uniquified |
| Surface-only via item atom | Same planner skip as rule atom |
| Rule atom + item atom for same rule | Union — rule atom wins (full) |
| Oplog compat | `item_key` additive, old lines parse fine |
| Golden drift | Only tm-snapshots + xcode-archives goldens change (reviewed) |

## Deliberately not done

- `Item.Risk` / `Item.Note` fields — G2's classifier and G's model metadata are the first real producers; adding dead fields now proves nothing.
- In-TUI execution — unchanged scope, plan screen still points at `regrow clean`.
