#!/bin/bash
set -euo pipefail

# No network or tool installations: prove that the standalone binary and shell
# setup work as an ordinary user without Go or any managed tool present.
test "$(id -u)" != 0
! command -v go
skytools --help
skytools version
sky-tools list
skytools status
skytools </dev/null
if skytools ensure --only does-not-exist; then exit 1; fi
if skytools status unexpected-argument; then exit 1; fi
printf '%s\n' 'export KEEP_MY_SETTING=yes' > "$HOME/.bashrc"
skytools setup
skytools setup
test "$(grep -c '^# skytools environment$' "$HOME/.bashrc")" = 1
grep -q 'export KEEP_MY_SETTING=yes' "$HOME/.bashrc"
test "$(cat "$HOME/.bashrc.skytools.bak")" = 'export KEEP_MY_SETTING=yes'
bash -c '. "$HOME/.bashrc"; test "$KEEP_MY_SETTING" = yes; test -z "${GOROOT:-}"; original=$PATH; . "$HOME/.bashrc"; test "$PATH" = "$original"'
sh -c 'eval "$(skytools env)"; test -z "${JAVA_HOME:-}"'
echo 'standalone binary and shell smoke checks passed'
