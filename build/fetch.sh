#!/usr/bin/env bash
# Usage: build/fetch.sh {go|node-linux|node-win}
# One-time, online. Downloads a pinned toolchain into .cache/,
# verifies its sha256, and does nothing if the result already exists.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
cache="$root/.cache"
# shellcheck source=versions.env
. "$here/versions.env"

fetch() { # url file sha256
  local url="$1" file="$2" sha="$3"
  mkdir -p "$cache/dl"
  if [ ! -f "$cache/dl/$file" ]; then
    echo "downloading $url"
    curl -fsSL --retry 3 -o "$cache/dl/$file.part" "$url"
    mv "$cache/dl/$file.part" "$cache/dl/$file"
  fi
  (cd "$cache/dl" && echo "$sha  $file" | sha256sum -c -) || {
    rm -f "$cache/dl/$file"
    echo "checksum mismatch for $file" >&2
    exit 1
  }
}

case "${1:-}" in
  go)
    [ -x "$cache/go/bin/go" ] && exit 0
    f="go${GO_VERSION}.linux-amd64.tar.gz"
    fetch "https://go.dev/dl/$f" "$f" "$GO_SHA256_LINUX_AMD64"
    rm -rf "$cache/go"
    tar -C "$cache" -xzf "$cache/dl/$f"
    ;;
  node-linux)
    d="node-v${NODE_VERSION}-linux-x64"
    [ -x "$cache/$d/bin/node" ] && exit 0
    fetch "https://nodejs.org/dist/v${NODE_VERSION}/$d.tar.xz" "$d.tar.xz" "$NODE_SHA256_LINUX_X64"
    rm -rf "$cache/$d"
    tar -C "$cache" -xJf "$cache/dl/$d.tar.xz"
    ;;
  node-win)
    d="node-v${NODE_VERSION}-win-x64"
    [ -f "$cache/$d/node.exe" ] && exit 0
    fetch "https://nodejs.org/dist/v${NODE_VERSION}/$d.zip" "$d.zip" "$NODE_SHA256_WIN_X64"
    rm -rf "$cache/$d"
    unzip -q "$cache/dl/$d.zip" -d "$cache"
    ;;
  *)
    echo "usage: $0 {go|node-linux|node-win}" >&2
    exit 2
    ;;
esac
