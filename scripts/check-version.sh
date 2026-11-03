#!/bin/sh
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Checks that the docs name the newest released version: the first
# "## [X.Y.Z]" heading of CHANGELOG.md (not "Unreleased"), or VERSION if given.
# README.md (download URL and file name) and the "v=" line of docs/setup.md must
# all name it. The release workflow makes the same greps after a tag push; this
# finds a mismatch before.   scripts/check-version.sh [VERSION]
set -eu

cd "$(dirname "$0")/.."
want=${1:-}
if [ -z "$want" ]; then
	want=$(sed -n 's/^## \[\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)\].*/\1/p' CHANGELOG.md | head -n 1)
fi
[ -n "$want" ] || { echo "no released version in CHANGELOG.md" >&2; exit 1; }

bad=0
found=$(grep -o 'releases/download/v[0-9][0-9.]*/' README.md | sed 's#.*/v\(.*\)/#\1#') || true
[ -n "$found" ] || { echo "README.md: no download URL found" >&2; bad=1; }
for v in $found; do
	[ "$v" = "$want" ] || { echo "README.md: download URL names $v, expected $want" >&2; bad=1; }
done
found=$(grep -o 'little-watchdog_[0-9][0-9.]*_' README.md | sed 's/little-watchdog_\(.*\)_/\1/') || true
for v in $found; do
	[ "$v" = "$want" ] || { echo "README.md: file name names $v, expected $want" >&2; bad=1; }
done
grep -qx "v=$want" docs/setup.md || { echo "docs/setup.md: line 'v=' is not 'v=$want'" >&2; bad=1; }

[ "$bad" = 0 ] || exit 1
echo "docs name version $want"
