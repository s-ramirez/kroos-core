#!/bin/sh
# Install/refresh launchd agents. Run from the code checkout.
# Instance state lives in $KROOS_HOME (default ~/.kroos), never in the checkout.
set -eu
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
KROOS_HOME="${KROOS_HOME:-$HOME/.kroos}"
GO_DIR="$(dirname "$(command -v go)")"
LA="$HOME/Library/LaunchAgents"
mkdir -p "$LA" "$HOME/Library/Logs/kroos" "$ROOT/bin" "$KROOS_HOME/workspace"
(cd "$ROOT" && go build -o bin/kroos ./cmd/kroos)
swiftc -O "$ROOT/deploy/macos/KroosBar.swift" -o "$ROOT/bin/kroosbar"
for name in com.kroos.agent com.kroos.deploy com.kroos.menubar; do
  sed -e "s#__ROOT__#$ROOT#g" -e "s#__HOME__#$HOME#g" -e "s#__GO_DIR__#$GO_DIR#g" \
    -e "s#__KROOS_HOME__#$KROOS_HOME#g" \
    "$ROOT/deploy/macos/$name.plist" > "$LA/$name.plist"
  launchctl bootout "gui/$(id -u)/$name" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$LA/$name.plist"
done
echo "installed; instance in $KROOS_HOME; logs in ~/Library/Logs/kroos"
