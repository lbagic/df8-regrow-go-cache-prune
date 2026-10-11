# Product Plan — "regrow"

*Synthesis of docs/research/01–03, 2026-07-13.*

## 1. Strategic verdict

Do NOT build "a better Mole." Mole (58.8k stars, famous author, $19 companion app, weekly releases) owns "terminal dev+system cleaner for macOS." Attacking that head-on = worse distribution against a beloved incumbent.

Build instead: **the disk tool that knows what everything is, what regenerates, and how — safety-obsessed, dev-deep, cross-ecosystem.** Occupy the three unowned gaps:

1. **ML/AI model caches** — HuggingFace / Ollama / LM Studio / ComfyUI. Biggest (10–400GB), fastest-growing, zero mature tools, and naive `rm` corrupts HF's blob/symlink dedup. This is the wedge and the launch headline.
2. **Global/daemon caches nobody unifies** — Docker host-reclaim (the "pruned but disk still full" phantom), Gradle/Maven, Android SDK/AVDs (no tool exists at all), JetBrains/VSCode leftovers, Xcode CoreSimulator (outside `~`, invisible to every home-dir scanner).
3. **Safety as the brand** — dry-run default, trash-not-rm, undo log, semantic labels ("this 14GB regenerates via `cargo build`, ~10 min"), correct paths where even blogs are wrong, danger-case aware (Android keystores, HF symlinks, Archives dSYMs, yarn zero-install).

Answer to "why won't tw93 add this in a weekend": depth (dedup-aware model handling, per-tool native commands, phantom-space semantics), a safety architecture a broad optimizer can't bolt on (trash/undo/golden-test rules), and **cross-platform** — Mole is macOS-only; ML devs live on Linux too.

## 2. Positioning

**Broad product, narrow headline.** The product is a full all-around cleaner (owner uses it personally for everything: system junk, wallpapers, trash, dev caches). The *marketing* leads exclusively with the wedge: AI/ML model caches, `doctor` hero-bug scans, phantom-space explainers, "knows what regenerates" labels. Launch story is never "another mac cleaner" — it's "found 143GB from one Apple bug." Breadth = retention; wedge = acquisition + moat. Guardrail: README/UI always list wedge categories first; generic junk cleaning second.

- Tagline direction: *"Know what's eating your disk — and what's safe to take back."*
- Anti-CleanMyMac, anti-snake-oil: never nags, never exaggerates, shows its work, every rule auditable in the open repo.
- The number the product optimizes: **GB reclaimed with zero regret.**

## 3. Product pillars

1. **Semantic inventory, not bytes.** Every finding = {what it is, why it exists, what breaks if removed, how it regenerates, regen cost (time/network), last used}. Analyzers (dust/gdu/DaisyDisk) show bytes; we show meaning.
2. **Three handling classes, enforced by architecture:**
   - Auto-clean (Safe): regenerable caches.
   - Review-and-select (Caution): shown with cost, native command used.
   - Surface-only (Expert/user data): VM bundles, model libraries, iOS backups, Downloads, Messages media — size + pointer to the right tool, never deletable through us.
3. **Reversible by default.** Dry-run default → trash (not rm) → ops log → `reclaim undo`. `--rm` for the brave.
4. **Native commands first.** `hf cache delete`, `ollama rm`, `pnpm store prune`, `go clean -modcache`, `simctl runtime delete`, `docker builder prune` — raw rm only where no steward exists.
5. **Rules as open data.** Each target = a declarative YAML rule (paths per OS/version, marker files, native command, risk, regen story) + tiny amount of code for the weird ones (Docker df parsing, HF scan). Community PRs a rule, not code. Golden tests run every rule against fixture homes.
6. **Phantom-space explainers.** TM snapshots, Docker VM files, APFS purgeable — own category with "why Finder still shows full" copy. Nobody explains this; everyone googles it.
7. **Hero-bug scanners.** Claude Code debug-loop (100–200GB), mediaanalysisd leak (≤143GB), Spotlight runaway (233GB observed), Playwright transform cache (26GB). Cheap checks, massive "found X GB" screenshots — the viral loop.

## 4. UX sketch (TUI)

