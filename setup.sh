#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
mkdir -p bin
if command -v go >/dev/null 2>&1 && command -v cc >/dev/null 2>&1; then
    CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/margin ./cmd/margin
elif command -v docker >/dev/null 2>&1; then
    docker build --target binary --output type=local,dest=bin .
else
    echo "Install Docker, or Go 1.26+ and a C compiler, then run ./setup.sh again." >&2
    exit 1
fi
exec ./bin/margin setup --app-dir "$(pwd -P)" "$@"
