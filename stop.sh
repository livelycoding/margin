#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
if command -v systemctl >/dev/null 2>&1 \
    && [[ "$(systemctl --user show margin.service -p WorkingDirectory --value 2>/dev/null || true)" == "$(pwd -P)" ]]; then
    systemctl --user stop margin.service
    echo "Margin stopped. Your library is saved."
else
    echo "No matching Margin user service. Stop the foreground process with Ctrl+C." >&2
    exit 1
fi
