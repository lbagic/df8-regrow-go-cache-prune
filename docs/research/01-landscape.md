# Competitive Landscape: macOS Disk-Cleanup & Dev-Cache Tools

*Research date: 2026-07-13. Stars/activity verified via GitHub API.*

**Bottom line:** The position "interactive terminal dev-cache cleaner, dry-run-first, cross-ecosystem, honest/no-BS" is **largely occupied by Mole (tw93)**: 58.8k stars, actively maintained, famous author (creator of Pake), already sells a $19 native app. The genuinely unowned whitespace: **ML/AI model caches**, **global/daemon caches beyond project directories** (Docker host-reclaim, Gradle/Maven, Android AVDs), and a **truly safety-first, size-ranked, semantically-labeled** selection UX.

## Verified hard data (GitHub API, July 2026)

| Tool | Stars | Last push | Maintained? | License | Scope |
|---|---|---|---|---|---|
| **Mole** (tw93) | **58,799** | 2026-07-11 | Very active | GPL-3.0 | Dev + system cleaner, TUI |
| Czkawka (qarmin) | 32,061 | 2026-07-09 | Very active | MIT/GPL | Duplicates/big files |
| dust (bootandy) | 11,981 | 2026-02-21 | Active | Apache-2.0 | Disk analyzer (read-only) |
| npkill (voidcosmos) | 9,386 | 2026-05-28 | Active | MIT | node_modules TUI |
| Stacer (oguzhaninan) | 9,295 | 2024-02-10 | **Abandoned** | GPL-3.0 | Linux-only optimizer |
| BleachBit | 6,273 | 2026-07-13 | Active | GPL-3.0 | Win/Linux privacy cleaner |
| dua-cli (Byron) | 6,022 | 2026-07-09 | Active | MIT | Disk analyzer + delete |
| gdu (dundee) | 5,811 | 2026-07-08 | Active | MIT | Disk analyzer + delete |
| docker-gc (spotify) | 5,019 | 2021-02-01 | **Archived** | Apache-2.0 | Docker GC (dead) |
| mac-cleanup-py | 2,372 | 2026-04-14 | Active | Apache-2.0 | Mac system+dev cleaner |
| kondo (tbillington) | 2,334 | 2026-04-24 | Active | MIT | Cross-ecosystem project cleaner |
| DevCleaner for Xcode | 1,594 | 2026-05-18 | Active | GPL-3.0 | Xcode GUI cleaner |
| cargo-cache (matthiaskrgr) | 989 | 2023-06-04 | **Stale** | MIT/Apache | Rust ~/.cargo cleaner |

## A) Commercial / freemium GUI

- **CleanMyMac (MacPaw)** — 800-lb gorilla. July 2025 restructure: Basic ~$47.50/yr or $119.95 one-time, Plus ~$71.40/yr or $195.95 one-time; also in Setapp ($9.99/mo, MacPaw-owned). Polished, Trustpilot 4.7. **Complaints are the market's opening:** "snake oil" skepticism, over-cleaning breakage, fake-urgency alerts, aggressive marketing, hostile billing. **No first-class dev-cache targeting.**
- **DaisyDisk** — $9.99 one-time, beautiful sunburst *analyzer* only; no cache knowledge, no dev awareness.
- **Sensei (Cindori)** — $29/yr or $59 one-time; performance monitor + thin cleanup. No dev caches.
- **OnyX** — free GUI over macOS's own maintenance scripts. High power-user trust — the honest-utility reputation model to emulate. Dated UX, no dev awareness.
- **AppCleaner** — free, trusted, uninstaller only.
- **PrettyClean** — tiny free Rust cleaner, the one consumer tool marketing "Developer Mode" (claims to be the only disk cleaner with developer options). Obscure, thin track record — a claim-stake, not a moat.
- **Disk Drill** — data recovery ($89) with bolt-on cleanup. Shallow.
- **Apple Storage Management** — free, but "System Data" opaque; **macOS never reclaims dev artifacts** (DerivedData, node_modules, Docker, Gradle, ML caches) — the gap that justifies the project.

Three tiers: free (OnyX, AppCleaner, PrettyClean, Apple) / cheap one-time ($10–60: DaisyDisk, Sensei) / premium (CleanMyMac $47–196). **No commercial GUI meaningfully targets dev caches.** Biggest category liability: snake-oil stigma from CleanMyMac; honest "here's exactly what regenerates" tool is the counter-position — where Mole already sits.

## B) Open source

