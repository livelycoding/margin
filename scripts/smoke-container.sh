#!/usr/bin/env bash
# Exercise the shipped Compose configuration with a disposable, empty library.
set -euo pipefail

image=${1:-margin:validation}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
project="margin-smoke-$(date +%s)-$$"

compose() {
  docker compose --project-name "$project" --project-directory "$work" \
    --env-file "$work/.env" -f "$repo/compose.yaml" "$@"
}

cleanup() {
  result=$?
  trap - EXIT
  if (( result != 0 )); then
    compose logs --no-color --tail=100 >&2 || true
  fi
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Pick a free loopback port and avoid inheriting any installed deployment's .env.
port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
export MARGIN_IMAGE="$image" MARGIN_PORT="$port"
export MARGIN_BIND_ADDRESS=127.0.0.1 MARGIN_PUBLIC_ORIGINS=
touch "$work/.env"

printf 'Starting %s with empty data and model-cache volumes.\n' "$image"
compose up --detach --no-build --pull never --wait --wait-timeout 900
python3 "$repo/scripts/smoke-api.py" generate "http://127.0.0.1:$port" "$work/state.json"

printf 'Recreating the container with the same persistent volumes.\n'
compose down
compose up --detach --no-build --pull never --wait --wait-timeout 180
python3 "$repo/scripts/smoke-api.py" verify "http://127.0.0.1:$port" "$work/state.json"
printf 'Container smoke test passed.\n'
