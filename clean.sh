#!/usr/bin/env bash
#
# clean.sh — interactive macOS disk cleaner for developers.
#
# DRY-RUN BY DEFAULT: nothing is ever deleted unless you pass --run
# and type "yes" at the confirmation prompt.
#
# Design rules (keep these when adding modules):
#   1. Delegate to native cleaners where they exist (npm cache clean,
#      go clean, docker system prune, brew cleanup, tmutil, simctl…)
#      instead of re-implementing deletion with rm.
#   2. Every mutating command goes through x() / xrm() so dry-run
#      prints the exact command that --run would execute.
#   3. "info" modules are read-only reports and never mutate anything.
#   4. New module = one mod_<id>() function + one entry in MODULES.
#
# Usage:
#   bash clean.sh                 # scan + interactive checklist, dry-run
#   bash clean.sh --run           # same, but actually executes after confirm
#   bash clean.sh --list          # list modules
#   bash clean.sh --only go_build,trash [--run]
#   bash clean.sh --safe --run    # everything risk=safe, no UI
#
set -u

VERSION="0.1.0"
MODE="dry"                # dry | run
SELECT_MODE="ui"          # ui | only | safe | all
ONLY_IDS=""
DO_SCAN=1
ASSUME_YES=0
LOG_FILE="$HOME/Library/Logs/cleaner.log"
WORKSPACE="${CLEANER_WORKSPACE:-$HOME/workspace}"

# ---------------------------------------------------------------- registry --
# Order = display order. Ids must match a mod_<id>() function.
MODULES=(
  aerial
  go_build
  go_mod
  yarn_cache
  npm_cache
  pnpm_store
  docker_prune
  docker_volumes
  xcode_derived
  xcode_devicesupport
  sim_unavailable
  sim_caches
  xcode_archives
  brew_cache
  pip_cache
  trash
  macos_installers
  library_updates
  tm_snapshots
  media_analysis
  user_logs
  spotlight_index
  docker_raw
  caches_top
  node_modules
  ios_backups
)

# ----------------------------------------------------------------- helpers --
c_reset=$'\033[0m'; c_bold=$'\033[1m'; c_dim=$'\033[2m'
c_green=$'\033[32m'; c_yellow=$'\033[33m'; c_red=$'\033[31m'; c_cyan=$'\033[36m'

say()  { printf '%s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }
log()  { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$LOG_FILE"; }

# KB integer -> human string
hum() {
  [ -z "${1:-}" ] && { printf -- "-"; return; }
  awk -v k="$1" 'BEGIN{v=k; split("K M G T",u," "); i=1
    while (v>=1024 && i<4) {v/=1024; i++}
    fmt = (v>=10 || i==1) ? "%.0f%s" : "%.1f%s"
    printf fmt, v, u[i]}'
}

# du -sk over any number of paths, summed; empty output if nothing exists
kb_du() {
  local t=0 s p found=0
  for p in "$@"; do
    [ -e "$p" ] || continue
    found=1
    s=$(du -sk "$p" 2>/dev/null | awk '{print $1+0}')
    t=$((t + ${s:-0}))
  done
  [ "$found" = 1 ] && echo "$t"
}

# Parse `docker system df` reclaimable for a Type regex -> KB (approx)
docker_kb() {
  have docker || return 0
  docker system df --format '{{.Type}}\t{{.Reclaimable}}' 2>/dev/null |
    awk -F'\t' -v re="$1" '
      $1 ~ re {
        n=$2; sub(/ .*/, "", n)
        if (match(n, /[A-Za-z]/)) { unit=substr(n, RSTART); val=substr(n, 1, RSTART-1)+0 }
        else { unit="B"; val=n+0 }
        m=0
        if (unit=="B") m=1/1024
        else if (unit=="kB"||unit=="KB") m=1
        else if (unit=="MB") m=1024
        else if (unit=="GB") m=1024*1024
        else if (unit=="TB") m=1024*1024*1024
        sum+=val*m
      }
      END { if (sum>0) printf "%d", sum }'
}

# Run-or-echo wrapper. ALL mutating commands must go through this.
x() {
  if [ "$MODE" = "run" ]; then
    printf '  %s+ %s%s\n' "$c_dim" "$*" "$c_reset"
    log "RUN: $*"
    "$@" 2>&1 | sed 's/^/    /'
    local rc=${PIPESTATUS[0]:-$?}
    [ "$rc" != 0 ] && { say "    ${c_yellow}(exit $rc — continuing)${c_reset}"; log "FAIL($rc): $*"; }
  else
    printf '    %swould run:%s %s\n' "$c_dim" "$c_reset" "$*"
  fi
  return 0
}

# rm -rf with guard rails; use for paths that have no native cleaner
xrm() {
  local p
  for p in "$@"; do
    case "$p" in
      ""|"/"|"$HOME"|"$HOME/"|"/Users"|"/Library"|"/System"*|"/Applications")
        say "    ${c_red}refusing suspicious path: '$p'${c_reset}"; continue ;;
    esac
    [ -e "$p" ] || continue
    x rm -rf -- "$p"
  done
}

