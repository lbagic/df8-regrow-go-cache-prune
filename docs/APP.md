# The menubar app

`Regrow.app` keeps regrow in the menu bar: free space, purgeable space, swap and days-to-full at a glance, a watch tick every 15 minutes and on wake whose alerts arrive as notifications, and a window that scans, plans and runs a clean. It is a shell around `regrow engine` ([ENGINE.md](ENGINE.md)): the Go binary sits inside the bundle and does all the work. The scan page follows [DESIGN.md](../DESIGN.md).

It builds with the Command Line Tools alone. No Xcode is needed.

## Install

```sh
scripts/signing-cert.sh   # once
just app
```

1. `scripts/signing-cert.sh` makes a self-signed code-signing identity, "regrow local signing", in the login keychain and writes its SHA-1 to `.signing-identity` (not committed). The first signature raises a keychain dialog: choose "Always Allow". Run again, it finds the identity, prints the same hash and creates nothing. `--dry-run` only reads the keychain.
2. `just app` builds the app and the engine, signs both by that hash with the hardened runtime, asks a running Regrow to quit, waits for its engine to finish the action it is on, exchanges the bundle in `~/Applications` in one step, and launches it. From `~/Applications` the app registers itself to open at login and asks to send notifications.

macOS ties its grants to the bundle id and the signature. Signed with the same identity, a rebuilt app keeps Full Disk Access, the right to control Finder and its notifications. `just app-build` makes an ad-hoc signed bundle under `app/.build/bundle` for a look; it keeps no grant, registers nothing, asks for nothing and posts no notification.

## Grants

| Grant | When macOS asks | Without it |
|---|---|---|
| Notifications | first launch from `~/Applications` | no alert reaches you; the menu still shows the figures |
| Control of Finder | the first confirm step that holds a Trash move | Trash moves land in regrow staging; `regrow undo` restores them, emptying the Trash does not free them |
| Full Disk Access | never; granted in System Settings ▸ Privacy & Security, then the app is relaunched | folders macOS protects read as "unreadable" or as a lower bound |

Full Disk Access goes to the app, not to a terminal: the engine inside the bundle is covered by the app's grant.

## What the app runs

- One engine at a time, started again when it exits, with the environment of a login shell, so `go`, `docker` and `brew` are found as they are in a terminal. When that environment cannot be read the menu and the page say so, and a plan skips each command whose program is not on the PATH.
- A watch tick at launch, every 15 minutes and on wake. The autotrim switch sends the tick with `autotrim`; the engine refuses it until one `regrow prune go-build --yes` has completed.
- A scan only when the window asks for one. Nothing runs without the plan screen and the confirm screen, whose tick box unlocks the button.

The menu's "Show in Finder" opens regrow's staging folder: what a run moved there because Finder was not available, and the export made before a docker volume is removed. The app deletes nothing there.

## Checks

```sh
just app-test          # the Swift package's tests
just page-check        # the page through WebKit: assertions and a PNG per state
just app-smoke trash   # through the installed app: a folder it creates goes to the Trash and comes back
just app-smoke alert   # through the installed app: one tick that must raise a notification
just app-smoke doors   # through the installed app: it runs and moves nothing its launch names
```

The smoke checks start the installed app through LaunchServices, so the grants they exercise are the app's, and use a state directory of their own. Each prints one `PASS` or `FAIL` line per stage; one that fails leaves its directory in place and names it, with the app's `result.json`, `smoke.log` and `engine.log` inside. `doors` starts the app with `open --env` and `open --args` the ways any program could, each with a probe that leaves a file if it runs: `REGROW_ENGINE`, `HOME` and `ZDOTDIR` for the login shell, `DYLD_INSERT_LIBRARIES`, a smoke folder of another shape and a `--swap` of two other paths. A second argument checks another bundle: `scripts/app.sh signed` builds and signs one as `just app` does, in `app/.build/bundle`, without installing it.

## What a launch can choose

Anything running as you can start the app with variables and arguments of its choosing, and what the app runs then has its Full Disk Access. So a bundle signed with the identity takes nothing from its launch beyond `--open-scan`, which opens the window, and `-autotrim YES`, which turns autotrim on for that run:

- No `REGROW_` variable. `REGROW_ENGINE`, `REGROW_PAGE`, `REGROW_RULES_DIR`, `REGROW_HOME`, `REGROW_STATE_HOME` and `REGROW_LOG` point a development run at other files only in an ad hoc or unsigned build (`just app-build`, `swift run`). The line is the signature, not the path: a copy of the installed app anywhere holds its grants.
- Nothing the engine, the login shell, osascript or the relaunch helper inherit from the app's environment: each starts from the login shell's environment, or from your account's (home, user and shell from the password database, the system `PATH`).
- No `DYLD_` variable: app and engine run with the hardened runtime.
- A smoke check only in a folder `mktemp` made as `$(getconf DARWIN_USER_TEMP_DIR)regrow-smoke.<x>`, and the trash check only on `~/regrow-smoke-*`, through the rule the bundle carries. `just app-smoke trash` refuses to start while one is there already.
- `--swap` only of `~/Applications/.Regrow.app.staged` and `~/Applications/Regrow.app`, from a bundle that resolves where the staged path does, as `just app` runs it; the two are renamed as written, so a link at either is what moves, and a link at the staged path lets any bundle swap them.

What else stays open needs your files changed: the bundle in `~/Applications` is yours to write, a copy of it with its engine replaced still gets Full Disk Access (macOS checks the app's own executable, not the engine inside it), the signing key signs for any program without asking, and the login shell runs your profile with the app's access. So Regrow's Full Disk Access is only as safe as your own files. [ARCHITECTURE.md](ARCHITECTURE.md)'s decisions log has the list.

The app's log is `~/Library/Logs/regrow/app.log`, with the engine's stderr in `engine.log` beside it.

## Known limits

- Neither log is rotated.
- An alert is a crossing, posted once as a notification. The menu shows the figures, not past alerts.
- The first tick on an empty history counts a line free space is already under as crossed, so a state directory with no history and under 50 GiB free posts "Disk space is low" once.
- With autotrim on and its gate locked, the menu says why under the switch. Once free space is low enough that a prune would have run, the tick ends in an error instead: the menu reads "Autotrim did not run", and the notification comes once, not with every tick.
- A second copy of the app started on the same state directory says so and quits. Where `app.lock` cannot be locked at all, both copies run, and neither stops an engine an earlier run left.
- Quitting while a plan runs waits up to 30 s for the action in flight, then stops the engine. A steward command the engine had started runs on, or stops part-way once it writes to the engine that is gone; either way the oplog lacks that action's end.
- If the engine stops during a command that exports first, such as a docker volume's removal, the export may sit in the run's folder in regrow staging while the oplog has only the action's start, not where the export went; the window's notice after the stop does not mention it.
- `just app` finds the running app through the pid file in the state directory its own terminal names. It does not stop first an app whose login shell set another `XDG_STATE_HOME`, a copy running from another path, or an app that has not started its engine yet: the first seconds after launch, or up to 37 s while it waits out an engine an earlier run left.
