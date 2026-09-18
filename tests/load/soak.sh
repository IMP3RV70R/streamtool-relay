#!/bin/sh
set -eu
hours="${SOAK_HOURS:-24}"
seconds=$((hours*3600))
STREAM_SMOKE_SECONDS="$seconds" ./tests/e2e/run.sh
