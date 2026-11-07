#!/bin/sh
# Install or update skytools from its GitHub releases:
#
#   curl -fsSL https://github.com/sky87/skytools/releases/latest/download/install.sh | sh
#
# A first install goes to ~/.local/bin/skytools. If skytools is already on PATH
# in a directory you can write to, that copy is updated in place, through any
# symlinks. `skytools self-update` runs this same script from the latest release.
#
# No GitHub account, token, or API request is needed: the latest release comes
# from the github.com/.../releases/latest redirect, and the binary is checked
# against the release's SHA256SUMS and its own reported version before it
# atomically replaces the old one. On any failure the old binary stays.
#
# Environment:
#   SKYTOOLS_VERSION      release tag to install, e.g. v0.7.1 (default: latest;
#                         an explicit tag is installed even if it is older)
#   SKYTOOLS_INSTALL_DIR  where a new install goes (default: ~/.local/bin)
#   SKYTOOLS_TARGET       exact executable to replace (used by self-update)
#   SKYTOOLS_RELEASES     release base URL (default:
#                         https://github.com/sky87/skytools/releases)
set -eu

say() { printf 'skytools: %s\n' "$*" >&2; }
die() {
  say "$*"
  exit 1
}

releases=${SKYTOOLS_RELEASES:-https://github.com/sky87/skytools/releases}
command -v curl >/dev/null 2>&1 || die "curl is required"

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported operating system $(uname -s)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac
asset=skytools_${os}_${arch}

# resolve follows symlinks without readlink -f, which older macOS lacks.
resolve() {
  path=$1
  hops=0
  while [ -L "$path" ]; do
    hops=$((hops + 1))
    [ "$hops" -le 40 ] || die "too many symlinks at $1"
    link=$(readlink "$path")
    case $link in
      /*) path=$link ;;
      *) path=$(dirname "$path")/$link ;;
    esac
  done
  printf '%s\n' "$path"
}

# version_cmp prints -1, 0 or 1, or nothing when either version is not numeric.
version_cmp() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    sub(/^v/, "", a); sub(/^v/, "", b)
    if (a !~ /^[0-9]+(\.[0-9]+)*$/ || b !~ /^[0-9]+(\.[0-9]+)*$/) exit
    n = split(a, x, "."); m = split(b, y, "."); if (m > n) n = m
    for (i = 1; i <= n; i++) {
      if (x[i] + 0 < y[i] + 0) { print -1; exit }
      if (x[i] + 0 > y[i] + 0) { print 1; exit }
    }
    print 0
  }'
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

# Choose the release.
tag=${SKYTOOLS_VERSION:-}
if [ -z "$tag" ]; then
  effective=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest") ||
    die "cannot reach $releases/latest"
  tag=${effective##*/releases/tag/}
  [ "$tag" != "$effective" ] || die "no latest release at $releases"
fi
printf '%s\n' "$tag" | grep -Eqx 'v[0-9]+(\.[0-9]+){2,3}' || die "invalid release tag '$tag'"

# Choose the executable to install or replace.
if [ -n "${SKYTOOLS_TARGET:-}" ]; then
  target=$(resolve "$SKYTOOLS_TARGET")
elif [ -n "${SKYTOOLS_INSTALL_DIR:-}" ]; then
  target=$(resolve "$SKYTOOLS_INSTALL_DIR/skytools")
else
  target=$(resolve "$HOME/.local/bin/skytools")
  if existing=$(command -v skytools 2>/dev/null) && [ -n "$existing" ]; then
    existing=$(resolve "$existing")
    if [ -w "$(dirname "$existing")" ]; then
      target=$existing
    elif [ "$existing" != "$target" ]; then
      say "$existing is not writable (an image-owned copy?); leaving it and installing $target"
    fi
  fi
fi
dir=$(dirname "$target")

if [ -x "$target" ]; then
  current=$("$target" version 2>/dev/null | sed -n 's/^skytools //p' | head -n 1) || current=
  comparison=$(version_cmp "$current" "$tag")
  if [ "$comparison" = 0 ]; then
    say "skytools $tag is already installed at $target"
    exit 0
  fi
  if [ "$comparison" = 1 ] && [ -z "${SKYTOOLS_VERSION:-}" ]; then
    say "skytools $current at $target is newer than the latest release $tag; keeping it"
    exit 0
  fi
fi

mkdir -p "$dir" 2>/dev/null || die "cannot create $dir"
[ -w "$dir" ] || die "cannot write to $dir; use a user-owned copy or rebuild the container image"

# Stage next to the target so the final rename is atomic. The process ID in the
# name lets self-update remove this file if it has to kill the script.
staged=$(mktemp "$dir/.skytools-update-$$-XXXXXX") || die "cannot stage the update in $dir"
sums=$(mktemp "${TMPDIR:-/tmp}/skytools-sums-$$-XXXXXX") || die "cannot create a temporary file"
trap 'rm -f "$staged" "$sums"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

base=$releases/download/$tag
say "downloading skytools $tag for $os/$arch"
curl -fsSL "$base/SHA256SUMS" -o "$sums" || die "cannot download $base/SHA256SUMS"
expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$sums")
case $expected in
  "" | *[!0-9a-fA-F]*) die "release $tag lists no SHA-256 for $asset" ;;
esac
[ "${#expected}" -eq 64 ] || die "release $tag lists no SHA-256 for $asset"
curl -fsSL "$base/$asset" -o "$staged" || die "cannot download $base/$asset"
actual=$(sha256 "$staged")
[ "$actual" = "$(printf '%s' "$expected" | tr 'A-F' 'a-f')" ] || die "SHA-256 mismatch for $asset; keeping the current skytools"

# Keep the old executable's permissions, and check the new one runs here and
# reports the release it came from.
mode=755
if [ -e "$target" ]; then
  mode=$(stat -c %a "$target" 2>/dev/null || stat -f %Lp "$target" 2>/dev/null || echo 755)
fi
chmod "$mode" "$staged"
reported=$("$staged" version 2>/dev/null) || die "the downloaded skytools does not run on this machine"
[ "$reported" = "skytools $tag" ] || die "the downloaded skytools reports '$reported', not $tag"

mv -f "$staged" "$target" || die "cannot replace $target"
say "installed skytools $tag at $target"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "$dir is not on PATH; run: export PATH=\"$dir:\$PATH\"" ;;
esac
