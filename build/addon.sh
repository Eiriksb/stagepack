#!/usr/bin/env bash
# Usage: build/addon.sh {linux|win32}
# Copies the export addon (addon/, slidev-addon-stagepack) into dist/<linux|windows>/runtimes/<major>/node_modules,
# so that talks naming it in addons: open in the viewer (where it does nothing; see addon/setup/vite-plugins.ts).
# It is our own code, not an npm package, so it is copied in rather than installed. Offline.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
major=53 # runtime.Major(); a test checks the two agree
pkg=slidev-addon-stagepack # brand.AddonPkg

case "${1:-}" in
  linux) dir="$root/dist/linux/runtimes/$major" ;;
  win32) dir="$root/dist/windows/runtimes/$major" ;;
  *) echo "usage: $0 {linux|win32}" >&2; exit 2 ;;
esac
[ -d "$dir/node_modules" ] || { echo "missing $dir/node_modules: run 'make runtime OS=$1'" >&2; exit 1; }

dest="$dir/node_modules/$pkg"
rm -rf "$dest"
mkdir -p "$dest"
cp -r "$root/addon/package.json" "$root/addon/README.md" "$root/addon/custom-nav-controls.vue" "$root/addon/setup" "$dest/"
cp "$root/LICENSE" "$dest/"
