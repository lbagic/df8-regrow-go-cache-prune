# Docker usage timestamps — research

**Status:** shipped 2026-07-13 as Prompt G2 (`internal/docker` provider, usage ledger, tiered rules, volume export pre-action). Decisions and deviations in the ARCHITECTURE.md decisions log; this doc stays as the evidence base.

**Question:** can regrow attach/derive timestamps for Docker volumes, images, networks, build cache to classify what's safe to remove vs. precious project data (e.g. named DB volumes)?

**Method:** probed live Docker Desktop 27.5.1 (arm64) on this machine, read-only (`inspect`, `system df -v`, `buildx du`, `events`). All findings verified against real daemon output, 2026-07-13.

## 1. What Docker tracks natively

| Object | Created | Last used | Identity signals |
|---|---|---|---|
| Volume | ✅ `CreatedAt` | ❌ nothing | compose labels (`com.docker.compose.project/volume`); anonymous volumes labeled `com.docker.volume.anonymous` |
| Container | ✅ `Created` | ✅ `.State.StartedAt` / `.State.FinishedAt` (survives while container exists, incl. stopped) | compose labels, `.Mounts` → volume names |
| Image | ✅ `Created` = **build** time (can be months before you pulled it) | ❌; `.Metadata.LastTagTime` = local pull/tag time (verified: postgres:16 built 05-19, tagged locally 06-01) | repo:tag, dangling filter |
| Network | ✅ `Created` | ❌; but live attached-container count | compose labels |
| Build cache | ✅ | ✅ `LastUsedAt` + `UsageCount` — the only object with native last-used | buildkit record type |

Labels are **immutable after creation** for volumes/networks — you cannot retro-attach metadata to Docker objects without recreating them. So "attaching timestamps" must live in regrow's own state, keyed by object identity.

## 2. Volume last-used is derivable — the join

Volumes have no last-used, but containers do, and stopped containers persist their mounts and run times:

```
last_used(volume) = max over containers C where volume ∈ C.Mounts
                    of (C.State.StartedAt, C.State.FinishedAt)
```

Verified live: `pebble_pebble-pgdata` → mounted by exited `pebble-postgres`, finished 2026-06-12 → volume last used 2026-06-12. Works for every volume on this machine because compose leaves stopped containers around.

**The join breaks** when containers are removed (`docker rm`, `compose down`, `--rm`). Then the volume is orphaned with only `CreatedAt`. Two mitigations:

- **Usage ledger (recommended):** on every regrow scan with the daemon reachable, snapshot `{volume name + CreatedAt → last-known-used, owning project, size}` into regrow state (same dir family as oplog). Merge over time. `CreatedAt` in the key guards against name reuse. History then survives container removal. No daemon subscription needed — scan-time snapshots are enough for a CLI tool.
- `docker events` is in-memory only (not durable across daemon/VM restart) — not worth building on.
- Volume-content mtimes are unreachable from the macOS host (data lives inside the VM). A deep probe (`docker run --rm -v vol:/v:ro busybox find /v -newer …`) is possible but opt-in territory — skip for v1.

## 3. "Dangling ≠ safe" — proven on this machine

The 3 dangling volumes here are `dune_timescaledb_data`, `dune_timescaledb_data_v3`, `dune_test_timescaledb_data_v3` — **named, compose-labeled, belonging to a project whose other containers are up right now**. Exactly the data the user must not lose; classic "old schema version kept as backup" pattern. `docker volume prune --all` would delete them.

Prune semantics (Docker ≥23.0, confirmed on 27.5.1): plain `docker volume prune` removes only **anonymous** dangling volumes; named ones need `--all`. Also: anonymous volumes of stopped-but-kept containers are *not* dangling (verified: anonymous vol of exited `osprey-searxng-1` is protected). So our current `docker-volumes.yaml` (`docker volume prune -f`) would remove **nothing** on this machine today — its note overstates what the default command touches; tighten wording when next editing that rule.

## 4. Classification model for a future docker provider

Per-volume rows, each with `{name, project, size, created, last_used(join or ledger), class}`:

- **protected** — referenced by any container (running or stopped). Never shown as deletable.
- **caution, named** — named + dangling. Show project label, created, last-known-used, size. Never auto-selected, per-item confirm. (The dune timescale volumes land here — visible but loud.)
- **caution, anonymous** — anonymous + dangling + last-used/created older than N days. Honest default candidate.
- **safe (other objects)** — build cache by native age (`docker builder prune --filter unused-for=720h`); stopped containers by age (`docker container prune --filter until=`); dangling images.
- **Image caveat:** `docker image prune -a --filter until=` filters on *build* time — an actively-used image built a year ago gets deleted. Regrow should compute image last-used from referencing containers + `LastTagTime` instead of using that filter.
- Networks: ~zero bytes; hygiene only, low priority.

Plus a user keep-list (name/glob/project) in config, since labels can't be retro-attached.

Sizes come free from `docker system df -v`. Whole provider is daemon-gated (skip cleanly when Docker isn't running). OrbStack/colima speak the same API — detect via `docker context`.

## 5. Where it fits

Per-volume rows need item identity in the selection model — the same wall as per-model toggles in the ML module (Prompt G / candidate 5). Build the docker provider in Phase 3 alongside Prompt G so both consume the item-identity work. The usage ledger piece is independent and could land earlier since it only writes state, but there's no urgency: it's most valuable once per-item rows exist to display it.

**Not in scope for Prompt F** (safety hardening) — no code change now.
