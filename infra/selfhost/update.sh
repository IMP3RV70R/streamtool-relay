#!/usr/bin/env bash
# Root administrative entry point. Cabinet authorization will use a separate
# fixed protocol; never expose these filesystem arguments through the public API.
set -euo pipefail
[[ $(id -u) == 0 && $(uname -s) == Linux && $# == 4 ]] || { echo 'Usage: sudo bash update.sh MANIFEST SIGNATURE ARCHIVE JOB_UUID' >&2; exit 2; }
case $(uname -m) in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported host CPU.' >&2; exit 1;; esac
exec python3 /usr/local/libexec/streamtool-updater/updater.py queue \
 --manifest "$1" --signature "$2" --bundle "$3" --id "$4" --architecture "$arch"
