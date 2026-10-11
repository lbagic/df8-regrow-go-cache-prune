#!/bin/sh
# Checks the installed Regrow.app end to end. The app is started through
# LaunchServices, so macOS attributes what it does to Regrow and not to
# this terminal. Both checks use a state directory of their own: the
# oplog and the headroom history are not touched.
#
#   scripts/app-smoke.sh trash   Moves a folder this script creates in your home
#                                to the Trash through the app and restores it with
#                                undo. The first run raises "Regrow wants to
#                                control Finder": allow it.
#   scripts/app-smoke.sh alert   One watch tick against a made-up history in which
#                                free space just fell 6 GiB: one notification
#                                should appear.
#
# A check that fails leaves its directory in place and names it: the
# app's result.json, smoke.log and engine.log are in it.
set -eu

APP="$HOME/Applications/Regrow.app"
mode="${1:-}"
case "$mode" in
trash | alert) ;;
*)
	echo "usage: $0 trash|alert" >&2
	exit 64
	;;
esac
if [ ! -d "$APP" ]; then
	echo "$APP is not installed: run \`just app\` first." >&2
	exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/regrow-smoke.XXXXXX")
mkdir -p "$work/state/regrow"

finish() {
	if [ "$1" -eq 0 ]; then
		rm -rf "${work:?}"
	else
		echo "Left for a look: $work" >&2
	fi
	exit "$1"
}

# The app writes result.txt: one "PASS|FAIL <stage>: <detail>" line per
# stage, then "RESULT ok" or "RESULT failed".
report() {
	if [ ! -f "$work/result.txt" ]; then
		echo "FAIL: the app wrote no result. Its log: ~/Library/Logs/regrow/app.log" >&2
		return 1
	fi
	cat "$work/result.txt"
	grep -q '^RESULT ok$' "$work/result.txt"
}

if [ "$mode" = alert ]; then
	total=$(df -k /System/Volumes/Data | awk 'NR==2 { printf "%.0f", $2 * 1024 }')
	free=$(df -k /System/Volumes/Data | awk 'NR==2 { printf "%.0f", $4 * 1024 }')
	before=$(awk -v f="$free" 'BEGIN { printf "%.0f", f + 6 * 1024 * 1024 * 1024 }')
	# Swap as it is now, so the fall in free space is the only change.
	swap=$(sysctl -n vm.swapusage | awk '{ printf "%.0f", $6 * 1024 * 1024 }')
	printf '{"at":"%s","total":%s,"free":%s,"purgeable":0,"swap_used":%s}\n' \
		"$(date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)" "$total" "$before" "$swap" >"$work/state/regrow/headroom.jsonl"
	open -n -W "$APP" --args --smoke-alert "$work"
	status=0
	report || status=1
	finish "$status"
fi

token="regrow smoke $$ $(date +%s)"
sentinel="$HOME/regrow-smoke-$$"
mkdir "$sentinel"
printf '%s\n' "$token" >"$sentinel/sentinel.txt"

mkdir "$work/rules"
cat >"$work/rules/smoke-sentinel.yaml" <<EOF
id: smoke-sentinel
title: Smoke test sentinel
category: dev-caches
risk: caution
paths:
  darwin:
    - ~/regrow-smoke-$$
regen:
  story: Nothing regrows it; the smoke test restores it with undo.
  cost: none
sudo: false
EOF
printf '["smoke-sentinel"]\n' >"$work/select.json"

open -n -W "$APP" --args --smoke "$work"

status=0
report || status=1

if [ "$(cat "$sentinel/sentinel.txt" 2>/dev/null)" = "$token" ]; then
	echo "PASS restored: $sentinel is back with its content."
	rm -rf "${sentinel:?}"
else
	status=1
	if [ -e "$sentinel" ]; then
		echo "FAIL restored: $sentinel is there but its content changed. It is left as it is." >&2
	else
		echo "FAIL restored: $sentinel was not put back. It is in the Trash as regrow-smoke-$$, or under $work/state/regrow/staging." >&2
	fi
fi
finish "$status"
