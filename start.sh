#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
if [[ ! -x bin/margin ]]; then
    echo "Run ./setup.sh first to build Margin and install the speech assets." >&2
    exit 1
fi
if [[ $# -eq 0 || ( $# -eq 1 && "$1" == --background ) ]] \
    && command -v systemctl >/dev/null 2>&1 \
    && [[ "$(systemctl --user show margin.service -p WorkingDirectory --value 2>/dev/null || true)" == "$(pwd -P)" ]]; then
    systemctl --user start margin.service
    echo "Margin is running. Open your configured Margin address."
    exit 0
fi
if [[ "${1:-}" == --background ]]; then
    echo "Background mode uses a user service. Install margin.service, or run ./start.sh in a terminal." >&2
    exit 1
fi
exec ./bin/margin serve --app-dir "$(pwd -P)" "$@"