mod_call() { "mod_$1" "$2"; }

# ----------------------------------------------------------------- modules --
# Each module answers: title | risk (safe|moderate|risky|info) |
# needs_sudo (yes|no) | note | size (KB int or empty) | clean

mod_aerial() { case "$1" in
  title) echo "Aerial / moving wallpaper videos" ;;
  risk)  echo safe ;;
  needs_sudo) echo yes ;;
  note)  echo "4K screensaver/wallpaper .mov files. Switch to a static wallpaper/screensaver FIRST or idleassetsd re-downloads everything." ;;
  size)  kb_du "/Library/Application Support/com.apple.idleassetsd/Customer" ;;
  clean)
    x sudo find "/Library/Application Support/com.apple.idleassetsd/Customer" -type f -name '*.mov' -delete
    x sudo killall idleassetsd
    ;;
esac; }

mod_go_build() { case "$1" in
  title) echo "Go build cache" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Rebuilt automatically; first builds after cleaning are slower." ;;
  size)  kb_du "$HOME/Library/Caches/go-build" ;;
  clean)
    if have go; then x go clean -cache
    else xrm "$HOME/Library/Caches/go-build"; fi
    ;;
esac; }

mod_go_mod() { case "$1" in
  title) echo "Go module cache" ;;
  risk)  echo moderate ;;
  needs_sudo) echo no ;;
  note)  echo "All downloaded module sources; re-downloaded on next build (network + time)." ;;
  size)  kb_du "$HOME/go/pkg/mod" ;;
  clean)
    if have go; then x go clean -modcache
    else xrm "$HOME/go/pkg/mod"; fi
    ;;
esac; }

mod_yarn_cache() { case "$1" in
  title) echo "Yarn cache" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Package tarball cache; re-downloaded on next install." ;;
  size)  kb_du "$HOME/Library/Caches/Yarn" "$HOME/.cache/yarn" ;;
  clean)
    have yarn && x yarn cache clean
    xrm "$HOME/Library/Caches/Yarn" "$HOME/.cache/yarn"
    ;;
esac; }

mod_npm_cache() { case "$1" in
  title) echo "npm cache" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Content-addressed cache; npm verifies/re-fetches as needed." ;;
  size)  kb_du "$HOME/.npm" ;;
  clean)
    if have npm; then x npm cache clean --force
    else xrm "$HOME/.npm"; fi
    ;;
esac; }

mod_pnpm_store() { case "$1" in
  title) echo "pnpm store (unreferenced packages)" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "'pnpm store prune' removes only packages no project references." ;;
  size)  kb_du "$HOME/Library/pnpm/store" "$HOME/.pnpm-store" ;;
  clean)
    if have pnpm; then x pnpm store prune
    else xrm "$HOME/Library/pnpm/store" "$HOME/.pnpm-store"; fi
    ;;
esac; }

mod_docker_prune() { case "$1" in
  title) echo "Docker: dangling images, stopped containers, build cache" ;;
  risk)  echo moderate ;;
  needs_sudo) echo no ;;
  note)  echo "'docker system prune' — removes stopped containers too. Does NOT touch volumes." ;;
  size)  { local a b; a=$(docker_kb '^Images'); b=$(docker_kb '^Build Cache'); [ -n "$a$b" ] && echo $(( ${a:-0} + ${b:-0} )); } ;;
  clean)
    if docker info >/dev/null 2>&1; then x docker system prune -f
    else say "    docker daemon not running — skipped"; fi
    ;;
esac; }

