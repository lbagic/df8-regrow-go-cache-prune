# Direction after the blind reviews

**Status:** D1–D3, D5, D6 decided (2026-10-04; go-build policy: 48 h floor, confirmed by the owner); D4 proposed and refined in the [attack plan](2026-10-04-attack-plan.md). Prototype on branch `proto/web-serve`.

Four blind agents looked at this machine and at regrow without reading the repo's code, docs or rule catalog: a disk-usage inspector, a residue/growth inspector, a product reviewer using the binary (dry-run only), and a first-principles product designer. Their raw reports name private projects and paths, so they live outside the repo. This doc keeps the generic findings and the decisions.

## The problem, measured

The owner "runs out of memory every now and then". On this machine that is disk, and the two are coupled:

- Free space on the 460 GiB Data volume: 112.8 GB (09-28, from DiagnosticReports) → 31 GiB (10-04 10:08) → 11 GiB (10-04 11:05).
- The last drop was swap: a runaway 28 GB process — an orphaned analysis script left by this review's own agents, not the owner's workload — pushed swap from 4 to 23 GiB in under an hour, and swap files live on the same disk. The owner's own acute event on record today is a jetsam report whose largest process was the Docker VM (8 GiB resident on a 16 GiB host). When the disk is near full, swap cannot grow and macOS reports "out of application memory". Low disk and memory pressure feed each other, so free-space *headroom* is the thing to protect.

Where the space goes, ranked by how much it explains the recurring crunch:

| # | Consumer | Size | Why it recurs |
|---|---|---|---|
| 1 | Go build cache | 55 GiB, every byte < 5 days old | Bursts of builds across many git worktrees. Go keys cache entries by absolute package dir unless `-trimpath` is set, so each worktree recompiles the main module (24 copies of one 437 MB package seen). Go trims entries after 5 days unused, so the cache oscillates instead of growing forever. |
| 2 | Swap | 4 → 23 GiB today | Memory pressure from runaway processes; directly converts RAM pressure into disk loss. |
| 3 | Aerial wallpaper videos | 44.3 GiB | Shuffle downloads everything (79 files in 2 hours on 09-26, ~1/day since). Reviewers disagree on whether macOS treats it as purgeable: purgeable total (43.8) matches it and idleassetsd is a purgeable provider, but the files carry no per-file purgeable flag. Fix is a setting, not a delete. |
| 4 | Agent scratch in `/private/tmp/claude-*` | 14.6 GiB, 768k files | Accumulates between reboots; nothing else clears it. |
| 5 | One-off residue | ~45 GiB | Simulator runtime with no Xcode (12), Android SDK with no Studio (11.2), stale macOS installer data (10.6), data of an uninstalled editor (5.6–7.2), browser on-device model (4), 15 Node versions (4.3). |
| 6 | Worktrees and project build output | ~15 GiB | One agent-heavy repo has 243 registered worktrees (78 merged); `node_modules`, `.next` per worktree. |
| 7 | Docker | ~9–13 GB reclaimable | Unused (non-dangling) images, unused volumes, old build cache. |

The takeaway: most of the space *regrows*. A one-off clean does not hold; the product has to watch headroom and act on the regrowing classes, and separately surface the one-off residue nobody owns.

## What the product review found

Right: per-row sizes are accurate, the regeneration stories are the best part, and the safety core (trash, oplog, undo) is sound.

Bugs, each reproduced by the reviewer:

- **B1 — scan hangs forever.** Without Full Disk Access, `open(2)` on `~/Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches` blocks indefinitely (a plain `ls` blocks too; the parent dir opens fine). The TUI spinner never ends — this is the "not loading" report. *Fixed on the prototype branch:* a 3 s open probe turns it into an error row.
- **B2 — the headline total double-counts.** `~/Library/Caches` (69.8) contains go-build, Yarn, Playwright, Homebrew, pip (62.6); `Docker.raw` contains the docker rows; APFS purgeable ≈ aerials. Claimed 279.8 GiB vs ~164 GiB distinct vs ~67 GiB actually freed today.
- **B3 — permission failures render as `0 B`** (Trash, Spotlight); `doctor` prints them as healthy.
- **B4 — "last used" is the directory mtime**, so a cache written yesterday can read "unused 225d".
- **B5 — Empty Trash is ordered mid-plan**, so it would permanently delete what the same run just trashed. Safety bug; also the "Trash first, undo restores" footer is false for ~113 of 118 GiB (native commands).
- **B6 — `node_modules` discovery depth cap** misses 26 of 58 dirs (3.7 GiB).
- **B7 — docker build cache** claims 1.6 GiB where `docker system df` says 0 reclaimable.
- **B8 — `--help`** prints "Usage of scan:" with no subcommand list; CLI `plan` and the TUI preselect different things.

## Decisions

**D1 — No rewrite. Keep Go.** (decided, evidence)
The scan is bound by kernel file-metadata work, not the language: C `du` 9.6 s vs sequential Go 11.6 s on the same 411k-entry tree; the system/user CPU split of a full scan is 35 s / 6 s. Parallel walking within a tree is the lever: 4 workers → 3.2 s (8–32 no better). `getattrlistbulk` with 8 threads measured 1.9–2.7 s on that tree, a further ~25% — worth having later, not a reason to switch languages. One walk instantiates ~379k vnodes, more than the kernel keeps cached, which is why a second scan is no faster.

