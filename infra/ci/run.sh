#!/usr/bin/env bash
# Runs on an isolated Linux VM with Docker accessible to the test scripts.
set -euo pipefail

suite="${1:-}"
case "$suite" in
  checks|media|integration|acceptance|security-images|release) ;;
  *) echo 'Usage: bash infra/ci/run.sh {checks|media|integration|acceptance|security-images|release}' >&2; exit 2 ;;
esac
cd "$(dirname "$0")/../.."

if [[ "$(uname -s)" != Linux ]]; then
  echo 'This bootstrap requires a Linux CI worker; use the root Make targets locally.' >&2
  exit 1
fi

docker info >/dev/null
# Install only missing host tools on the disposable runner.
missing=false
for tool in make gcc curl python3 openssl rg timeout jq; do
  command -v "$tool" >/dev/null || missing=true
done
if [[ "$missing" == true ]]; then
  elevate=()
  if [[ "$(id -u)" != 0 ]]; then elevate=(sudo -n); fi
  "${elevate[@]}" apt-get update
  "${elevate[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    build-essential ca-certificates curl python3 openssl ripgrep coreutils jq
fi

# Reuse the same versioned toolchain images as local builds.
# The native worker must already provide a working Docker Engine and CLI.
tools_dir="$(mktemp -d /tmp/streamtool-ci-tools.XXXXXX)"
container_id=""
cleanup_tools() {
  if [[ -n "$container_id" ]]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
  rm -rf "$tools_dir"
}
trap cleanup_tools EXIT

if ! command -v go >/dev/null || [[ "$(go env GOVERSION)" != go1.25.14 ]]; then
  container_id="$(docker create golang:1.25.14-trixie)"
  docker cp "$container_id:/usr/local/go" "$tools_dir/go"
  docker rm "$container_id" >/dev/null
  container_id=""
  export GOROOT="$tools_dir/go"
  export PATH="$GOROOT/bin:$PATH"
fi

# Cleanup steps need the plugins too; use the runner Docker configuration.
if ! docker compose version >/dev/null 2>&1 || ! docker buildx version >/dev/null 2>&1; then
  plugins_dir="${DOCKER_CONFIG:-$HOME/.docker}/cli-plugins"
  mkdir -p "$plugins_dir"
  container_id="$(docker create docker:29.8.1-cli)"
  docker cp "$container_id:/usr/local/libexec/docker/cli-plugins/." "$plugins_dir/"
  docker rm "$container_id" >/dev/null
  container_id=""
fi

go version
docker compose version
docker buildx version

case "$suite" in
  release)
    : "${STREAMTOOL_RELEASE_VERSION:?Release version required}"
    timeout --signal=TERM --kill-after=60s 45m bash infra/selfhost/package.sh "$STREAMTOOL_RELEASE_VERSION"
    case "$(uname -m)" in x86_64) release_arch=amd64 ;; aarch64|arm64) release_arch=arm64 ;; *) exit 1 ;; esac
    timeout --signal=TERM --kill-after=60s 30m bash infra/release/scan.sh ".artifacts/streamtool-relay-$STREAMTOOL_RELEASE_VERSION-$release_arch"
    CADDY_TEST_IMAGE="$(cat ".artifacts/streamtool-relay-$STREAMTOOL_RELEASE_VERSION-$release_arch/proxy-image")" \
      timeout --signal=TERM --kill-after=10s 2m python3 tests/selfhost/public_ip_tls_test.py
    ;;
  checks)
    GOBIN="$tools_dir" go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
    "$tools_dir/actionlint" .github/workflows/*.yml
    timeout --signal=TERM --kill-after=60s 15m make check race security ;;
  media) timeout --signal=TERM --kill-after=60s 20m make e2e-fast ;;
  integration) timeout --signal=TERM --kill-after=60s 15m make integration security ;;
  acceptance) timeout --signal=TERM --kill-after=60s 20m make control-db-test users-test control-e2e control-video-e2e ;;
  security-images) timeout --signal=TERM --kill-after=60s 30m make security-images ;;
esac
