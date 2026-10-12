# What a scan reads and runs

A scan is what `regrow`, `regrow scan`, `regrow plan` and the first step of `regrow clean` do: measure every rule in the catalog. It removes nothing. This page says what a scan reads, which programs it starts, and what it writes, and then what a run adds. [RULES.md](RULES.md) gives each rule's folders and commands.

Beta rules join a scan only with `--beta-rules`. Several of them read more than the default scan does, and the sections below say which.

## Folders it reads

- **Each rule's paths**, listed under "Looks at" in [RULES.md](RULES.md). Most are in your home folder: `~/Library/Caches`, `~/Library/Developer`, `~/.cache`, `~/.npm`, `~/go/pkg/mod`, `~/.claude` and the like. Three are system folders: `/Library/Updates`, `/Applications/Install macOS*.app`, and the aerial wallpaper folder `/Library/Application Support/com.apple.idleassetsd/Customer` on macOS 15 and earlier.
- **Your project folders**: `~/workspace`, `~/dev`, `~/projects`, `~/src` and `~/code`, those that exist, walked for `node_modules` folders (10 levels down) and Rust `target` folders that hold `CACHEDIR.TAG` (6 levels down). With `--beta-rules`, the same folders are also walked for build output (`.next`, `dist`, `.turbo`, `.venv`) and git repositories, and `/private/tmp/claude-<uid>` for git repositories too.
- **With `--beta-rules`**: your home folder, 5 levels down, for Stable Diffusion `models` folders that hold `checkpoints`; Claude Code's scratch folder `/private/tmp/claude-<uid>` and its config folders (`$CLAUDE_CONFIG_DIR`, `~/.claude`, every `~/.claude*` that holds `projects/`, and the one each running `claude` names in its environment); the Hugging Face hub cache; Ollama's model manifests; npx's folders in `~/.npm/_npx`; the `.dmg`, `.pkg` and `.zip` files at the top of `~/Downloads`, and the apps in `/Applications` and `~/Applications` and one folder down; fnm's folder, every `.nvmrc` and `.node-version` in your home folder and project folders (10 levels down, following links), and the launchd job plists in `~/Library/LaunchAgents`, `/Library/LaunchAgents` and `/Library/LaunchDaemons`; `~/Library/Android/sdk` and JetBrains Toolbox's apps; the `.nvmrc` and `.node-version` in every folder a launchd plist names and every folder above it, following links; Chrome's on-device AI model; and `/System/Library/AssetsV2/com_apple_MobileAsset_MacSoftwareUpdate`, for a macOS update Software Update downloaded.
- **Docker Desktop's disk image**, `~/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw` (or `Docker.qcow2`): its size only.

How it reads them:

- It reads names, sizes, modification times and where links lead, and with `--beta-rules` a download's change and creation times, a download's or an app's date added, and who owns your home folder and fnm's folder. Sizes are the blocks files take on disk, so a sparse file counts what it really holds; Ollama's models are the exception, sized by what their manifests declare.
- It reads a file's contents only where a rule needs them: Ollama's manifests, Claude Code's session registry files (`<config>/sessions/<pid>.json`), git's `.git` link files, `commondir`, worktree `gitdir` and config files, the `package.json` in each npx folder, an installed app's `Info.plist` (and that of an app named like Xcode), the `.nvmrc` and `.node-version` files and launchd plists above, a `.zip` download's list of contents and its app's `Info.plist` (read from the archive without extracting it), your `~/.config/regrow/config.yaml`, and regrow's own state. Of Claude Code's transcripts and git's `HEAD` it reads only the modification times. git, where it runs, reads what it needs from the repository.
- Measuring a folder, it follows no symbolic link inside it. A rule's path with a wildcard is matched by listing the folders above it, and one of those that is a link is followed. A project folder that is itself a link is walked through it, except by the build-output rule, which leaves such a folder out.
- A search for folders (in your project folders, or your home folder) does not enter `.git`, `.Trash`, `Library` or `node_modules` (unless that name is what the rule looks for), nor a folder it has already matched. A folder it measures, it measures whole.
- Every filesystem call that measures a rule is watched. One that makes no progress for 5 seconds while nothing else in the scan answers is written off: its folder is shown as unreadable, or its size as a lower bound ("≥ 2.1 GiB"), and the scan moves on; a rule that judges items one by one (build output, worktrees, npx folders, downloads) instead keeps the item that call was about, and does not show it; where the call decides what the fnm, Android or simulator-runtime rules keep, or whether Android Studio or Xcode is installed, those rules show an error or list nothing. A folder macOS keeps from programs without Full Disk Access shows as unreadable too.

## Programs it starts

A scan starts these programs, each read-only. A program that is not installed means its rules do not apply to this Mac: they find nothing, and say nothing. Some rules take a missing program otherwise: without `ps` or `lsof`, or without `fnm` on your `PATH`, their rules show an error (below); `mdfind` missing, failing or finding nothing counts as Spotlight knowing no Android Studio or Xcode; and `xcrun` finding no `simctl` is what sends sim-runtimes-no-xcode looking.

| Program | Why |
|---|---|
| `sw_vers -productVersion` | the macOS version, for rules whose paths moved between releases |
| `xcrun simctl list devices -j`, `xcrun simctl runtime list -j` | simulator devices and runtimes |
| `tmutil listlocalsnapshots /` | local Time Machine snapshots |
| `osascript -l JavaScript` | asks macOS for free space with and without purgeable space; it reads two volume values and sends no Apple Event |
| `docker system df -v`, `docker container inspect`, `docker volume inspect`, `docker image inspect` | Docker's containers, images, volumes and build cache, from the daemon your current docker context names; the container query asks for id, name, state, times, mounts, labels, and image name and id, never the environment |

