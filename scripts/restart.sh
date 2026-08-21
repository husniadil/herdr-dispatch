#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./scripts/stop.sh
# A moment for the socket and the lock to go before the next daemon takes them.
sleep 1
exec ./scripts/start.sh
