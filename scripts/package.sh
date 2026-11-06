#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Makes the .deb and the .tar.gz of each binary that scripts/build.sh left in
# OUTDIR. Only dpkg-deb, tar and gzip; fixed owners, modes and times (from
# SOURCE_DATE_EPOCH) so the files are reproducible.
#   SOURCE_DATE_EPOCH=... scripts/package.sh VERSION OUTDIR
set -euo pipefail

version=${1:?usage: package.sh VERSION OUTDIR}
out=$(cd "${2:?usage: package.sh VERSION OUTDIR}" && pwd)
: "${SOURCE_DATE_EPOCH:?set SOURCE_DATE_EPOCH}"
cd "$(dirname "$0")/.."
umask 022
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
date_rfc=$(date -u -R -d "@$SOURCE_DATE_EPOCH")
# the binary links the Go standard library (BSD-3-Clause): its licence goes along
golicense=$(go env GOROOT)/LICENSE
[ -f "$golicense" ] || { echo "package.sh: $golicense not found" >&2; exit 1; }

fix() { # tree: fixed modes and times
	find "$1" -type d -exec chmod 0755 {} +
	find "$1" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
}

deb() { # debarch binary-suffix
	local arch=$1 bin="$out/little-wd_${version}_linux_$2"
	local root="$work/deb-$arch" doc
	doc="$root/usr/share/doc/little-watchdog"
	mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/lib/systemd/system" "$root/usr/lib/sysusers.d" \
		"$root/usr/lib/tmpfiles.d" "$root/usr/share/man/man1" "$doc/examples"
	install -m 0755 "$bin" "$root/usr/bin/little-wd"
	install -m 0644 contrib/little-watchdog.service "$root/usr/lib/systemd/system/little-watchdog.service"
	install -m 0644 contrib/sysusers.d/little-wd.conf "$root/usr/lib/sysusers.d/little-wd.conf"
	install -m 0644 contrib/tmpfiles.d/little-wd.conf "$root/usr/lib/tmpfiles.d/little-wd.conf"
	install -m 0644 contrib/little-watchdog.env.example "$doc/examples/little-watchdog.env.example"
	gzip -9n <docs/little-wd.1 >"$root/usr/share/man/man1/little-wd.1.gz"
	chmod 0644 "$root/usr/share/man/man1/little-wd.1.gz"
	install -m 0644 README.md "$doc/README.md"
	install -m 0644 packaging/debian/copyright "$doc/copyright"
	install -m 0644 "$golicense" "$doc/LICENSE.go"
	{
		echo "little-watchdog ($version) unstable; urgency=medium"
		echo
		echo "  * Release $version. See CHANGELOG.md in the source:"
		echo "    https://github.com/DockSeed/little-watchdog/blob/main/CHANGELOG.md"
		echo
		echo " -- DockSeed <dockseed@proton.me>  $date_rfc"
	} | gzip -9n >"$doc/changelog.gz"
	chmod 0644 "$doc/changelog.gz"
	for s in postinst prerm postrm; do install -m 0755 "packaging/debian/$s" "$root/DEBIAN/$s"; done
	local kb
	kb=$(find "$root" -path "$root/DEBIAN" -prune -o -type f -printf '%s\n' | awk '{s+=$1} END {print int((s+1023)/1024)}')
	cat >"$root/DEBIAN/control" <<EOF
Package: little-watchdog
Version: $version
Architecture: $arch
Maintainer: DockSeed <dockseed@proton.me>
Installed-Size: $kb
Depends: systemd
Section: admin
Priority: optional
Homepage: https://github.com/DockSeed/little-watchdog
Description: homelab watchdog with Telegram alarms
 Checks web pages, TCP ports, hosts and the machine it runs on, and sends a
 Telegram message when something changes. Pings a dead-man's-switch so you
 notice when the watchdog itself stops. One static binary, no open port.
EOF
	fix "$root"
	dpkg-deb --root-owner-group -Zxz -b "$root" "$out/little-watchdog_${version}_${arch}.deb" >/dev/null
}

tarball() { # binary-suffix
	local name="little-watchdog_${version}_linux_$1" bin="$out/little-wd_${version}_linux_$1"
	local root="$work/tar-$1/$name"
	mkdir -p "$root"
	install -m 0755 "$bin" "$root/little-wd"
	install -m 0755 packaging/tarball/install.sh "$root/install.sh"
	install -m 0644 packaging/tarball/README.txt "$root/README.txt"
	install -m 0644 docs/little-wd.1 "$root/little-wd.1"
	install -m 0644 LICENSE "$root/LICENSE"
	install -m 0644 "$golicense" "$root/LICENSE.go"
	# the four files by name: a stray local file in contrib/ must never end up in the archive
	local f
	for f in little-watchdog.service little-watchdog.env.example sysusers.d/little-wd.conf tmpfiles.d/little-wd.conf; do
		install -D -m 0644 "contrib/$f" "$root/contrib/$f"
	done
	fix "$work/tar-$1"
	(cd "$work/tar-$1" && tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" -cf - "$name") |
		gzip -9n >"$out/$name.tar.gz"
}

deb amd64 amd64
deb arm64 arm64
deb armhf armv6
tarball amd64
tarball arm64
tarball armv6
