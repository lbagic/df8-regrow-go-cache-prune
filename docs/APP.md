# The menubar app

`Regrow.app` keeps regrow in the menu bar: free space, purgeable space, swap and days-to-full at a glance, a watch tick every 15 minutes and on wake whose alerts arrive as notifications, and a window that scans, plans and runs a clean. It is a shell around `regrow engine` ([ENGINE.md](ENGINE.md)): the Go binary sits inside the bundle and does all the work. The scan page follows [DESIGN.md](../DESIGN.md).

It builds with the Command Line Tools alone. No Xcode is needed.

## Install

```sh
scripts/signing-cert.sh   # once
just app
```

1. `scripts/signing-cert.sh` makes a self-signed code-signing identity, "regrow local signing", in the login keychain and writes its SHA-1 to `.signing-identity` (not committed). The first signature raises a keychain dialog: choose "Always Allow". Run again, it finds the identity, prints the same hash and creates nothing. `--dry-run` only reads the keychain.
2. `just app` builds the app and the engine, signs both by that hash, asks a running Regrow to quit, waits for its engine to finish the action it is on, exchanges the bundle in `~/Applications` in one step, and launches it. From `~/Applications` the app registers itself to open at login and asks to send notifications.

macOS ties its grants to the bundle id and the signature. Signed with the same identity, a rebuilt app keeps Full Disk Access, the right to control Finder and its notifications. `just app-build` makes an ad-hoc signed bundle under `app/.build/bundle` for a look; it keeps no grant, registers nothing and asks for nothing.

## Grants

| Grant | When macOS asks | Without it |
|---|---|---|
| Notifications | first launch from `~/Applications` | alerts show only in the menu |
| Control of Finder | the first confirm step that holds a Trash move | Trash moves land in regrow staging; `regrow undo` restores them, emptying the Trash does not free them |
| Full Disk Access | never; granted in System Settings ▸ Privacy & Security, then the app is relaunched | folders macOS protects read as "unreadable" or as a lower bound |

Full Disk Access goes to the app, not to a terminal: the engine inside the bundle is covered by the app's grant.

## What the app runs

- One engine for its lifetime, started with the environment of a login shell, so `go`, `docker` and `brew` are found as they are in a terminal. When that environment cannot be read the menu and the page say so, and a plan skips each command whose program is not on the PATH.
- A watch tick at launch, every 15 minutes and on wake. The autotrim switch sends the tick with `autotrim`; the engine refuses it until one `regrow prune go-build --yes` has completed.
- A scan only when the window asks for one. Nothing runs without the plan screen and the confirm screen, whose tick box unlocks the button.

The menu's "Show in Finder" opens regrow's staging folder, the undo copies of runs in which Finder was not available. The app deletes nothing there.

## Checks

```sh
just app-test          # the Swift package's tests
just page-check        # the page through WebKit: assertions and a PNG per state
just app-smoke trash   # through the installed app: a folder it creates goes to the Trash and comes back
just app-smoke alert   # through the installed app: one tick that must raise a notification
```

Both smoke checks start the installed app through LaunchServices, so the grants they exercise are the app's, and use a state directory of their own. `REGROW_ENGINE`, `REGROW_PAGE`, `REGROW_RULES_DIR`, `REGROW_HOME`, `REGROW_STATE_HOME` and `REGROW_LOG` point a development run at other files; the app's log is `~/Library/Logs/regrow/app.log`.
