#!/usr/bin/env bash
# Scan exported release images without exposing the Docker socket or secrets.
# Failure to fetch the vulnerability database fails the gate.
set -euo pipefail
cd "$(dirname "$0")/../.."
scan_dir=$(mktemp -d /tmp/streamtool-image-scan.XXXXXX)
trap 'rm -r "$scan_dir"' EXIT
mkdir "$scan_dir/cache"
chmod 777 "$scan_dir/cache"
docker compose -f compose.control.yml build api agent worker
docker build --target security-source -f backend/Dockerfile backend
docker build -t streamtool-relay-proxy:local infra/selfhost/proxy
docker build --target security-source infra/selfhost/proxy
failed=0
scan_user="$(id -u):$(id -g)"
if [[ $(id -u) == 0 ]]; then scan_user=65532:65532; fi
for service in api agent worker edge proxy; do
 image="streamtool-control-$service:latest"
 if [[ $service == worker ]]; then image=streamtool-managed-worker:local; fi
 if [[ $service == proxy ]]; then image=streamtool-relay-proxy:local; fi
 if [[ $service == edge ]]; then image=bluenviron/mediamtx:1.21.0; fi
 [[ -n $image ]]
 docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image"
 image=$(docker image inspect --format '{{.Id}}' "$image")
 docker image save -o "$scan_dir/image.tar" "$image"
 chmod 644 "$scan_dir/image.tar"
 role_failed=0
 if docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user "$scan_user" --tmpfs /tmp:rw,noexec,nosuid,nodev,size=1g \
  --env TRIVY_CACHE_DIR=/cache \
  --mount "type=bind,src=$scan_dir/cache,dst=/cache" \
  --mount "type=bind,src=$scan_dir/image.tar,dst=/scan/image.tar,readonly" \
  aquasec/trivy:0.74.0 image --input /scan/image.tar --scanners vuln \
  --severity HIGH,CRITICAL --exit-code 1 --no-progress --format json --output /cache/report.json; then
  : # Final result follows report and worker coverage validation.
 else
  role_failed=1
  echo "$service: scan failed or HIGH/CRITICAL vulnerabilities found"
 fi
 if [[ -f $scan_dir/cache/report.json ]]; then
  jq -r '.Results[] | .Target as $target | .Vulnerabilities[]? | [$target,.VulnerabilityID,.PkgName,.InstalledVersion,.FixedVersion,.Status] | @tsv' "$scan_dir/cache/report.json"
  mkdir -p .artifacts/security-images
  cp "$scan_dir/cache/report.json" ".artifacts/security-images/$service.json"
  printf '%s\n' "$image" > ".artifacts/security-images/$service-image"
  if [[ $service == worker ]]; then
   python3 infra/release/worker_coverage.py ".artifacts/security-images/$service.json" || role_failed=1
  fi
  rm "$scan_dir/cache/report.json"
 else
  role_failed=1
  echo "$service: JSON report missing"
 fi
 if [[ $service == worker ]]; then
  mkdir -p .artifacts/security-images
  python3 infra/release/media_scan.py "$image" .artifacts/security-images || role_failed=1
 fi
 if [[ $role_failed == 0 ]]; then
  echo "$service: PASS"
 else
  failed=1
  echo "$service: FAIL"
 fi
done
exit "$failed"
