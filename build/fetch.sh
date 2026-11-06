#!/usr/bin/env bash
# Usage: build/fetch.sh {go|node-linux|node-win|ffmpeg-linux|ffmpeg-win|sources|ffmpeg-libraries}
# One-time, online. Downloads a pinned toolchain or bundled program into .cache/, verifies its sha256,
# and does nothing if the result already exists.
#   ffmpeg-linux, ffmpeg-win  BtbN's static GPL ffmpeg; only bin/ffmpeg[.exe] and LICENSE.txt are unpacked
#   sources                   ffmpeg's source and BtbN's build scripts at the build's commits, in .cache/dl/
#   ffmpeg-libraries          the sources of the libraries linked into ffmpeg (build/ffmpeg-sources.tsv) in one
#                             archive, .cache/dl/ffmpeg-<version>-libraries-source.tar.xz, after checking that
#                             their licence texts are the ones in build/ffmpeg-licenses.txt. Needs git, and svn
#                             for xvid (and for lame when SourceForge's snapshot is gone). A run that stops
#                             part-way goes on where it stopped; a row or table that changed is fetched again.
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

# treehash dir: one sha256 of the names and contents of the files below dir (the tree= of an svn row)
treehash() {
  (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) | sha256sum | cut -d' ' -f1
}

# stamp: what the libraries archive is made from (the table and the licence texts it is checked against); the
# archive is made again when it changes
stamp() {
  cat "$here/ffmpeg-sources.tsv" "$here/ffmpeg-licenses.txt" | sha256sum | cut -d' ' -f1
}

# snapshot url dir: SourceForge's snapshot zip of an svn revision (one folder), unpacked as dir. Returns 1 when
# it cannot be downloaded (SourceForge drops old snapshots); one that is there but not one folder stops the run.
snapshot() {
  local tmp top
  tmp="$(mktemp -d "$cache/snapshot.XXXXXX")"
  if ! curl -fsSL --retry 3 -o "$tmp/s.zip" "$1"; then
    echo "no snapshot at $1: using svn" >&2
    rm -rf "$tmp"
    return 1
  fi
  unzip -q "$tmp/s.zip" -d "$tmp/x" || { echo "the snapshot at $1 is not a zip" >&2; rm -rf "$tmp"; exit 1; }
  top="$(find "$tmp/x" -mindepth 1 -maxdepth 1)"
  [ -d "$top" ] || { echo "the snapshot at $1 is not one folder" >&2; rm -rf "$tmp"; exit 1; }
  mkdir -p "$(dirname "$2")"
  mv "$top" "$2"
  rm -rf "$tmp"
}

# svn_export url revision dir: the files as the repository holds them (keywords not expanded, LF line ends), as
# SourceForge's snapshots have them, so that both give the same tree hash
svn_export() {
  command -v svn >/dev/null || { echo "$3 needs svn (Subversion): install it, then run this again" >&2; exit 1; }
  mkdir -p "$(dirname "$3")"
  # the anonymous read access BtbN's script uses (svn.xvid.org asks for a user name)
  svn export -q --non-interactive --ignore-keywords --native-eol LF --username anonymous --password '' \
    -r "$2" "$1@$2" "$3"
}

# entry name text: a licence text, or a pointer to the same text given earlier (seen, in notices)
entry() {
  local h
  h="$(printf '%s' "$2" | sha256sum | cut -d' ' -f1)"
  if [ -n "${seen[$h]:-}" ]; then
    printf -- '---- %s: the same text as %s\n' "$1" "${seen[$h]}"
  else
    seen[$h]="$1"
    printf -- '---- %s\n%s\n' "$1" "$2"
  fi
}

