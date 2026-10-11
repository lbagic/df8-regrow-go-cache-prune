# Distribution, Monetization & Trust Strategy

*Research date: 2026-07-13.*

**BLUF: donations won't fund this; a paid companion GUI app (the Mole model) or open-core Pro tier is the real business. Ship a single static binary, distribute via Homebrew + curl|sh + GitHub Releases, skip the Mac App Store (sandbox makes a real cleaner impossible), win trust with dry-run-by-default + trash-not-rm + undo log.**

## 1. Monetization reality

- Pure donations for a useful-but-not-critical tool: **$0–500/month**, unpredictable.
- Success stories are outliers: Caleb Porzio $100k/yr via **sponsorware** (release to sponsors first); early milestone 101 sponsors ≈ $2,633/mo. Specific tiers ("priority issue response in 48h") beat "support my work". Most >$1k/mo maintainers combine 2+ methods.
- Comparable cleaners: czkawka (~32k stars) + BleachBit — donation-funded, no impressive revenue. **Big stars ≠ money for utilities.**
- **Mole = the model to copy:** free GPL CLI + separate proprietary native macOS app ($19 lifetime, mole.fit). Open-core via companion GUI, not donations.
- Recommendation: MIT CLI core (auditable rules = trust product; don't gate source of a deletion tool) + paid notarized GUI companion as primary earner + Sponsors/Ko-fi tip jar budgeted at $0–500/mo.

## 2. Distribution

- **Homebrew** #1: own tap day one; homebrew-core needs notability (≥75 stars, or **≥225 stars self-submitted**), stable tagged release.
- **npx for non-JS binary:** esbuild/biome `optionalDependencies` pattern — per-platform scoped packages with os/cpu fields + thin JS wrapper; postinstall fallback.
- **curl|sh:** fine if script is short/auditable, checksummed, prints actions; offer brew alternative.
- **Mac App Store: NO.** App Sandbox blocks whole-filesystem read/delete; FDA does not override sandbox.
- **Notarization mandatory** for outside-store distribution: Apple Developer Program $99/yr, Developer ID cert, hardened runtime, `notarytool`.
- Priority: Homebrew tap → curl|sh → notarized GitHub Releases → homebrew-core → npx → cargo/go install.

## 3. Launch & traction

- **Mole blueprint:** 0→20k stars <80 days: precise dev pain + "reclaim tens of GB" instant payoff, influential-dev amplification (Tuist author), HN + Chinese dev community (HelloGitHub), fast visible iteration.
- **Show HN:** neutral 8–12-word title, Tue–Thu 9am–12pm ET, respond to every comment first 60 min, repo + GIF demo. Expect 5k–50k visitors/48h, ~1.4 stars per upvote.
- Viral driver for cleaners: the **"found X GB" moment** — before/after screenshot + one-liner. That screenshot IS the funnel.

## 4. Trust engineering

- Dry-run by default; show exact delete set + reclaim before touching anything.
- **Trash, don't rm** — biggest single trust lever (CleanMyMac's reputation damage = immediate deletion).
- Transaction log + `history` + restore.
- Protected paths, whitelists, skip-on-uncertainty.
- **Open auditable rule definitions** (YAML/JSON modules others can PR) — the trust story.
- Code signing + notarization.
- Testing: temp-dir fixture sandbox + fake $HOME everywhere (`tempfile`, pytest tmp_path); **golden/snapshot tests of the planned deletion set**; inject FS root as parameter.
- Post-mortems: Steam `rm -rf "$STEAMROOT/"*` (refuse empty/`/`/$HOME base paths); GitLab 2017; CleanMyMac (trash + conservative "junk" definition); Synacktiv rm-rf class.

## 5. Tech stack

- **Go + Bubble Tea/Charm (recommended):** fastest path to polished TUI, proven category stack (Mole, gdu, lazygit), GoReleaser = brew+curl+notarized releases near one config.
- Rust + ratatui: valid alternative; "memory-safe" is a real trust line (czkawka); steeper, slower to ship.
- Node/Ink: weakest fit (runtime baggage) despite Claude Code using it.
- Single static binary non-negotiable (trust + distribution).
- Auto-update: prefer brew upgrade; a self-modifying binary with FDA is a scary threat surface. At most "new version available".
- **Telemetry: opt-in only or none** (GitHub CLI opt-out backlash 419-pt HN thread; Go toolchain backtracked). For a deletion tool, opt-in is the only defensible default.
- Config: XDG `~/.config/<tool>/config.toml`.

## Launch checklist

1. Notarized static binary + brew tap + curl|sh, one-liners in README.
2. README hero = GIF of scan finding "X GB" + before/after; dry-run shown default.
3. Trust section front and center: dry-run default, trash-not-rm, undo/history, protected paths, open rules, "how we test deletion safely".
4. Ops log + history/restore; path guards.
5. Test suite entirely in sandboxes with fake $HOME; golden tests over planned deletions.
6. Opt-in-only/no telemetry, stated explicitly.
7. Show HN Tue–Thu morning ET; author present first hour; 2–3 credible devs primed; cross-post r/macos, Lobsters, HelloGitHub.
8. Fast follow-up releases first weeks.