mod_docker_volumes() { case "$1" in
  title) echo "Docker: unused volumes" ;;
  risk)  echo risky ;;
  needs_sudo) echo no ;;
  note)  echo "DELETES DATA in volumes not attached to any container (old DB data etc.). Check 'docker volume ls' first." ;;
  size)  docker_kb '^Local Volumes' ;;
  clean)
    if docker info >/dev/null 2>&1; then x docker volume prune -f
    else say "    docker daemon not running — skipped"; fi
    ;;
esac; }

mod_xcode_derived() { case "$1" in
  title) echo "Xcode DerivedData" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Build intermediates; regenerated on next build." ;;
  size)  kb_du "$HOME/Library/Developer/Xcode/DerivedData" ;;
  clean) xrm "$HOME/Library/Developer/Xcode/DerivedData" ;;
esac; }

mod_xcode_devicesupport() { case "$1" in
  title) echo "Xcode device support symbols" ;;
  risk)  echo moderate ;;
  needs_sudo) echo no ;;
  note)  echo "Re-generated next time each device is plugged in (takes a few minutes per device)." ;;
  size)  kb_du "$HOME/Library/Developer/Xcode/iOS DeviceSupport" "$HOME/Library/Developer/Xcode/watchOS DeviceSupport" "$HOME/Library/Developer/Xcode/tvOS DeviceSupport" ;;
  clean)
    local d
    for d in "$HOME/Library/Developer/Xcode/iOS DeviceSupport" \
             "$HOME/Library/Developer/Xcode/watchOS DeviceSupport" \
             "$HOME/Library/Developer/Xcode/tvOS DeviceSupport"; do
      [ -d "$d" ] && x find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
    done
    ;;
esac; }

mod_sim_unavailable() { case "$1" in
  title) echo "Unavailable iOS simulators" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Simulators for SDKs no longer installed ('xcrun simctl delete unavailable')." ;;
  size)  echo "" ;;
  clean)
    if have xcrun; then x xcrun simctl delete unavailable
    else say "    xcrun not found — skipped"; fi
    ;;
esac; }

mod_sim_caches() { case "$1" in
  title) echo "CoreSimulator caches" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Simulator runtime caches; regenerated as needed." ;;
  size)  kb_du "$HOME/Library/Developer/CoreSimulator/Caches" ;;
  clean) xrm "$HOME/Library/Developer/CoreSimulator/Caches" ;;
esac; }

mod_xcode_archives() { case "$1" in
  title) echo "Xcode Archives (released builds + dSYMs)" ;;
  risk)  echo risky ;;
  needs_sudo) echo no ;;
  note)  echo "Needed to symbolicate crash reports of shipped builds. Delete only if you archive elsewhere." ;;
  size)  kb_du "$HOME/Library/Developer/Xcode/Archives" ;;
  clean) xrm "$HOME/Library/Developer/Xcode/Archives" ;;
esac; }

mod_brew_cache() { case "$1" in
  title) echo "Homebrew cache + old versions" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "'brew cleanup -s --prune=all' removes downloads and outdated kegs." ;;
  size)  kb_du "$HOME/Library/Caches/Homebrew" ;;
  clean)
    have brew && x brew cleanup -s --prune=all
    xrm "$HOME/Library/Caches/Homebrew"
    ;;
esac; }

mod_pip_cache() { case "$1" in
  title) echo "pip cache" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Wheel/download cache; re-fetched on next install." ;;
  size)  kb_du "$HOME/Library/Caches/pip" ;;
  clean)
    if have pip3; then x pip3 cache purge
    else xrm "$HOME/Library/Caches/pip"; fi
    ;;
esac; }

mod_trash() { case "$1" in
  title) echo "Empty Trash" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Your terminal may need Full Disk Access (System Settings > Privacy) to touch ~/.Trash." ;;
  size)  kb_du "$HOME/.Trash" ;;
  clean) x find "$HOME/.Trash" -mindepth 1 -maxdepth 1 -exec rm -rf {} + ;;
esac; }

