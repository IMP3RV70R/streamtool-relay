#!/usr/bin/env bash
# The privileged tester enters only the disposable labelled worker's netns.
# It does not change host firewall rules or production containers.
set -euo pipefail
cd "$(dirname "$0")/../.."
allocation="security-policy-$$"
worker="stream-worker-$allocation"
source_container="streamtool-security-source-$$"
network=streamtool-security-network
cleanup(){
 docker rm -f "$worker" "$source_container" >/dev/null 2>&1 || true
 docker network rm "$network" >/dev/null 2>&1 || true
}
# Do not take ownership of an already existing network/test run.
if docker network inspect "$network" >/dev/null 2>&1; then echo 'Security network already exists; refusing to reuse it' >&2; exit 1; fi
docker network create --subnet 172.30.249.0/28 "$network" >/dev/null
trap cleanup EXIT
docker build -f tests/security/Dockerfile.network -t streamtool-network-test:local .
docker run -d --name "$source_container" --network "$network" --ip 172.30.249.3 \
 --entrypoint python3 streamtool-network-test:local -c \
 'import socket; t=socket.socket();t.bind(("0.0.0.0",9090));t.listen();s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("0.0.0.0",8890));exec("while True:\n b,a=s.recvfrom(100);s.sendto(b,a)")' >/dev/null
docker run -d --name "$worker" --label "streamtool.allocation=$allocation" \
 --network "$network" --ip 172.30.249.2 --cap-drop ALL --security-opt no-new-privileges \
 streamtool-network-test:local -c 'sleep 120' >/dev/null
docker run --rm --privileged --pid host --volume /var/run/docker.sock:/var/run/docker.sock \
 streamtool-network-test:local -euc 'pid=$(docker inspect --format "{{.State.Pid}}" "stream-worker-$1"); nsenter --target "$pid" --net -- python3 -c "import socket;socket.create_connection((\"172.30.249.3\",9090),3)"; /usr/local/libexec/streamtool-worker-policy "$1" 20000000 4000000; python3 /probe.py "$1"' -- "$allocation"