**D2 — Straight to a menubar app; no browser-tab stage.** (decided by the owner)
A SwiftUI `MenuBarExtra` shell owns the long-running loop (headroom watch, alerts, autopilot) and holds Full Disk Access as a stable .app identity; the Go binary ships inside the bundle as the engine and streams JSON lines over stdio (the prototype's SSE events map 1:1). Rich views can reuse the prototype page in a `WKWebView`. Buildable with the Command Line Tools alone (Swift 6.1, SwiftUI/AppKit/WebKit/ServiceManagement in the SDK) — no Xcode, which would cost ~40 GiB to install on a disk-starved machine. Signing: a self-signed code-signing certificate, so the TCC grant survives rebuilds (none exists yet). Electron ruled out on footprint; the localhost server stays a debugging aid at most.

**D3 — The product loop shifts from "cleaner you run" to "never run out".** (decided; owner confirmed the 48 h floor)
Watch headroom (free space and swap) and forecast days-to-full; alert before the crunch. Autopilot only the self-regenerating classes under explicit per-rule policy (e.g. go-build entries untouched > 48 h, scratch dirs of dead agent sessions), opt-in after the owner has run the rule by hand once. Everything else lands in a short review queue where every byte has an owner (app, repo/worktree, toolchain, container engine, OS, user) and residue is "owner gone or done".

Go build cache policy (reasoned 2026-10-04): Go itself deletes entries unused for 5 days (`trimLimit`, checked daily; use refreshes mtime at most hourly), so any age threshold ≥ 5 days — "a week" included — reclaims nothing. Measured age split of the 55 GiB: < 24 h 0.1, 24–48 h 13.4, 48–72 h 13.4, 3–5 d 28.1. Policy: never touch entries used in the last 48 h (active worktrees refresh theirs on every build, so only idle ones pay a one-off main-module recompile); trigger only on low headroom (free < 50 GiB or days-to-full < 3), deleting oldest-first until free space recovers or the cache is ≤ 15 GiB; skip while a `go` build is running. Go has no "trim older than" command, so this mirrors Go's own trim (direct delete, no Trash) — an explicit exception, for tool-owned purely regenerable caches only, to invariants 1 (no per-run opt-in once autopilot is on), 2 (no Trash) and 4 (no steward command); oplog-recorded with bytes. `-trimpath` attacks the cause: worktrees at the same code share entries (two-worktree test: +13 cache files without it, +4 with it).

**D4 — Coverage priority follows the table above, not the old catalog order.** (proposed)
New coverage, highest value first: worktrees (merged by ancestry or PR + clean + idle; delete only ignored build dirs when unmerged), agent scratch dirs with no live session, project build output in stale projects (`.next`, `dist`, `.turbo`, `.venv`, `target`), unused Docker images, toolchains nothing references (Node versions — guard versions hard-coded in LaunchAgents — Android NDKs, simulator runtimes without Xcode), app leftovers by bundle id (archive, don't delete), installers in Downloads, npx cache, stale macOS installer data. Plus a "fix the cause" channel: `-trimpath` for worktree-heavy Go repos, aerial shuffle off, Docker build-cache limit.

**D5 — Personal tool first.** (decided by the owner) Optimize for this machine's crunch; launch work (PLAN.md Prompt I, M2 publish, notarization, brew) is parked.

**D6 — Full Disk Access goes to the menubar app only** (decided by the owner): not to the terminal. macOS attributes a process's file access to the app that launched it, so the engine inside the app's bundle is covered by the app's grant; terminal scans meanwhile report protected folders as unreadable.

## Plan

1. **Stop the bleeding (owner, manual, now).** Kill runaway processes; `go clean -cache` (55 GiB, will regrow); try `GOFLAGS=-trimpath` in the worktree-heavy repos and watch whether worktrees start sharing cache; switch the wallpaper/screensaver off aerial shuffle; prune merged worktrees; reboot clears agent scratch. Expected: roughly +80–100 GiB of headroom on top of whatever the runaway's swap gives back.
2. **Correctness and trust** (fix B1–B8). Merge the open probe, the parallel walker and `ScanStream` from the prototype; dedupe nested paths in totals and show macOS-managed space outside them; explicit "unreadable — grant Full Disk Access" state; last-used from newest file mtime; Empty Trash always last; split the total into *frees now / after Trash is emptied / macOS-managed / regrows within N days*.
3. **Engine protocol**: `regrow engine` speaking JSON lines over stdio — scan stream, plan, execute — the contract the menubar shell consumes.
4. **Menubar shell**: free space, swap and days-to-full at a glance; scan window; execute behind the double confirm; launch at login; self-signed so Full Disk Access sticks. TUI frozen.
5. **Watch and autopilot** inside the shell: headroom sampling, alerts, the go-build policy above, agent scratch of dead sessions.
6. **Coverage** per D4, each new rule with its golden fixture.

Performance work rides along with 2–3: walk each subtree once (container rules subtract children instead of re-walking them), stream cheapest-first, later `getattrlistbulk` and a persistent index refreshed by FSEvents.

## Prototype verdict (`proto/web-serve`)

Question: does a localhost web UI feel better than the TUI and is it fast enough? Answer: yes on speed and information density — rows stream in as rules finish instead of a 28 s spinner, and the honest per-row "unique" size makes double counting visible. It deliberately has no execute endpoint, so the execute flow in a browser is still unproven. Run it with `just proto` (or `go run ./cmd/regrow proto-serve`).

## Owner answers (2026-10-04)

1. Straight to menubar → D2.
2. Autopilot go-build trim: "is a week better?" → no, a week is a no-op (D3); owner chose the 48 h floor.
3. Personal first → D5.
4. Full Disk Access: yes, but to the app, not iTerm → D6.
