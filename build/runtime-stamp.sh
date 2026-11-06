#!/usr/bin/env bash
# Usage: build/runtime-stamp.sh                 prints the stamp of runtime/
#        build/runtime-stamp.sh check {linux|win32}   exits 0 when dist/<os>/runtimes/<major> carries that stamp
# The stamp is a sha256 over runtime/package.json, package-lock.json, pnpm-workspace.yaml and every file in
# runtime/vendor/ (sorted by name): what build/runtime.sh installs from. build/runtime.sh writes it into the
# dist runtime as .runtime-stamp; the Makefile and build/release.sh compare it, so a runtime built from an older
# runtime/ is rebuilt, or refused. Offline.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
major=53 # runtime.Major(); a test checks the two agree

stamp() {
	cd "$root/runtime"
	local files=(package.json package-lock.json pnpm-workspace.yaml)
	if [ -d vendor ]; then
		while IFS= read -r f; do files+=("$f"); done < <(find vendor -type f | LC_ALL=C sort)
	fi
	sha256sum "${files[@]}" | sha256sum | cut -d' ' -f1
}

case "${1:-}" in
"") stamp ;;
check)
	case "${2:-}" in
	linux) dir="$root/dist/linux/runtimes/$major" ;;
	win32) dir="$root/dist/windows/runtimes/$major" ;;
	*) echo "usage: $0 check {linux|win32}" >&2; exit 2 ;;
	esac
	[ -d "$dir/node_modules" ] && [ "$(cat "$dir/.runtime-stamp" 2>/dev/null)" = "$(stamp)" ]
	;;
*) echo "usage: $0 [check {linux|win32}]" >&2; exit 2 ;;
esac
