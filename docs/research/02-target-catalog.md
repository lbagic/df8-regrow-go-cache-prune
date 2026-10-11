# Target Inventory & Module Catalog (macOS-first, 2025/2026-verified)

*Research date: 2026-07-13. This is the raw module catalog for the product. 🚩 = commonly a 10GB+ single win.*

## 0. Risk taxonomy & handling classes

- **Safe** — pure regenerable cache/log. Auto-clean by default. Cost = slower next build / re-download.
- **Caution** — regenerable with real cost or footgun (long rebuilds, network, only-copy risk, naive rm corrupts a managing DB). Opt-in / shown.
- **Expert** — system-level, root-owned, SIP-adjacent, or destroys live user data. Never auto-select; prefer vendor command.

Product handling classes: (1) **Auto-clean** (Safe), (2) **Review-and-select** (Caution, native commands), (3) **Surface-only, never delete** (Expert/user data: VM bundles, model libraries, Downloads, Messages/Telegram media, iOS backups — report size, link right tool).

## 1. Build-priority map

**Tier S — build first (huge + common):**
- Xcode CoreSimulator Devices + runtimes — 20–100GB+, invisible to Finder. Biggest hidden win on iOS machines.
- Xcode DerivedData (10–50GB); old Xcode.app copies (15–40GB each).
- Docker Desktop VM disk — "pruned but disk still full" 50GB+ phantom (macOS/Win only).
- node_modules discovery — aggregate = largest JS category.
- Rust target/ discovery — 5–20GB per project; dominates Rust win.
- AI model caches — Ollama + HF hub: 10–100GB+ on AI devs.
- iOS device backups — 5–50GB each (surface-only).

**Tier A:** Gradle caches + wrapper dists · Android system-images + AVDs · Go GOCACHE+GOMODCACHE · package-manager caches (npm/pnpm/pip/uv/cargo) · orphaned .venv/conda discovery · Electron-app caches · JetBrains leftover versions · TM local snapshots · aerial wallpapers.

**Tier B — hero-bug scanners (cheap, huge when present):**
- Claude Code `~/.claude/debug/` log-loop bug → 100–200GB.
- mediaanalysisd/photoanalysisd leak → up to 143GB (fixed 15.2+).
- Spotlight `.Spotlight-V100` runaway on node_modules-heavy disks → observed 233GB.
- Playwright transform-cache bug → 26GB+.

**Surface-only:** UTM/Parallels/VMware VMs · ComfyUI/SD model libraries · Downloads · Messages/Telegram/WhatsApp media.

## 2. JS / Node

| Target | Path (macOS) | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| npm cache | `~/.npm/_cacache` | `npm cache verify` (GC, preferred) / `clean --force` | Safe | 1–5GB | |
| yarn classic | `~/Library/Caches/Yarn` | `yarn cache clean` | Safe | 🚩 5–10GB | |
| yarn berry | global `~/.yarn/berry/cache` only if `enableGlobalCache:true`; else project `.yarn/cache` | `yarn cache clean --all` | Caution | GBs | Zero-install `.yarn/cache` committed to git — deleting breaks offline/CI. Check `.yarnrc.yml` |
| pnpm store | `~/Library/pnpm/store/v*`; confirm `pnpm store path` | `pnpm store prune` | Safe | 🚩 10–20GB | Shared CAS |
| bun | `~/.bun/install/cache` | `bun pm cache rm` | Safe | ≤2GB | |
| deno | `~/Library/Caches/deno` | `deno clean` | Safe | ≤2GB | |
| corepack | `~/.cache/node/corepack` (XDG on macOS!) | rm | Safe | small | |
| nvm/fnm/volta versions | `~/.nvm/versions/node/*` etc. | `nvm uninstall <v>` | Caution | 80–200MB ea | Keep active/default/.nvmrc-pinned |
| node_modules discovery | walk | npkill-style | Caution | 🚩 aggregate | Exclude app-owned (Spotify/Discord/VS Code) |
| Turborepo / Nx | `.turbo/cache`, `.nx/cache`+`.nx/workspace-data` | rm / `nx reset` (misses workspace-data, 2025 bug) | Safe | GBs | |
| Next.js | `.next/cache` | rm | Safe | 🚩 multi-GB image-heavy | |
| Vite/webpack/Parcel/Babel/ESLint/Metro | project `.cache` dirs, `$TMPDIR/metro-*` | rm | Safe | MBs–1GB | |
| Playwright | `~/Library/Caches/ms-playwright` | `npx playwright uninstall --all` | Safe | 100s MB/browser; ⚠️ transform-cache bug 26GB+ | |
| Puppeteer | `~/.cache/puppeteer` (all platforms) | `npx @puppeteer/browsers clear` | Safe | ≤1GB | |
| Cypress | `~/Library/Caches/Cypress` | `cypress cache prune` | Safe | 200–400MB/ver | |
| Electron / electron-builder | `~/Library/Caches/electron{,-builder}` | rm | Safe | 🚩 builder 1–3GB | |

