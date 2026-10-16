#!/bin/sh
# Launchd entrypoint. kroos exits when stdin hits EOF (CLI channel), so keep
# stdin open with a never-ending pipe.
cd "$(dirname "$0")/../.." || exit 1
[ -x bin/kroos ] || go build -o bin/kroos ./cmd/kroos || exit 1
tail -f /dev/null | exec bin/kroos
