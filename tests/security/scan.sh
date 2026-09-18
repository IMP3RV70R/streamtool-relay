#!/bin/sh
set -eu
python3 tests/security/repository_hygiene.py
if rg -n 'WORKER_(SOURCE_TOKEN|DESTINATION_SECRET):' compose.yml compose.control.yml infra; then echo "plaintext worker secret environment found" >&2; exit 1; fi
if rg -n 'rtmp://[^/[:space:]]+:[^/@[:space:]]+@' . --glob '!ARCHITECTURE.md' --glob '!*_test.go'; then echo "credential-bearing RTMP URL found" >&2; exit 1; fi
go -C backend test ./internal/auth ./internal/crypto ./internal/netpolicy ./internal/application
go -C backend run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
echo "Security static and unit checks passed"
