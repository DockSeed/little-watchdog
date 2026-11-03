#!/bin/sh
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Tests release-prep.sh and check-version.sh in a scratch copy of the docs (no Go
# checks, no git, nothing in the real tree is touched): happy path, a version
# that is not greater, a second run, --dry-run, and a mismatch in the docs.
#   scripts/test-release-prep.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# fresh copy: the scripts, the docs, and fake go/gofmt that always succeed
fresh() {
	rm -rf "$work/t"
	mkdir -p "$work/t/scripts" "$work/t/docs" "$work/bin"
	cp "$root"/scripts/check-version.sh "$root"/scripts/release-prep.sh "$root"/scripts/release-notes.sh "$work/t/scripts/"
	cp "$root/README.md" "$root/CHANGELOG.md" "$work/t/"
	cp "$root/docs/setup.md" "$work/t/docs/"
	# a known state: released 0.1.0, something unreleased
	cat >"$work/t/CHANGELOG.md" <<'CL'
# Changelog

## [Unreleased]

### Fixed

- Something.

## [0.1.0] - 2026-11-03

First release.
CL
	sed -i -e 's#releases/download/v[0-9.]*/#releases/download/v0.1.0/#g' -e 's#little-watchdog_[0-9][0-9.]*_#little-watchdog_0.1.0_#g' "$work/t/README.md"
	sed -i 's#^v=[0-9.]*$#v=0.1.0#' "$work/t/docs/setup.md"
	printf '#!/bin/sh\nexit 0\n' >"$work/bin/go"
	printf '#!/bin/sh\nexit 0\n' >"$work/bin/gofmt"
	chmod +x "$work/bin/go" "$work/bin/gofmt"
}
prep() { (cd "$work/t" && PATH="$work/bin:$PATH" RELEASE_DATE=2030-01-02 scripts/release-prep.sh "$@"); }
check() { (cd "$work/t" && scripts/check-version.sh "$@"); }
sum() { (cd "$work/t" && cat README.md CHANGELOG.md docs/setup.md | cksum); }

fresh
check >/dev/null || fail "check-version: clean copy should pass"

# happy path
prep 0.2.0 >/dev/null 2>&1 || fail "happy path: exit status"
grep -q 'releases/download/v0.2.0/' "$work/t/README.md" || fail "README url"
grep -q 'little-watchdog_0.2.0_' "$work/t/README.md" || fail "README file name"
grep -qx 'v=0.2.0' "$work/t/docs/setup.md" || fail "setup.md v="
grep -q '0\.1\.0' "$work/t/README.md" "$work/t/docs/setup.md" && fail "old version left in docs"
[ "$(grep -c '^## ' "$work/t/CHANGELOG.md")" = 3 ] || fail "changelog: expected 3 sections"
grep -qx '## \[0.2.0\] - 2030-01-02' "$work/t/CHANGELOG.md" || fail "changelog: dated section"
awk '/^## /{n++; if (n==1 && $0!="## [Unreleased]") exit 1; if (n==2 && $0!="## [0.2.0] - 2030-01-02") exit 1}' "$work/t/CHANGELOG.md" ||
	fail "changelog: order"
(cd "$work/t" && scripts/release-notes.sh 0.2.0 | grep -q Something) || fail "release-notes of the new section"
check >/dev/null || fail "check-version after prep"

# second run, same version: refused, nothing changes
before=$(sum)
prep 0.2.0 >/dev/null 2>&1 && fail "second run should fail"
[ "$(sum)" = "$before" ] || fail "second run changed files"
[ "$(grep -c '^## \[0.2.0\]' "$work/t/CHANGELOG.md")" = 1 ] || fail "duplicate section"
# empty Unreleased: refused
prep 0.3.0 >/dev/null 2>&1 && fail "empty Unreleased should fail"
[ "$(sum)" = "$before" ] || fail "empty Unreleased changed files"

# not greater / bad format
fresh
before=$(sum)
for v in 0.1.0 0.0.9 0.1 v0.2.0 01.2.0 0.2.0.1 ""; do
	prep $v >/dev/null 2>&1 && fail "version '$v' should fail"
done
prep 0.0.9 >/dev/null 2>&1 && fail "lower version should fail"
[ "$(sum)" = "$before" ] || fail "refused versions changed files"
prep 0.1.10 >/dev/null 2>&1 || fail "0.1.10 is greater than 0.1.0 (numeric compare)"

# dry run
fresh
before=$(sum)
out=$(prep --dry-run 0.2.0 2>&1) || fail "dry run: exit status"
[ "$(sum)" = "$before" ] || fail "dry run wrote files"
printf '%s' "$out" | grep -q '^+v=0.2.0' || fail "dry run shows no diff"

# drift
fresh
sed -i 's#^v=0.1.0$#v=0.0.9#' "$work/t/docs/setup.md"
check >/dev/null 2>&1 && fail "drift in setup.md not found"
fresh
sed -i '0,/releases\/download\/v0.1.0/s##releases/download/v0.0.9#' "$work/t/README.md"
check >/dev/null 2>&1 && fail "drift in README url not found"
fresh
sed -i '0,/little-watchdog_0.1.0_/s##little-watchdog_0.0.9_#' "$work/t/README.md"
check >/dev/null 2>&1 && fail "drift in README file name not found"
fresh
check 0.2.0 >/dev/null 2>&1 && fail "explicit version mismatch not found"

echo "ok"
