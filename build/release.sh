#!/usr/bin/env bash
# Usage: build/release.sh {check|pack|github} VERSION
#   check   VERSION is x.y.z[-pre], the tree has no uncommitted changes (ALLOW_DIRTY=1 skips that), and a
#           tag vVERSION, if there is one, is this commit. Offline.
#   pack    dist/release/vVERSION/ from dist/linux and dist/windows (`make release` builds those first):
#             Stagepack-Setup.exe, PRESENTER.txt                the Windows installer and its guide
#             stagepack-VERSION-linux-x64.tar.xz               dist/linux (program, node/, runtimes/) and LICENSE
#             SHA256SUMS                                       of the three
#             RELEASE_NOTES.md                                 the text of the GitHub release
#             npm/stagepack-VERSION.tgz                        the npm launcher (npm/), SHA256SUMS inside
#           Offline. The tarball is the same bytes for the same dist/linux and commit.
#   github  creates the GitHub release vVERSION from dist/release/vVERSION. The commit must be on origin.
#           Online; it publishes.
# PRERELEASE=1 (at pack) adds a pre-release note to the release text. It does not set GitHub's pre-release
# flag: releases/latest skips those, and install.sh and the README's download link go by releases/latest.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
repo=Eiriksb/stagepack
major=53 # runtime.Major(); a test checks the two agree

cmd="${1:-}"
ver="${2:-}"
[[ "$ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || {
	echo "usage: $0 {check|pack|github} VERSION (like 0.1.0, no v)" >&2
	exit 2
}
tag="v$ver"
out="$root/dist/release/$tag"
tarname="stagepack-$ver-linux-x64"
assets=(Stagepack-Setup.exe PRESENTER.txt "$tarname.tar.xz")

die() { echo "$*" >&2; exit 1; }

check() {
	cd "$root"
	if [ "${ALLOW_DIRTY:-}" != 1 ] && [ -n "$(git status --porcelain --untracked-files=no)" ]; then
		die "uncommitted changes: commit them first, or ALLOW_DIRTY=1 for a trial build"
	fi
	if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
		[ "$(git rev-parse "$tag^{commit}")" = "$(git rev-parse HEAD)" ] || die "tag $tag is not this commit"
	fi
}

# winstamped is true when the Windows program $1 has VERSION as its ProductVersion (the version resource,
# which `make winres` writes from the same VERSION as the -ldflags; -trimpath keeps those out of `go version -m`).
winstamped() {
	node -e '
		const b = require("fs").readFileSync(process.argv[1]);
		const key = Buffer.from("ProductVersion\0", "utf16le"), val = Buffer.from(process.argv[2] + "\0", "utf16le");
		for (let i = b.indexOf(key); i >= 0; i = b.indexOf(key, i + 1)) {
			const j = b.indexOf(val, i + key.length);
			if (j >= 0 && j <= i + key.length + 4) process.exit(0);
		}
		process.exit(1);
	' "$1" "$ver"
}

pack() {
	cd "$root"
	local lin="$root/dist/linux" win="$root/dist/windows"
	for f in "$lin/stagepack" "$lin/node/bin/node" "$lin/runtimes/$major/node_modules" "$win/Stagepack-Setup.exe" "$win/PRESENTER.txt"; do
		[ -e "$f" ] || die "missing $f: run 'make release VERSION=$ver'"
	done
	[ "$("$lin/stagepack" version)" = "$ver" ] || die "$lin/stagepack is not built as $ver: run 'make release VERSION=$ver'"
	winstamped "$win/Stagepack-Setup.exe" || die "$win/Stagepack-Setup.exe is not built as $ver: run 'make release VERSION=$ver'"

	rm -rf "$out"
	mkdir -p "$out/npm"
	cp "$win/Stagepack-Setup.exe" "$win/PRESENTER.txt" "$out/"

	# One top folder, files sorted, owner and times fixed (the commit's time): the same input gives the same
	# bytes. A talk being shown (runtimes/<major>/work) and the viewer's own state (logs, journal, ...) stay out.
	local when
	when="$(git log -1 --format=%ct)"
	tar --create --format=gnu --sort=name --mtime="@$when" --owner=0 --group=0 --numeric-owner \
		--anchored --no-wildcards-match-slash --exclude="runtimes/*/work" \
		--transform="s,^,$tarname/,S" \
		-C "$lin" stagepack node runtimes \
		-C "$root" LICENSE |
		xz -T0 -6 >"$out/$tarname.tar.xz.part"
	mv "$out/$tarname.tar.xz.part" "$out/$tarname.tar.xz"

	(cd "$out" && sha256sum "${assets[@]}" >SHA256SUMS)
	notes >"$out/RELEASE_NOTES.md"

	# The npm package: npm/ with this version and the checksums it checks downloads against.
	local stage="$out/npm/package"
	cp -r "$root/npm" "$stage"
	cp "$out/SHA256SUMS" "$root/LICENSE" "$stage/"
	node -e '
		const fs = require("fs"), p = process.argv[1] + "/package.json";
		const j = JSON.parse(fs.readFileSync(p, "utf8"));
		j.version = process.argv[2];
		fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n");
	' "$stage" "$ver"
	(cd "$stage" && npm pack --silent --pack-destination "$out/npm" >/dev/null)
	rm -rf "$stage"

	echo "wrote $out:"
	(cd "$out" && ls -l "${assets[@]}" SHA256SUMS RELEASE_NOTES.md npm/)
}

notes() {
	local size
	size="$(du -m "$out/$tarname.tar.xz" | cut -f1)"
	cat <<EOF
Stagepack $ver: pack a Slidev talk into one \`.sptx\` file that opens with a double-click, offline, in
Slidev's presenter view. Notes you edit are saved back into the file.

## Install

**Windows:** download \`Stagepack-Setup.exe\` and run it (no administrator rights needed). It is not code-signed
yet: on "Windows protected your PC", click **More info**, then **Run anyway**. \`PRESENTER.txt\` is the guide to
hand over with it.

**Linux (x86_64):**

\`\`\`bash
curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sh
\`\`\`

**npm (Linux or Windows):**

\`\`\`bash
npx stagepack install
\`\`\`

Or download \`$tarname.tar.xz\` ($size MB), check it against \`SHA256SUMS\`, unpack it and run
\`$tarname/stagepack install\`.

Everything installs for the current user only. See the README for details and how to uninstall.
EOF
	if [ "${PRERELEASE:-}" = 1 ]; then
		cat <<EOF

This is a pre-release: the Linux build is tested on Ubuntu with GNOME; the Windows installer has not yet
been run on a real Windows machine.
EOF
	fi
}

github() {
	cd "$root"
	[ -f "$out/SHA256SUMS" ] || die "missing $out: run 'make release VERSION=$ver' first"
	(cd "$out" && sha256sum --quiet -c SHA256SUMS) || die "$out does not match its SHA256SUMS"
	check
	local head
	head="$(git rev-parse HEAD)"
	git fetch -q origin
	[ -n "$(git branch -r --contains "$head")" ] || die "commit $head is not on origin: push it first"
	gh release create "$tag" --repo "$repo" --target "$head" --title "Stagepack $ver" \
		--notes-file "$out/RELEASE_NOTES.md" \
		"${assets[@]/#/$out/}" "$out/SHA256SUMS"
}

case "$cmd" in
check) check ;;
pack) pack ;;
github) github ;;
*) die "usage: $0 {check|pack|github} VERSION" ;;
esac
