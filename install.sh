#!/bin/sh
# install.sh — build cifail and place it at ~/.local/bin/cifail. It is a
# single-shot CLI (no daemon), so there is no service to manage.
set -eu
DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="$HOME/.local/bin/cifail"

"$DIR/build.sh"
mkdir -p "$HOME/.local/bin"
install -m 0755 "$DIR/bin/cifail" "$BIN"
echo "installed: $BIN"

case ":$PATH:" in
    *":$HOME/.local/bin:"*) ;;
    *) echo "note: $HOME/.local/bin is not on PATH. Add to your shell rc:"
       echo "      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac
