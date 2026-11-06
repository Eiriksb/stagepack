#!/usr/bin/env bash
# Usage: build/fetch.sh {go|node-linux|node-win|shell-linux|shell-win|ffmpeg-linux|ffmpeg-win|sources|ffmpeg-libraries}
# One-time, online. Downloads a pinned toolchain or bundled program into .cache/, verifies its sha256,
# and does nothing if the result already exists.
#   shell-linux, shell-win    Playwright's chromium-headless-shell, unpacked in .cache/chrome-headless-shell-<version>-<os>/
#   ffmpeg-linux, ffmpeg-win  BtbN's static GPL ffmpeg; only bin/ffmpeg[.exe] and LICENSE.txt are unpacked
#   sources                   the source archives the release mirrors (Node, Chromium, ffmpeg, BtbN's build
#                             scripts), in .cache/dl/ (about 2.1 GB, most of it Chromium's)
#   ffmpeg-libraries          the sources of the libraries linked into ffmpeg (build/ffmpeg-sources.tsv) in one
#                             archive, .cache/dl/ffmpeg-<version>-libraries-source.tar.xz; needs git, and svn
#                             for two of them. A run that stops part-way goes on where it stopped.
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

# fetch_shell <linux64|win64> url sha256: the zip holds one folder, chrome-headless-shell-<linux64|win64>/
fetch_shell() {
  local d="chrome-headless-shell-${CHROMIUM_SHELL_VERSION}-$1"
  [ -f "$cache/$d/chrome-headless-shell-$1/LICENSE.headless_shell" ] && return 0
  fetch "$2" "$d.zip" "$3"
  rm -rf "$cache/$d" "$cache/$d.part"
  unzip -q "$cache/dl/$d.zip" -d "$cache/$d.part"
  mv "$cache/$d.part" "$cache/$d"
}

# fetch_ffmpeg url sha256 <""|.exe>: the archive holds one folder, named as the archive
fetch_ffmpeg() {
  local f="${1##*/}" d
  d="${f%.tar.xz}"
  d="${d%.zip}"
  [ -f "$cache/$d/bin/ffmpeg$3" ] && return 0
  fetch "$1" "$f" "$2"
  rm -rf "$cache/$d" "$cache/$d.part"
  mkdir -p "$cache/$d.part"
  case "$f" in
    *.tar.xz) tar -C "$cache/$d.part" -xJf "$cache/dl/$f" "$d/bin/ffmpeg" "$d/LICENSE.txt" ;;
    *.zip) unzip -q "$cache/dl/$f" "$d/bin/ffmpeg.exe" "$d/LICENSE.txt" -d "$cache/$d.part" ;;
  esac
  mv "$cache/$d.part/$d" "$cache/$d"
  rmdir "$cache/$d.part"
}

