#!/usr/bin/env bash
set -euo pipefail
mode="${1:-live}"
if [[ "$mode" == "live" || "$mode" == "demo" ]]; then shift || true; else echo "usage: ./go.sh [live|demo] [flags]" >&2; exit 2; fi
exec go run -buildvcs=false ./cmd/watchlist-tool -mode "$mode" "$@"
