#!/bin/sh
# Build release archives and SHA256SUMS into dist/.
#   scripts/build-release.sh v0.1.0
set -eu
version="${1:?usage: build-release.sh VERSION}"
rm -rf dist
mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  os="${target%/*}"
  arch="${target#*/}"
  name="tringify-$os-$arch"
  work="dist/$name"
  mkdir -p "$work"
  binary="tringify"
  [ "$os" = windows ] && binary="tringify.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$work/$binary" ./cmd/tringify
  cp LICENSE README.md "$work/"
  if [ "$os" = windows ]; then
    (cd dist && zip -qr "$name.zip" "$name")
  else
    tar -C dist -czf "dist/$name.tar.gz" "$name"
  fi
  rm -rf "$work"
done
(cd dist && sha256sum tringify-* > SHA256SUMS)
cat dist/SHA256SUMS
