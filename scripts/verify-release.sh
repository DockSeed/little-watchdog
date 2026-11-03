#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Independent rebuild of a release: builds the checked-out tag with
# scripts/build.sh and compares every sha256 with the SHA256SUMS you downloaded.
# Run it on a second machine, with the tag checked out and the same Go version
# as the release (go.mod / release.yml), before signing:
#   git checkout vX.Y.Z && scripts/verify-release.sh DIR_WITH_DOWNLOADED_SHA256SUMS
set -euo pipefail

dir=${1:?usage: verify-release.sh DIR (with the downloaded SHA256SUMS)}
cd "$(dirname "$0")/.."
sums=$dir/SHA256SUMS
[ -f "$sums" ] || { echo "no $sums" >&2; exit 2; }
version=$(sed -n 's/^[0-9a-f]* \+little-wd_\(.*\)_linux_amd64$/\1/p' "$sums" | head -n1)
[ -n "$version" ] || { echo "cannot read the version from $sums" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
echo "rebuilding $version with $(go version)"
scripts/build.sh "$version" "$work" >/dev/null

if diff <(sort "$sums") <(sort "$work/SHA256SUMS") >"$work/diff"; then
	echo "OK: all $(wc -l <"$sums") files are bit-for-bit identical to the rebuild"
else
	echo "MISMATCH (< downloaded, > rebuilt):" >&2
	cat "$work/diff" >&2
	exit 1
fi
