#!/usr/bin/env bash
set -euo pipefail

source_root="$(cd "$(dirname "$0")" && pwd)"
package_root="$(dirname "$source_root")"

for target in windows-amd64 linux-amd64 darwin-amd64 darwin-arm64; do
  goos="${target%-*}"
  goarch="${target#*-}"
  executable="showcase"
  if [ "$goos" = windows ]; then executable="showcase.exe"; fi
  mkdir -p "$package_root/backend/$target"
  (cd "$source_root" && GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "$package_root/backend/$target/$executable" .)
done