- **Mole (tw93)** — most direct competitor, formidable. macOS-native, GPL-3.0, Shell ~81% + Go ~19%. "CleanMyMac + AppCleaner + DaisyDisk + iStat Menus in one binary." Interactive TUI (hjkl), live dashboard, `--dry-run`, operation logging, protected paths. Cleans node_modules, Rust target, Python venv, Xcode simulators, browser/app/system caches. **Monetizes: $19 one-time native SwiftUI app (mole.fit, lifetime, 2 Macs).** Weaknesses: broad whole-system optimizer → breadth risk — documented macOS 16 incident (WindowServer crash, root-owned config dirs from sudo), over-aggressive resets (Spotify/screensaver settings). Leans mac-system cleaner, not deep dev-cache specialist. macOS-only.
- **kondo** — closest on cross-ecosystem dev axis: Rust, cross-platform, 20+ project types, dry-run since v0.9, `--older 3M`. **Strictly project-directory-scoped** — zero awareness of global/tool caches. Discovery buggy (stops recursing at first project match; can report 0 bytes from $HOME). Confirm-prompt CLI, not size-first TUI.
- **npkill** — owns the canonical interactive-TUI cache UX (size-sorted, multi-select, regex filter, dry-run). **Node-only.** Complaints: slow deletion, blocks while deleting.
- **mac-cleanup-py** — 40+ hardcoded modules, `--configure` checkboxes, `-n` dry-run. Broad but scripted, not discovered size-ranked inventory.
- **DevCleaner for Xcode** — free MAS GUI, active. Xcode-only but **well-served** — hard to displace.
- **Analyzers (cache-blind):** dust / gdu / dua-cli / ncdu — show *bytes, not meaning*. Semantic labeling is the opening.
- **Czkawka** — 32k stars, duplicates/media, not dev-cache.
- **BleachBit** (Win/Linux only), **Stacer** (abandoned).

## C) Dev-niche: fragmented status quo

Polyglot dev must remember ~15+ commands across ~11 ecosystems:

- **Docker** — prune commands; VM image grows 20–200GB, doesn't return host space easily. Only dedicated tool archived. *No good tool.*
- **Rust** — cargo clean per-project; cargo-cache stale since 2023. target/ dirs 5–15GB each, 50–150GB scattered. *Partially served.*
- **Node** — fragmented cache commands (pnpm macOS path `~/Library/pnpm/store` — blogs cite Linux path). npkill owns node_modules.
- **JVM** — `gradlew clean` misleadingly cleans `build/` not `~/.gradle/caches` (10–50GB, no version eviction); `~/.m2` unbounded. Best tool stale (2022). *No good tool.*
- **Python** — pip/uv/conda/poetry scattered; uv cache at `~/.cache/uv` (commonly mis-documented), cases of 75–86GB. *No dominant tool.*
- **JetBrains** — version-suffixed caches 2–10GB each, 100GB extremes; config never auto-deleted.
- **VSCode** — workspaceStorage never GC'd (pathological ~100GB case). *Niche extensions only.*
- **Android** — AVDs 6–20GB each, SDK+AVD+Gradle regularly 100+GB. Danger: `~/.android` holds signing keystores. ***No Android-specific tool at all.***
- **Xcode** — 20–100+GB. *Well-served by DevCleaner.*
- **ML/AI** — `hf cache` (CLI renamed huggingface-cli→hf), `ollama rm`, LM Studio UI. Models 40–400GB; HF cache never evicts; weights duplicated across HF/Ollama/LM Studio, no dedup. Only brand-new unproven entrants. ***Biggest, least-served hog.***
- **Homebrew** — built-in command suffices.

**Hogs ranked:** (1) ML/AI caches, (2) Xcode 20–100GB, (3) Android 100+GB, (4) Rust targets 50–150GB, (5) JVM 20–60GB, (6) Docker 20–200GB. **No maintained dedicated cleaner:** Docker host-reclaim, JVM, Android, JetBrains, ML/AI.

## Positioning map

- Premium consumer suite: CleanMyMac. Visualization: DaisyDisk. Honest free maintenance: OnyX. Uninstall: AppCleaner.
- Terminal "replace the paid suite" dev+system cleaner: **Mole — dominant, monetizing.**
- node_modules TUI: npkill. Cross-ecosystem project-dir cleaner: kondo. Xcode: DevCleaner. Duplicates: Czkawka. Analyzers: dust/gdu/dua/ncdu.
- **Wide open:** ML/AI model caches, Docker host-reclaim, JVM, Android AVDs, unified global-cache view.

## 5 clearest unmet needs

1. **Unified tool spanning project caches AND global/daemon caches** in one size-ranked inventory.
2. **ML/AI model cache management** — largest, fastest-growing, naive rm corrupts HF blob/symlink layout. Strongest single opening.
3. **Genuinely safety-first, reversible, protected-path-aware deletion** (Docker named volumes, Android keystores, Archives dSYMs).
4. **Semantic labeling over raw bytes** — "what regenerates and how" answers delete-anxiety.
5. **Size-first multi-select TUI + dev-cache awareness combined** — unoccupied.

## Defensibility

Weak if building "a better Mole." Real if narrowed to what Mole/kondo structurally don't do: ML/AI wedge, global/daemon caches, correctness/safety brand (right paths where blogs are wrong; danger-case handling), semantic regeneration labels + true dry-run-first. Pressure test: "why won't tw93 add this in a weekend?" — answer = depth in ML/global-cache domain + safety model a broad optimizer can't bolt on (+ cross-platform: Mole is macOS-only).

*Caveats: CleanMyMac tier numbers shift across sources post-restructure. Cargo global GC still nightly-only (`-Zgc`) in fetched docs.*
