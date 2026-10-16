#!/bin/sh
# Pull origin/main; if changed, test, build, and restart the agent.
# Old binary keeps running if tests/build fail.
set -u
cd "$(dirname "$0")/../.." || exit 1
git fetch -q origin main || exit 1
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] && exit 0
echo "$(date '+%F %T') deploying $(git rev-parse --short origin/main)"
git reset -q --hard origin/main
go test ./... >/dev/null || { echo "tests failed, not deploying"; exit 1; }
go build -o bin/kroos.new ./cmd/kroos || { echo "build failed"; exit 1; }
mv bin/kroos.new bin/kroos
launchctl kickstart -k "gui/$(id -u)/com.kroos.agent"
echo "deployed"
