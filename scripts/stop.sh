#!/bin/sh
# Herdr has no shutdown hook, so this is the only way to turn the plugin off.
# `hdis stop` asks the daemon over its own socket and starts nothing when none
# is listening: it answers NOT_RUNNING, and none running is what was wanted.
set -eu
cd "$(dirname "$0")/.."
if [ ! -x bin/hdis ]; then
  echo "dispatch: bin/hdis is missing; nothing to ask" >&2
  exit 0
fi
./bin/hdis stop || echo "dispatch: no daemon was running"
exit 0