# fetch_libraries: every row of build/ffmpeg-sources.tsv, fetched below one folder, then packed into one archive.
# A row that is done is marked in .done/, so a run that failed (a host down) goes on from there.
fetch_libraries() {
  local out="ffmpeg-${FFMPEG_VERSION}-libraries-source"
  [ -f "$cache/dl/$out.tar.xz" ] && return 0
  local work="$cache/$out" path kind url ref opt builds d mark
  mkdir -p "$work/$out" "$work/.done"
  while IFS=$'\t' read -r path kind url ref opt builds; do
    case "$path" in '#'* | '') continue ;; esac
    d="$work/$out/$path"
    mark="$work/.done/${path//\//__}"
    [ -e "$mark" ] && continue
    echo "$kind $url $ref"
    rm -rf "$d"
    case "$kind" in
      git)
        mkdir -p "$d"
        git -C "$d" init -q
        git -C "$d" remote add origin "$url"
        # by commit when the server allows it, else the whole history
        git -C "$d" fetch -q --depth=1 origin "$ref" 2>/dev/null || git -C "$d" fetch -q origin
        git -C "$d" -c advice.detachedHead=false checkout -q "$ref"
        [ "$(git -C "$d" rev-parse HEAD)" = "$ref" ] || { echo "$path: not at $ref" >&2; exit 1; }
        case "$opt" in
          submodules) git -C "$d" submodule update -q --init --recursive --depth=1 ;;
          submodules=*) git -C "$d" submodule update -q --init --recursive --depth=1 -- "${opt#submodules=}" ;;
          drop=*) rm -rf "${d:?}/${opt#drop=}" ;;
        esac
        ;;
      svn)
        command -v svn >/dev/null || { echo "$path needs svn (Subversion): install it, then run this again" >&2; exit 1; }
        mkdir -p "$(dirname "$d")"
        # the anonymous read access BtbN's script uses (svn.xvid.org asks for a user name)
        svn export -q --non-interactive --username anonymous --password '' -r "$ref" "$url@$ref" "$d"
        ;;
      file)
        mkdir -p "$(dirname "$d")"
        curl -fsSL --retry 3 -o "$d" "$url"
        echo "$ref  $d" | sha256sum -c --quiet - || { echo "checksum mismatch for $url" >&2; exit 1; }
        ;;
      crates)
        # the [[package]] entries from a registry carry name, version and checksum
        mkdir -p "$d"
        awk '/^\[\[package\]\]/ { n = v = "" } /^name = / { n = $3 } /^version = / { v = $3 }
             /^checksum = / { c = $3; gsub(/"/, "", n); gsub(/"/, "", v); gsub(/"/, "", c); print n, v, c }' \
          "$work/$out/$ref" | while read -r n v c; do
          curl -fsSL --retry 3 -o "$d/$n-$v.crate" "$url/$n/$n-$v.crate"
          echo "$c  $d/$n-$v.crate" | sha256sum -c --quiet - || { echo "checksum mismatch for crate $n $v" >&2; exit 1; }
        done
        ;;
      *) echo "build/ffmpeg-sources.tsv: unknown kind $kind for $path" >&2; exit 1 ;;
    esac
    touch "$mark"
  done <"$here/ffmpeg-sources.tsv"
  cp "$here/ffmpeg-sources.tsv" "$work/$out/SOURCES.tsv"
  # the same files give the same bytes: sorted, fixed owner and time, no .git
  tar --create --format=gnu --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --exclude=.git \
    -C "$work" "$out" | xz -T0 -6 >"$cache/dl/$out.tar.xz.part"
  mv "$cache/dl/$out.tar.xz.part" "$cache/dl/$out.tar.xz"
  rm -rf "$work"
  echo "wrote $cache/dl/$out.tar.xz ($(du -h "$cache/dl/$out.tar.xz" | cut -f1))"
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
  shell-linux) fetch_shell linux64 "$CHROMIUM_SHELL_URL_LINUX_X64" "$CHROMIUM_SHELL_SHA256_LINUX_X64" ;;
  shell-win) fetch_shell win64 "$CHROMIUM_SHELL_URL_WIN_X64" "$CHROMIUM_SHELL_SHA256_WIN_X64" ;;
  ffmpeg-linux) fetch_ffmpeg "$FFMPEG_URL_LINUX_X64" "$FFMPEG_SHA256_LINUX_X64" "" ;;
  ffmpeg-win) fetch_ffmpeg "$FFMPEG_URL_WIN_X64" "$FFMPEG_SHA256_WIN_X64" .exe ;;
  sources)
    fetch "$NODE_SOURCE_URL" "node-v${NODE_VERSION}.tar.xz" "$NODE_SOURCE_SHA256"
    fetch "$CHROMIUM_SOURCE_URL" "${CHROMIUM_SOURCE_URL##*/}" "$CHROMIUM_SOURCE_SHA256"
    fetch "$FFMPEG_SOURCE_URL" "FFmpeg-${FFMPEG_COMMIT}.tar.gz" "$FFMPEG_SOURCE_SHA256"
    fetch "$FFMPEG_BUILDS_SOURCE_URL" "FFmpeg-Builds-${FFMPEG_BUILDS_COMMIT}.tar.gz" "$FFMPEG_BUILDS_SOURCE_SHA256"
    ;;
  ffmpeg-libraries) fetch_libraries ;;
  *)
    echo "usage: $0 {go|node-linux|node-win|shell-linux|shell-win|ffmpeg-linux|ffmpeg-win|sources|ffmpeg-libraries}" >&2
    exit 2
    ;;
esac
