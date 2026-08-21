#!/bin/sh
# Start the dispatcher and exit, which is what Herdr expects of a startup
# command. `hdis daemon` refuses when one already holds the lock, and that
# refusal is success here: the daemon we wanted running is running.
set -eu
cd "$(dirname "$0")/.."
if [ ! -x bin/hdis ]; then
  echo "dispatch: bin/hdis is missing; the [[build]] step did not run" >&2
  exit 1
fi
# Absolute path deliberately: it is what shows in the process list, and a
# daemon nobody can name is a daemon nobody can stop.
nohup "$(pwd)/bin/hdis" daemon >/dev/null 2>&1 &
exit 0
