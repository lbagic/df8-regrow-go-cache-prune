# Attack plan — personal-first regrow

**Status:** rev 2 (2026-10-04) — revised after a three-critic blind review (data model, failure modes, premises). Implements the [direction doc](2026-10-04-direction.md) (D1–D6). One coordinator session executes it; each numbered PR below is one PR. The review's verbatim reports live outside the repo.

## Outcome

This Mac stops running out of space, in two regimes:

- **Slow regrowth** (days): the owner is warned before the disk fills, the Go build cache is trimmed under the agreed policy, and residue is listed with owners and regeneration stories.
- **Acute pressure** (minutes): a fast fall in free space or a swap jump raises an alert naming the top process.

Scans never hang. Every total says which bucket it belongs to: frees now, frees after the Trash is emptied, shown only, macOS-managed.

## Phase 0 — owner checklist (minutes, not a PR; first checkpoint)

Nothing in PRs 1–3 frees a byte, and the disk loses ~14 GB/day in bursts. Before the build starts, the owner (or the coordinator, with the owner's yes for each line) does:

1. `go clean -cache` (55 GiB; regrows).
2. Decide `-trimpath`: globally (`go env -w GOFLAGS=-trimpath`) or only in the agent harness env. Trade-off: debugger/stack-trace paths become module-relative. Worktrees at the same code then share cache entries; 149 cache files over 100 MB are ~24 copies each of six packages.
3. Stop the aerial video downloads. They are not a shuffle setting: on macOS 15.7, opening System Settings → Wallpaper makes `idleassetsd` batch-download the aerial catalogue (about 137 videos at roughly 0.5 GB each) even with a still wallpaper, and quitting System Settings does not stop a batch that is already running (`/Library/Application Support/com.apple.idleassetsd/Customer` went from 5 MB to 27 GB in about 90 minutes). `sudo launchctl disable system/com.apple.idleassetsd` and `sudo killall idleassetsd`, then remove the videos; whether SIP lets the disable through is untested. Until then they come back whenever the Wallpaper pane is opened.
4. Cap Docker Desktop's VM memory (today's jetsam event's largest process was the Docker VM at 8 GiB resident on a 16 GiB host).
5. Reboot when convenient (clears ~15 GiB of agent scratch in `/private/tmp`).

Record the before/after free space in the PR 1 description.

## Fences (every PR)

- **Safety invariants** (ARCHITECTURE.md) hold. The single sanctioned exception is the go-build prune in PR 4; it crosses invariant 1 (no per-run opt-in once autopilot is on), invariant 2 (direct delete, no Trash) and invariant 4 (raw delete, no steward command), and its decisions-log entry and amended invariant text name all three.
- **Tests never touch the owner's data.** Executor, Mover, Journal, `RunNative` and the state dir are injected. Tests that execute use a synthetic catalog of trash-only rules rooted in a temp home — never `LoadEmbedded` — a `RunNative` that fails the test if called, and `XDG_STATE_HOME` in a temp dir. Selection logic is tested as pure functions, never through `run([]string{...})`, which scans the real `$HOME`. `Host.Root` re-anchors paths only, never native argv.
- **Nothing is deleted on the real machine** during development, except Phase 0 lines the owner approves and the owner-checkpoint runs below.
- **Disk headroom for the build itself:** check free space before each phase and stop below 20 GiB. Subagent worktrees each compile Go and SwiftPM writes `.build`; set `GOFLAGS=-trimpath` in subagent shells regardless of the owner's global choice.
- **Memory-light:** stream large data; after background jobs, check for orphaned processes (ppid 1) and kill your own.
- **Public repo:** no client project names or personal paths in code, fixtures, commits or docs; recorded docker/git/plist output is scrubbed or synthetic.
- **Commits and PRs** are authored as the owner with no AI attribution or trailers. Every PR gets `/code-review` via an Opus agent before its last push, then babysitting until CI is green and bot threads are answered.
- `go build ./... && go vet ./... && go test ./...` green; every new or changed rule keeps its golden fixture. The Go engine stays CGO-free.
- **CLT-only Swift** (no Xcode): no XCTest (use swift-testing), no `#Preview`, no asset catalogs/xibs (`actool`/`ibtool` need Xcode; icons are `iconutil` .icns or SF Symbols), no SwiftPM resources (`Bundle.module` falls back to `.build` paths and breaks after `swift package clean`; copy the page into `Contents/Resources` or serve it from the engine's `go:embed`).
- Tick `docs/PLAN.md` Status per merged PR and keep its "Now:" line current.

## Data shapes (pin these; do not invent alternatives per PR)

```go
// internal/scanner/size.go — the one definition of size and recency
type Usage struct{ Bytes int64; Newest time.Time; Partial bool }
func DirSize(ctx context.Context, path string) (Usage, error) // error is ctx only

// internal/engine/finding.go — Item stays pure scan output
type Item struct {
	Path, Label, Key, Arg string
	Bytes    int64     `json:"bytes"`              // whole item, nested items included
	LastUsed time.Time `json:"last_used,omitzero"` // Usage.Newest (files AND dirs), or tool-reported
	Partial  bool      `json:"partial,omitempty"`  // Bytes/LastUsed are lower bounds: something refused or blocked
}
// Finding.Err stays for rule-level failures only (tool missing, unknown query).

// internal/engine/account.go — containment is computed, never stored
type Totals struct{ FreesNow, AfterTrash, ShownOnly, MacOSManaged int64; Partial int }
type Ledger struct {
	Exclusive map[string]int64 // item ID → Bytes − Σ Bytes of direct children
	Totals    Totals
}
func Account(findings []Finding) Ledger
func DefaultSelection(findings []Finding) map[string]bool // safe ∧ items ∧ no Err; every face calls this
```

- **Containment forest:** an item's parent is the nearest item from any rule whose path strictly contains it; on equal paths the later rule in catalog order is the child. Every measured byte lands in exactly one item.
- **Buckets** derive from the rule, no new field: native command → FreesNow; trash rules → AfterTrash (a Finder move frees nothing until the Trash is emptied); surface-only → ShownOnly; phantom-space category → MacOSManaged.
- **Planner uses the same forest:** an item whose ancestor is also selected is skipped ("inside X, also selected"); an actionable item with a surface-only descendant is refused ("contains Y, which regrow never deletes"); `Plan.TotalBytes` is the union.
- **Schema:** `empties_trash: true` (validated: native command, not per-item, not `safe`) — the planner orders such actions **first**, so this run's trash moves stay restorable. `prune:` block (PR 4). `Action.Sudo bool`.
- **Doctor verdict** is three-way: flagged (lower bound already over the line), normal (complete and under), unknown (partial and under).

## PR 1 — Planner safety (branch from `plan/personal-first`; no prototype code)

B5 is live on `main` through `regrow clean` and the TUI.

- `empties_trash` on `trash-empty`, ordered first.
- The containment forest in `BuildPlan` (subsume nested selections, refuse surface-only descendants, union total).
- `engine.DefaultSelection`; `plan` with no ids and the TUI both call it. Empty or missing ids in any programmatic call mean an empty plan, never "everything".
- Plan screen and `clean` summary show FreesNow / AfterTrash subtotals and per-action reversibility; remove the blanket "Trash first, undo restores" copy (`tui.go` plan footer, `clean.go` total line).
- `regrow help` / `--help` prints the subcommand usage.

Done when: a multi-rule plan test (the golden harness scans one rule per subtest, so this is a new test) shows `trash-empty` first; a fixture run of trash-empty + one trash move leaves the move restorable by `Undo`; tests for subsumption, surface-only refusal, union total, empty-ids → empty plan.

## PR 2 — Scan correctness

Port `ScanStream` and the 4-worker `DirSize` from `proto/web-serve` commit `93e0f1f`, then fix what the prototype got wrong:

- **Every open is bounded**, not just the root: the walker's opener is injectable; a per-item watchdog abandons a walk whose directories stop completing for N s and returns the bytes so far with `Partial: true`. Same bound for glob expansion parents, discover walks and the hf/ollama walkers. Remember blocked paths per process (no re-probing, no goroutine pile-up); probes honour ctx. The message says "blocked or unreadable (Full Disk Access may be needed)", not a certain diagnosis.
- `Usage` / `Item.Partial`; a blocked root is `Item{Path, Partial: true}`, not `Finding.Err`. `LastUsed` = newest mtime over files and directories (npm stamps 1985 on package files). `hfLastUsed` uses `Usage.Newest`.
- `Account` and the four buckets in scan text, TUI and `--json`; display "unreadable" (partial, ~0 B) vs "≥ X, some folders unreadable".
- Doctor three-way verdict.
- `node-modules-dirs` `max_depth` 6 → 10.
- Docker build-cache figure (B7): find why the rule claims 1.6 GiB unused-30d+ while `docker system df` says 0 reclaimable; fix the query or the label; note the cause in the decisions log.

Done when: injected-blocking-opener tests at depth ≥ 2 and on a glob parent, asserting the scan **returns** and the process exits; three-level nesting fixture (exclusive(A) = A − B) and an equal-path fixture; a two-actionable-rules fixture for totals (a surface-only container would pass trivially); `chmod 000` fixture with `t.Cleanup` restoring mode; full `regrow scan` wall time before/after recorded in the PR; node_modules count compared with a `find` that mirrors discover's excludes.

## PR 3 — Engine protocol (`regrow engine`, documented in `docs/ENGINE.md`)

JSON lines over stdin/stdout, written by one goroutine fed from a buffered channel (a slow reader never blocks the scan or `cancel`).

| Request | Events (each carries `re` = request id) | Terminal |
|---|---|---|
| process start | `hello{protocol:1, version, fda}` — `fda` from a bounded `readdir` of a classic TCC path (`~/.Trash`, `~/Library/Safari`), never an app container | — |
| `scan{id}` | `headroom{Sample}`, `start{scan_id, rules}`, per rule `finding{scan_id, index, took_ms, finding}` + `summary{scan_id, totals, exclusive}` | `done{elapsed_ms, canceled}` |
| `plan{id, scan_id, select?}` | `plan{plan_id, plan, totals}`; sudo actions already Skips ("needs administrator rights — run `regrow clean <id>` in Terminal"); no `select` → `DefaultSelection`; empty `select` → empty plan | `done` / `error` (unknown, canceled or superseded scan) |
| `execute{id, plan_id}` | `journal{entry}` per oplog append, emitted after the sync | `done{result}`; plan consumed |
| `cancel{id, target}` | target ends with `done{canceled:true}`; an execute stops between actions | `done` |
| `tick{id, autotrim}` | `tick{Tick}` (PR 4) | `done` |

Rules: one scan or execute in flight (`error{busy}` otherwise); a new scan supersedes plans from older scans; `plan_id` is the oplog run id, unguessable, single-use, expires after 10 min; on stdin EOF cancel a scan but let an execute finish and journal its current action, then exit; request lines up to 1 MiB, longer → `error`; native commands run through an engine `RunNative` with `/dev/null` stdin and captured stdout/stderr (tail into the journal's error) — never the TUI's inherited stdio; `regrow scan --json` emits the same lines minus `hello`.

Done when: an in-process test drives scan → plan → execute over pipes against a synthetic trash-only catalog (fences above) and checks the event sequence and oplog; tests for cancel-then-rescan, empty `select`, double execute of one plan, an undrained reader, EOF mid-execute, an over-long line, a native command that writes to stdout and reads stdin.

## PR 4 — Headroom and prune, Go only (the piece that stops the crunch)

No Swift, no Full Disk Access needed (`go-build` and statfs are not TCC-gated); runnable from the terminal if the menubar slips.

```go
// internal/headroom
type Sample struct {
	At        time.Time `json:"at"`
	Total     int64     `json:"total"`
	Free      int64     `json:"free"`      // statfs f_bavail (df); thresholds use this
	Purgeable int64     `json:"purgeable"` // existing APFS query
	SwapUsed  int64     `json:"swap_used"`
}
func Take(ctx context.Context) (Sample, error)
func Append(path string, s Sample) error          // ~/.local/state/regrow/headroom.jsonl, 30 days
func Tail(path string, since time.Time) ([]Sample, error) // skips malformed lines (ENOSPC partial writes)
func Forecast(s []Sample) (daysToFull float64, ok bool)    // window resets at upward jumps; ≤ 72 h
func Alerts(prev, cur []Sample) []Alert                     // crossings only
type Tick struct { Sample; DaysToFull *float64; Alerts []Alert; Pruned *PruneResult }
```

- Alerts are crossings with hysteresis: free-space bands 50/25/10/5 GiB, days-to-full crossing 3, swap up ≥ 4 GiB, and acute: free fell > 5 GiB within 30 min (name the top process by resident memory). No `used` field (on APFS `total − used ≠ free`). `headroom.jsonl` is not the oplog.
- **Prune** as a rule block, executed as an ordinary plan action:
  ```yaml
  # rules/go-build-cache.yaml
  prune:
    min_age: 48h
    keep_under: 15GiB
    unless_running: [go, compile, link]
  ```
  `PrunePlan` walks the cache once into an hourly mtime histogram (memory O(hours)), picks cutoff T where oldest-first bytes reach `min(cache − keep_under, target − free)` with T ≤ now − min_age, and emits `Action{Kind: "prune", Command: find <GOCACHE> -mindepth 2 -maxdepth 2 -type f -name '*-[ad]' ! -newermt <T> -delete, Bytes: estimate}` (find re-stats each file, so entries used since planning survive). The planner refuses unless `<GOCACHE>/README` carries Go's cache header and the path came from `go env GOCACHE` under the owner's login environment.
- Stop on bytes against the target computed up front; report statfs before/after separately (TM snapshots can hold freed blocks). Single-flight via flock on a state file. Journal start and done/fail with actual count and bytes. If a build is running, defer; after 2 h of continuous deferral with free < 10 GiB, prune anyway (entries used in the last 48 h are spared by the cutoff regardless).
- `regrow tick` (one sample + alerts, printed) and `regrow prune go-build` (dry-run by default, `--yes` executes). `tick{autotrim:true}` refuses until the oplog holds a completed prune of that rule, so "on only after one manual run" is enforced by data.
- Decisions-log entry + amended invariant text (1, 2, 4).

Done when: unit tests for forecast (including a jump fixture), alert crossings and hysteresis, the tolerant reader, `PrunePlan` on a fixture cache with controlled mtimes and decoys (`README`, `trim.txt`, `testexpire.txt`, non-matching names), the README/GOCACHE refusal, the deferral rule; **owner checkpoint**: the owner runs `regrow prune go-build`, reviews the dry run, then `--yes` once.

## PR 5 — Menubar shell (CLT-only SwiftUI)

- **Signing** (`scripts/signing-cert.sh`, owner asked Claude to script it): creates a self-signed code-signing identity "regrow local signing", validity ≥ 10 years, via `/usr/bin/openssl` (LibreSSL PKCS#12 that macOS imports) and `security import … -T /usr/bin/codesign`; records its SHA-1 in a gitignored local file; a re-run is a provable no-op (same hash); `just app` signs by that hash, never by name; codesign runs under a timeout. **Tell the owner before running it** — the first key use raises a keychain dialog; they click "Always Allow".
- **Environment:** apps launched by LaunchServices get `PATH=/usr/bin:/bin:/usr/sbin:/sbin` (docker, go, brew vanish; `GOCACHE`, `XDG_STATE_HOME`, `HF_HOME` differ). The app captures the login shell's environment once (`$SHELL -lic env`, bounded) and passes it to the engine; a tool missing from that PATH is a visible state, not "does not apply".
- **App:** `MenuBarExtra` shows free / purgeable / swap and days-to-full; a timer (15 min and on wake) sends `tick` and posts the returned alerts via `UNUserNotificationCenter`; the autotrim toggle sends `tick{autotrim:true}`. "Scan…" opens a window: `WKWebView` with the prototype page rewired to the engine protocol, navigation denied after the initial load. Execute behind the TUI's double confirm. FDA state from `hello.fda`, with a button opening the Full Disk Access pane and "relaunch to apply". Staging bytes shown with a way to empty them.
- **Install:** `just app` builds, quits a running Regrow, waits for its engine to exit, swaps the bundle atomically into `~/Applications`, registers `SMAppService.mainApp` only from there.

Done when: `just app` builds from the CLT only; the app launches into the menu bar; a scan streams; ticks produce a notification; a **non-destructive smoke test through the real bundle** trashes a sentinel file the test created under `$HOME` and undoes it (proves Finder Automation consent, oplog, receipt, undo); after the owner grants Full Disk Access, a rebuild keeps it (a scan still reads `~/.Trash`).

## PR 6+ — Coverage for this machine (one rule family per PR, any order after PR 2)

Catalog test first: no two rules share a literal path or a discover `name` (one owner per byte).

1. **Agent sessions** (largest regrowing owner here): scratch dirs under `/private/tmp/claude-<uid>/` keyed by session id; live = a running process for that session or transcript activity within N hours; dead sessions' scratch → caution. Their worktrees belong to rule 2.
2. **Git worktrees**, whole-worktree removal only: merged (ancestry, or upstream gone) + `git status --ignored --porcelain` shows nothing beyond allowlisted build dirs + idle ≥ 7 days → `git -C {path} worktree remove {path}` (not `-C {arg}`: `Arg` is the item key, and a shared repo arg would make all worktrees of a repo one item).
3. **Build output** (`.next`, `dist`, `.turbo`, `.venv`) in projects idle N days, one rule; only `git check-ignore`d dirs (tracked `dist/` exists). `node_modules` stays with `node-modules-dirs`, `target` with `rust-target-dirs`.
4. **Docker unused images** (no container uses them, older than N days).
5. **Toolchains:** Node versions nothing references (guard versions named in LaunchAgent plists, `.nvmrc`, `.node-version`); Android NDKs/SDK with no Studio; simulator runtime with no Xcode (root-owned → surface-only with instructions).
6. **App leftovers** (needs the app's Full Disk Access to be meaningful): `~/Library/{Application Support,Containers,Group Containers,Caches,…}/<id>` with no installed owner — enumeration covers `/Applications`, `~/Applications`, `/System/Applications`, `PlugIns/*.appex` ids and installed apps' `application-groups` entitlements; idle ≥ 90 days → caution, to Trash. Fuzzy matches are a separate surface-only rule (risk is per rule).
7. **Small fry:** installers in `~/Downloads` for apps already installed, npx cache, stale macOS installer data (surface + how), browser on-device models (surface).
8. **Fix-the-cause rows** (doctor): aerial videos present or still downloading in `idleassetsd`'s folder (the Wallpaper pane triggers the download, not a shuffle setting; see Phase 0 line 3), many Go worktrees without `-trimpath`, Docker VM memory cap and build-cache limit unset. Each row names the cause and the owner's fix; `regrow doctor` changes nothing.
9. **regrow-trash expiry** (later, owner checkpoint): permanently delete items regrow itself trashed more than N days ago, from oplog receipts — frees space without ever emptying the owner's own Trash.

## Owner checkpoints

Phase 0 · before the signing script runs (keychain dialog) · first `regrow prune go-build --yes` · first real execute from the app (Finder Automation prompt) · enabling autotrim · granting Full Disk Access to `Regrow.app`.

## Out of scope

Launch work (Prompt I, M2 publish, notarization, brew), Linux, TUI improvements beyond PR 1's copy fixes (frozen), Electron/Tauri, `getattrlistbulk` and a persistent FSEvents index, per-file clone/hardlink-aware sizing (noted: pnpm hardlinks and APFS clones make path-unique bytes an overestimate for some build dirs).
