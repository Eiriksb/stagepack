#!/usr/bin/env bash
# Usage: build/programs.sh dist/<linux|windows>
# The programs a build ships beside the viewer (Node, the headless browser, ffmpeg), which the viewer runs as
# separate processes: checks the build against build/programs.tsv and adds their licence texts to the build's
# runtimes/<major>/THIRD_PARTY_LICENSES.txt, in a section of its own (written again on each run).
# Fails when a folder of the build is not in the table (a new program: check its licence and add it), when a
# program in the table is not in the build, when its licence file is missing or empty, or when it names no
# source archive. Offline. Needs the build's node/, runtimes/ (with THIRD_PARTY_LICENSES.txt) and programs.
# Policy (README "Licences"): the viewer and what it links stay permissive; these separate programs may be
# GPL or LGPL, shipped unmodified, each with its licence text here and its exact source in the release assets.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
table="$here/programs.tsv"
releases=https://github.com/Eiriksb/stagepack/releases
# shellcheck source=versions.env
. "$here/versions.env"

[ $# -eq 1 ] && [ -d "$1" ] || { echo "usage: $0 dist/<linux|windows>" >&2; exit 2; }
dist="${1%/}"

# The folders of a build that are not programs: the npm side (build/licenses.mjs checks it) and what the
# viewer writes when it runs from the build folder (logs, the browser profile, ...).
not_programs=" runtimes browser instances logs journal "

failed=0
fail() { echo "FAIL: $*" >&2; failed=1; }

# expand replaces each ${NAME} with its value from build/versions.env
expand() {
	local s="$1" name
	while [[ "$s" =~ \$\{([A-Za-z_][A-Za-z0-9_]*)\} ]]; do
		name="${BASH_REMATCH[1]}"
		[ -n "${!name+x}" ] || { echo "build/programs.tsv: \${$name} is not in build/versions.env" >&2; exit 1; }
		s="${s//"\${$name}"/${!name}}"
	done
	printf '%s' "$s"
}

names=() versions=() licences=() folders=() files=() sources=()
while IFS=$'\t' read -r name version licence folder file src; do
	case "$name" in '#'* | '') continue ;; esac
	names+=("$name")
	versions+=("$(expand "$version")")
	licences+=("$licence")
	folders+=("$folder")
	files+=("$file")
	sources+=("$(expand "${src:-}")")
done <"$table"

# every folder of the build is a listed program (or not a program)
for d in "$dist"/*/; do
	d="$(basename "$d")"
	case "$not_programs" in *" $d "*) continue ;; esac
	listed=0
	for f in "${folders[@]}"; do [ "$f" = "$d" ] && listed=1; done
	[ "$listed" = 1 ] || fail "$dist/$d/ is not in build/programs.tsv: check its licence and add it"
done

printf '%-46s %-22s %s\n' program version "licence file"
for i in "${!names[@]}"; do
	printf '%-46s %-22s %s\n' "${names[$i]}" "${versions[$i]%%,*}" "${files[$i]}"
	[ -d "$dist/${folders[$i]}" ] || { fail "${names[$i]} is not in $dist/${folders[$i]}/"; continue; }
	[ -s "$dist/${files[$i]}" ] || fail "${names[$i]}: no licence file $dist/${files[$i]}"
	[ -n "${sources[$i]// /}" ] || fail "${names[$i]}: build/programs.tsv names no source archive"
done
[ "$failed" = 0 ] || exit 1

shopt -s nullglob
tp=("$dist"/runtimes/*/THIRD_PARTY_LICENSES.txt)
[ "${#tp[@]}" = 1 ] || { echo "FAIL: expected one $dist/runtimes/<major>/THIRD_PARTY_LICENSES.txt, found ${#tp[@]}: run 'make runtime'" >&2; exit 1; }
tp="${tp[0]}"

marker="==== Programs shipped beside the viewer (separate processes, unmodified) ===="
# the file without an earlier copy of this section, and without the blank lines before it
awk -v m="$marker" '$0 == m { exit } { line[++n] = $0 } END { while (n > 0 && line[n] == "") n--; for (i = 1; i <= n; i++) print line[i] }' "$tp" >"$tp.part"
{
	printf '\n\n%s\n\n' "$marker"
	echo "Stagepack runs these programs as their own processes; it does not link them. They are shipped exactly as"
	echo "published, unmodified. Each GitHub release of Stagepack carries, as release assets, a copy of the source"
	echo "archives named below, downloaded from the URLs given: $releases"
	for i in "${!names[@]}"; do
		printf '\n%s %s\n' "${names[$i]}" "${versions[$i]}"
		printf 'Licence: %s\n' "${licences[$i]}"
		printf 'Installed in: %s/\n' "${folders[$i]}"
		for s in ${sources[$i]}; do
			case "$s" in
				*=*://*) printf 'Source: %s (release asset %s)\n' "${s#*=}" "${s%%=*}" ;;
				*://*) printf 'Source: %s (release asset %s)\n' "$s" "${s##*/}" ;;
				*) printf 'Source: release asset %s (made by the release from the sources in its SOURCES.tsv)\n' "$s" ;;
			esac
		done
		printf '%s\n' "------------------------------------------------------------"
		cat "$dist/${files[$i]}"
		echo
	done
} >>"$tp.part"
mv "$tp.part" "$tp"
echo "ok: ${#names[@]} programs, each listed with its licence text and source: $tp"
