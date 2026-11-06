#!/bin/sh
# SPDX-FileCopyrightText: the little-watchdog contributors
# SPDX-License-Identifier: MIT
#
# Installs little-watchdog from the unpacked tar.gz on systems without a .deb:
# binary to /usr/local/bin, user, config directory, unit; then starts it.
#   sudo ./install.sh              install or update
#   sudo ./install.sh --uninstall  remove (keeps the config; add --purge to delete it)
#   ./install.sh --dry-run         print what would be done, change nothing
set -e

here=$(cd "$(dirname "$0")" && pwd)
bin=/usr/local/bin/little-wd
man=/usr/local/share/man/man1/little-wd.1
unit=/etc/systemd/system/little-watchdog.service
mode=install purge=0 dry=0
for a in "$@"; do
	case "$a" in
	--uninstall) mode=uninstall ;;
	--purge) purge=1 ;;
	--dry-run) dry=1 ;;
	-h | --help)
		sed -n '5,9p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown option: $a (try --help)" >&2
		exit 2
		;;
	esac
done

run() {
	if [ "$dry" = 1 ]; then echo "+ $*"; else "$@"; fi
}

if [ "$dry" = 0 ]; then
	[ "$(id -u)" = 0 ] || {
		echo "Run this as root: sudo ./install.sh" >&2
		exit 1
	}
	[ -d /run/systemd/system ] || {
		echo "This needs systemd running (no /run/systemd/system). See the README for a manual setup." >&2
		exit 1
	}
fi

if [ "$mode" = uninstall ]; then
	run systemctl disable --now little-watchdog.service || true
	run rm -f "$unit" "$bin" /etc/sysusers.d/little-wd.conf /etc/tmpfiles.d/little-wd.conf "$man"
	if [ "$purge" = 1 ]; then
		run rm -rf /etc/little-watchdog /var/lib/little-watchdog
	else
		echo "The config in /etc/little-watchdog stays (it holds your bot token). Delete it with: sudo ./install.sh --uninstall --purge"
	fi
	run systemctl daemon-reload
	exit 0
fi

if [ "$dry" = 1 ]; then
	echo "+ install -m 0755 $here/little-wd $bin"
	echo "+ write $unit (ExecStart=$bin run)"
	echo "+ install -m 0644 $here/little-wd.1 $man"
else
	install -D -m 0755 "$here/little-wd" "$bin"
	install -d -m 0755 /etc/systemd/system
	sed "s#^ExecStart=/usr/bin/little-wd#ExecStart=$bin#" "$here/contrib/little-watchdog.service" >"$unit.new"
	chmod 0644 "$unit.new"
	mv "$unit.new" "$unit"
	install -D -m 0644 "$here/little-wd.1" "$man"
fi
run install -D -m 0644 "$here/contrib/sysusers.d/little-wd.conf" /etc/sysusers.d/little-wd.conf
run systemd-sysusers /etc/sysusers.d/little-wd.conf
run install -D -m 0644 "$here/contrib/tmpfiles.d/little-wd.conf" /etc/tmpfiles.d/little-wd.conf
run systemd-tmpfiles --create /etc/tmpfiles.d/little-wd.conf
run systemctl daemon-reload
# not enabled yet (first run, or an earlier run stopped half way): enable and start;
# already enabled (an update): restart. Running the script again is always safe.
if systemctl is-enabled --quiet little-watchdog.service 2>/dev/null; then
	run systemctl try-restart little-watchdog.service
else
	run systemctl enable --now little-watchdog.service
fi

# Colour only on a terminal and without NO_COLOR (no-color.org).
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ]; then
	hd=$(printf '\033[1;32m') cm=$(printf '\033[1;36m') rs=$(printf '\033[0m')
else
	hd= cm= rs=
fi

# ask: yes only for y or yes typed on a terminal. No terminal (unattended upgrades, a pipe),
# DEBIAN_FRONTEND=noninteractive, Enter or anything else is no.
ask() {
	[ -t 0 ] && [ -t 1 ] && [ "${DEBIAN_FRONTEND:-}" != noninteractive ] || return 1
	printf '%s [y/N] ' "$1"
	read -r answer || return 1
	case $answer in y | Y | yes | Yes | YES) return 0 ;; esac
	return 1
}

# The group can read the Telegram token and change what is watched, so a user joins it only
# when someone at the terminal says so. Otherwise the command is shown.
user=${SUDO_USER:-}
[ "$user" != root ] || user=
if [ -n "$user" ] && ! getent passwd "$user" >/dev/null 2>&1; then
	user=
fi
if [ -n "$user" ] && id -nG "$user" | tr ' ' '\n' | grep -qx little-wd; then
	: # already in the group
elif [ -n "$user" ] && [ "$dry" = 0 ] && ask "Add $user to the group little-wd (reads the Telegram token, edits the checks)?"; then
	run usermod -aG little-wd "$user"
	printf '\n%s==> little-watchdog%s\n' "$hd" "$rs"
	printf '    Added %s to the group little-wd.\n' "$user"
	printf '    Log out and in again, then run:  %slittle-wd%s\n\n' "$cm" "$rs"
else
	printf '\n%s==> little-watchdog%s\n' "$hd" "$rs"
	printf '    To set it up, add your user to the group:\n'
	printf '        %ssudo usermod -aG little-wd %s%s\n' "$cm" "${user:-YOUR_USER}" "$rs"
	printf '    Then log out and in again and run:  %slittle-wd%s\n\n' "$cm" "$rs"
fi
