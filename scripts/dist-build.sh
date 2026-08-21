#!/bin/sh
set -eu

target=${CARGO_DIST_TARGET:-${DIST_TARGET:-}}
case "$target" in
  aarch64-apple-darwin)
    goos=darwin
    goarch=arm64
    ;;
  x86_64-apple-darwin)
    goos=darwin
    goarch=amd64
    ;;
  aarch64-unknown-linux-gnu)
    goos=linux
    goarch=arm64
    ;;
  x86_64-unknown-linux-gnu)
    goos=linux
    goarch=amd64
    ;;
  *)
    echo "unsupported or missing cargo-dist target: $target" >&2
    exit 2
    ;;
esac

version=${BIEWER_VERSION:-}
if [ -z "$version" ]; then
  version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
fi
version=${version#v}

mkdir -p dist-release
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
  -trimpath \
  -ldflags "-s -w -X github.com/shaktsin/biewer/internal/cli.version=$version" \
  -o dist-release/biewer \
  ./cmd/biewer
