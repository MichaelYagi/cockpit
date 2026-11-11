#!/bin/sh
# Remove the Cockpit system service installed by deploy/install.sh.
#
#   sudo ./deploy/uninstall.sh [--prefix DIR] [--purge]
#
# Stops and removes the units and the binary. Data (/var/lib/cockpit-monitor),
# configuration (/etc/cockpit-monitor) and the cockpit-monitor user are kept
# unless --purge is given. --purge deletes the database, including all API
# key hashes and history; it cannot be undone.
#
# DESTDIR=/some/dir removes files from a staged tree and skips every
# system-changing step; used by the tests.
set -eu

prefix=/usr/local
purge=no

while [ $# -gt 0 ]; do
	case $1 in
	--prefix) prefix=${2:?--prefix needs a directory}; shift 2 ;;
	--purge) purge=yes; shift ;;
	-h | --help) sed -n '2,12s/^# \{0,1\}//p' "$0"; exit 0 ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

dest=${DESTDIR:-}
if [ -z "$dest" ] && [ "$(id -u)" -ne 0 ]; then
	echo "uninstall.sh must run as root (or set DESTDIR)." >&2
	exit 1
fi

run() {
	if [ -n "$dest" ]; then
		echo "skipped (DESTDIR): $*"
	else
		"$@" || true
	fi
}

run systemctl disable --now cockpit-smart.timer
run systemctl stop cockpit-smart.service
run systemctl disable --now cockpit-agent.service

rm -f "$dest/etc/systemd/system/cockpit-agent.service" \
	"$dest/etc/systemd/system/cockpit-smart.service" \
	"$dest/etc/systemd/system/cockpit-smart.timer" \
	"$dest$prefix/bin/cockpit"
run systemctl daemon-reload

if [ "$purge" = yes ]; then
	echo "Purging data, configuration and the cockpit-monitor user"
	rm -rf "$dest/var/lib/cockpit-monitor" "$dest/etc/cockpit-monitor" "$dest/run/cockpit-monitor"
	rm -f "$dest/etc/sysusers.d/cockpit-monitor.conf"
	run userdel cockpit-monitor
	run groupdel cockpit-monitor
else
	echo "Kept /var/lib/cockpit-monitor, /etc/cockpit-monitor and the cockpit-monitor user (use --purge to remove them)."
fi
