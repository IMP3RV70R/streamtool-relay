#!/bin/sh
set -eu
# Only workers owned by this local managed node; unrelated containers are untouched.
node="${CONTROL_TEST_NODE_ID:-00000000-0000-4000-8000-000000000002}"
if [ "$node" = "00000000-0000-4000-8000-000000000001" ]; then
  echo 'Refusing to clean the self-hosted node from the control fixture.' >&2
  exit 1
fi
ids=$(docker ps -aq --filter "label=streamtool.node=$node")
if [ -n "$ids" ]; then docker stop -t 10 $ids >/dev/null; docker rm $ids >/dev/null; fi
docker compose -f compose.control.yml down --remove-orphans