## 3. Go

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| Build cache | `~/Library/Caches/go-build` | `go clean -cache` | Safe | 🚩 1–15GB | |
| Module cache | `~/go/pkg/mod` | `go clean -modcache` | Caution | 🚩 2–20GB | **Files read-only 0444 — naive rm fails**; use go clean or chmod first |
| Test/fuzz cache | in GOCACHE | `go clean -testcache/-fuzzcache` | Safe/Caution | small | Fuzz loses corpus |
| gopls / staticcheck / golangci-lint | `~/Library/Caches/{gopls,staticcheck,golangci-lint}` | rm / `golangci-lint cache clean` | Safe | 100s MB–GBs | staticcheck never auto-trims |

## 4. Rust

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| registry cache+src | `~/.cargo/registry/{cache,src}` | `cargo cache --autoclean` | Safe | 3–7GB | cargo-cache = separate install, stale but works |
| git deps | `~/.cargo/git` | `cargo cache -r git-repos,git-checkouts` | Caution | multi-GB | |
| **target/ dirs** | per-project; marker `CACHEDIR.TAG` | `cargo clean` / cargo-sweep | Safe (costly) | 🚩🚩 5–20GB each, 50–100GB agg | **#1 Rust win.** Rank by project mtime |
| sccache | `~/Library/Caches/Mozilla.sccache` | stop server, rm | Safe | 10GB default cap | Intentionally big — flag |
| rustup toolchains | `~/.rustup/toolchains` | `rustup toolchain uninstall` | Caution | 🚩 0.5–1.5GB each | Pinned re-installs |

## 4b. Python

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| pip | `~/Library/Caches/pip` | `pip cache purge` | Safe | 0.5–5GB | |
| uv | `~/.cache/uv` (**XDG even on macOS**) | `uv cache prune` | Safe | 🚩 multi-GB (75–86GB cases) | Hardlink store — never manual-edit |
| poetry | `~/Library/Caches/pypoetry` (+`/virtualenvs`) | `poetry cache clear pypi --all` | Safe/Caution | 0.5–3GB | |
| conda | `~/{mini,ana}conda3/pkgs` | `conda clean --all` | Safe | 🚩 5–10GB | Softlink envs caveat |
| pyenv | `~/.pyenv/{versions,cache}` | `pyenv uninstall` | Caution/Safe | 200–500MB ea | |
| `__pycache__`/.pytest_cache/.mypy_cache/.ruff_cache/.tox | scattered | find+rm / native | Safe | 100s MB–1GB | |
| **.venv discovery** | marker `pyvenv.cfg` + `bin/python` | rm per dir | **Expert** | 🚩 200MB–1GB each | Verify marker; ad-hoc installs unrecoverable |
| pipx | `~/.local/pipx` (1.5+; 1.2–1.4 `~/.local/share/pipx`) | `pipx uninstall-all` | Caution | 50–300MB/tool | Check both paths |
| Jupyter | `~/Library/Jupyter` | `jupyter --paths` | Caution | 10s–100s MB | |

