#!/usr/bin/env bash
# Usage: build/payload.sh
# Zips dist/windows/{node,runtimes,stagepack.exe} into internal/install/payload.zip, which the
# `payload` build tag embeds in the Windows program (see `make setup`).
# Offline. Needs `make node runtime OS=win32` and `make viewer` first.
# Stored (already compressed): fonts, images, media. Deflated: everything else, programs
# and libraries too (they take about 80 MB less that way; the installer travels by mail and links).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
src="$root/dist/windows"
out="$root/internal/install/payload.zip"
major=53 # runtime.Major(); a test checks the two agree

[ -f "$src/node/node.exe" ] || { echo "missing $src/node/node.exe: run 'make node OS=win32'" >&2; exit 1; }
[ -f "$src/stagepack.exe" ] || { echo "missing $src/stagepack.exe: run 'make viewer'" >&2; exit 1; }
[ -d "$src/runtimes/$major/node_modules" ] || { echo "missing $src/runtimes/$major/node_modules: run 'make runtime OS=win32'" >&2; exit 1; }
command -v zip >/dev/null || { echo "zip is not installed" >&2; exit 1; }

stored=".woff:.woff2:.ttf:.otf:.png:.jpg:.jpeg:.gif:.webp:.avif:.ico:.mp4:.webm:.mp3:.ogg:.zip:.gz:.tgz"
rm -f "$out" "$out.part"
# a talk being shown (runtimes/<major>/work) is not part of the product
(cd "$src" && zip -qr -X -n "$stored" "$out.part" node runtimes stagepack.exe -x 'runtimes/*/work/*')
mv "$out.part" "$out"
echo "wrote $out ($(du -h "$out" | cut -f1), $(unzip -l "$out" | tail -1 | awk '{print $2}') files)"
