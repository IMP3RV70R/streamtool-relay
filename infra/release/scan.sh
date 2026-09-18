#!/usr/bin/env bash
# Scan exact locally available IDs exported by the native package builder.
set -euo pipefail
[[ $# == 1 && -d $1 ]] || { echo 'Usage: scan.sh BUNDLE_DIRECTORY' >&2; exit 2; }
bundle=$(cd "$1" && pwd)
report_dir="$bundle/../security-$(basename "$bundle")"
[[ ! -e $report_dir ]] || { echo 'Reports already exist.' >&2; exit 1; }
mkdir -m 0755 "$report_dir"
scan_dir=$(mktemp -d /tmp/streamtool-release-scan.XXXXXX)
trap 'rm -rf "$scan_dir"' EXIT
mkdir "$scan_dir/cache"
chmod 777 "$scan_dir/cache"
failed=0
scan_user="$(id -u):$(id -g)"
if [[ $(id -u) == 0 ]]; then scan_user=65532:65532; fi
for role in api worker proxy edge; do
  role_failed=0
  image=$(cat "$bundle/$role-image")
  [[ $image =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Invalid immutable image ID.' >&2; exit 1; }
  [[ $(docker image inspect --format '{{.Id}}' "$image") == "$image" ]] || { echo 'Exact release image unavailable.' >&2; exit 1; }
  docker save -o "$scan_dir/image.tar" "$image"
  chmod 644 "$scan_dir/image.tar"
  if docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
    --user "$scan_user" --tmpfs /tmp:rw,noexec,nosuid,nodev,size=1g \
    --env TRIVY_CACHE_DIR=/cache \
    --mount "type=bind,src=$scan_dir/cache,dst=/cache" \
    --mount "type=bind,src=$scan_dir/image.tar,dst=/scan/image.tar,readonly" \
    aquasec/trivy:0.74.0 image --input /scan/image.tar --scanners vuln \
    --severity HIGH,CRITICAL --exit-code 1 --no-progress --format json --output /cache/report.json; then
    : # Final result follows report/coverage validation.
  else
    role_failed=1
    echo "$role: scanner found vulnerabilities or was unavailable"
  fi
  if [[ -f $scan_dir/cache/report.json ]]; then
    cp "$scan_dir/cache/report.json" "$report_dir/$role.json"
    # Scratch media images must still expose Debian inventory; an unsupported OS
    # is incomplete coverage, not a clean native-library scan.
    if [[ $role == worker ]]; then
      if ! python3 "$(dirname "$0")/worker_coverage.py" "$report_dir/$role.json"
      then
        role_failed=1
      fi
    fi
    rm "$scan_dir/cache/report.json"
  else
    role_failed=1
    echo "$role: JSON report missing"
  fi
  if [[ $role == worker ]]; then
    if ! python3 "$(dirname "$0")/media_scan.py" "$image" "$report_dir"; then
      role_failed=1
    fi
  fi
  if [[ $role_failed == 0 ]]; then
    echo "$role: PASS"
  else
    failed=1
    echo "$role: FAIL"
  fi
  printf '%s\n' "$image" > "$report_dir/$role-image"
done
exit "$failed"
