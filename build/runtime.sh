#!/usr/bin/env bash
# Usage: build/runtime.sh {linux|win32}
# Installs the pinned Slidev runtime (runtime/package.json + package-lock.json) into
# dist/<linux|windows>/runtimes/<major>/ for that os, then checks the licences of what it installed.
# Online (npm ci). For win32 npm picks the win32-x64 native packages (rolldown, oxc, lightningcss).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
major=53 # runtime.Major(); a test checks the two agree

case "${1:-}" in
  linux) dir="$root/dist/linux/runtimes/$major"; flags=() ;;
  win32) dir="$root/dist/windows/runtimes/$major"; flags=(--os=win32 --cpu=x64 --omit=dev) ;;
  *) echo "usage: $0 {linux|win32}" >&2; exit 2 ;;
esac

mkdir -p "$dir"
# a fresh node_modules every time; work/ (decks being shown) is left alone
rm -rf "$dir/node_modules"
cp "$root/runtime/package.json" "$root/runtime/package-lock.json" "$root/runtime/pnpm-workspace.yaml" "$dir/"
(cd "$dir" && npm ci --no-audit --no-fund "${flags[@]}")
node "$here/licenses.mjs" "$dir"
"$here/addon.sh" "$1"
