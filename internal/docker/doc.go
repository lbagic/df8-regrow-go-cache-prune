// Package docker is the docker provider (Prompt G2, research in
// docs/plans/2026-07-13-docker-usage-timestamps.md): it enumerates
// volumes, containers, images and build cache through the docker CLI,
// derives the timestamps Docker does not track, and classifies
// everything into tiers the rules expose.
//
// The provider talks to whatever daemon the current docker context
// points at — Docker Desktop, OrbStack and colima all speak the same
// API, so context resolution is the CLI's job, not ours. A missing
// binary or a stopped daemon means "these rules do not apply right
// now": nil results, no error.
//
// Core facts the classifier is built on (verified live, 2026-07-13):
//
//   - Volumes have no native last-used. It is derived by joining
//     container .Mounts with .State.StartedAt/.FinishedAt, and
//     persisted in a usage ledger keyed name@CreatedAt so history
//     survives container removal (labels are immutable post-create,
//     so regrow's own state is the only place to keep it).
//   - Dangling ≠ safe: the live probe found all three dangling volumes
//     were named, compose-labeled, active-project data. Named dangling
//     volumes are caution-tier, per-item, never auto-selected.
//   - Image "Created" is build time, which can predate the pull by
//     months — `image prune --filter until=` is never used. Images no
//     container runs or mounts are offered by ID: dangling ones, and
//     tagged ones docker records as pulled from or pushed to their
//     repository and last tagged 30+ days ago.
//   - Build cache is the one object with native last-used; it ages via
//     `builder prune --filter unused-for=`.
package docker
