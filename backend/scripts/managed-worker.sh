#!/bin/sh
set -eu
# The agent uploads the complete secret archive to tmpfs before setting ready.
while [ ! -f /run/secrets/ready ]; do sleep 0.1; done
exec /usr/local/bin/stream-worker
