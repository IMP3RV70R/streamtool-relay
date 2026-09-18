#!/bin/sh
set -eu

compose="docker compose -f compose.yml"
artifact_dir=".artifacts"
log_file="$artifact_dir/worker.log"
secret_a="local-read-token-1234"
secret_b="fixture"
smoke_seconds="${STREAM_SMOKE_SECONDS:-300}"

./tests/control/certificates.sh
mkdir -p "$artifact_dir/recordings"
$compose up --build -d

cleanup() {
  $compose down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_log() {
  pattern="$1"
  deadline=$(( $(date +%s) + 45 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    $compose logs --no-color worker > "$log_file"
    if grep -q "$pattern" "$log_file"; then return 0; fi
    sleep 1
  done
  $compose ps
  $compose logs --no-color
  echo "timed out waiting for $pattern" >&2
  return 1
}

wait_log_count() {
  pattern="$1"
  previous="$2"
  deadline=$(( $(date +%s) + 45 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    $compose logs --no-color worker > "$log_file"
    current=$(grep -c "$pattern" "$log_file" || true)
    if [ "$current" -gt "$previous" ]; then return 0; fi
    sleep 1
  done
  $compose logs --no-color worker
  echo "timed out waiting for a new $pattern" >&2
  return 1
}

wait_bytes() {
  deadline=$(( $(date +%s) + 45 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS http://localhost:9998/v3/rtmpconns/list | grep -Eq '"bytesReceived"[[:space:]]*:[[:space:]]*[1-9]'; then return 0; fi
    sleep 1
  done
  echo "receiver did not report media bytes" >&2
  return 1
}

receiver_bytes() {
  curl -fsS http://localhost:9998/v3/rtmpconns/list \
    | sed -n 's/.*"bytesReceived"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' \
    | head -n 1
}

wait_log 'INPUT_LIVE'
wait_log 'DESTINATION_STREAMING'
wait_bytes
bytes_before=$(receiver_bytes)
sleep "$smoke_seconds"
bytes_after=$(receiver_bytes)
if [ -z "$bytes_before" ] || [ -z "$bytes_after" ] || [ "$bytes_after" -le "$bytes_before" ]; then
  echo "receiver bytes did not increase during ${smoke_seconds}s smoke window" >&2
  exit 1
fi

streaming_before=$(grep -c 'DESTINATION_STREAMING' "$log_file" || true)
worker_id=$($compose ps -q worker)
restart_before=$(docker inspect -f '{{.RestartCount}}' "$worker_id")
$compose restart receiver
wait_log 'DESTINATION_RECONNECTING'
wait_log_count 'DESTINATION_STREAMING' "$streaming_before"
wait_bytes

restart_after=$(docker inspect -f '{{.RestartCount}}' "$worker_id")
if [ "$restart_after" -ne "$restart_before" ]; then
  echo "worker process restarted during destination outage" >&2
  exit 1
fi

if $compose top worker | grep -Fq "$secret_a" || $compose top worker | grep -Fq "$secret_b"; then
  echo "secret found in worker process arguments" >&2
  exit 1
fi

$compose stop -t 10 worker
$compose logs --no-color worker > "$log_file"
grep -q 'WORKER_STOPPED' "$log_file"

if grep -Fq "$secret_a" "$log_file" || grep -Fq "$secret_b" "$log_file"; then
  echo "secret found in worker logs" >&2
  exit 1
fi
find "$artifact_dir/recordings" -type f -size +0c -print | grep -q .
echo "E2E passed: media bytes, isolated reconnect, clean shutdown, no secret leakage"
