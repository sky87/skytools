#!/bin/sh
set -eu
output=$(skytools status "$@")
printf '%s\n' "$output"
if printf '%s\n' "$output" | grep -Eq '(^|[[:space:]])missing($|[[:space:]])|version check timed out|incomplete'; then
  echo 'Integration failed: at least one selected tool is unavailable' >&2
  exit 1
fi
versions=$(skytools status --versions "$@")
printf '%s\n' "$versions"
if printf '%s\n' "$versions" | grep -Eq '(^|[[:space:]])missing($|[[:space:]])|version check timed out'; then
  echo 'Integration failed: an installed tool cannot report its version' >&2
  exit 1
fi
if [ "$#" -eq 0 ]; then
  # Full integration also executes Lean offline. Fast inventory deliberately
  # reads local installation files without invoking any package binary.
  "$HOME/.elan/bin/lean" --version
fi