mod_macos_installers() { case "$1" in
  title) echo "Stale macOS installers in /Applications" ;;
  risk)  echo safe ;;
  needs_sudo) echo yes ;;
  note)  echo "'Install macOS *.app' left after upgrades — 12GB+ each, re-downloadable." ;;
  size)
    local t=0 p found=
    for p in /Applications/Install\ macOS*.app; do
      [ -e "$p" ] || continue
      found=1; t=$((t + $(kb_du "$p")))
    done
    [ -n "$found" ] && echo "$t"
    ;;
  clean)
    local p found=
    for p in /Applications/Install\ macOS*.app; do
      [ -e "$p" ] || continue
      found=1; x sudo rm -rf -- "$p"
    done
    [ -z "$found" ] && say "    none found"
    ;;
esac; }

mod_library_updates() { case "$1" in
  title) echo "Stuck update payloads in /Library/Updates" ;;
  risk)  echo risky ;;
  needs_sudo) echo yes ;;
  note)  echo "Downloaded update payloads; system re-downloads. Skip if an update is mid-install. Check 'softwareupdate -l' first." ;;
  size)  kb_du "/Library/Updates" ;;
  clean)
    x sudo find /Library/Updates -mindepth 1 -maxdepth 1 ! -name 'index.plist' -exec rm -rf {} +
    ;;
esac; }

mod_tm_snapshots() { case "$1" in
  title) echo "Thin Time Machine local snapshots" ;;
  risk)  echo moderate ;;
  needs_sudo) echo yes ;;
  note)  echo "Purges local (on-disk) TM snapshots — big chunk of 'System Data'. External TM backups untouched. OS-update snapshots (com.apple.os.update-*) are system-managed and cannot be removed this way." ;;
  size)  echo "" ;;
  clean)
    tmutil listlocalsnapshots / 2>/dev/null | sed 's/^/    /'
    x sudo tmutil thinlocalsnapshots / 9999999999999 4
    ;;
esac; }

mod_media_analysis() { case "$1" in
  title) echo "mediaanalysisd cache (Sequoia leak)" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "Sequoia 15.1 bug: photo-analysis daemon leaks cache bundles (reports of 100GB+). Photos untouched; analysis regenerates." ;;
  size)  kb_du "$HOME/Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches" ;;
  clean)
    local d="$HOME/Library/Containers/com.apple.mediaanalysisd/Data/Library/Caches"
    [ -d "$d" ] && x find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
    ;;
esac; }

mod_user_logs() { case "$1" in
  title) echo "User crash reports + diagnostic logs" ;;
  risk)  echo safe ;;
  needs_sudo) echo no ;;
  note)  echo "~/Library/Logs/DiagnosticReports and CrashReporter. Keep if you're actively debugging a crashing app." ;;
  size)  kb_du "$HOME/Library/Logs/DiagnosticReports" "$HOME/Library/Logs/CrashReporter" ;;
  clean)
    local d
    for d in "$HOME/Library/Logs/DiagnosticReports" "$HOME/Library/Logs/CrashReporter"; do
      [ -d "$d" ] && x find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
    done
    ;;
esac; }

mod_spotlight_index() { case "$1" in
  title) echo "Rebuild bloated Spotlight index" ;;
  risk)  echo moderate ;;
  needs_sudo) echo yes ;;
  note)  echo "Sequoia bug: corespotlightd index can grow to 100GB+. 'mdutil -E /' erases + rebuilds; Spotlight search unavailable and CPU busy during reindex (can take hours)." ;;
  size)  kb_du "$HOME/Library/Metadata/CoreSpotlight" ;;
  clean)
    say "    index sizes (user + system):"
    du -sh "$HOME/Library/Metadata/CoreSpotlight" 2>/dev/null | sed 's/^/      /'
    sudo -n du -sh /System/Volumes/Data/.Spotlight-V100 2>/dev/null | sed 's/^/      /'
    x sudo mdutil -E /
    ;;
esac; }

# ------------------------------------------------------------ info modules --
# Read-only reports. They never mutate, so they run for real even in dry mode.

mod_docker_raw() { case "$1" in
  title) echo "[report] Docker disk image usage" ;;
  risk)  echo info ;;
  needs_sudo) echo no ;;
  note)  echo "Docker.raw is sparse (huge logical size, smaller real usage). Prune shrinks contents; Docker Desktop > Settings > Resources caps it." ;;
  size)  kb_du "$HOME/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw" ;;
  clean)
    local raw="$HOME/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw"
    [ -f "$raw" ] && { say "    real usage: $(hum "$(kb_du "$raw")")  (logical: $(ls -lh "$raw" | awk '{print $5}'))"; }
    docker system df 2>/dev/null | sed 's/^/    /' || say "    docker daemon not running"
    say "    tip: run docker_prune/docker_volumes modules, or Docker Desktop > Troubleshoot > Purge data (destroys everything)."
    ;;
