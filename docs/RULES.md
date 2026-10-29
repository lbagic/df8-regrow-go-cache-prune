<!-- Generated from rules/*.yaml by `go test ./internal/rulesdoc -update`. Edit the rules, not this file. -->

# What regrow scans

regrow's catalog holds 62 rules: 27 safe, 23 caution and 12 surface-only. 25 of them are beta, and a scan includes them only with `--beta-rules`. Every entry below is written from the rule's file in [`rules/`](../rules), the catalog `regrow rules --beta-rules` lists. Paths are the macOS ones; the rules also carry Linux paths, for a Linux build that has not shipped. [SCAN.md](SCAN.md) says what a scan reads, which programs it runs, and what regrow writes.

How to read an entry:

- **safe**: ticked from the start. `regrow clean` with no ids, the checklist and the app begin with every safe rule that found something without an error. When some of a rule's items could not be read at all, only the rest are ticked, and a rule whose command acts on all its items at once is not ticked.
- **caution**: acted on only when you name it or tick it.
- **surface-only**: shown with its size, never deleted. regrow refuses it even when you name it, and refuses anything that holds it.
- **beta**: in a scan only with `--beta-rules`.
- **Looks at**: the folders the rule measures, or what its scan walks and runs.
- **Removed by**: a move to the Trash, which `regrow undo` puts back (into regrow staging when Finder fails); or the command the entry names, mostly the tool's own cleanup command, which deletes for good; or never. Nothing is removed until you confirm a plan (`--yes` confirms it in advance), except by the Go build cache prune: `regrow prune go-build --yes` deletes without showing a plan first, and autotrim, which deletes the same kind of entries by itself when free space is low, stays off until you turn it on.
- **Keeps**: what the rule leaves alone, where that is not plain from what it looks at. The list names the main reasons; the code has narrower ones, mostly an item it cannot read or judge, and each of them keeps more, never less.
- **Comes back**: what recreates it, and what that costs.

## AI models

Model caches and libraries of AI tools.

| Rule | Risk | Removed by |
|---|---|---|
| [Chrome on-device AI model](#chrome-ai-model) | surface-only, beta | never |
| [Stable Diffusion model libraries (ComfyUI/SD-WebUI)](#comfyui-sd-models) | surface-only, beta | never |
| [GPT4All models](#gpt4all-models) | caution, beta | Trash |
| [HuggingFace datasets cache](#hf-datasets) | caution, beta | Trash |
| [HuggingFace hub models](#hf-hub) | caution, beta | Trash |
| [HuggingFace Xet download cache](#hf-xet) | safe, beta | Trash |
| [Keras models + datasets cache](#keras-cache) | safe, beta | Trash |
| [llama.cpp model cache](#llama-cpp-cache) | safe, beta | Trash |
| [LM Studio models](#lm-studio-models) | caution, beta | Trash |
| [Ollama models](#ollama-models) | caution, beta | `ollama rm {arg}` |
| [PyTorch hub cache](#torch-hub-cache) | safe, beta | Trash |
| [Weights & Biases cache](#wandb-cache) | safe, beta | Trash |
| [Whisper model cache](#whisper-cache) | safe, beta | Trash |

### chrome-ai-model

**Chrome on-device AI model** · surface-only · beta · [`rules/chrome-ai-model.yaml`](../rules/chrome-ai-model.yaml)

- **Looks at:** `~/Library/Application Support/Google/Chrome/OptGuideOnDeviceModel`.
- **Removed by:** never: surface-only.
- **Comes back:** Chrome downloads the model again while On-device AI is on and the Mac has room for it. Cost: n/a — regrow never deletes here.
- **Note:** Chrome ▸ Settings ▸ AI Innovations ▸ turn off On-device AI: Chrome deletes the model and does not download it again while the setting is off. Deleting the folder by hand does not stop the next download. On a managed Mac, the GenAILocalFoundationalModelSettings policy set to 1 does the same.

### comfyui-sd-models

**Stable Diffusion model libraries (ComfyUI/SD-WebUI)** · surface-only · beta · [`rules/comfyui-sd-models.yaml`](../rules/comfyui-sd-models.yaml)

- **Looks at:** folders named `models` that hold `checkpoints`, up to 5 levels below `~`, those that exist; the walk does not enter `.Trash`, `.git`, `Library` or `node_modules` folders, or a folder it matched.
- **Removed by:** never: surface-only.
- **Comes back:** Not regenerable — downloaded checkpoints may no longer be hosted; merges and finetunes are one of a kind. Cost: potentially unrecoverable models.
- **Note:** Model libraries are often irreplaceable (custom merges, deleted civitai uploads) — review and prune inside the app's folder yourself.

### gpt4all-models

**GPT4All models** · caution · beta · [`rules/gpt4all-models.yaml`](../rules/gpt4all-models.yaml)

- **Looks at:** `~/Library/Application Support/nomic.ai/GPT4All/*.gguf`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Keeps:**
  - The LocalDocs database beside the models: only `.gguf` files match.
- **Comes back:** Re-download any model from GPT4All's in-app catalog. Cost: re-download GBs; network.

### hf-datasets

**HuggingFace datasets cache** · caution · beta · [`rules/hf-datasets.yaml`](../rules/hf-datasets.yaml)

- **Looks at:** `~/.cache/huggingface/datasets`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** datasets re-downloads and re-processes on next load_dataset(). Cost: re-processing time (can be hours for large corpora); network.
- **Note:** Preprocessed Arrow caches — the datasets library rebuilds them, but big corpora reprocess for hours.

### hf-hub

**HuggingFace hub models** · caution · beta · [`rules/hf-hub.yaml`](../rules/hf-hub.yaml)

- **Looks at:** each `models--…`, `datasets--…` and `spaces--…` folder in the Hugging Face hub cache (`$HF_HUB_CACHE`, `$HUGGINGFACE_HUB_CACHE` or `$HF_HOME/hub`, else `~/.cache/huggingface/hub`); no program runs.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Re-downloads on the next from_pretrained()/hf download of that repo. Cost: re-download GBs; network.
- **Note:** Removes every cached revision of that model/dataset. Gated repos need your HF token again to re-download.

### hf-xet

**HuggingFace Xet download cache** · safe · beta · [`rules/hf-xet.yaml`](../rules/hf-xet.yaml)

- **Looks at:** `~/.cache/huggingface/xet`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Chunks re-populate as models are downloaded through Xet. Cost: slightly slower next downloads.

### keras-cache

**Keras models + datasets cache** · safe · beta · [`rules/keras-cache.yaml`](../rules/keras-cache.yaml)

- **Looks at:** `~/.keras/models`, `~/.keras/datasets`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Keeps:**
  - `~/.keras/keras.json`, your Keras settings: only the two download folders are looked at.
- **Comes back:** Keras re-downloads weights and datasets on next use. Cost: re-download; network.

### llama-cpp-cache

**llama.cpp model cache** · safe · beta · [`rules/llama-cpp-cache.yaml`](../rules/llama-cpp-cache.yaml)

- **Looks at:** `~/Library/Caches/llama.cpp`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** llama.cpp re-downloads on the next -hf run. Cost: re-download GBs; network.

### lm-studio-models

**LM Studio models** · caution · beta · [`rules/lm-studio-models.yaml`](../rules/lm-studio-models.yaml)

- **Looks at:** `~/.lmstudio/models/*/*`, `~/.cache/lm-studio/models/*/*`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Re-download any model from LM Studio's in-app catalog. Cost: re-download GBs; network.
- **Note:** Only the default models location is scanned — if you moved it in LM Studio's settings, manage models in-app.

### ollama-models

**Ollama models** · caution · beta · [`rules/ollama-models.yaml`](../rules/ollama-models.yaml)

- **Looks at:** Ollama's model manifests under `$OLLAMA_MODELS/manifests`, else `~/.ollama/models/manifests`, so models show while the server is stopped; no program runs. A model's size is what removing it alone frees: the blobs no other model uses, at the sizes the manifests declare.
- **Removed by:** `ollama rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** ollama pull re-downloads any registry model on demand. Cost: re-download GBs; network.
- **Note:** Custom models built with `ollama create` are not re-pullable. `ollama rm` needs the Ollama server running.

### torch-hub-cache

**PyTorch hub cache** · safe · beta · [`rules/torch-hub-cache.yaml`](../rules/torch-hub-cache.yaml)

- **Looks at:** `~/.cache/torch/hub`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** torch.hub re-downloads checkpoints on next load. Cost: re-download; network.

### wandb-cache

**Weights & Biases cache** · safe · beta · [`rules/wandb-cache.yaml`](../rules/wandb-cache.yaml)

- **Looks at:** `~/.cache/wandb`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** wandb re-downloads artifacts from the cloud on next use. Cost: re-download; network.

### whisper-cache

**Whisper model cache** · safe · beta · [`rules/whisper-cache.yaml`](../rules/whisper-cache.yaml)

- **Looks at:** `~/.cache/whisper`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** whisper re-downloads the model on next run. Cost: re-download; network.

## Developer caches and build output

Package caches, build caches and build output of developer tools, and what coding agents leave behind.

| Rule | Risk | Removed by |
|---|---|---|
| [Agent session scratch (ended sessions)](#agent-scratch) | caution, beta | Trash |
| [Agent session scratch (kept)](#agent-scratch-kept) | surface-only, beta | never |
| [Homebrew cache + old versions](#brew-cache) | safe | `brew cleanup -s --prune=all` |
| [Build output in idle projects](#build-output) | caution, beta | Trash |
| [Claude Code logs + caches](#claude-code-cache) | safe | Trash |
| [Finished git worktrees](#git-worktrees) | caution, beta | `git -C {path} worktree remove {path}` |
| [Go build cache](#go-build-cache) | safe | `go clean -cache` |
| [Go module cache](#go-mod-cache) | caution | `go clean -modcache` |
| [node_modules in project trees](#node-modules-dirs) | caution | Trash |
| [npm cache](#npm-cache) | safe | `npm cache clean --force` |
| [npx package folders](#npx-cache) | safe, beta | Trash |
| [pip cache](#pip-cache) | safe | `pip3 cache purge` |
| [Playwright browser cache](#playwright-cache) | safe | Trash |
| [pnpm store (unreferenced packages)](#pnpm-store) | safe | `pnpm store prune` |
| [Rust target/ build directories](#rust-target-dirs) | safe | Trash |
| [uv cache](#uv-cache) | safe | `uv cache prune` |
| [Yarn cache](#yarn-cache) | safe | `yarn cache clean` |

### agent-scratch

**Agent session scratch (ended sessions)** · caution · beta · [`rules/agent-scratch.yaml`](../rules/agent-scratch.yaml)

- **Looks at:** Claude Code's session scratch folders, `/private/tmp/claude-<uid>/<project>/<session-id>/`. Which sessions have ended comes from `ps` (your processes, and `CLAUDE_CONFIG_DIR` and `HOME` from the environment of each running `claude`), Claude Code's session registries (`<config>/sessions/`) and its transcripts (`~/.claude*/projects/`).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back. Right before each removal, regrow judges the session again, and leaves the folder in place if the session is kept now or regrow cannot tell.
- **Keeps:**
  - A session a running `claude` process is registered for, or was started with (`--resume`, `-r`, `--session-id`).
  - A session whose scratch holds the working folder of a running, registered `claude` session, or the program of a running process started by its full path (another process working in the scratch, a dev server say, does not keep it).
  - A session whose transcript or scratch changed in the last 24 hours, or since an unregistered `claude` process that a session started began running.
  - A session whose scratch holds a git worktree, or a repository with a worktree elsewhere.
  - A session whose transcript or scratch could not be read in full, and every session when a project folder under the scratch root cannot be read.
  - Everything, when regrow cannot tell which sessions are running (`ps` or a registry it cannot read): the rule then shows an error and offers nothing.
  - Kept sessions are listed, with the reason, under `agent-scratch-kept`.
- **Comes back:** Not regenerated — scratch is what an agent wrote while it worked; new sessions start with an empty one. Cost: an ended session's intermediate files; nothing for new sessions.
- **Note:** Resuming an ended session finds its scratchpad gone. Live sessions, and sessions holding a git worktree, are listed under "Agent session scratch (kept)".

### agent-scratch-kept

**Agent session scratch (kept)** · surface-only · beta · [`rules/agent-scratch-kept.yaml`](../rules/agent-scratch-kept.yaml)

- **Looks at:** the sessions `agent-scratch` reads, in the same pass; each row says why it is kept.
- **Removed by:** never: surface-only.
- **Comes back:** n/a — regrow never deletes these; end the session, or remove the worktree it holds, and rescan. Cost: n/a.
- **Note:** Each row says why it is kept. Ended sessions move to "Agent session scratch (ended sessions)" a day or more after their last change.

### brew-cache

**Homebrew cache + old versions** · safe · [`rules/brew-cache.yaml`](../rules/brew-cache.yaml)

- **Looks at:** `~/Library/Caches/Homebrew`.
- **Removed by:** `brew cleanup -s --prune=all`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Homebrew re-downloads bottles on the next install/upgrade. Cost: next upgrades re-download; network.

### build-output

**Build output in idle projects** · caution · beta · [`rules/build-output.yaml`](../rules/build-output.yaml)

- **Looks at:** folders named `.next`, `dist`, `.turbo` and `.venv`, up to 10 levels below `~/workspace`, `~/dev`, `~/projects`, `~/src` and `~/code`, without entering `.git`, `.Trash`, `Library` or `node_modules`. For each project: what git says (`rev-parse`, `check-ignore`, `ls-files`, all with `--no-optional-locks`), its files' change times and those of git's `HEAD`, and the working folders and command lines of your processes (`lsof`, `ps`).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back. Right before each removal, regrow judges the folder again, as the scan did, and leaves it in place if any reason to keep it holds now.
- **Keeps:**
  - A folder git tracks a file in, one no ignore rule names by its own name, or one beside which git tracks nothing.
  - A `.venv` without a `pyvenv.cfg`, and a folder that holds a git repository of its own.
  - A folder whose project changed in the last 30 days (a file changed, or git's HEAD moved; a deletion or a rename alone does not count), or that a running process works in or names.
  - A folder in a project git cannot read, a submodule whose superproject is outside the roots, and a project whose repository is kept elsewhere (a separate git dir, `core.worktree`).
  - Everything under a project root reached through a symlink.
  - Everything, when `lsof` or `ps` fails: the rule then shows an error and offers nothing.
- **Comes back:** The project's build recreates .next, dist and .turbo. A .venv comes back with a new environment and an install of its packages (python -m venv and pip install, or uv sync), which downloads them again. Cost: seconds to minutes per build, no network; minutes and the network per .venv.
- **Note:** A project is left out while a file in its folder changed in the last 30 days or a process works in it; a deletion or a rename alone does not count. A tool you run without changing it reads as idle - check the list before removing its dist or .venv.

### claude-code-cache

**Claude Code logs + caches** · safe · [`rules/claude-code-cache.yaml`](../rules/claude-code-cache.yaml)

- **Looks at:** `~/.claude/debug`, `~/.claude/cache`, `~/.claude/shell-snapshots`, `~/.claude/paste-cache`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Claude Code recreates logs and caches as it runs. Cost: none noticeable.
- **Doctor:** `regrow doctor` flags it above 5 GiB. Known Claude Code bug loops the debug log — 100–200GB observed. Healthy installs stay under a few hundred MB.
- **Note:** Session history in ~/.claude/projects is untouched.

### git-worktrees

**Finished git worktrees** · caution · beta · [`rules/git-worktrees.yaml`](../rules/git-worktrees.yaml)

- **Looks at:** git repositories (a folder holding `.git`) up to 6 levels below `~/workspace`, `~/dev`, `~/projects`, `~/src`, `~/code` and `/tmp/claude-<uid>`, and their linked worktrees as git describes them (`worktree list`, `for-each-ref`, `rev-list`, `status --ignored`, `ls-files`, all with `--no-optional-locks`), with each worktree's file times.
- **Removed by:** `git -C {path} worktree remove {path}`, once per item; `{path}` is the item's path. It deletes for good: `regrow undo` cannot bring it back. Right before each removal, regrow checks the worktree again, and leaves it in place if it is no longer clean or its HEAD is detached at a commit no branch holds. git itself refuses a worktree with modified or untracked files.
- **Keeps:**
  - The main worktree, and a worktree with `main`, `master` or the default branch checked out.
  - A worktree whose commits are in none of `main`, `master`, `origin/main`, `origin/master` and the remote's default branch, unless its branch's upstream is gone.
  - A worktree changed in the last 7 days: any file or folder, or git moving its HEAD.
  - A worktree where `git status --ignored` shows anything but whole ignored build folders (`node_modules`, `target`, `dist`, `.venv` and the like), so an ignored `.env` keeps it; and one with a tracked file flagged assume-unchanged or skip-worktree.
  - A locked or prunable worktree, one in the middle of a rebase, merge, cherry-pick, revert or bisect, and one that has or had a submodule.
  - Every worktree of a repository with no `origin/HEAD`, `main` or `master` to compare against.
- **Comes back:** git worktree add &lt;path&gt; &lt;branch&gt; checks the branch out again; installs and builds recreate the build output. Cost: seconds for the checkout; minutes to reinstall and rebuild.
- **Note:** Removes the whole worktree folder with its ignored build output (node_modules, target, .next, dist, …); the plan names any other rule's items inside it. The branch and its commits stay in the repository. git refuses a worktree with modified or untracked files, and the recheck right before the removal refuses one that gained any other unclean file, or a commit on a detached HEAD, since the scan.

### go-build-cache

**Go build cache** · safe · [`rules/go-build-cache.yaml`](../rules/go-build-cache.yaml)

- **Looks at:** `~/Library/Caches/go-build`.
- **Removed by:** `go clean -cache`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Repopulated automatically by the next go build. Cost: next builds slower once; no network.
- **Prune:** `regrow prune go-build --yes` deletes entries unused for 24h directly, oldest first, an hour of entries at a time, until the cache is down to about 15 GiB. Autotrim, once you turn it on, does the same while free space is low, and stops when it is back at its target. No Trash and no undo. A running `go`, `compile` or `link` refuses a prune by hand, and puts autotrim off unless builds have kept it waiting for hours and free space is critically low.
- **Fix the cause:** Go worktrees without -trimpath. `regrow doctor` says whether it is in effect here and prints the fix; regrow changes nothing.

### go-mod-cache

**Go module cache** · caution · [`rules/go-mod-cache.yaml`](../rules/go-mod-cache.yaml)

- **Looks at:** `~/go/pkg/mod`.
- **Removed by:** `go clean -modcache`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Modules re-download on the next build of each project. Cost: network + minutes; offline builds break until re-fetched.

### node-modules-dirs

**node_modules in project trees** · caution · [`rules/node-modules-dirs.yaml`](../rules/node-modules-dirs.yaml)

- **Looks at:** folders named `node_modules`, up to 10 levels below `~/workspace`, `~/dev`, `~/projects`, `~/src` and `~/code`, those that exist; the walk does not enter `.Trash`, `.git` or `Library` folders, or a folder it matched.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** npm/yarn/pnpm install recreates it from the project lockfile. Cost: minutes per project; network.
- **Note:** Pick projects you are not actively working in — reinstall is a command away but takes minutes each.

### npm-cache

**npm cache** · safe · [`rules/npm-cache.yaml`](../rules/npm-cache.yaml)

- **Looks at:** `~/.npm/_cacache`.
- **Removed by:** `npm cache clean --force`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** npm re-fetches packages into the cache as installs need them. Cost: next installs re-download; network.

### npx-cache

**npx package folders** · safe · beta · [`rules/npx-cache.yaml`](../rules/npx-cache.yaml)

- **Looks at:** the folders in `~/.npm/_npx`, one per package spec npx ran, each with the newest modification time of anything in it and the package names in its `package.json`, and the working folders and command lines of your processes (`lsof`, `ps`).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back. Right before each removal, regrow judges the folder again, as the scan did, and leaves it in place if any reason to keep it holds now or your processes cannot be listed.
- **Keeps:**
  - A folder a running process works in or names on its command line.
  - A folder with anything in it modified in the last 7 days: npx changes its folder each time it runs the package.
  - A folder that cannot all be read, and one whose path `lsof` and `ps` would not print as it is.
  - Everything, when `lsof` or `ps` fails: the rule then shows an error and offers nothing.
- **Comes back:** npx installs a package again the next time it runs it. Cost: next npx run re-downloads; network.

### pip-cache

**pip cache** · safe · [`rules/pip-cache.yaml`](../rules/pip-cache.yaml)

- **Looks at:** `~/Library/Caches/pip`.
- **Removed by:** `pip3 cache purge`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** pip re-downloads wheels as installs need them. Cost: next installs re-download; network.

### playwright-cache

**Playwright browser cache** · safe · [`rules/playwright-cache.yaml`](../rules/playwright-cache.yaml)

- **Looks at:** `~/Library/Caches/ms-playwright`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** npx playwright install re-downloads the browsers a project needs. Cost: next test run re-downloads (100s of MB per browser).
- **Doctor:** `regrow doctor` flags it above 10 GiB. Playwright's transform-cache bug grows this dir unbounded — 26GB+ observed. A few browser builds should stay in the low GBs.

### pnpm-store

**pnpm store (unreferenced packages)** · safe · [`rules/pnpm-store.yaml`](../rules/pnpm-store.yaml)

- **Looks at:** `~/Library/pnpm/store`, `~/.pnpm-store`.
- **Removed by:** `pnpm store prune`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Only unreferenced packages are pruned; anything needed re-downloads. Cost: re-download if an old project resurfaces; network.

### rust-target-dirs

**Rust target/ build directories** · safe · [`rules/rust-target-dirs.yaml`](../rules/rust-target-dirs.yaml)

- **Looks at:** folders named `target` that hold `CACHEDIR.TAG`, up to 6 levels below `~/workspace`, `~/dev`, `~/projects`, `~/src` and `~/code`, those that exist; the walk does not enter `.Trash`, `.git`, `Library` or `node_modules` folders, or a folder it matched.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** cargo rebuilds the project on the next cargo build. Cost: minutes to tens of minutes per project; no network.

### uv-cache

**uv cache** · safe · [`rules/uv-cache.yaml`](../rules/uv-cache.yaml)

- **Looks at:** `~/.cache/uv`.
- **Removed by:** `uv cache prune`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** uv re-populates the cache as environments are built. Cost: next installs re-download; network.

### yarn-cache

**Yarn cache** · safe · [`rules/yarn-cache.yaml`](../rules/yarn-cache.yaml)

- **Looks at:** `~/Library/Caches/Yarn`.
- **Removed by:** `yarn cache clean`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Yarn re-downloads package tarballs on the next install. Cost: next installs re-download; network.

## Toolchains

Installed SDKs and tool versions.

| Rule | Risk | Removed by |
|---|---|---|
| [Android NDKs (no Android Studio)](#android-ndk) | caution, beta | Trash |
| [Android SDK (no Android Studio)](#android-sdk) | caution, beta | Trash |
| [Node versions nothing references (fnm)](#fnm-node-versions) | caution, beta | `fnm uninstall {arg}` |

### android-ndk

**Android NDKs (no Android Studio)** · caution · beta · [`rules/android-ndk.yaml`](../rules/android-ndk.yaml)

- **Looks at:** the NDKs in `~/Library/Android/sdk/ndk/` and `~/Library/Android/sdk/ndk-bundle`, and, to tell whether an Android Studio is installed, the apps in `/Applications` and `~/Applications` and one folder down, JetBrains Toolbox's apps in `~/Library/Application Support/JetBrains/Toolbox/apps`, and Spotlight (`mdfind` for Android Studio's bundle identifier).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Keeps:**
  - Every NDK while an Android Studio is installed in any of those places.
  - Every NDK when an Applications folder or JetBrains Toolbox's apps folder cannot be read in full, or a link in an Applications folder cannot be followed: the rule then shows an error. A Toolbox folder that is a link leading nowhere, and `mdfind` missing, failing or finding nothing, count as no Android Studio; `mdfind` hanging ends in the query's 2-minute error.
  - An NDK reached through a symlink, and an SDK anywhere but `~/Library/Android/sdk` or reached through a symlink.
- **Comes back:** sdkmanager "ndk;&lt;version&gt;" (or the Gradle build that names it) downloads it again. Cost: a 1–2 GB download per NDK; needs the network.
- **Note:** Command-line Android builds (Gradle, React Native, Flutter) use the SDK without Studio; a build that needs this NDK fails until it is installed again. The android-sdk row says when ANDROID_HOME points at the SDK.

### android-sdk

**Android SDK (no Android Studio)** · caution · beta · [`rules/android-sdk.yaml`](../rules/android-sdk.yaml)

- **Looks at:** `~/Library/Android/sdk` as one folder; `ANDROID_HOME` and `ANDROID_SDK_ROOT`, to say on the row when one points at it; and, to tell whether an Android Studio is installed, the apps in `/Applications` and `~/Applications` and one folder down, JetBrains Toolbox's apps in `~/Library/Application Support/JetBrains/Toolbox/apps`, and Spotlight (`mdfind` for Android Studio's bundle identifier).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Keeps:**
  - The SDK while an Android Studio is installed in any of those places.
  - The SDK when an Applications folder or JetBrains Toolbox's apps folder cannot be read in full, or a link in an Applications folder cannot be followed: the rule then shows an error. A Toolbox folder that is a link leading nowhere, and `mdfind` missing, failing or finding nothing, count as no Android Studio; `mdfind` hanging ends in the query's 2-minute error.
  - An SDK anywhere but `~/Library/Android/sdk`, or reached through a symlink.
- **Comes back:** Installing Android Studio, or sdkmanager from the command-line tools, downloads the SDK packages a build needs again. Cost: several GB of downloads; needs the network.
- **Note:** Command-line Android builds (Gradle, React Native, Flutter) use the SDK without Studio, and emulators need its system images; the row says when ANDROID_HOME points here. Undo restores it from the Trash.

### fnm-node-versions

**Node versions nothing references (fnm)** · caution · beta · [`rules/fnm-node-versions.yaml`](../rules/fnm-node-versions.yaml)

- **Looks at:** fnm's folder (`$FNM_DIR`, or whichever exists of `fnm` in `$XDG_DATA_HOME` (else `~/.local/share`), `~/.fnm` and `~/Library/Application Support/fnm`) with its `node-versions` and `aliases`; every `.nvmrc` and `.node-version` in your home folder and, its name in any case (`.NVMRC`), in folders up to 10 levels below `~/workspace`, `~/dev`, `~/projects`, `~/src` and `~/code`, following links, counting levels along the path as the link spells it, and not entering `.git`, `.Trash` or `node_modules`; the launchd job plists in `~/Library/LaunchAgents`, `/Library/LaunchAgents` and `/Library/LaunchDaemons`, and the version files in each folder a plist names and the folders above it; and the files your running processes execute or map (`lsof -d txt`).
- **Removed by:** `fnm uninstall {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back. Right before each removal, regrow reads every source again, as the scan did, and leaves the version installed if anything names it now or a source cannot be read.
- **Keeps:**
  - A version an fnm alias points at, the default included.
  - A version a `.nvmrc` or `.node-version` there resolves to, the way fnm resolves one (`20` is the newest installed 20.x).
  - A version a launchd job's plist names (any string holding its `/node-versions/<version>` folder, or an absolute path that leads into it through links), runs through fnm (`fnm exec --using=18`), or leads fnm to through a version file.
  - A version a running process of yours executes or maps a file from.
  - A folder in `node-versions` not named like a release (`vX.Y.Z`).
  - Every version, when any of those cannot be read or understood, when a link among those folders, plists, the paths a plist names, or version files leads nowhere (but for a link to a name gone from its own folder, such as a build's lock file) or cannot be followed, when fnm has more than one folder and `FNM_DIR` names none, when a folder belongs to another user (a run under `sudo`), or when `fnm` is not on your `PATH`: the rule then shows an error and offers nothing.
- **Comes back:** fnm install &lt;version&gt; downloads and installs it again; npm packages installed globally into it need installing again too. Cost: a download of about 30–50 MB per version, plus its global packages; needs the network.
- **Note:** A project that pins this version only in package.json engines or sits outside the project roots, a shell where you ran `fnm use` with it, and a cron job are not checked; each asks for `fnm install` again.

## Xcode and simulators

| Rule | Risk | Removed by |
|---|---|---|
| [CoreSimulator caches](#sim-caches) | safe | Trash |
| [iOS simulator devices](#sim-devices) | caution | `xcrun simctl delete {arg}` |
| [Unavailable simulator devices](#sim-devices-unavailable) | safe | `xcrun simctl delete unavailable` |
| [Simulator runtimes](#sim-runtimes) | caution | `xcrun simctl runtime delete {arg}` |
| [Simulator runtimes (no Xcode)](#sim-runtimes-no-xcode) | surface-only, beta | never |
| [Xcode Archives (released builds + dSYMs)](#xcode-archives) | caution | Trash |
| [Xcode DerivedData](#xcode-derived-data) | safe | Trash |
| [Xcode device support symbols](#xcode-device-support) | safe | Trash |

### sim-caches

**CoreSimulator caches** · safe · [`rules/sim-caches.yaml`](../rules/sim-caches.yaml)

- **Looks at:** `~/Library/Developer/CoreSimulator/Caches`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Simulator runtime caches rebuild as simulators boot. Cost: first simulator boot slower; no network.

### sim-devices

**iOS simulator devices** · caution · [`rules/sim-devices.yaml`](../rules/sim-devices.yaml)

- **Looks at:** `xcrun simctl list devices -j`; one row per available device, with the size simctl reports.
- **Removed by:** `xcrun simctl delete {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Recreate any device via Xcode or `xcrun simctl create`. Cost: simulator contents (apps, settings, data) are gone.
- **Note:** Erases that simulator's installed apps and data; the device itself is recreatable in seconds.

### sim-devices-unavailable

**Unavailable simulator devices** · safe · [`rules/sim-devices-unavailable.yaml`](../rules/sim-devices-unavailable.yaml)

- **Looks at:** `xcrun simctl list devices -j`; the devices simctl reports as unavailable.
- **Removed by:** `xcrun simctl delete unavailable`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** Nothing to regenerate — these simulators reference SDKs that are gone. Cost: none.

### sim-runtimes

**Simulator runtimes** · caution · [`rules/sim-runtimes.yaml`](../rules/sim-runtimes.yaml)

- **Looks at:** `xcrun simctl runtime list -j`; one row per runtime, with the size simctl reports.
- **Removed by:** `xcrun simctl runtime delete {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - Runtimes simctl reports as not deletable.
- **Comes back:** Xcode re-downloads a runtime on demand (Settings ▸ Components). Cost: multi-GB download per runtime.
- **Note:** Existing simulators of a deleted runtime stop booting until it is re-downloaded.

### sim-runtimes-no-xcode

**Simulator runtimes (no Xcode)** · surface-only · beta · [`rules/sim-runtimes-no-xcode.yaml`](../rules/sim-runtimes-no-xcode.yaml)

- **Looks at:** `xcrun --find simctl`; the apps named like Xcode in `/Applications` and `~/Applications` and one folder down, with their `Info.plist`; Spotlight (`mdfind` for Xcode's bundle identifier); and, only when none of these finds Xcode, `simctl runtime list -j` from CoreSimulator's own copy, `/Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/Resources/bin/simctl`.
- **Removed by:** never: surface-only.
- **Keeps:**
  - Every runtime while `xcrun` finds `simctl`, when `sim-runtimes` lists them, or an Xcode is installed; with an Xcode installed but `xcode-select` pointing at the Command Line Tools, neither rule lists them.
  - Every runtime when an Xcode-named app, an Applications folder or a link there cannot be read or followed, since an Xcode there cannot be ruled out.
- **Comes back:** Xcode downloads a runtime again when a simulator needs it (Settings ▸ Components). Cost: n/a — regrow never deletes these; a 5–10 GB download per runtime if one is needed again.
- **Note:** Regrow does not delete these. With the simctl Xcode left behind, /Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/Resources/bin/simctl runtime list names each runtime's identifier, and the same simctl's runtime delete &lt;identifier&gt; --dry-run shows what would go; without --dry-run it is meant to delete it, which is untested without Xcode. Simulators of a deleted runtime stop booting until it is downloaded again.

### xcode-archives

**Xcode Archives (released builds + dSYMs)** · caution · [`rules/xcode-archives.yaml`](../rules/xcode-archives.yaml)

- **Looks at:** `~/Library/Developer/Xcode/Archives/*/*.xcarchive`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Not regenerable — each archive is a build artifact of a past release. Cost: symbolication of old crash reports lost unless dSYMs live elsewhere.
- **Note:** dSYMs here symbolicate crashes of shipped builds — fine to delete only if uploaded to App Store Connect or archived elsewhere.

### xcode-derived-data

**Xcode DerivedData** · safe · [`rules/xcode-derived-data.yaml`](../rules/xcode-derived-data.yaml)

- **Looks at:** `~/Library/Developer/Xcode/DerivedData`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Xcode rebuilds intermediates and re-indexes on the next build. Cost: first build per project slower; no network.

### xcode-device-support

**Xcode device support symbols** · safe · [`rules/xcode-device-support.yaml`](../rules/xcode-device-support.yaml)

- **Looks at:** `~/Library/Developer/Xcode/iOS DeviceSupport`, `~/Library/Developer/Xcode/watchOS DeviceSupport`, `~/Library/Developer/Xcode/tvOS DeviceSupport`, `~/Library/Developer/Xcode/visionOS DeviceSupport`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Xcode re-extracts symbols the next time each device is connected. Cost: a few minutes per device on next connect; no network.

## Docker

Docker objects, listed through the `docker` command; regrow never touches Docker's files directly.

| Rule | Risk | Removed by |
|---|---|---|
| [Docker build cache unused 30d+](#docker-build-cache) | safe | `docker builder prune --force --filter unused-for=720h` |
| [Docker containers stopped 30d+](#docker-containers-stopped) | safe | `docker rm {arg}` |
| [Docker dangling images](#docker-images-dangling) | safe | `docker image rm {arg}` |
| [Docker unused images pulled 30d+](#docker-images-unused) | caution, beta | `docker image rm {arg}` |
| [Docker stale anonymous volumes](#docker-volumes-anon) | caution | `docker volume rm {arg}` |
| [Docker volumes (kept)](#docker-volumes-kept) | surface-only | never |
| [Docker dangling named volumes](#docker-volumes-named) | caution | `docker volume rm {arg}` |

### docker-build-cache

**Docker build cache unused 30d+** · safe · [`rules/docker-build-cache.yaml`](../rules/docker-build-cache.yaml)

- **Looks at:** `docker system df -v`, through the daemon your current docker context points at. The size counts records not in use, not shared with an image layer, and last used 30 or more days ago; a record with no last-used time is not counted.
- **Removed by:** `docker builder prune --force --filter unused-for=720h`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - Build records used in the last 30 days: the command's own filter, `unused-for=720h`, leaves them.
- **Comes back:** The next docker build repopulates exactly the layers it needs. Cost: next builds slower.
- **Fix the cause:** Docker build-cache limit. `regrow doctor` says whether it is in effect here and prints the fix; regrow changes nothing.

### docker-containers-stopped

**Docker containers stopped 30d+** · safe · [`rules/docker-containers-stopped.yaml`](../rules/docker-containers-stopped.yaml)

- **Looks at:** `docker system df -v` and `docker container inspect` (id, name, state, times, mounts, labels, image name and id; never the environment), through the daemon your current docker context points at.
- **Removed by:** `docker rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - A container in any state but created, exited or dead: running, paused, restarting or being removed.
  - A container created, started or stopped in the last 30 days.
  - A container the keep-list in `~/.config/regrow/config.yaml` names, by name glob or compose project.
- **Comes back:** docker run / compose up recreates containers from their images; uncommitted writable-layer changes do not come back. Cost: container-layer changes are lost; recreate is one command.
- **Note:** Anonymous volumes of removed containers are NOT deleted — they become dangling and show up under the volume rules on the next scan.

### docker-images-dangling

**Docker dangling images** · safe · [`rules/docker-images-dangling.yaml`](../rules/docker-images-dangling.yaml)

- **Looks at:** `docker system df -v`, `docker image inspect` and `docker container inspect`, through the daemon your current docker context points at; an image `docker system df` shows no repository for and whose `RepoTags` is empty, that no container uses, is listed whatever its age.
- **Removed by:** `docker image rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - An image any container uses, stopped containers included; when Docker cannot say, every image counts as used.
  - An image with a tag, which belongs to `docker-images-unused`, and, on Docker's containerd image store, one pulled by digest alone, which Docker records in `RepoTags` and which is in neither rule. On the classic image store such an image has an empty `RepoTags`, so it is listed here.
- **Comes back:** Dangling layers are superseded build leftovers; the next build or pull recreates what is still needed. Cost: next build slower; network for re-pulled layers.

### docker-images-unused

**Docker unused images pulled 30d+** · caution · beta · [`rules/docker-images-unused.yaml`](../rules/docker-images-unused.yaml)

- **Looks at:** `docker system df -v`, `docker image inspect` and `docker container inspect`, through the daemon your current docker context points at.
- **Removed by:** `docker image rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - An image any container uses, stopped containers included; when Docker cannot say, every image counts as used.
  - An image pulled or tagged here in the last 30 days, or with no such date.
  - An image built here, one with no tag or more than one, and one known only by digest.
  - An image whose repository is not one Docker records it as pulled from or pushed to.
  - An image the keep-list in `~/.config/regrow/config.yaml` names, by repository or `repository:tag`.
- **Comes back:** docker pull, or the next docker run / compose up that names the image, downloads it again from the registry it was pulled from or pushed to. Cost: re-download over the network; needs registry access and the registry's copy, and a tag that moved since brings a newer image.
- **Note:** Docker records when an image was pulled, not when it last ran. An image used only by `docker run --rm`, or by a compose stack that is down, is listed here. Listed images are ones Docker records as pulled from or pushed to the repository they are named for. Docker does not record the tag, so a tag you made yourself inside that repository is listed too, though no registry has it; and an image built here and pushed under its own name is listed when Docker kept no record of the build, and only the registry's copy brings it back.

### docker-volumes-anon

**Docker stale anonymous volumes** · caution · [`rules/docker-volumes-anon.yaml`](../rules/docker-volumes-anon.yaml)

- **Looks at:** `docker system df -v`, `docker volume inspect` and `docker container inspect`, through the daemon your current docker context points at. A volume's last use is the latest create, start or stop of a container that mounted it when a scan looked, remembered in `~/.local/state/regrow/docker-volumes.json` so it outlives that container; a volume no scan saw mounted counts from its creation.
- **Removed by:** `docker volume rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back. Right before each removal, regrow exports the volume to a tarball in regrow staging, and leaves the volume in place if the export fails. That tarball is the only way back. A volume bigger than the export cap is never offered.
- **Keeps:**
  - A volume any container mounts, stopped containers included.
  - A volume the keep-list in `~/.config/regrow/config.yaml` names, by name glob or compose project.
  - A volume bigger than the export cap (10 GiB unless `volume_export_cap` says otherwise).
  - An anonymous volume created in the last 30 days, or whose last use a scan recorded in the last 30 days.
  - Kept volumes are listed, with the reason, under `docker-volumes-kept`.
- **Comes back:** Not regenerable — the pre-delete tarball in regrow staging is the recovery copy (docker volume create, then untar). Cost: data is gone for good once staging is cleared.
- **Note:** Anonymous volumes appear when a container runs without a named mount. Unreferenced and untouched for 30+ days; each is exported to a tarball in regrow staging before removal.

### docker-volumes-kept

**Docker volumes (kept)** · surface-only · [`rules/docker-volumes-kept.yaml`](../rules/docker-volumes-kept.yaml)

- **Looks at:** the volumes the two other volume rules read, in the same pass; each row says why it is kept.
- **Removed by:** never: surface-only.
- **Comes back:** n/a — regrow never deletes these; remove a keep reason (stop the container, edit the keep-list) and rescan to reclassify. Cost: n/a.
- **Note:** Each row says why it is kept. Protect more volumes via the keep-list (name glob or compose project) in ~/.config/regrow/config.yaml.

### docker-volumes-named

**Docker dangling named volumes** · caution · [`rules/docker-volumes-named.yaml`](../rules/docker-volumes-named.yaml)

- **Looks at:** `docker system df -v`, `docker volume inspect` and `docker container inspect`, through the daemon your current docker context points at. A named volume no container mounts is listed however recently it was used.
- **Removed by:** `docker volume rm {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back. Right before each removal, regrow exports the volume to a tarball in regrow staging, and leaves the volume in place if the export fails. That tarball is the only way back. A volume bigger than the export cap is never offered.
- **Keeps:**
  - A volume any container mounts, stopped containers included.
  - A volume the keep-list in `~/.config/regrow/config.yaml` names, by name glob or compose project.
  - A volume bigger than the export cap (10 GiB unless `volume_export_cap` says otherwise).
  - Kept volumes are listed, with the reason, under `docker-volumes-kept`.
- **Comes back:** Not regenerable — the pre-delete tarball in regrow staging is the recovery copy (docker volume create, then untar). Cost: data is gone for good once staging is cleared.
- **Note:** Named volumes hold real data (old database contents are the classic case). Check the project label and last-used date; each volume is exported to a tarball in regrow staging before removal.

## macOS

System caches, logs, downloads and the Trash.

| Rule | Risk | Removed by |
|---|---|---|
| [Aerial wallpaper videos](#aerial-wallpapers) | surface-only | never |
| [Installers of installed apps](#downloaded-installers) | caution, beta | Trash |
| [Local iOS device backups](#ios-backups) | surface-only | never |
| [App caches (~/Library/Caches)](#library-caches) | surface-only | never |
| [Stuck update payloads (/Library/Updates)](#library-updates) | surface-only | never |
| [Stale macOS installers](#macos-installers) | safe | Trash |
| [Downloaded macOS update](#macos-update-download) | surface-only, beta | never |
| [mediaanalysisd cache](#media-analysis-cache) | safe | Trash |
| [Rebuild Spotlight index](#spotlight-index) | caution | `sudo mdutil -E /` |
| [Empty Trash](#trash-empty) | caution | `osascript -e 'tell application "Finder" to empty trash'` |
| [Crash reports + diagnostic logs](#user-logs) | safe | Trash |

### aerial-wallpapers

**Aerial wallpaper videos** · surface-only · [`rules/aerial-wallpapers.yaml`](../rules/aerial-wallpapers.yaml)

- **Looks at:** `/Library/Application Support/com.apple.idleassetsd/Customer` (macOS 15.x and earlier), `~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches` (macOS 26 and later).
- **Removed by:** never: surface-only.
- **Comes back:** macOS downloads the aerial catalogue again when System Settings ▸ Wallpaper is opened, and any aerial selected as wallpaper or screensaver. To remove the videos yourself: sudo find "/Library/Application Support/com.apple.idleassetsd/Customer" -type f -name '\*.mov' -delete (macOS 15 and earlier), or sudo find ~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches -type f -name '\*.mov' -delete (macOS 26 and later). Cost: n/a — regrow never deletes here; roughly 0.5 GB of download per video macOS fetches again.
- **Fix the cause:** Aerial video downloads (macOS 15.x and earlier). `regrow doctor` says whether it is in effect here and prints the fix; regrow changes nothing.
- **Note:** The videos come back whenever System Settings ▸ Wallpaper is opened, even with a still wallpaper; `regrow doctor` prints how to stop the downloads.

### downloaded-installers

**Installers of installed apps** · caution · beta · [`rules/downloaded-installers.yaml`](../rules/downloaded-installers.yaml)

- **Looks at:** the `.dmg`, `.pkg` and `.zip` files at the top of `~/Downloads`, with their modification, change, creation and date-added times; a `.zip`'s list of contents and its app's `Info.plist`, read without extracting it; and the apps in `/Applications` and `~/Applications` and one folder down, with each app's date added, and its `Info.plist` where a judgement needs it (a binary one converted to text by `/usr/bin/plutil`).
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back. Right before each removal, regrow judges the download against the installed apps again, as the scan did, and leaves it in place if any reason to keep it holds now.
- **Keeps:**
  - A download modified, changed, created or added to the folder in the last 14 days.
  - A `.zip` that holds no app, more than one app, or anything beside the app (macOS's `__MACOSX` folder aside), and one whose app's `Info.plist` cannot be read.
  - A download no installed app matches: a `.zip` whose app's bundle identifier no installed app carries; a `.dmg` or `.pkg` whose file name matches no installed app's name, both read without dotted versions (`4.2`, `v1.10.3`), architectures, a copy number such as `(1)`, words such as `mac`, `intel` or `install` and an `installer` or `setup` ending, and with case, spaces and punctuation aside; so `Sequoia.dmg` matches `Install macOS Sequoia.app`.
  - A download newer than an installed copy of its app: a `.zip` whose app has a higher version, or the same version and a higher build, a `.dmg` or `.pkg` whose name spells a higher version, and a `.dmg` or `.pkg` that arrived or changed after the app was installed.
  - A `.zip` whose app's version cannot be compared with an installed copy's, and one whose app shares its name with an installed app whose `Info.plist` cannot be read.
  - A `.dmg` or `.pkg` when an installed copy's date added cannot be read, or, if its name spells a version, when that copy's `Info.plist` cannot be read or its version compared.
  - Every download, when an app folder cannot be read in full, since it could hide an installed copy; and a download that cannot be read.
- **Comes back:** The app stays installed; its maker offers the installer again if you ever reinstall. Cost: one download per app you reinstall.
- **Note:** A .dmg or .pkg is matched by its file name alone, and a disk image can hold more than the app (an uninstaller, a driver) - check the list before moving one.

### ios-backups

**Local iOS device backups** · surface-only · [`rules/ios-backups.yaml`](../rules/ios-backups.yaml)

- **Looks at:** `~/Library/Application Support/MobileSync/Backup`.
- **Removed by:** never: surface-only.
- **Comes back:** Delete via Finder ▸ (device) ▸ Manage Backups, where you can see what each backup is. Cost: n/a — a backup may be the only copy of a device.

### library-caches

**App caches (~/Library/Caches)** · surface-only · [`rules/library-caches.yaml`](../rules/library-caches.yaml)

- **Looks at:** `~/Library/Caches`.
- **Removed by:** never: surface-only.
- **Comes back:** Review per app — well-behaved apps rebuild caches, badly-behaved ones break. Cost: n/a — regrow reports the total, deletion stays manual.

### library-updates

**Stuck update payloads (/Library/Updates)** · surface-only · [`rules/library-updates.yaml`](../rules/library-updates.yaml)

- **Looks at:** `/Library/Updates`.
- **Removed by:** never: surface-only.
- **Comes back:** Check `softwareupdate -l` first; payloads re-download if an update still needs them. Cost: n/a — regrow never deletes here.

### macos-installers

**Stale macOS installers** · safe · [`rules/macos-installers.yaml`](../rules/macos-installers.yaml)

- **Looks at:** `/Applications/Install macOS*.app`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** Any macOS installer re-downloads from Software Update or the App Store. Cost: 12GB+ download if you ever need it again.

### macos-update-download

**Downloaded macOS update** · surface-only · beta · [`rules/macos-update-download.yaml`](../rules/macos-update-download.yaml)

- **Looks at:** `/System/Library/AssetsV2/com_apple_MobileAsset_MacSoftwareUpdate/*.asset`.
- **Removed by:** never: surface-only.
- **Comes back:** Software Update downloads updates by itself while Download new updates when available is on. Cost: n/a — regrow never deletes here, and System Integrity Protection refuses even sudo.
- **Note:** Installing the update uses the download up: System Settings ▸ General ▸ Software Update, or `softwareupdate --list` to see it first. To stop macOS downloading the next one, turn off Download new updates when available (Software Update ▸ Automatic updates ⓘ), or run `sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticDownload -bool false`.

### media-analysis-cache

**mediaanalysisd cache** · safe · [`rules/media-analysis-cache.yaml`](../rules/media-analysis-cache.yaml)

- **Looks at:** `~/Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** The photo-analysis daemon rebuilds its cache in the background. Cost: some background CPU; photos and analysis results unaffected.
- **Doctor:** `regrow doctor` flags it above 2 GiB. Sequoia 15.1 leak filled disks with up to 143GB of mediaanalysisd cache (fixed in 15.2). Healthy caches stay around 100MB.

### spotlight-index

**Rebuild Spotlight index** · caution · [`rules/spotlight-index.yaml`](../rules/spotlight-index.yaml)

- **Looks at:** `~/Library/Metadata/CoreSpotlight`.
- **Removed by:** `sudo mdutil -E /`, once for the whole rule: all of it or nothing. It runs as root through `sudo`, which asks for your password in a terminal; the app leaves it for you to run there. It deletes for good: `regrow undo` cannot bring it back.
- **Comes back:** mdutil erases the index and macOS rebuilds it automatically. Cost: hours of background indexing; search degraded meanwhile.
- **Doctor:** `regrow doctor` flags it above 20 GiB. Sequoia bug grows the Spotlight index without bound on node_modules-heavy disks — 233GB observed. Normal indexes run 1–10GB.
- **Note:** Spotlight search is unavailable and CPU is busy while it reindexes — can take hours on big disks.

### trash-empty

**Empty Trash** · caution · [`rules/trash-empty.yaml`](../rules/trash-empty.yaml)

- **Looks at:** `~/.Trash`.
- **Removed by:** `osascript -e 'tell application "Finder" to empty trash'`, once for the whole rule: all of it or nothing. It deletes for good: `regrow undo` cannot bring it back. It runs before every other action of the plan, so the plan's own Trash moves stay restorable.
- **Comes back:** Not regenerable — the Trash is the undo buffer. Cost: contents unrecoverable.
- **Note:** Permanent — this is the point of no return for everything regrow (and you) previously trashed. Terminal may need Full Disk Access.

### user-logs

**Crash reports + diagnostic logs** · safe · [`rules/user-logs.yaml`](../rules/user-logs.yaml)

- **Looks at:** `~/Library/Logs/DiagnosticReports`, `~/Library/Logs/CrashReporter`.
- **Removed by:** a move to the Trash, one per item; `regrow undo` puts it back.
- **Comes back:** macOS recreates the directories and writes new reports as crashes happen. Cost: history of past crashes is gone.
- **Note:** Keep these if you are actively debugging a crashing app — they are the evidence.

## Phantom space

Space your files do not add up to: snapshots, sparse disk images and purgeable space. These rows explain the number; most are never deleted.

| Rule | Risk | Removed by |
|---|---|---|
| [APFS purgeable space](#apfs-purgeable) | surface-only | never |
| [Docker Desktop VM disk](#docker-vm-disk) | surface-only | never |
| [Time Machine local snapshots](#tm-snapshots) | caution | `tmutil deletelocalsnapshots {arg}` |

### apfs-purgeable

**APFS purgeable space** · surface-only · [`rules/apfs-purgeable.yaml`](../rules/apfs-purgeable.yaml)

- **Looks at:** the data volume's free space with and without purgeable space, as macOS reports it to `osascript` (JavaScript for Automation); no files.
- **Removed by:** never: surface-only.
- **Comes back:** Nothing to regenerate — macOS frees purgeable space on its own under pressure. Cost: n/a — regrow only explains this number.
- **Note:** Why Finder still shows full — Finder counts purgeable as free, df does not. macOS frees it automatically when the disk fills; deleting TM snapshots converts the biggest chunk immediately.

### docker-vm-disk

**Docker Desktop VM disk** · surface-only · [`rules/docker-vm-disk.yaml`](../rules/docker-vm-disk.yaml)

- **Looks at:** the size of Docker Desktop's disk image, `~/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw` (or `Docker.qcow2`); no program runs, so it works with Docker stopped.
- **Removed by:** never: surface-only.
- **Comes back:** Shrinks itself after pruning (Docker.raw, 4.28+); last resort is Docker Desktop ▸ Troubleshoot ▸ Purge Data. Cost: n/a — regrow never touches this file.
- **Fix the cause:** Docker VM memory cap. `regrow doctor` says whether it is in effect here and prints the fix; regrow changes nothing.
- **Note:** Why Finder still shows full — the file is sparse; only the "real" number is on disk. Pruning inside Docker (see the docker rules) lets it shrink itself.

### tm-snapshots

**Time Machine local snapshots** · caution · [`rules/tm-snapshots.yaml`](../rules/tm-snapshots.yaml)

- **Looks at:** `tmutil listlocalsnapshots /`. A snapshot's size is unknown until it is deleted, so each row shows 0 B.
- **Removed by:** `tmutil deletelocalsnapshots {arg}`, once per item; `{arg}` is the item as the tool names it. It deletes for good: `regrow undo` cannot bring it back.
- **Keeps:**
  - Snapshots not named `com.apple.TimeMachine.…`, such as the ones macOS takes for an update.
- **Comes back:** macOS creates fresh local snapshots on the next backup cycle. Cost: point-in-time restores between backups are gone.
- **Note:** Snapshot sizes are invisible until deleted — freed space shows up as purgeable, sometimes minutes later.
