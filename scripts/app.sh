#!/bin/sh
# Builds Regrow.app with the Command Line Tools alone (no Xcode).
#
#   scripts/app.sh build     assemble app/.build/bundle/Regrow.app, ad-hoc signed.
#                            It runs, but macOS keeps no grant for it across rebuilds.
#   scripts/app.sh install   build, sign with the identity scripts/signing-cert.sh
#                            recorded, replace ~/Applications/Regrow.app, launch it.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BUNDLE="$ROOT/app/.build/bundle/Regrow.app"
DEST="$HOME/Applications/Regrow.app"
RECORD="$ROOT/.signing-identity"
STATE="${XDG_STATE_HOME:-$HOME/.local/state}/regrow"
SIGN_LIMIT=60
QUIT_LIMIT=45

mode="${1:-}"
case "$mode" in
build | install) ;;
*)
	echo "usage: $0 build|install" >&2
	exit 64
	;;
esac

identity=-
if [ "$mode" = install ]; then
	if [ ! -s "$RECORD" ]; then
		echo "No signing identity recorded. Run scripts/signing-cert.sh once, then this again." >&2
		exit 1
	fi
	identity=$(tr -d '[:space:]' <"$RECORD")
	case "$identity" in
	*[!0-9A-Fa-f]* | "")
		echo "$RECORD does not hold a SHA-1 hash." >&2
		exit 1
		;;
	esac
	if [ "${#identity}" -ne 40 ]; then
		echo "$RECORD does not hold a SHA-1 hash." >&2
		exit 1
	fi
fi

# codesign waits forever on a keychain dialog nobody answers.
sign() {
	codesign --force --timestamp=none --sign "$identity" "$@" &
	signer=$!
	(
		sleep "$SIGN_LIMIT"
		kill "$signer" 2>/dev/null
	) >/dev/null 2>&1 &
	watchdog=$!
	status=0
	wait "$signer" || status=$?
	kill "$watchdog" 2>/dev/null || true
	wait "$watchdog" 2>/dev/null || true
	if [ "$status" -ne 0 ]; then
		echo "codesign failed or did not finish within ${SIGN_LIMIT}s." >&2
		echo "If a keychain dialog is open, choose \"Always Allow\" and run this again." >&2
		exit 1
	fi
}

# The executable a pid runs, or nothing when the pid is gone.
exe_of() {
	ps -o comm= -p "$1" 2>/dev/null || true
}

# Waits until pid is gone or limit seconds pass; fails when it is still there.
gone_within() {
	n=0
	while kill -0 "$1" 2>/dev/null; do
		[ "$n" -ge "$2" ] && return 1
		sleep 1
		n=$((n + 1))
	done
}

# Asks the installed app to quit and waits for its engine: an execute in
# flight finishes the action it is running. Only pids from the app's own
# pid file are signalled, and only while they still run the installed
# bundle's executables.
stop_running() {
	[ -f "$STATE/app.pid" ] || return 0
	app_pid=$(sed -n 1p "$STATE/app.pid" | tr -cd '0-9')
	engine_pid=$(sed -n 2p "$STATE/app.pid" | tr -cd '0-9')

	if [ -n "$app_pid" ] && [ "$(exe_of "$app_pid")" = "$DEST/Contents/MacOS/Regrow" ]; then
		echo "Quitting the running Regrow (pid $app_pid)."
		kill -TERM "$app_pid" 2>/dev/null || true
		if ! gone_within "$app_pid" "$QUIT_LIMIT"; then
			echo "Regrow (pid $app_pid) is still running after ${QUIT_LIMIT}s. Quit it from its menu, then run this again." >&2
			exit 1
		fi
	fi

	if [ -n "$engine_pid" ] && [ "$(exe_of "$engine_pid")" = "$DEST/Contents/MacOS/regrow-engine" ]; then
		if ! gone_within "$engine_pid" "$QUIT_LIMIT"; then
			echo "The engine (pid $engine_pid) is still running an action after ${QUIT_LIMIT}s; stopping it."
			kill -TERM "$engine_pid" 2>/dev/null || true
			if ! gone_within "$engine_pid" 5; then
				# The engine dies at once on its second SIGTERM.
				kill -TERM "$engine_pid" 2>/dev/null || true
				gone_within "$engine_pid" 2 || kill -KILL "$engine_pid" 2>/dev/null || true
			fi
		fi
	fi
}

cd "$ROOT"
if xcode-select -p 2>/dev/null | grep -q CommandLineTools; then
	echo "Toolchain: Command Line Tools ($(swift --version 2>&1 | sed -n 's/.*\(Swift version [0-9.]*\).*/\1/p' | head -1))"
fi

version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
short=$(printf '%s' "$version" | sed 's/^v//; s/-.*//')
case "$short" in
[0-9]*.[0-9]*) ;;
*) short=0.0.0 ;;
esac

swift build -c release --package-path "$ROOT/app"
bin=$(swift build -c release --package-path "$ROOT/app" --show-bin-path)

rm -rf "${BUNDLE:?}"
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"
cp "$bin/Regrow" "$BUNDLE/Contents/MacOS/Regrow"
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$version" \
	-o "$BUNDLE/Contents/MacOS/regrow-engine" ./cmd/regrow
cp "$ROOT/app/Resources/page.html" "$BUNDLE/Contents/Resources/page.html"
cp "$ROOT/app/Resources/Info.plist" "$BUNDLE/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $short" \
	-c "Set :CFBundleVersion $version" "$BUNDLE/Contents/Info.plist"

# The engine first: the bundle's signature seals it.
sign "$BUNDLE/Contents/MacOS/regrow-engine"
sign "$BUNDLE"
codesign --verify --strict "$BUNDLE"
echo "Built $BUNDLE ($version)"

if [ "$mode" = build ]; then
	echo "Ad-hoc signed: for a look, not for grants. \`scripts/app.sh install\` signs and installs it."
	exit 0
fi

codesign -d -r- "$BUNDLE" 2>&1 | sed -n 's/^designated => /Signed as: /p'

stop_running

# Staged beside the destination, so the exchange stays on one volume.
mkdir -p "$HOME/Applications"
staged="$HOME/Applications/.Regrow.app.staged"
rm -rf "${staged:?}"
ditto "$BUNDLE" "$staged"
"$staged/Contents/MacOS/Regrow" --swap "$staged" "$DEST"
rm -rf "${staged:?}"

open "$DEST"
echo "Installed $DEST and launched it."
