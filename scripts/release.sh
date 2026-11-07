#!/bin/sh
set -eu

version=${1:-${VERSION:-dev}}
if [ -z "$version" ]; then version=dev; fi
mkdir -p /out
for system in linux darwin; do
  for architecture in amd64 arm64; do
    asset="skytools_${system}_${architecture}"
    CGO_ENABLED=0 GOOS="$system" GOARCH="$architecture" \
      go build -trimpath -ldflags="-s -w -X main.version=$version" -o "/out/$asset" ./cmd/skytools
  done
done
# install.sh is the installer and the update logic self-update runs; it ships
# with each release and is listed in SHA256SUMS like the binaries.
install -m 0644 install.sh /out/install.sh
cd /out
sha256sum skytools_linux_amd64 skytools_linux_arm64 skytools_darwin_amd64 skytools_darwin_arm64 install.sh > SHA256SUMS