With `--beta-rules`, also:

| Program | Why |
|---|---|
| `ps -x -U <your uid>`, and `ps -E -p <pids>` for running `claude` processes | which agent sessions are still running; from a `claude` process's environment regrow uses only `CLAUDE_CONFIG_DIR` and `HOME` |
| `lsof -a -d cwd -u <your uid>`, `ps -x -U <your uid> -o args=` | which projects and npx folders a running process works in or names |
| `lsof -a -u <your uid> -d txt -F pn` | which Node versions a running process executes or maps a file from |
| `mdfind` for Android Studio's or Xcode's bundle identifier | whether Android Studio or Xcode is installed somewhere Applications does not show |
| `xcrun --find simctl`, then `/Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/Resources/bin/simctl runtime list -j` | simulator runtimes left after Xcode went, listed only when no Xcode is found |
| `git` (`worktree list`, `for-each-ref`, `rev-list`, `status --ignored`, `ls-files`, `rev-parse`, `check-ignore`, `config --get`) | whether a worktree is finished and whether a build folder is ignored; every call runs with `--no-optional-locks`, so git does not even refresh its index, and each has 30 seconds |
| `/usr/bin/plutil -convert xml1 -o - -` | turns an app's binary `Info.plist`, handed to it on its input, into text, for the app's identifier and version |

When they need `ps` or `lsof` and cannot run it, the agent-session, build-output, npx and fnm rules show an error and offer nothing.

`regrow scan --json` also takes one free-space sample before the scan. `regrow doctor` scans only the rules that watch for a runaway bug and the phantom-space rules, so of the programs above it starts `sw_vers`, `tmutil` and `osascript`, and it adds `go env GOFLAGS` (with `GOTOOLCHAIN=local`, so Go downloads nothing). Its other checks read Docker Desktop's settings file and `~/.docker/daemon.json`, look for videos in the aerial wallpaper folder, and search your project folders, 3 levels down, for git repositories and read their worktree records.

Each rule's query has 2 minutes. One that gives no answer by then turns its rule into an error, and a rule with an error is never ticked.

## What it writes

One file: `~/.local/state/regrow/docker-volumes.json`, written through `docker-volumes.json.tmp`, and only when Docker's daemon answers. It records, for each Docker volume, the latest use a scan saw a container make of it, so that date outlives the container; a use no scan saw is not recorded. `regrow doctor`, `regrow history` and `regrow rules` write nothing.

`XDG_STATE_HOME` moves this folder and every other `~/.local/state/regrow` path on this page; `XDG_CONFIG_HOME` moves `~/.config/regrow`.

## What a run adds

A run starts only from a plan you confirmed, at the prompt or in advance with `--yes`. On top of the scan it:

- appends to `~/.local/state/regrow/oplog.jsonl`, one line synced to disk before each action and one when it ends;
- tells Finder through `osascript` to move each Trash item to the Trash, or, when Finder fails for any reason but a cancelled run, renames the item into `~/.local/state/regrow/staging/<run-id>/`; an item on another volume cannot be renamed there, so that action fails and the item stays;
- runs each rule's own command as the plan shows it, `sudo` included where the rule asks for it, in your terminal so `sudo` can ask for your password (the app leaves such a command for you to run in Terminal);
- for a Docker volume, first writes `~/.local/state/regrow/staging/<run-id>/docker-volume-<name>.tar` by running `docker run --rm -v <name>:/volume:ro busybox tar …`;
- right before removing an agent session's scratch, a worktree, a build folder, an npx folder, a download or a Node version, runs that rule's recheck; [RULES.md](RULES.md) says what each one checks.

`regrow undo` renames items back from the Trash or staging, without Finder, and appends to the oplog. `regrow tick` asks for free space through `osascript` and appends the sample to `~/.local/state/regrow/headroom.jsonl`, which about once a day it rewrites through `headroom.jsonl.tmp` to drop samples older than 30 days; on an acute alert it runs `ps -axo pid=,ppid=,rss=,comm=` to name the process using the most memory; with `--autotrim`, when free space is low and autotrim is unlocked, it records in `~/.local/state/regrow/prune.state` whether a running build defers the prune, and otherwise deletes Go build cache entries as `regrow prune go-build --yes` does, described next, until free space is back at its target. `regrow prune go-build --yes` runs `go env GOCACHE`, `ps -axo pid=,ppid=,rss=,comm=` and `/usr/bin/find <GOCACHE> … -delete`, journals to the oplog, and locks `~/.local/state/regrow/prune.state`. The menubar app's own files are in [APP.md](APP.md).

regrow never deletes its staging folder. A Docker volume's tarball stays there until you remove it, and anything Finder could not take until you remove it or `regrow undo` moves it back.

## The network

regrow's own code opens no network connection: it has no telemetry, no update check and no account. Some programs it starts can reach the network on their own:

- the Docker volume export runs the `busybox` image, which Docker downloads the first time;
- `docker` commands go to the daemon your docker context names, which is on this Mac for Docker Desktop and may be elsewhere if you pointed it there;
- a tool's own cleanup command does what that tool does: `go clean` run inside a module that asks for a newer Go can download that toolchain first, and `ollama rm` talks to the Ollama server.

Nothing regrow learns about your disk leaves the machine.
