#!/bin/sh
# Install/refresh launchd agents. Run from the prod clone.
set -eu
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO_DIR="$(dirname "$(command -v go)")"
LA="$HOME/Library/LaunchAgents"
mkdir -p "$LA" "$HOME/Library/Logs/kroos" "$ROOT/bin"
swiftc -O "$ROOT/deploy/macos/KroosBar.swift" -o "$ROOT/bin/kroosbar"
for name in com.kroos.agent com.kroos.deploy com.kroos.menubar; do
  sed -e "s#__ROOT__#$ROOT#g" -e "s#__HOME__#$HOME#g" -e "s#__GO_DIR__#$GO_DIR#g" \
    "$ROOT/deploy/macos/$name.plist" > "$LA/$name.plist"
  launchctl bootout "gui/$(id -u)/$name" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$LA/$name.plist"
done
echo "installed; logs in ~/Library/Logs/kroos"
