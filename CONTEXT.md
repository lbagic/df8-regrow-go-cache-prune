# regrow

A disk cleaner that knows what everything is, what regenerates, and how. One context: the whole product speaks this language.

## Language

### Catalog

**Rule**:
One declarative cleaning target: what it is, where it lives, how risky, how it regenerates. Data (YAML), never code.
_Avoid_: cleaner, module, task

**Catalog**:
The full set of rules compiled into the binary (or loaded from a rules directory).

**Tool query**:
A named code hook a rule references when only a steward tool can enumerate its targets (docker df, simctl list).
_Avoid_: plugin, integration

**Fixture**:
A rule's own test data: the files planted in a fake home and the fake tool-query results its golden test runs against. Travels inside the rule.
_Avoid_: test data, mock setup

### Scanning

**Host**:
The machine rules are resolved against: OS, version, home, and a re-anchorable root. Fake hosts make every rule testable.

**Finding**:
One rule plus everything the scan measured for it.
_Avoid_: result, entry

**Item**:
One concrete thing a rule found: a directory, or a tool-reported entry like a docker image.

**Regen story**:
What brings deleted data back and what that costs. Shown next to every finding; the product's core promise.

**Exclusive bytes**:
An item's size counted as what deleting *it alone* frees — bytes in blobs shared with other items are excluded and annotated in the label instead (ollama models). Sizes never promise space deletion won't reclaim.
_Avoid_: logical size, reported size

**Partial**:
An item whose size and last-used are lower bounds, because a folder in it refused or never answered. Shown as "≥ X", or "unreadable" when nothing could be measured. Not an error: the rule found its target.
_Avoid_: failed, errored

**Scan ledger**:
A scan's accounting: every measured byte in exactly one item (its size minus the items nested directly inside it), and each item's share in one bucket.
_Avoid_: total found; not the docker usage ledger

**Bucket**:
When, if ever, an item's bytes become free space, derived from its rule: frees now (steward command), after Trash (Trash move), shown only (surface-only), macOS-managed (phantom space).

**Written off**:
A filesystem call the scan stopped waiting for: it made no progress for the stall window while nothing else answered. Its path stays blocked, never touched again, until that call answers.
_Avoid_: timed out

**Usage ledger**:
Regrow's own persisted record of derived last-used timestamps (docker volumes), merged on every scan so history survives the objects that supplied it (containers). Keyed by identity + creation time to defeat name reuse.
_Avoid_: cache, database

**Kept tier**:
Objects a provider deliberately withholds from deletion — in use, keep-listed, over a safety cap, or too recent — surfaced in one surface-only rule with the reason in each row.

**Idle project**:
A project that nobody is working in: no file under its folder changed within the idle window, git's HEAD did not move, and no running process works in it. The project is a git work tree (a main checkout, a linked worktree or a nested clone, each on its own); a submodule belongs to the outermost project that holds it. Build output is offered only from an idle project.
_Avoid_: stale build, old build output (the build folder's own age does not decide)

**Keep-list**:
User config that durably protects objects regrow could otherwise offer (docker volumes and stopped containers by name glob or compose project, unused images by name glob). Exists because docker labels are immutable after creation.
_Avoid_: whitelist, ignore list

### Planning

**Plan**:
The dry-run output: the exact commands that would run, plus what was deliberately skipped and why.

**Action**:
One exact command the plan would run — a steward command or a move to the Trash.

**Steward command**:
The tool's own cleanup command, preferred over raw deletion (`go clean -cache`, `simctl runtime delete`).
_Avoid_: native command (in prose; the YAML field keeps its name)

**Preview command**:
The exact command the trash mechanism would run for a path, shown on the plan screen before anything executes.

**Default selection**:
What every face starts from when nothing is named: whole safe rules that found items without an error. An empty selection plans nothing.

**Containment forest**:
Every scanned item placed under the nearest item, from any rule, whose path contains it (on equal paths the later rule is the child). The planner uses it to plan nested selections once and to refuse deleting anything that holds a surface-only item.

**Frees now / after Trash**:
The two plan subtotals: steward commands free space as they run; Trash moves free nothing until the Trash is emptied.

**Pre-action**:
A named executor hook a rule declares (`pre_action:`) that must succeed before each item's steward command runs — the docker volume tarball export. No backup, no deletion.
_Avoid_: pre-hook, before-script

**Export receipt**:
The journal record of a pre-delete backup that cannot be renamed back (a volume tarball in staging). Undo reports it and points at the file; recovery is manual.

**Risk class**:
Architectural handling class of a rule: safe (auto-clean), caution (review), surface-only (never deletable through regrow).
_Avoid_: severity, danger level

**Surface-only**:
Shown so you know it exists; regrow never deletes it (iOS backups, Docker VM disk).

### Safety

**The fence**:
The non-negotiable safety invariants: dry-run default, trash-not-rm, path guards, oplog before action, surface-only never deletable.

**Path guard**:
The check every destructive target must pass: never empty, root, home, top-level, or mount roots.

**Oplog**:
The journal every executed action lands in before it runs; the source for undo and history.
_Avoid_: audit log

**Golden test**:
A rule's snapshot test: scan its fixture, plan, compare the normalized command list. Every rule has one.

### Doctor

**Hero bug**:
A known runaway bug whose signature is a rule's target growing past a size no healthy machine reaches. Declared on the rule (`doctor:` block: healthy line + story); `regrow doctor` flags crossings.
_Avoid_: health check, diagnostic

**Cause**:
Something outside regrow's reach that keeps filling the disk a rule's target sits on: a setting, a daemon, a build habit. Declared on the rule (`causes:` — a named check, the story, the owner's fix lines); `regrow doctor` says whether it is in effect and prints the fix. regrow applies none.
_Avoid_: recommendation, tip, optimization

**Phantom space**:
Disk usage Finder counts but your files don't add up to — TM snapshots, sparse VM disks, APFS purgeable. Its own category, surfaced with "why Finder still shows full" copy; mostly surface-only.

**Purgeable**:
Space macOS has promised it can free on demand; Finder counts it as free, df does not. Measured as the gap between those two numbers.
