#!/bin/sh
# check.sh — the full local verification, runnable by you or by Claude Code with
# no TTY. Mirrors what .github/workflows/build.yml enforces in CI, so a green run
# here means a green CI. GOTOOLCHAIN=local uses whatever toolchain is installed
# (the go.mod floor is a supported minor).
set -eu
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local

# Module hygiene: fail if go.mod/go.sum are not tidy, and verify the downloaded
# dependencies match go.sum. `-diff` prints the needed changes and exits non-zero
# without touching the files (Go 1.23+), so this is a pure gate under `set -e`.
echo "→ go mod tidy -diff && go mod verify"
go mod tidy -diff
go mod verify

echo "→ go build"
go build ./...

echo "→ go vet"
go vet ./...

echo "→ go test -race"
go test -race ./...

echo "→ fuzz smoke (extract budget invariants, 5s)"
go test -run='^$' -fuzz='^FuzzExtract$' -fuzztime=5s ./internal/extract

if command -v golangci-lint >/dev/null 2>&1; then
  echo "→ golangci-lint"
  golangci-lint run ./...
else
  echo "→ golangci-lint (skipped — not installed; CI runs it)"
fi

if command -v govulncheck >/dev/null 2>&1; then
  echo "→ govulncheck"
  govulncheck ./...
else
  echo "→ govulncheck (skipped — not installed; CI runs it)"
fi

echo "→ build binary for live checks"
go build -o bin/cifail ./cmd/cifail
BIN="$(pwd)/bin/cifail"

echo "→ smoke: version / --version / --help / usage errors"
"$BIN" version >/dev/null
"$BIN" version --ndjson >/dev/null # subcommand must own --ndjson (else exit 2)
"$BIN" --version >/dev/null
"$BIN" --help >/dev/null
# a bad flag must exit 2 (usage), not crash
if "$BIN" --budget-bytes -1 --repo x/y --run 1 >/dev/null 2>&1; then
  echo "  expected a usage error for --budget-bytes -1" >&2
  exit 1
fi
echo "✓ all checks passed"
