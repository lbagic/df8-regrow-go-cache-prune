# regrow dev recipes — `just` runs `check` by default.

default: check

# build + vet + full test suite (the session-rule green bar)
check:
    go build ./... && go vet ./... && go test ./...

test:
    go test ./...

# refresh per-rule golden snapshots after a deliberate rule/plan change
golden:
    go test ./internal/scanner -run Golden -update

# interactive TUI scan of this machine (read-only; plan screen, nothing executes)
run:
    go run ./cmd/regrow scan

# hero bugs, fix-the-cause rows, phantom space (read-only, never plans)
doctor:
    go run ./cmd/regrow doctor

# plain outputs for piping/scripting
scan-json:
    go run ./cmd/regrow scan --json

# dry-run: exact commands that WOULD run (optionally: just plan rule-id ...)
plan *ids:
    go run ./cmd/regrow plan {{ids}}

# list the embedded rule catalog
rules:
    go run ./cmd/regrow rules

# install the binary to GOBIN as `regrow`
install:
    go install ./cmd/regrow

# build Regrow.app with the Command Line Tools, sign it with the identity from
# scripts/signing-cert.sh, replace ~/Applications/Regrow.app and launch it
app:
    scripts/app.sh install

# build Regrow.app into app/.build/bundle, ad-hoc signed; installs nothing
app-build:
    scripts/app.sh build

# end-to-end checks through the installed app: `just app-smoke trash` or `just app-smoke alert`
app-smoke mode:
    scripts/app-smoke.sh {{mode}}

# the Swift package's tests (swift-testing; no Xcode needed)
app-test:
    swift test --package-path app

# draw the scan page through WebKit from the recorded engine transcript: assertions, and a PNG per state
page-check out="app/.build/page-check":
    mkdir -p app/.build
    swiftc -O -o app/.build/pagecheck app/pagecheck/pagecheck.swift
    app/.build/pagecheck app/Resources/page.html internal/protocol/testdata/page-transcript.jsonl {{out}}