## 5. JVM / Android

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| Gradle deps + build-cache | `~/.gradle/caches/{modules-2,build-cache-1}` | rm (auto-purge 30d/7d) | Safe | 🚩 5–35GB | |
| **Gradle wrapper dists** | `~/.gradle/wrapper/dists` | rm old | Safe | 🚩 ~100–285MB × every version ever | Classic surprise |
| Gradle daemons | `~/.gradle/daemon` | `./gradlew --stop` per version | Safe | RAM+small | |
| Maven | `~/.m2/repository` | rm / purge plugin | Safe | 2–20GB | |
| Android SDK platforms/build-tools/NDK/sources | `~/Library/Android/sdk/...` | `sdkmanager --uninstall` | Caution | 100MB–2GB ea | |
| **system-images** | `.../system-images` | `sdkmanager --uninstall` | Caution | 🚩 1–3GB each | AVDs depend |
| **AVDs** | `~/.android/avd/*.avd` | `avdmanager delete avd -n` | Caution | 🚩 GBs each | |
| `~/.android` misc | cache dirs only | rm | Caution | <1GB | **KEEP `adbkey*`, `debug.keystore`** |
| Android Studio caches | `~/Library/Caches/Google/AndroidStudio<v>` | rm (quit first) | Safe | 🚩 2–8GB/version, old versions survive upgrades | |
| Kotlin/Native | `~/.konan` | rm | Safe | 1–5GB | Kotlin 2.0+: project-local `.kotlin` |
| sbt/Ivy/Coursier | `~/.sbt/boot`, `~/.ivy2/cache`, `~/Library/Caches/Coursier/v1` | rm / `cs cache clear` | Safe | 1–10GB | Coursier macOS path ≠ Linux |

## 6. Apple / Xcode (densest cluster)

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| **DerivedData** | `~/Library/Developer/Xcode/DerivedData` | rm | Safe | 🚩 10–50GB | incl. ModuleCache.noindex, Previews |
| **iOS/watchOS/tvOS/visionOS DeviceSupport** | `~/Library/Developer/Xcode/* DeviceSupport` | rm per version | Safe | 🚩 2–5GB × N | Re-generated on device connect |
| **Sim runtimes (Xcode 14+)** | `/Library/Developer/CoreSimulator/{Images,Volumes,Cryptex}` — root, simdiskimaged-managed | `xcrun simctl runtime delete <id>` / `--unused` / `--notUsedSinceDays N` | Expert if raw-rm | 🚩🚩 5–10GB each, 20–60GB total | **Never raw rm — desyncs DB.** GUI: Settings ▸ Components (Xcode 26) |
| Sim runtimes ≤13 | `/Library/Developer/CoreSimulator/Profiles/Runtimes` | sudo rm | Caution | 3–8GB ea | `/Library` not `~/Library` — common doc error |
| **CoreSimulator Devices** | `~/Library/Developer/CoreSimulator/Devices` | `simctl delete unavailable` / `<UDID>` | Caution | 🚩🚩 30–100GB+, hidden | Biggest surprise |
| CoreSimulator Caches | `.../CoreSimulator/Caches/dyld` | rm | Safe | 0.5–3GB | |
| **Archives** | `~/Library/Developer/Xcode/Archives` | Organizer | Caution | 🚩 5–30GB | Local dSYMs for symbolication — fine if uploaded to ASC |
| SPM caches | `~/Library/Caches/org.swift.swiftpm` | `swift package purge-cache` | Safe | 1–5GB | |
| **Old Xcode.app** | `/Applications/Xcode*.app`; verify `xcode-select -p` | rm old | Caution/Expert | 🚩🚩 15–40GB each | |
| Xcode installer cache | `~/Library/Caches/com.apple.dt.Xcode/Downloads` | rm | Safe | 🚩 up to 10GB (stuck .xip) | |
| Doc cache, device logs, Products | various `~/Library/Developer/Xcode` | rm | Safe | 1–3GB | |

## 7. Containers / VMs

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| **Docker Desktop VM disk** | `~/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw` (VZ) / `.qcow2` (QEMU) | see gotcha | Caution | 🚩🚩 50GB+ phantom | See gotcha below |
| Docker build cache / prune | in VM | `docker builder prune -a`; `docker system prune -a --volumes` | Safe→Caution | 5–20GB | Volumes flag = data loss |
| colima | `~/.colima/_lima/colima/diffdisk` | `colima ssh -- sudo fstrim -a` | Caution | 🚩 5–20GB (77GB reports) | |
| lima | `~/.lima/_images` | `limactl prune --keep-referred` | Safe | 2–10GB | |
| podman machine | `~/.local/share/containers/podman/machine` | `podman system prune`; `machine rm` | Caution/Expert | 5–20GB | |
| OrbStack | `~/Library/Group Containers/*.dev.orbstack/data/data.img` | auto-shrinks (true sparse) | Safe/Expert | check du not ls | Its differentiator |
| minikube | `~/.minikube` | `minikube delete --purge` | Caution | 🚩 multi-GB | --purge needed |
| kind/k3d | inside Docker store | `docker image prune -a` | Safe | 0.5–1GB/node | |
| Vagrant | `~/.vagrant.d/boxes` | `vagrant box prune --dry-run` first | Caution | 🚩 0.5–5GB × versions | |
| **UTM / Parallels / VMware VMs** | `~/Library/Containers/com.utmapp.UTM/...`, `~/Parallels/*.pvm`, `~/Documents/Virtual Machines` | vendor "free up space" (lossless) | **Expert** | 🚩 5–100GB/VM | **Surface-only** |