# notices root: the licence texts of every row of build/ffmpeg-sources.tsv, from its sources below root
notices() {
  local root="$1" path kind url ref opt builds lic f file range c b names n
  local -a files
  declare -A seen=()
  echo "The libraries linked into the bundled ffmpeg ($FFMPEG_VERSION, BtbN FFmpeg-Builds $FFMPEG_BUILD), and the"
  echo "licence texts and notices their sources carry, as build/fetch.sh ffmpeg-libraries reads them from the sources"
  echo "listed in build/ffmpeg-sources.tsv. The release asset ffmpeg-$FFMPEG_VERSION-libraries-source.tar.xz holds those sources."
  while IFS=$'\t' read -r path kind url ref opt builds lic; do
    case "$path" in '#'* | '') continue ;; esac
    [ "$kind" = file ] && continue
    printf '\n==== %s %s %s\n' "$path" "$url" "$ref"
    if [ "$kind" = crates ]; then
      for c in "$root/$path"/*.crate; do
        b="$(basename "$c" .crate)"
        names="$(tar -tzf "$c" | awk -F/ -v b="$b" '$1 == b && NF == 2 &&
          tolower($2) ~ /^(copying|copyright|licen[cs]e|notice|unlicense)([-._].*)?$/ { print $2 }' | LC_ALL=C sort)"
        if [ -z "$names" ]; then
          entry "$b (no licence file in the crate: its Cargo.toml)" "$(tar -xzOf "$c" "$b/Cargo.toml" |
            awk '/^license *=/ { print } /^authors *= *\[/ { a = 1 } a { print } a && /\]/ { a = 0 }')"
        else
          for n in $names; do entry "$b/$n" "$(tar -xzOf "$c" "$b/$n")"; done
        fi
      done
      continue
    fi
    IFS=, read -ra files <<<"$lic"
    for f in "${files[@]}"; do
      file="${f%@*}"
      [ -f "$root/$path/$file" ] || { echo "$path: its licence file $file is not in its source" >&2; exit 1; }
      if [ "$f" = "$file" ]; then
        entry "$f" "$(cat "$root/$path/$file")"
      else
        range="${f#*@}"
        entry "$f" "$(sed -n "${range%-*},${range#*-}p" "$root/$path/$file")"
      fi
    done
  done <"$here/ffmpeg-sources.tsv"
}

# fetch_libraries: every row of build/ffmpeg-sources.tsv, fetched below one folder, then packed into one archive.
# A row that is done is marked in .done/<path>.row with the fields it was fetched with, so a run that failed
# (a host down) goes on from there, and a row whose fields changed is fetched again, with the rows inside its
# folder. The archive has its stamp beside it (.sources) and is made again when the stamp changes.
fetch_libraries() {
  local out="ffmpeg-${FFMPEG_VERSION}-libraries-source"
  if [ -f "$cache/dl/$out.tar.xz" ] && [ "$(cat "$cache/dl/$out.tar.xz.sources" 2>/dev/null)" = "$(stamp)" ]; then
    return 0
  fi
  rm -f "$cache/dl/$out.tar.xz" "$cache/dl/$out.tar.xz.sources"
  local work="$cache/$out" path kind url ref opt builds lic d mark fields tree snap got x
  local -a opts
  mkdir -p "$work/$out" "$work/.done" "$cache/dl"
  while IFS=$'\t' read -r path kind url ref opt builds lic; do
    case "$path" in '#'* | '') continue ;; esac
    d="$work/$out/$path"
    mark="$work/.done/$path.row"
    fields="$(printf '%s\t' "$path" "$kind" "$url" "$ref" "$opt")"
    [ "$(cat "$mark" 2>/dev/null)" = "$fields" ] && continue
    echo "$kind $url $ref"
    # this row, and the rows inside its folder, are fetched again
    rm -rf "$d" "$mark" "${work:?}/.done/$path"
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
        # tree=<sha256> (treehash of the files at that revision), and maybe snapshot=<url>, by a comma
        tree="" snap=""
        IFS=, read -ra opts <<<"$opt"
        for x in "${opts[@]}"; do
          case "$x" in tree=*) tree="${x#tree=}" ;; snapshot=*) snap="${x#snapshot=}" ;; esac
        done
        [ -n "$tree" ] || { echo "build/ffmpeg-sources.tsv: $path has no tree=<sha256> (or tree=unset)" >&2; exit 1; }
        [ "$tree" != unset ] || echo "$path: its tree hash is not pinned in build/ffmpeg-sources.tsv yet: run make sources once on a machine with svn, which prints it, and pin it there as tree=<sha256>" >&2
        # only a snapshot that is not there falls back to svn; either way the files must hash to tree
        if [ -z "$snap" ] || ! snapshot "$snap" "$d"; then svn_export "$url" "$ref" "$d"; fi
        got="$(treehash "$d")"
        if [ "$tree" = unset ]; then
          echo "$path: the files at r$ref hash to $got: check them, then set tree=$got for it in build/ffmpeg-sources.tsv" >&2
          exit 1
        fi
        [ "$got" = "$tree" ] || { echo "$path: the files at r$ref hash to $got, not to the pinned $tree" >&2; exit 1; }
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
    mkdir -p "$(dirname "$mark")"
    printf '%s' "$fields" >"$mark"
  done <"$here/ffmpeg-sources.tsv"
  # the notices shipped with ffmpeg (build/ffmpeg-licenses.txt) must be the ones these sources carry
  LC_ALL=C notices "$work/$out" >"$work/$out/LICENSES.txt"
  if ! cmp -s "$work/$out/LICENSES.txt" "$here/ffmpeg-licenses.txt"; then
    cp "$work/$out/LICENSES.txt" "$cache/dl/ffmpeg-licenses.txt"
    echo "build/ffmpeg-licenses.txt is not what the sources carry: compare it with .cache/dl/ffmpeg-licenses.txt," >&2
    echo "copy that over build/ffmpeg-licenses.txt when it is right, commit it, and run this again" >&2
    exit 1
  fi
  cp "$here/ffmpeg-sources.tsv" "$work/$out/SOURCES.tsv"
  # the same files give the same bytes: sorted, fixed owner and time, no .git
  tar --create --format=gnu --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --exclude=.git \
    -C "$work" "$out" | xz -T0 -6 >"$cache/dl/$out.tar.xz.part"
  mv "$cache/dl/$out.tar.xz.part" "$cache/dl/$out.tar.xz"
  stamp >"$cache/dl/$out.tar.xz.sources"
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
  ffmpeg-linux) fetch_ffmpeg "$FFMPEG_URL_LINUX_X64" "$FFMPEG_SHA256_LINUX_X64" "" ;;
  ffmpeg-win) fetch_ffmpeg "$FFMPEG_URL_WIN_X64" "$FFMPEG_SHA256_WIN_X64" .exe ;;
  sources)
    fetch "$FFMPEG_SOURCE_URL" "FFmpeg-${FFMPEG_COMMIT}.tar.gz" "$FFMPEG_SOURCE_SHA256"
    fetch "$FFMPEG_BUILDS_SOURCE_URL" "FFmpeg-Builds-${FFMPEG_BUILDS_COMMIT}.tar.gz" "$FFMPEG_BUILDS_SOURCE_SHA256"
    ;;
  ffmpeg-libraries) fetch_libraries ;;
  *)
    echo "usage: $0 {go|node-linux|node-win|ffmpeg-linux|ffmpeg-win|sources|ffmpeg-libraries}" >&2
    exit 2
    ;;
esac
