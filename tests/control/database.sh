#!/bin/sh
set -eu
if [ "$#" -eq 0 ]; then set -- ./internal/control ./internal/persistence; fi
GOCACHE=/tmp/streamtool-go-cache GOMODCACHE=/tmp/streamtool-go-mod go -C backend test -race -count=1 "$@"
