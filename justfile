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