esac; }

mod_caches_top() { case "$1" in
  title) echo "[report] Largest ~/Library/Caches entries" ;;
  risk)  echo info ;;
  needs_sudo) echo no ;;
  note)  echo "Shows top 15 so you can judge app caches case by case." ;;
  size)  kb_du "$HOME/Library/Caches" ;;
  clean)
    du -skx "$HOME/Library/Caches"/* 2>/dev/null | sort -rn | head -15 |
      while IFS=$'\t ' read -r kb path; do
        printf '    %8s  %s\n' "$(hum "$kb")" "${path#"$HOME"/Library/Caches/}"
      done
    ;;
esac; }

mod_node_modules() { case "$1" in
  title) echo "[report] node_modules in $WORKSPACE" ;;
  risk)  echo info ;;
  needs_sudo) echo no ;;
  note)  echo "Interactive per-project deletion: 'npx npkill -d $WORKSPACE' (set \$CLEANER_WORKSPACE to change root)." ;;
  size)  echo "" ;;
  clean)
    if [ -d "$WORKSPACE" ]; then
      find "$WORKSPACE" -maxdepth 6 -type d -name node_modules -prune 2>/dev/null |
        while read -r d; do printf '%s\t%s\n' "$(kb_du "$d")" "$d"; done |
        sort -rn | head -10 |
        while IFS=$'\t' read -r kb d; do printf '    %8s  %s\n' "$(hum "$kb")" "$d"; done
      say "    tip: npx npkill -d $WORKSPACE"
    else
      say "    $WORKSPACE not found (set CLEANER_WORKSPACE)"
    fi
    ;;
esac; }

mod_ios_backups() { case "$1" in
  title) echo "[report] Local iOS device backups" ;;
  risk)  echo info ;;
  needs_sudo) echo no ;;
  note)  echo "Delete via Finder > (device) > Manage Backups; too risky to automate." ;;
  size)  kb_du "$HOME/Library/Application Support/MobileSync/Backup" ;;
  clean)
    local b="$HOME/Library/Application Support/MobileSync/Backup"
    if [ -d "$b" ]; then
      du -sk "$b"/* 2>/dev/null | while IFS=$'\t ' read -r kb path; do
        printf '    %8s  %s  (modified %s)\n' "$(hum "$kb")" "$(basename "$path")" \
          "$(stat -f '%Sm' -t '%Y-%m-%d' "$path")"
      done
      say "    delete via Finder > Manage Backups"
    else
      say "    no local iOS backups"
    fi
    ;;
esac; }

# -------------------------------------------------------------------- scan --
SELECTED=() ; SIZEKB=() ; SIZEH=()

init_selection() {
  local i id risk
  for i in "${!MODULES[@]}"; do
    id="${MODULES[$i]}"
    risk=$(mod_call "$id" risk)
    case "$SELECT_MODE" in
      only)  case ",$ONLY_IDS," in *",$id,"*) SELECTED[$i]=1;; *) SELECTED[$i]=0;; esac ;;
      safe)  [ "$risk" = safe ] && SELECTED[$i]=1 || SELECTED[$i]=0 ;;
      all)   [ "$risk" = info ] && SELECTED[$i]=0 || SELECTED[$i]=1 ;;
      *)     [ "$risk" = safe ] && SELECTED[$i]=1 || SELECTED[$i]=0 ;;
    esac
  done
}

scan_sizes() {
  local i id kb
  for i in "${!MODULES[@]}"; do
    id="${MODULES[$i]}"
    if [ "$DO_SCAN" = 1 ] && { [ "$SELECT_MODE" = ui ] || [ "${SELECTED[$i]:-0}" = 1 ]; }; then
      printf '\r\033[K  scanning %s…' "$id" >&2
      kb=$(mod_call "$id" size)
    else
      kb=""
    fi
    SIZEKB[$i]="$kb"
    SIZEH[$i]=$(hum "$kb")
  done
  printf '\r\033[K' >&2
}

risk_color() {
  case "$1" in
    safe) printf '%s' "$c_green" ;;
    moderate) printf '%s' "$c_yellow" ;;
    risky) printf '%s' "$c_red" ;;
    *) printf '%s' "$c_cyan" ;;
  esac
}

selected_total() {
  local i t=0
  for i in "${!MODULES[@]}"; do
    [ "${SELECTED[$i]}" = 1 ] || continue
    [ "$(mod_call "${MODULES[$i]}" risk)" = info ] && continue
    [ -n "${SIZEKB[$i]}" ] && t=$((t + SIZEKB[$i]))
  done
  echo "$t"
}

# ---------------------------------------------------------------------- ui --
CURSOR=0

draw_ui() {
  printf '\033[H\033[2J'
  say "${c_bold}cleaner v$VERSION${c_reset} — mode: $([ "$MODE" = run ] && printf '%sRUN%s' "$c_red" "$c_reset" || printf '%sdry-run%s' "$c_green" "$c_reset")"
  say "${c_dim}↑/↓ or j/k move · space toggle · a all · n none · s safe-only · enter continue · q quit${c_reset}"
  say ""
  local i id mark cur risk rc sudo_tag
  for i in "${!MODULES[@]}"; do
    id="${MODULES[$i]}"
    risk=$(mod_call "$id" risk)
    rc=$(risk_color "$risk")
    mark=" "; [ "${SELECTED[$i]}" = 1 ] && mark="x"
    cur="  "; [ "$i" -eq "$CURSOR" ] && cur="${c_cyan}> ${c_reset}"
    sudo_tag=""; [ "$(mod_call "$id" needs_sudo)" = yes ] && sudo_tag=" ${c_dim}(sudo)${c_reset}"
    printf '%s[%s] %8s  %s%-8s%s %s%s\n' \
      "$cur" "$mark" "${SIZEH[$i]}" "$rc" "$risk" "$c_reset" "$(mod_call "$id" title)" "$sudo_tag"
    if [ "$i" -eq "$CURSOR" ]; then
      printf '      %s%s%s\n' "$c_dim" "$(mod_call "$id" note)" "$c_reset"
    fi
  done
  say ""
  say "selected total: ${c_bold}$(hum "$(selected_total)")${c_reset} ${c_dim}(reports and unknown sizes excluded)${c_reset}"
}

interactive_select() {
  local key k2 n=${#MODULES[@]} i
  while :; do
    draw_ui
    IFS= read -rsn1 key
    case "$key" in
      $'\033')
        read -rsn2 -t 1 k2 || k2=""
        case "$k2" in
          '[A') CURSOR=$(( (CURSOR + n - 1) % n )) ;;
          '[B') CURSOR=$(( (CURSOR + 1) % n )) ;;
        esac ;;
      k) CURSOR=$(( (CURSOR + n - 1) % n )) ;;
      j) CURSOR=$(( (CURSOR + 1) % n )) ;;
      ' ') [ "${SELECTED[$CURSOR]}" = 1 ] && SELECTED[$CURSOR]=0 || SELECTED[$CURSOR]=1 ;;
      a) for i in "${!MODULES[@]}"; do [ "$(mod_call "${MODULES[$i]}" risk)" = info ] || SELECTED[$i]=1; done ;;
      n) for i in "${!MODULES[@]}"; do SELECTED[$i]=0; done ;;
      s) for i in "${!MODULES[@]}"; do [ "$(mod_call "${MODULES[$i]}" risk)" = safe ] && SELECTED[$i]=1 || SELECTED[$i]=0; done ;;
      ''|$'\n') break ;;
      q) printf '\033[H\033[2J'; say "aborted, nothing done."; exit 0 ;;
    esac
  done
  printf '\033[H\033[2J'
}

print_table() {
  local i id risk
  say ""
  for i in "${!MODULES[@]}"; do
    id="${MODULES[$i]}"
    risk=$(mod_call "$id" risk)
    printf '  %-20s %8s  %s%-8s%s %s\n' \
      "$id" "${SIZEH[$i]}" "$(risk_color "$risk")" "$risk" "$c_reset" "$(mod_call "$id" title)"
  done
  say ""
}

list_modules() {
  DO_SCAN=0
  scan_sizes
  init_selection
  say "modules (select with --only id1,id2 …):"
  print_table
}

# --------------------------------------------------------------- execution --
run_selected() {
  local i id any=0 any_sudo=0
  for i in "${!MODULES[@]}"; do
    [ "${SELECTED[$i]}" = 1 ] || continue
    any=1
    [ "$(mod_call "${MODULES[$i]}" needs_sudo)" = yes ] && any_sudo=1
  done
  [ "$any" = 0 ] && { say "nothing selected, nothing done."; exit 0; }

  say "${c_bold}plan${c_reset} (mode: $([ "$MODE" = run ] && echo "${c_red}RUN${c_reset}" || echo "${c_green}dry-run${c_reset}")) — estimated reclaim: ${c_bold}$(hum "$(selected_total)")${c_reset}"
  say ""

  local df_before df_after
  if [ "$MODE" = run ]; then
    if [ "$ASSUME_YES" != 1 ]; then
      say "${c_red}This WILL delete the selected items.${c_reset}"
      printf 'Type "yes" to proceed: '
      read -r ans
      [ "$ans" = "yes" ] || { say "aborted, nothing done."; exit 1; }
    fi
    [ "$any_sudo" = 1 ] && sudo -v
    mkdir -p "$(dirname "$LOG_FILE")"
    log "=== run started (v$VERSION) ==="
    df_before=$(df -k "$HOME" | awk 'NR==2 {print $4}')
  fi

  for i in "${!MODULES[@]}"; do
    [ "${SELECTED[$i]}" = 1 ] || continue
    id="${MODULES[$i]}"
    say "${c_bold}$(mod_call "$id" title)${c_reset} ${c_dim}[$id, ${SIZEH[$i]}]${c_reset}"
    mod_call "$id" clean
    say ""
  done

  if [ "$MODE" = run ]; then
    df_after=$(df -k "$HOME" | awk 'NR==2 {print $4}')
    say "${c_bold}freed: $(hum $((df_after - df_before)))${c_reset} (df delta; snapshots/purgeable may free later)"
    log "=== run finished, freed $((df_after - df_before)) KB ==="
    say "${c_dim}log: $LOG_FILE${c_reset}"
  else
    say "${c_green}DRY RUN — nothing was deleted.${c_reset} Re-run with ${c_bold}--run${c_reset} to execute."
  fi
}

# -------------------------------------------------------------------- args --
usage() {
  cat <<EOF
clean.sh v$VERSION — macOS dev disk cleaner (dry-run by default)

usage: bash clean.sh [options]
  (no options)      scan + interactive checklist, then DRY RUN of selection
  --run             actually execute (asks for "yes" confirmation)
  --yes             skip confirmation (for scripting; use with care)
  --safe            select all risk=safe modules, skip checklist UI
  --all             select everything except reports, skip checklist UI
  --only a,b,c      select exactly these module ids, skip checklist UI
  --list            list module ids and exit
  --no-scan         skip size scanning (faster startup)
  -h, --help        this help

env: CLEANER_WORKSPACE  root for node_modules report (default ~/workspace)
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --run) MODE=run ;;
    --dry|--dry-run) MODE=dry ;;
    --yes) ASSUME_YES=1 ;;
    --safe) SELECT_MODE=safe ;;
    --all) SELECT_MODE=all ;;
    --only) shift; ONLY_IDS="${1:-}"; SELECT_MODE=only ;;
    --list) list_modules; exit 0 ;;
    --no-scan) DO_SCAN=0 ;;
    -h|--help) usage; exit 0 ;;
    *) say "unknown option: $1"; usage; exit 2 ;;
  esac
  shift
done

# validate --only ids
if [ "$SELECT_MODE" = only ]; then
  IFS=',' read -ra _ids <<< "$ONLY_IDS"
  for _id in "${_ids[@]}"; do
    _ok=0
    for _m in "${MODULES[@]}"; do [ "$_m" = "$_id" ] && _ok=1; done
    [ "$_ok" = 1 ] || { say "unknown module id: $_id (see --list)"; exit 2; }
  done
fi

# --------------------------------------------------------------------- main --
init_selection
[ "$DO_SCAN" = 1 ] && say "scanning… (skip with --no-scan)"
scan_sizes

if [ "$SELECT_MODE" = ui ]; then
  if [ -t 0 ] && [ -t 1 ]; then
    interactive_select
  else
    say "not a TTY — showing scan only. Use --only/--safe/--all to select non-interactively."
    print_table
    exit 0
  fi
fi

run_selected
