#!/bin/sh
set -eu
compose="docker compose -f compose.yml"
tmp="$(mktemp -d)"
./tests/control/certificates.sh
cleanup(){ rm -r "$tmp"; $compose stop api receiver >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
# Destination validation resolves receiver DNS even in development mode.
$compose up --build -d api receiver
deadline=$(( $(date +%s)+60 ));until curl -H "Authorization: Bearer local-operator-token-development-only" -fsS http://localhost:${API_PORT:-8080}/healthz >/dev/null;do [ "$(date +%s)" -lt "$deadline" ]||{ $compose logs api;exit 1;};sleep 1;done

curl -H "Authorization: Bearer local-operator-token-development-only" -fsS -X POST -H 'Content-Type: application/json' --data '{"name":"integration-account"}' http://localhost:${API_PORT:-8080}/v1/accounts > "$tmp/account.json"
account_id=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' "$tmp/account.json")
curl -H "Authorization: Bearer local-operator-token-development-only" -fsS -X POST -H 'Content-Type: application/json' --data "{\"account_id\":\"$account_id\",\"name\":\"integration-stream\",\"region\":\"local\"}" http://localhost:${API_PORT:-8080}/v1/streams > "$tmp/stream-create.json"
stream_id=$(sed -n 's/.*"stream":{[^}]*"id":"\([^"]*\)".*/\1/p' "$tmp/stream-create.json")
ingest_key=$(sed -n 's/.*"ingest_key":"\([^"]*\)".*/\1/p' "$tmp/stream-create.json")
test -n "$stream_id";test -n "$ingest_key"
curl -H "Authorization: Bearer local-operator-token-development-only" -fsS "http://localhost:${API_PORT:-8080}/v1/streams/$stream_id" > "$tmp/stream-get.json"
! grep -q 'ingest_key' "$tmp/stream-get.json"
code=$(curl -H "Authorization: Bearer local-operator-token-development-only" -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data "{\"password\":\"$ingest_key\",\"action\":\"publish\",\"path\":\"$stream_id\",\"protocol\":\"srt\"}" http://localhost:${API_PORT:-8080}/v1/edge/auth)
test "$code" = 404
code=$(curl -H "Authorization: Bearer local-operator-token-development-only" -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data "{\"password\":\"wrong-key-xxxxxxxx\",\"action\":\"publish\",\"path\":\"$stream_id\",\"protocol\":\"rtmp\"}" http://localhost:${API_PORT:-8080}/v1/edge/auth)
test "$code" = 404
code=$(curl -H "Authorization: Bearer local-operator-token-development-only" -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{"name":"blocked","endpoint":"rtmp://127.0.0.1/live","secret":"blocked-secret-value"}' "http://localhost:${API_PORT:-8080}/v1/streams/$stream_id/destinations")
test "$code" = 422
destination_secret="never-plaintext-destination-987"
curl -H "Authorization: Bearer local-operator-token-development-only" -fsS -X POST -H 'Content-Type: application/json' --data "{\"name\":\"local receiver\",\"endpoint\":\"rtmp://receiver:1935/live\",\"secret\":\"$destination_secret\"}" "http://localhost:${API_PORT:-8080}/v1/streams/$stream_id/destinations" > "$tmp/destination.json"
! grep -Fq "$destination_secret" "$tmp/destination.json"
code=$(curl -H "Authorization: Bearer local-operator-token-development-only" -sS -o /dev/null -w '%{http_code}' -X PATCH -H 'Content-Type: application/json' -H 'If-Match: 1' --data '{"name":"updated","enabled":true}' "http://localhost:${API_PORT:-8080}/v1/streams/$stream_id")
test "$code" = 200
code=$(curl -H "Authorization: Bearer local-operator-token-development-only" -sS -o /dev/null -w '%{http_code}' -X PATCH -H 'Content-Type: application/json' -H 'If-Match: 1' --data '{"name":"stale","enabled":true}' "http://localhost:${API_PORT:-8080}/v1/streams/$stream_id")
test "$code" = 409
seen=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -H "Authorization: Bearer local-edge-control-development-only" -fsS -X POST -H 'Content-Type: application/json' --data "{\"edge_id\":\"edge-$$\",\"connection_id\":\"conn-a\",\"stream_id\":\"$stream_id\",\"protocol\":\"srt\",\"connected\":true,\"seen_at\":\"$seen\"}" http://localhost:${API_PORT:-8080}/v1/edge/observations > "$tmp/session.json"
code=$(curl -H "Authorization: Bearer local-edge-control-development-only" -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data "{\"edge_id\":\"edge-$$\",\"connection_id\":\"conn-b\",\"stream_id\":\"$stream_id\",\"protocol\":\"rtmp\",\"connected\":true,\"seen_at\":\"$seen\"}" http://localhost:${API_PORT:-8080}/v1/edge/observations)
test "$code" = 409
$compose stop api >/dev/null
api_container=$($compose ps -aq api)
docker cp "$api_container:/data/control.sqlite" "$tmp/database.sqlite"

! grep -Fq "$destination_secret" "$tmp/database.sqlite"
! $compose logs --no-color api | grep -Fq "$destination_secret"
echo "Control-plane integration passed: migrations, CRUD, auth, SSRF, encryption, concurrency, single publisher"
