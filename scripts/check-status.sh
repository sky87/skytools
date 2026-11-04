#!/bin/sh
set -eu
output=$(skytools status "$@")
printf '%s\n' "$output"
if printf '%s\n' "$output" | grep -Eq 'not installed / unavailable|version check timed out'; then
  echo 'Integration failed: at least one selected tool is unavailable' >&2
  exit 1
fi
