#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 && $1 =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || { echo 'Usage: package.sh VERSION' >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
version=$1
[[ $(uname -s) == Linux ]] || { echo 'Package on a native Linux runner.' >&2; exit 1; }
case "$(uname -m)" in
  x86_64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) echo 'Unsupported native architecture.' >&2; exit 1 ;;
esac
docker buildx version >/dev/null || { echo 'Docker Buildx is required.' >&2; exit 1; }
export DOCKER_BUILDKIT=1
output="$root/.artifacts/streamtool-relay-$version-$architecture"
[[ ! -e "$output" ]] || { echo 'Output already exists.' >&2; exit 1; }
mkdir -p "$output"
api_image="streamtool-relay-api:$version"
worker_image="streamtool-relay-worker:$version"
# Build this package on a Linux runner of the target CPU architecture.
docker build --build-arg VERSION="$version" --target control-api -t "$api_image" "$root/backend"
docker build --build-arg VERSION="$version" --target stream-worker -t "$worker_image" "$root/backend"
docker build --build-arg VERSION="$version" --target node-agent -t "streamtool-relay-agent:$version" "$root/backend"
proxy_reference="streamtool-relay-proxy:$version"
docker build -t "$proxy_reference" "$root/infra/selfhost/proxy"
docker pull bluenviron/mediamtx:1.21.0
proxy_image=$(docker image inspect --format '{{.Id}}' "$proxy_reference")
edge_image=$(docker image inspect --format '{{.Id}}' bluenviron/mediamtx:1.21.0)
docker image inspect --format '{{.Id}}' "$api_image" > "$output/api-image"
docker image inspect --format '{{.Id}}' "$worker_image" > "$output/worker-image"
printf '%s\n' "$proxy_image" > "$output/proxy-image"
printf '%s\n' "$edge_image" > "$output/edge-image"
# Save named references, but run immutable image IDs: load preserves IDs even
# when the registry's multi-platform manifest digest is absent offline.
docker save -o "$output/images.tar" "$api_image" "$worker_image" "$proxy_reference" bluenviron/mediamtx:1.21.0
container=$(docker create "streamtool-relay-agent:$version")
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker cp "$container:/usr/local/bin/node-agent" "$output/node-agent"
cp "$root/infra/selfhost/"{updater.py,updater_bridge.py,updater_engine.py,updater_host.py,maintenance.py,streamtool-updater.service,streamtool-updater-space.service,streamtool-updater-bridge.service,bootstrap.py,deploy.py,compose.yml,Caddyfile,mediamtx.yml,install.sh,update.sh,backup.py,renew.py,streamtool-certificates.service,streamtool-certificates.timer} "$output/"
cp "$root/infra/vm/"{streamtool-agent.service,streamtool-agent.sudoers,streamtool-worker-policy} "$output/"
cp "$root/LICENSE" "$output/LICENSE"
cp -a "$root/apps/web" "$output/web"
image_architecture=$(docker image inspect --format '{{.Architecture}}' "$worker_image")
[[ $image_architecture == "$architecture" ]] || { echo 'Worker architecture mismatch.' >&2; exit 1; }
for image in "$api_image" "$worker_image" "$proxy_reference" bluenviron/mediamtx:1.21.0; do
  [[ $(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image") == "linux/$architecture" ]] || { echo 'Release image platform mismatch.' >&2; exit 1; }
done
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go -C "$root/backend" build -trimpath -o "$output/release-tool" ./cmd/release-tool
printf '%s\n' "$version" > "$output/VERSION"
docker image inspect --format '{{.Architecture}}' "$worker_image" > "$output/ARCHITECTURE"
python3 "$root/infra/selfhost/archive.py" "$output" "$output.tar.gz"
echo "Bundle: $output.tar.gz"
