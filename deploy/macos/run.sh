#!/bin/sh
# Launchd entrypoint. Runs the binary from the code checkout with all
# instance state in $KROOS_HOME (cwd = $KROOS_HOME/workspace, set by the plist).
# kroos exits when stdin hits EOF (CLI channel), so keep stdin open.
REPO="$(cd "$(dirname "$0")/../.." && pwd)" || exit 1
[ -x "$REPO/bin/kroos" ] || (cd "$REPO" && go build -o bin/kroos ./cmd/kroos) || exit 1
tail -f /dev/null | exec "$REPO/bin/kroos"
