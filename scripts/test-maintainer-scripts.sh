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
# a link planted at the marker name is not followed by prerm or postinst, and is removed
ln -s "$work/victim" "$work/etc/.enable-on-install"
step "remove with a planted link" prerm remove
[ ! -e "$work/victim" ] || fail "prerm followed a link at the marker"
[ -d "$work/etc/.enable-on-install" ] || fail "prerm left no marker directory"
rmdir "$work/etc/.enable-on-install"; ln -s "$work/victim" "$work/etc/.enable-on-install"
step "reinstall with a planted link" postinst configure 0.1.0
[ ! -e "$work/victim" ] && [ ! -L "$work/etc/.enable-on-install" ] || fail "postinst followed or kept a link at the marker"
# the marker of older packages was a plain file: it still counts
rm -f "$work/enabled"; touch "$work/etc/.enable-on-install"
step "reinstall with an old file marker" postinst configure 0.1.0
enabled && [ ! -e "$work/etc/.enable-on-install" ] || fail "old file marker not honoured or not removed"
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
# the group can read the Telegram token: the user who ran sudo joins only on a typed yes at a
# terminal, never without one; then the command is shown instead
me=$(id -un)
printf '#!/bin/sh\necho users\n' >"$work/bin/id" && chmod +x "$work/bin/id" # not in little-wd yet
: >"$work/log"
out=$(SUDO_USER=$me PATH="$work/bin:$PATH" sh "$work/postinst" configure </dev/null 2>&1) || fail "group, no terminal: exit status"
hasnt "group, no terminal" usermod
printf '%s' "$out" | grep -q "usermod -aG little-wd $me" || fail "group, no terminal: command not shown"
# answer ARG CMD...: runs CMD on a pseudo terminal, types ARG once it asks [y/N]
answer() {
	python3 - "$@" <<'PY'
import os, pty, sys
ans, cmd = sys.argv[1], sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
buf, sent = b"", False
while True:
    try:
        chunk = os.read(fd, 1024)
    except OSError:
        break
    if not chunk:
        break
    buf += chunk
    if not sent and b"[y/N]" in buf:
        os.write(fd, ans.encode() + b"\n")
        sent = True
sys.exit(os.waitstatus_to_exitcode(os.waitpid(pid, 0)[1]))
PY
}
for a in n "" y; do
	: >"$work/log"
	(unset DEBIAN_FRONTEND; SUDO_USER=$me PATH="$work/bin:$PATH" answer "$a" sh "$work/postinst" configure >/dev/null) || fail "group, answer '$a': exit status"
	if [ "$a" = y ]; then has "group, yes" "usermod -aG little-wd $me"; else hasnt "group, answer '$a'" usermod; fi
done
: >"$work/log"
(unset DEBIAN_FRONTEND; SUDO_USER=$me PATH="$work/bin:$PATH" answer y sh "$work/postinst" configure 0.4.0 >/dev/null) || fail "group, upgrade: exit status"
hasnt "group, upgrade asks nothing" usermod
: >"$work/log" # a terminal, but apt told not to ask
DEBIAN_FRONTEND=noninteractive SUDO_USER=$me PATH="$work/bin:$PATH" answer y sh "$work/postinst" configure >/dev/null || fail "group, noninteractive: exit status"
hasnt "group, noninteractive" usermod
rm "$work/bin/id"
rmdir "$work/run" # no systemd running (container, chroot): nothing may call systemctl, nothing may fail
mkdir -p "$work/etc"
step "no systemd" postinst configure
hasnt "no systemd" systemctl
echo "maintainer scripts ok"
