#!/bin/sh
# Installs Stagepack for the current user on Linux (x86_64), without sudo:
#
#   curl -fsSL https://raw.githubusercontent.com/Eiriksb/stagepack/main/install.sh | sh
#
# It downloads the Linux build from the GitHub release (about 120 MB), checks it against the release's
# SHA256SUMS, unpacks it in a temporary folder under ~/.cache and runs `stagepack install --yes` from there,
# which copies it to ~/.local/share/stagepack and registers .sptx files. Arguments are passed on to
# `stagepack install` (`sh -s -- --home DIR`).
#
#   STAGEPACK_VERSION=0.1.0   a given release instead of the latest
#   STAGEPACK_DOWNLOAD_URL    a folder that holds SHA256SUMS and the tarball, instead of GitHub
set -eu

main() {
	repo=Eiriksb/stagepack

	case "$(uname -s)/$(uname -m)" in
	Linux/x86_64 | Linux/amd64) ;;
	*)
		err "this script is for Linux on x86_64, not $(uname -s)/$(uname -m)."
		err "On Windows, download Stagepack-Setup.exe from https://github.com/$repo/releases/latest"
		exit 1
		;;
	esac

	if [ -n "${STAGEPACK_DOWNLOAD_URL:-}" ]; then
		base=${STAGEPACK_DOWNLOAD_URL%/}
	elif [ -n "${STAGEPACK_VERSION:-}" ]; then
		base=https://github.com/$repo/releases/download/v${STAGEPACK_VERSION#v}
	else
		base=https://github.com/$repo/releases/latest/download
	fi

	for c in tar xz; do
		have "$c" || { err "$c is needed: install it with your package manager (xz is in xz-utils or xz)."; exit 1; }
	done
	if have sha256sum; then
		sha() { sha256sum "$1" | cut -d' ' -f1; }
	elif have shasum; then
		sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else
		err "sha256sum (coreutils) or shasum is needed to check the download."
		exit 1
	fi
	if have curl; then
		get() { curl -fL --proto-redir =https --retry 3 -o "$2" "$1"; }
	elif have wget; then
		get() { wget -O "$2" "$1"; }
	else
		err "curl or wget is needed."
		exit 1
	fi

	# Not /tmp: that is often a small tmpfs, and the unpacked build is about 800 MB.
	cache=${XDG_CACHE_HOME:-$HOME/.cache}
	mkdir -p "$cache"
	tmp=$(mktemp -d "$cache/stagepack-install.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	say "Downloading the checksums from $base"
	get "$base/SHA256SUMS" "$tmp/SHA256SUMS"
	line=$(grep -E '^[0-9a-f]{64}  stagepack-[0-9A-Za-z.-]+-linux-x64\.tar\.xz$' "$tmp/SHA256SUMS" | head -n 1) || true
	[ -n "$line" ] || { err "no Linux build listed in $base/SHA256SUMS"; exit 1; }
	want=${line%% *}
	name=${line##* }

	say "Downloading $name"
	get "$base/$name" "$tmp/$name"
	got=$(sha "$tmp/$name")
	if [ "$got" != "$want" ]; then
		err "$name does not match its checksum (got $got, want $want). Nothing was installed."
		exit 1
	fi
	say "Checksum OK. Unpacking"
	mkdir "$tmp/x"
	tar -xJf "$tmp/$name" -C "$tmp/x" --no-same-owner
	dir=$tmp/x/${name%.tar.xz}
	[ -x "$dir/stagepack" ] || { err "$name holds no $dir/stagepack"; exit 1; }

	"$dir/stagepack" install --yes "$@"
}

have() { command -v "$1" >/dev/null 2>&1; }
say() { printf 'stagepack: %s\n' "$1"; }
err() { printf 'stagepack: %s\n' "$1" >&2; }

# All of it is in main, so a download cut short runs nothing.
main "$@"
