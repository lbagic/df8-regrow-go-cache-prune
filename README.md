# cleaner

Interactive macOS disk cleaner for developers. Single bash file, zero dependencies,
**dry-run by default** — nothing is deleted unless you pass `--run` *and* type `yes`.

```sh
bash clean.sh              # scan sizes → checkbox UI → dry-run plan
bash clean.sh --run        # same, but executes after confirmation
bash clean.sh --list       # show all module ids
bash clean.sh --only go_build,trash --run
bash clean.sh --safe --run # everything risk=safe, no UI
```

Checklist UI: `↑/↓`/`j`/`k` move, `space` toggle, `a` all, `n` none, `s` safe-only,
`enter` continue, `q` quit. Risk `safe` modules are pre-checked; `moderate`/`risky`
are opt-in; `info` modules are read-only reports.

## What it can clean

| id | target | risk |
|---|---|---|
| `aerial` | macOS aerial/moving wallpaper videos (`com.apple.idleassetsd`, sudo) | safe |
| `go_build` | Go build cache (`go clean -cache`) | safe |
| `go_mod` | Go module cache (`go clean -modcache`) | moderate |
| `yarn_cache` / `npm_cache` / `pnpm_store` | JS package-manager caches (native commands) | safe |
| `docker_prune` | dangling images, stopped containers, build cache (`docker system prune`) | moderate |
| `docker_volumes` | unused volumes (`docker volume prune`) — deletes data | risky |
| `xcode_derived` | DerivedData | safe |
| `xcode_devicesupport` | iOS/watchOS/tvOS DeviceSupport symbols | moderate |
| `sim_unavailable` | `xcrun simctl delete unavailable` | safe |
| `sim_caches` | CoreSimulator caches | safe |
| `xcode_archives` | Archives + dSYMs of shipped builds | risky |
| `brew_cache` | `brew cleanup -s --prune=all` | safe |
| `pip_cache` | `pip3 cache purge` | safe |
| `trash` | empty `~/.Trash` | safe |
| `macos_installers` | stale `Install macOS *.app` (sudo) | safe |
| `library_updates` | stuck update payloads in `/Library/Updates` (sudo) | risky |
| `tm_snapshots` | thin Time Machine local snapshots (big "System Data" cause, sudo) | moderate |
| `media_analysis` | mediaanalysisd cache (Sequoia 15.1 leak, 100GB+ reports) | safe |
| `user_logs` | crash reports + diagnostic logs | safe |
| `spotlight_index` | rebuild bloated Spotlight index (Sequoia bug, `mdutil -E /`, sudo) | moderate |
| `docker_raw` | report: Docker.raw real vs sparse size, `docker system df` | info |
| `caches_top` | report: top 15 `~/Library/Caches` entries | info |
| `node_modules` | report: node_modules under `$CLEANER_WORKSPACE` (default `~/workspace`) | info |
| `ios_backups` | report: local iPhone backups (delete via Finder) | info |

Executed commands are logged to `~/Library/Logs/cleaner.log`.

## Safety design

- Dry-run default; `--run` requires typing `yes` (or `--yes` for scripting).
- Delegates to **native cleaners** (`npm cache clean`, `go clean`, `docker system prune`,
  `brew cleanup`, `tmutil`, `simctl`) instead of re-implementing deletion — `rm -rf` is
  used only for the handful of paths with no native command, via `xrm()` which refuses
  `/`, `$HOME`, and other suspicious roots.
- Dry-run prints the **exact commands** `--run` would execute (same code path via `x()`).
- Reports (`info` risk) never mutate anything.

## Adding a module

1. Write one `mod_<id>()` function answering `title|risk|needs_sudo|note|size|clean`
   (copy an existing one). Route mutations through `x`/`xrm`.
2. Add the id to the `MODULES` array.

## Notes

- Terminal may need **Full Disk Access** (System Settings → Privacy & Security) for
  `~/.Trash` and some `~/Library` paths.
- "System Data" bloat is usually: local TM snapshots (`tm_snapshots`), Docker.raw
  (`docker_prune` + Docker Desktop disk cap), `~/Library/Caches`, and staged OS
  updates. `com.apple.os.update-*` snapshots are OS-managed and clear themselves
  after a successful update/reboot.

## Existing tools considered (2026-07 survey)

- [npkill](https://github.com/voidcosmos/npkill) — `npx npkill` for node_modules; use it, it's the best at that one job.
- [mac-cleanup-py](https://github.com/mac-cleanup/mac-cleanup-py) — broadest module coverage, but config-based selection, dry-run is a flag not the default.
- [Mole](https://github.com/tw93/Mole) — most popular/active, but history of aggressive defaults (deleted LaunchAgents #567, wallpapers #577; dry-run mismatch #886).
- [DevCleaner](https://github.com/vashpan/xcode-dev-cleaner) — best deep-Xcode GUI cleaner.
- None default to dry-run, none deliberately handle aerial wallpapers or stuck installers → this script.
