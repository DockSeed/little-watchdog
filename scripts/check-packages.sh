#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Checks what scripts/build.sh made in OUTDIR: control fields, file modes and
# owners inside each .deb, syntax of the maintainer scripts and install.sh, and
# SHA256SUMS. Needs only dpkg-deb, tar and sh.   scripts/check-packages.sh [OUTDIR]
set -euo pipefail

out=$(cd "${1:-dist}" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

want() { # listing path "mode owner"
	local line
	line=$(grep -F " ./$2" <<<"$1" | head -n1) || fail "$2 missing"
	[[ "$line" == "$3"* ]] || fail "$2: '${line%% *}', want '$3'"
}

for deb in "$out"/little-watchdog_*.deb; do
	echo "== $(basename "$deb")"
	dpkg-deb -I "$deb" | sed -n '1,20p'
	dpkg-deb -f "$deb" Package Maintainer Depends Homepage Architecture
	[ "$(dpkg-deb -f "$deb" Package)" = little-watchdog ] || fail "package name"
	[ "$(dpkg-deb -f "$deb" Maintainer)" = "DockSeed <dockseed@proton.me>" ] || fail "maintainer"
	[ "$(dpkg-deb -f "$deb" Depends)" = systemd ] || fail "depends"
	list=$(dpkg-deb -c "$deb")
	want "$list" usr/bin/little-wd "-rwxr-xr-x root/root"
	want "$list" usr/lib/systemd/system/little-watchdog.service "-rw-r--r-- root/root"
	want "$list" usr/lib/sysusers.d/little-wd.conf "-rw-r--r-- root/root"
	want "$list" usr/lib/tmpfiles.d/little-wd.conf "-rw-r--r-- root/root"
	want "$list" usr/share/man/man1/little-wd.1.gz "-rw-r--r-- root/root"
	want "$list" usr/share/doc/little-watchdog/copyright "-rw-r--r-- root/root"
	want "$list" usr/share/doc/little-watchdog/changelog.gz "-rw-r--r-- root/root"
	want "$list" usr/share/doc/little-watchdog/LICENSE.go "-rw-r--r-- root/root"
	if grep -qvE '^[-dl][rwx-]{9} root/root ' <<<"$list"; then fail "a file is not owned by root"; fi
	dpkg-deb -e "$deb" "$work/ctl"
	for s in postinst prerm postrm; do
		sh -n "$work/ctl/$s" || fail "$s syntax"
		[ -x "$work/ctl/$s" ] || fail "$s not executable"
	done
	grep -q '^ExecStart=/usr/bin/little-wd run' < <(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/lib/systemd/system/little-watchdog.service) ||
		fail "unit ExecStart path"
	rm -rf "$work/ctl"
done

for t in "$out"/little-watchdog_*.tar.gz; do
	echo "== $(basename "$t")"
	list=$(tar tvzf "$t")
	grep -q -- '-rwxr-xr-x 0/0 .*/little-wd$' <<<"$list" || fail "binary mode in $t"
	grep -q -- '-rwxr-xr-x 0/0 .*/install.sh$' <<<"$list" || fail "install.sh mode in $t"
	grep -q -- '-rw-r--r-- 0/0 .*/LICENSE.go$' <<<"$list" || fail "LICENSE.go missing in $t"
	grep -q -- '-rw-r--r-- 0/0 .*/little-wd.1$' <<<"$list" || fail "little-wd.1 missing in $t"
	tar xzf "$t" -C "$work"
	d=$work/$(basename "$t" .tar.gz)
	sh -n "$d/install.sh" || fail "install.sh syntax"
	"$d/install.sh" --help >/dev/null
	"$d/install.sh" --dry-run >/dev/null
	"$d/install.sh" --uninstall --dry-run >/dev/null
done

(cd "$out" && sha256sum -c SHA256SUMS)
echo "packages ok"