**Docker never-shrinks gotcha (product UX):** VM disk = host-side sparse file; deleting images frees blocks *inside* guest ext4 only. Host shrinks only if guest TRIMs and backend honors it. Docker.raw (VZ, 4.28+) reclaims in seconds; .qcow2 (QEMU) barely. Last resort: Troubleshoot ▸ Purge Data. macOS/Windows-only tax (Linux native). OrbStack sidesteps it.

## 8. IDEs / Editors

| Target | Path | Risk | Win | Notes |
|---|---|---|---|---|
| VS Code caches | `~/Library/Application Support/Code/{Cache,CachedData,Code Cache,GPUCache}` | Safe | 1–10GB | |
| **workspaceStorage** | `.../Code/User/workspaceStorage` | Caution | 🚩 5–30GB | Some extensions store tokens/creds — vet |
| globalStorage / logs / old extensions | `.../globalStorage`, `.../logs`, `~/.vscode/extensions/*-oldver` | Caution/Safe | GBs | |
| Cursor / Windsurf / VSCodium | same layout, own dirs (`~/.cursor/extensions`) | Safe/Caution | 🚩 Cursor CachedData bug ≤10GB | |
| JetBrains caches | `~/Library/Caches/JetBrains/<Product><ver>` | Safe | 🚩 1–10GB/version | |
| **JetBrains leftover versions** | caches+config+plugins trees | Safe | 🚩 5–30GB | Help ▸ Delete Leftover IDE Directories; config persists forever |
| Zed / Sublime | `~/Library/{Application Support,Caches}/Zed`, Sublime Index | Safe | MB–4.6GB | |

## 9. AI / ML

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| **HuggingFace hub** | `~/.cache/huggingface/hub` | `hf cache` / `huggingface-cli scan-cache` + `delete-cache` | Safe | 🚩🚩 10–100GB+ | **Use CLI not rm** — blobs/snapshots/refs dedup |
| **Ollama** | `~/.ollama/models` | `ollama rm <model>` (blob GC) | Safe | 🚩🚩 4.7–40GB+ | Blobs dedup across models |
| torch hub / Whisper | `~/.cache/torch/hub`, `~/.cache/whisper` | rm | Safe | ≤3GB | |
| **LM Studio** | `~/.cache/lm-studio/models` (older `~/.lmstudio`) | app UI | Caution | 🚩 4–90GB | Detect dir, don't hardcode |
| **Claude Code** | `~/.claude/{debug,cache,shell-snapshots,paste-cache}` | rm | Safe | 🚩🚩 debug-loop bug 100–200GB | `~/.claude/projects` = history — KEEP |
| **ComfyUI / SD-WebUI** | `models/{checkpoints,loras,...}` | none | **Expert** | 🚩🚩 50–200GB | Often irreplaceable — **surface-only** |

## 10. macOS system / user

| Target | Path | Native clean | Risk | Win | Notes |
|---|---|---|---|---|---|
| **Aerial wallpapers** | Sonoma/Sequoia: `/Library/Application Support/com.apple.idleassetsd/Customer/4KSDR240FPS`. **Tahoe (26): moved** to `~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches/...` | rm videos | Safe | 🚩 full set ~64GB | Switch to static wallpaper first |
| **TM local snapshots** | APFS | `tmutil thinlocalsnapshots / <bytes> 4` | Caution | 🚩 10–100GB phantom | |
| Spotlight index | `/.Spotlight-V100` | `sudo mdutil -E /` | Expert | 1–10GB; 🚩 233GB observed | Search dead during reindex |
| **mediaanalysisd leak** | `~/Library/Containers/com.apple.mediaanalysisd` | pkill + rm; fixed 15.2+ | Caution | 🚩🚩 15–143GB | |
| ~/Library/Caches general | per-subfolder | rm | Safe | 🚩 10–50GB agg | |
| Mail downloads | `~/Library/Containers/com.apple.mail/.../Mail Downloads` | rm | Safe | 🚩 1–10GB | |
| Messages attachments | `~/Library/Messages/Attachments` | rm | Caution | 🚩 27–29GB reported | May be only copy |
| **iOS backups** | `~/Library/Application Support/MobileSync/Backup` | Finder | Caution | 🚩🚩 5–50GB each | Surface-only |
| iOS updates (.ipsw) | `~/Library/iTunes/iPhone Software Updates` | rm | Safe | 4–8GB each | |
| QuickLook thumbs | var/folders QuickLook cache | `qlmanage -r cache` | Safe | ≤GB | |
| Stuck installers | `/Applications/Install macOS*.app`, `/Library/Updates` | rm app / Expert | Safe/Expert | 🚩 12–15GB | |
| Localization .lproj | per-app | Monolingual | **Expert** | 100s MB | **Breaks code-signing — don't automate** |
| sleepimage/swap | `/private/var/vm` | pmset / never rm swap | Expert | RAM-size | |

