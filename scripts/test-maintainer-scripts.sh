#!/usr/bin/env bash
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Runs the .deb maintainer scripts through the package life cycle against fake
# systemctl/systemd-sysusers/usermod commands and a scratch /etc, no root needed:
# new install, upgrade, remove, reinstall after remove (must turn the service on
# again), a service the admin disabled by hand (must stay off), purge.
#   scripts/test-maintainer-scripts.sh
set -euo pipefail

cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/etc" "$work/var"
fail() { echo "FAIL: $*" >&2; exit 1; }

for c in systemd-sysusers systemd-tmpfiles usermod; do
	printf '#!/bin/sh\necho "%s $*" >>"%s/log"\n' "$c" "$work" >"$work/bin/$c"
done
cat >"$work/bin/systemctl" <<EOF
#!/bin/sh
echo "systemctl \$*" >>"$work/log"
case "\$1" in
is-enabled) [ -e "$work/enabled" ]; exit \$? ;;
enable) touch "$work/enabled" ;;
disable) rm -f "$work/enabled" ;;
esac
exit 0
EOF
chmod +x "$work"/bin/*

# the scripts with their fixed paths pointed into the scratch directory; a
# running systemd is faked by pointing /run/systemd/system at a directory
mkdir -p "$work/run"
for s in postinst prerm postrm; do
	sed -e "s#/etc/little-watchdog#$work/etc#g;s#/var/lib/little-watchdog#$work/var#g;s#/run/systemd/system#$work/run#g" \
		"packaging/debian/$s" >"$work/$s"
done

step() { # name script args... ; runs with SUDO_USER unset
	: >"$work/log"
	env -u SUDO_USER PATH="$work/bin:$PATH" sh "$work/$2" "${@:3}" >/dev/null || fail "$1: exit status"
}
has() { grep -q -- "$2" "$work/log" || fail "$1: expected '$2' in: $(tr '\n' ';' <"$work/log")"; }
hasnt() { ! grep -q -- "$2" "$work/log" || fail "$1: unexpected '$2'"; }
enabled() { [ -e "$work/enabled" ]; }

step "new install" postinst configure
has "new install" "enable --now"; enabled || fail "new install: not enabled"
step "upgrade" postinst configure 0.0.9
has "upgrade" "try-restart"; hasnt "upgrade" "enable"
step "upgrade prerm" prerm upgrade 0.2
hasnt "upgrade prerm" disable
step "remove" prerm remove
has "remove" "disable --now"; ! enabled || fail "remove: still enabled"
step "reinstall after remove" postinst configure 0.1.0
has "reinstall after remove" "enable --now"; enabled || fail "reinstall: service stays off"
[ ! -e "$work/etc/.enable-on-install" ] || fail "marker not cleaned up"
rm -f "$work/enabled" # the admin disables it by hand
step "upgrade of a disabled service" postinst configure 0.1.1
hasnt "disabled service" "enable"; ! enabled || fail "a service disabled by hand was turned on"
step "remove of a disabled service" prerm remove
step "reinstall of a disabled service" postinst configure 0.1.1
! enabled || fail "reinstall turned on a service the admin had disabled"
touch "$work/etc/config" "$work/var/state"
step "remove keeps data" postrm remove
[ -e "$work/etc/config" ] && [ -e "$work/var/state" ] || fail "remove deleted data"
step "purge" postrm purge
[ ! -e "$work/etc" ] && [ ! -e "$work/var" ] || fail "purge left data"
rmdir "$work/run" # no systemd running (container, chroot): nothing may call systemctl, nothing may fail
mkdir -p "$work/etc"
step "no systemd" postinst configure
hasnt "no systemd" systemctl
echo "maintainer scripts ok"