```
reclaim                     scan → inventory, grouped, size-ranked
──────────────────────────────────────────────────────────────
  DEV CACHES                                    41.2 GB safe
  ▸ [x] Go build cache          20.1 GB  safe   go clean -cache · rebuilds ~min
  ▸ [x] Yarn cache              11.0 GB  safe   re-downloads on install
  ▸ [ ] Go module cache         17.3 GB  caution re-download all modules
  AI MODELS                                     38.4 GB review
  ▸ [ ] ollama llama3:70b       39.1 GB  unused 84d · `ollama rm`
  PHANTOM SPACE                                 ~46 GB
  ▸ Docker VM file: 34 GB real (1 TB sparse) — prune reclaims inside; VZ auto-shrinks
  SURFACE ONLY
  ▸ iOS backup (iPhone 14, 2025-11-02) 23 GB — Finder ▸ Manage Backups
──────────────────────────────────────────────────────────────
  space toggle · enter plan · d dry-run diff · u undo last run
```

- First run ends on a **plan screen** (exact commands + trash destinations), not a deletion.
- `reclaim --json` for scripting/CI; `reclaim doctor` = hero-bug scan only; `reclaim watch` (later) = menubar/agent size watchdog.

## 5. Scope

**v0.1 (validate, 2–3 weeks of evenings):** Go + Bubble Tea skeleton, rule engine + YAML catalog, scan+dry-run+trash+undo, ~25 Tier-S/A rules: Xcode (DerivedData, DeviceSupport, CoreSimulator devices+runtimes via simctl, archives surface), Docker (prune + VM-file explainer), JS (npm/yarn/pnpm/node_modules discovery), Go, Homebrew, pip/uv, TM snapshots, aerial wallpapers, Trash. macOS only. Ship to brew tap, use it ourselves.

**v0.2 (the wedge):** AI models module done right — HF scan-cache integration (dedup-aware sizes, last-used), Ollama, LM Studio detection, ComfyUI surface-only; hero-bug scanners; `doctor`. This is the Show HN release.

**v0.3:** Android (SDK/AVD via sdkmanager/avdmanager, keystore protection), JVM (Gradle deps/wrapper-dists/daemons, Maven), JetBrains/VSCode/Cursor, Rust (target discovery via CACHEDIR.TAG + cargo-sweep-style mtime ranking), Python venv discovery. Linux support (XDG paths already in catalog).

**v1.0:** notarized binaries, npx distribution, homebrew-core, GUI companion decision.

**Explicitly out (v1):** duplicates (Czkawka owns it, false-positive risk), app uninstaller (AppCleaner/Mole own it), Windows, localization stripping (breaks signing), auto-scheduled deletion.

**Large files / games / docs:** large-file + old-Downloads finder ships as Review/Surface category (age+size heuristics, never auto); game caches = low priority on macOS (small wins) — Steam shader cache rule only.

## 6. Architecture notes

- Go + Bubble Tea + Lip Gloss; GoReleaser; single static binary.
- `rules/*.yaml` embedded at build; `--rules-dir` override for development and community testing.
- Engine walks: known paths (per-OS resolution incl. version-dependent paths from research §14) + marker-file discovery (CACHEDIR.TAG, pyvenv.cfg, node_modules) + tool queries (docker df, simctl list, hf scan-cache, ollama list).
- Every destructive action: path guard (never empty//`/`/$HOME/mount roots), trash via Finder API (`osascript`/`trash` syscall) with fallback staging dir, journaled to `~/.local/state/reclaim/oplog.jsonl`.
- Tests: fixture $HOME builder + golden dry-run snapshots per rule; CI matrix macOS/Linux.
- Telemetry: none. Optional `reclaim stats` prints local-only lifetime GB reclaimed (nice for screenshots).

## 7. Monetization

- MIT core CLI. GitHub Sponsors + Ko-fi from day one (tip jar, expect $0–500/mo).
- Real revenue path (post-traction): **paid notarized GUI companion** (Mole-proven, $15–25 one-time) — visual treemap fed by same engine, scheduled scans, menubar watchdog. Keep engine + rules open; sell convenience, not secrets.
- Skip Mac App Store (sandbox). $99/yr Apple Developer for notarization when distributing binaries.

## 8. Risks

- **Mole adds ML cleaning** → our moat must be depth + safety + Linux, not feature list.
- **Path rot** (Apple/tools move dirs yearly) → rules-as-data + community PRs + CI fixtures per OS version; research §14 list seeds a "path changelog" discipline.
- **One bad deletion story kills the product** → trash+undo default, conservative Safe class, golden tests, staged rollout of new rules behind `--beta-rules`.
- **Scope creep into CleanMyMac clone** → pillars 1–3 are the fence; "surface-only" exists so we never touch user data.

## 9. Name candidates

`reclaim` (verb, honest) · `hoard` · `disksmith` · `regen` (theme: everything we delete regenerates) · `sweeper`. Check brew/npm/crate collisions before committing.
