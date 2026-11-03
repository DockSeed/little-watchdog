#!/bin/sh
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Prepares the files of a release, before anything is committed or tagged:
#   scripts/release-prep.sh [--dry-run] X.Y.Z
# - README.md (download URL, file name, build example) and docs/setup.md name
#   X.Y.Z instead of the current version
# - CHANGELOG.md: "## [Unreleased]" becomes "## [X.Y.Z] - DATE", a new empty
#   "## [Unreleased]" goes above it
# - then the local checks of the release workflow run (docs, changelog section,
#   gofmt, go vet, go test -race)
# X.Y.Z must be greater than the version the docs name now, and Unreleased must
# not be empty, so a second run with the same version fails and changes nothing.
# --dry-run prints the diff and writes nothing. DATE is today (RELEASE_DATE
# overrides it). Needs only sh, sed, awk, diff and the Go toolchain.
set -eu

dry=0
if [ "${1:-}" = "--dry-run" ]; then dry=1; shift; fi
new=${1:-}
[ $# -eq 1 ] || { echo "usage: release-prep.sh [--dry-run] X.Y.Z" >&2; exit 2; }
die() { echo "release-prep: $*" >&2; exit 1; }

case $new in
[0-9]*.[0-9]*.[0-9]*) ;;
*) die "'$new' is not X.Y.Z" ;;
esac
printf '%s' "$new" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
	die "'$new' is not X.Y.Z (numbers without leading zeros)"

cd "$(dirname "$0")/.."
date=${RELEASE_DATE:-$(date +%Y-%m-%d)}

cur=$(grep -o 'releases/download/v[0-9][0-9.]*/' README.md | head -n 1 | sed 's#.*/v\(.*\)/#\1#') || true
[ -n "$cur" ] || die "README.md names no release download URL"

# $new > $cur, numerically per field
awk -v a="$new" -v b="$cur" 'BEGIN {
	split(a, x, "."); split(b, y, ".")
	for (i = 1; i <= 3; i++) { if (x[i] + 0 > y[i] + 0) exit 0; if (x[i] + 0 < y[i] + 0) exit 1 }
	exit 1
}' || die "$new is not greater than the current version $cur"

grep -q "^## \[$new\]" CHANGELOG.md && die "CHANGELOG.md already has a section for $new"
[ "$(grep -c '^## \[Unreleased\]' CHANGELOG.md)" = 1 ] || die "CHANGELOG.md needs exactly one '## [Unreleased]' heading"
body=$(awk '/^## /{on=($0=="## [Unreleased]");next} on{print}' CHANGELOG.md)
[ -n "$(printf '%s' "$body" | tr -d '[:space:]')" ] || die "the Unreleased section is empty"

if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
	echo "release-prep: warning: the working tree has uncommitted changes" >&2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sed -e "s#releases/download/v$cur/#releases/download/v$new/#g" \
	-e "s#little-watchdog_${cur}_#little-watchdog_${new}_#g" -e "s#scripts/build.sh $cur #scripts/build.sh $new #" README.md >"$work/README.md"
sed -e "s#^v=$cur\$#v=$new#" docs/setup.md >"$work/setup.md"
awk -v v="$new" -v d="$date" '
	$0 == "## [Unreleased]" && !done { print; print ""; print "## [" v "] - " d; done = 1; next }
	{ print }
' CHANGELOG.md >"$work/CHANGELOG.md"

if [ "$dry" = 1 ]; then
	echo "release-prep: dry run, $cur -> $new, nothing written"
	diff -u README.md "$work/README.md" || true
	diff -u docs/setup.md "$work/setup.md" || true
	diff -u CHANGELOG.md "$work/CHANGELOG.md" || true
	exit 0
fi

# cat keeps modes and ownership of the existing files
cat "$work/README.md" >README.md
cat "$work/setup.md" >docs/setup.md
cat "$work/CHANGELOG.md" >CHANGELOG.md

echo "release-prep: files changed, $cur -> $new; running the release checks"
scripts/check-version.sh "$new"
scripts/release-notes.sh "$new" >/dev/null
test -z "$(gofmt -l .)" || die "gofmt: files need formatting"
go vet ./...
go test -race -count=1 ./...

cat <<NEXT

Release $new is prepared and the local checks passed. Next:
 1. Review the diff:  git diff
 2. Commit "release: $new"
 3. Push main and wait for green CI
 4. Push the tag v$new
 5. Wait for the draft release (workflow "release")
 6. Rebuild independently:  scripts/verify-release.sh
 7. Sign SHA256SUMS
 8. Upload SHA256SUMS.sig to the draft
 9. Publish the release
NEXT