## 11. General user junk

| Target | Path | Risk | Win | Notes |
|---|---|---|---|---|
| Downloads heuristics | `~/Downloads` by age >90d / size top-N | Caution | 🚩 5–20GB | Confirm — user content |
| Large-file finder | whole disk top-N | Safe (discovery) | — | |
| Duplicates | SHA-256 vs size+mtime | Caution | GBs | Lower ROI than it looks; opt-in review only |
| Steam shader/depot cache | `~/Library/Application Support/Steam/{shadercache,depotcache}` | Safe | low-GB | Game launchers = much smaller win on Mac than Windows |
| Browsers (Chrome/Arc/Brave/Firefox/Safari) | caches + Service Worker CacheStorage | Safe | 1–5GB ea | SW cache survives in-app clear |
| Slack/Discord/Teams/Spotify/Zoom | app caches (Teams: new container paths) | Safe | 🚩 Slack ≤8GB, Spotify ≤10GB | |
| Telegram/WhatsApp media | Group Containers | Caution | 🚩 unbounded (TG never auto-clears on macOS) | May be only copy |

## 12. Trash variants

`~/.Trash` + per-volume `/Volumes/<Vol>/.Trashes/<uid>` (external drives silently hold GBs) + network trash. Enumerate: `find /Volumes -maxdepth 2 -iname .Trashes`.

## 14. Recent path changes to hard-code detection for

1. Xcode sim runtimes: ≤13 flat pkgs → 14+ cryptex/DMG via simdiskimaged; clean only via `simctl runtime delete`. Panel renamed Platforms→Components (Xcode 26).
2. Aerial wallpapers: Sequoia `/Library/.../idleassetsd/Customer` → Tahoe wallpaper.agent container cache.
3. Kotlin: `~/.gradle/kotlin` → project-local `.kotlin` (Kotlin 2.0).
4. LM Studio: `~/.lmstudio/models` → `~/.cache/lm-studio/models`.
5. pipx: moved to data dir in 1.2, reverted to `~/.local/pipx` in 1.5 — check both.
6. uv: XDG `~/.cache/uv` even on macOS.
7. Puppeteer/corepack: `~/.cache/...` on macOS too.
8. Coursier: macOS `~/Library/Caches/Coursier/v1`.
9. Teams: classic → container + group container.
10. colima: `~/.lima` → `~/.colima` (v0.6+).

## 15. Implementation notes

- **Discovery by marker file, not name:** target/ → `CACHEDIR.TAG`; .venv → `pyvenv.cfg`+`bin/python`; node_modules → name but exclude app-bundle-owned. Rank hits by parent-project mtime.
- **Permission gotchas:** Go mod cache read-only 0444; sim runtimes/CLT/Updates root+SIP.
- **Native commands over raw delete** wherever a tool manages a DB/dedup store: hf, ollama, pnpm, uv, simctl, conda.
- **Never auto-delete:** VM bundles, ComfyUI/SD models, iOS backups, Downloads, Messages/Telegram media, yarn zero-install cache, Archives with un-uploaded dSYMs.
- **Phantom space UX:** TM snapshots, Docker VM, APFS purgeable — own category, explain why Finder lags.
- **Dedup-aware sizing:** report du actual not logical; warn shared blobs.
- **Hero-bug scanners:** ~/.claude/debug, mediaanalysisd, .Spotlight-V100, Playwright transform cache.
- **Linux/Windows deltas:** Docker VM tax is macOS/Win-only; game/browser wins skew larger on Windows.
