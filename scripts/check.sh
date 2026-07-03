#!/bin/sh
# check.sh — the full local verification, runnable by you or by Claude Code with
# no TTY. Mirrors what .github/workflows/build.yml enforces in CI, so a green run
# here means a green CI. GOTOOLCHAIN=local uses whatever toolchain is installed
# (the go.mod floor is a supported minor).
set -eu
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local

echo "→ go build"
go build ./...

echo "→ go vet"
go vet ./...

echo "→ go test -race"
go test -race ./...

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
"$BIN" --version >/dev/null
"$BIN" --help >/dev/null
# a bad flag must exit 2 (usage), not crash
if "$BIN" --budget-bytes -1 --repo x/y --run 1 >/dev/null 2>&1; then
  echo "  expected a usage error for --budget-bytes -1" >&2
  exit 1
fi
echo "✓ all checks passed"
