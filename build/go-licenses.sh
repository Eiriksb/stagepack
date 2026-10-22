#!/usr/bin/env bash
# Usage: build/go-licenses.sh
# The licences of the Go modules that end up in the programs: builds the Linux and the Windows viewer to
# temporary files, reads their module lists with `go version -m`, and checks each module against
# build/go-licenses.tsv. Fails on a module that is not in the table (a new dependency: review its licence and
# add it), on a different version than the one reviewed, or on a licence that is not on the permissive list.
# Build tags don't change which modules are linked, so one build per OS is enough. Offline once the modules
# are in the module cache.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
go="${GO:-$root/.cache/go/bin/go}"
table="$here/go-licenses.tsv"
ALLOWED=" MIT ISC BSD-2-Clause BSD-3-Clause Apache-2.0 0BSD Unlicense CC0-1.0 BlueOak-1.0.0 "

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$root"
for os in linux windows; do
  GOOS=$os GOARCH=amd64 "$go" build -o "$tmp/$os" ./cmd/stagepack
  "$go" version -m "$tmp/$os" > "$tmp/$os.info"
  # the program's own "mod" line must be there, or the output isn't what this script reads
  awk -F'\t' '$2 == "mod" { found = 1 } END { exit !found }' "$tmp/$os.info" ||
    { echo "FAIL: 'go version -m' of the $os program has no mod line: cannot read its modules" >&2; exit 1; }
  awk -F'\t' '$2 == "dep" { print $3 "\t" $4 }' "$tmp/$os.info" > "$tmp/$os.mods"
done
# The Windows program links the tray (systray, x/sys): no modules there means the listing was not read
# properly, and an empty list would pass every check below. The Linux program may have none.
if [ ! -s "$tmp/windows.mods" ]; then
  echo "FAIL: 'go version -m' lists no modules for the Windows program; expected at least one" >&2
  exit 1
fi

printf '%-32s %-9s %-14s %s\n' module version licence "linked in"
cat "$tmp/linux.mods" "$tmp/windows.mods" | sort -u | while IFS=$'\t' read -r mod ver; do
  row="$(awk -F'\t' -v m="$mod" '$0 !~ /^#/ && $1 == m' "$table")"
  where=""
  grep -qF "$mod	" "$tmp/linux.mods" && where="linux"
  grep -qF "$mod	" "$tmp/windows.mods" && where="${where:+$where, }windows"
  if [ -z "$row" ]; then
    printf '%-32s %-9s %-14s %s\n' "$mod" "$ver" "NOT IN TABLE" "$where"
    echo "FAIL: $mod $ver is not in build/go-licenses.tsv: check its licence and add it" >&2
    echo fail >> "$tmp/failed"
    continue
  fi
  tver="$(cut -f2 <<<"$row")"; lic="$(cut -f3 <<<"$row")"
  printf '%-32s %-9s %-14s %s\n' "$mod" "$ver" "$lic" "$where"
  if [ "$tver" != "$ver" ]; then
    echo "FAIL: $mod is $ver, the table has $tver: re-check the licence and update the table" >&2
    echo fail >> "$tmp/failed"
  fi
  case "$ALLOWED" in *" $lic "*) ;; *)
    echo "FAIL: $mod is $lic, which is not on the permissive list" >&2
    echo fail >> "$tmp/failed" ;;
  esac
done
if [ -e "$tmp/failed" ]; then exit 1; fi
echo "ok: every linked Go module is on the table and permissively licensed"
