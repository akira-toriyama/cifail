#!/bin/sh
# build.sh — build cifail into bin/cifail with the version/commit/date stamped
# from git. Used by install.sh and the Homebrew cask's from-source fallback.
set -eu
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
PKG="github.com/akira-toriyama/cifail/internal/version"

mkdir -p bin
GOTOOLCHAIN=local go build -trimpath \
  -ldflags "-s -w -X '${PKG}.Version=${VERSION}' -X '${PKG}.Commit=${COMMIT}' -X '${PKG}.Date=${DATE}'" \
  -o bin/cifail ./cmd/cifail

echo "built: $DIR/bin/cifail  (${VERSION})"
