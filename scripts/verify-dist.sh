#!/bin/sh
# Checks a goreleaser dist/ directory the way an installer sees it (docs/runbooks/release.md):
# one archive per platform with the names mise's github: backend matches, an SBOM per archive,
# checksums.txt covering both, the binary at the top level of each archive, and (on linux/amd64)
# the binary reporting the expected version.
#
# Usage: scripts/verify-dist.sh <dist-dir> [<version>]
# The signature over checksums.txt is checked separately with cosign (release workflow).
set -eu

dist=${1:?usage: verify-dist.sh <dist-dir> [<version>]}
want=${2:-}
cd "$dist"

fail() {
	echo "verify-dist: $*" >&2
	exit 1
}

[ -f checksums.txt ] || fail "checksums.txt is missing"

if command -v sha256sum >/dev/null 2>&1; then
	sha256sum -c --quiet checksums.txt || fail "checksums.txt does not match the files"
else
	shasum -a 256 -c --quiet checksums.txt || fail "checksums.txt does not match the files"
fi

for os in linux darwin; do
	for arch in amd64 arm64; do
		set -- lucy_*_"${os}_${arch}".tar.gz
		[ "$#" -eq 1 ] && [ -f "$1" ] || fail "want exactly one lucy_<version>_${os}_${arch}.tar.gz, found: $*"
		archive=$1
		case "$archive" in
		lucy_[0-9]*) ;;
		*) fail "$archive: the version after lucy_ must start with a digit (no leading v)" ;;
		esac
		[ -f "$archive.spdx.json" ] || fail "$archive has no SBOM ($archive.spdx.json)"
		grep -q "  $archive\$" checksums.txt || fail "$archive is not in checksums.txt"
		grep -q "  $archive.spdx.json\$" checksums.txt || fail "$archive.spdx.json is not in checksums.txt"
		tar -tzf "$archive" | grep -qx lucy || fail "$archive has no lucy binary at its top level"
	done
done

# No other archive may match a platform, or mise could pick the wrong asset.
count=$(find . -maxdepth 1 -name 'lucy_*.tar.gz' | wc -l | tr -d ' ')
[ "$count" -eq 4 ] || fail "want 4 archives, found $count"

if [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ]; then
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	tar -xzf lucy_*_linux_amd64.tar.gz -C "$tmp" lucy
	got=$("$tmp/lucy" --version)
	case "$got" in
	"lucy $want "* | "lucy $want") ;;
	*) [ -z "$want" ] || fail "lucy --version printed \"$got\", want version $want" ;;
	esac
	echo "verify-dist: $got"
fi

echo "verify-dist: 4 archives, 4 SBOMs and checksums.txt check out"
