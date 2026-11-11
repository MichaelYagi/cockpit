#!/bin/sh
# Install Cockpit as a hardened system service (cockpit-agent.service).
#
#   sudo ./deploy/install.sh [--binary PATH] [--prefix DIR] [--smart-helper] [--no-start]
#
# Installs the binary, systemd units and the cockpit-monitor system user,
# then enables and starts the agent. --smart-helper also enables the hourly
# SMART helper timer (D20), which runs smartctl as root. Running it again
# upgrades in place: the binary and units are replaced, data is kept.
#
# DESTDIR=/some/dir stages the files there instead and skips every
# system-changing step (user creation, systemctl); used by the tests.
set -eu

binary=./bin/cockpit
prefix=/usr/local
smart=no
start=yes

usage() {
	sed -n '2,13s/^# \{0,1\}//p' "$0"
	exit "${1:-0}"
}

while [ $# -gt 0 ]; do
	case $1 in
	--binary) binary=${2:?--binary needs a path}; shift 2 ;;
	--prefix) prefix=${2:?--prefix needs a directory}; shift 2 ;;
	--smart-helper) smart=yes; shift ;;
	--no-start) start=no; shift ;;
	-h | --help) usage 0 ;;
	*) echo "unknown option: $1" >&2; usage 2 >&2 ;;
	esac
done

dest=${DESTDIR:-}
here=$(cd "$(dirname "$0")" && pwd)
bindir=$prefix/bin
unitdir=/etc/systemd/system
sysusersdir=/etc/sysusers.d

if [ -z "$dest" ] && [ "$(id -u)" -ne 0 ]; then
	echo "install.sh must run as root (or set DESTDIR to stage files)." >&2
	exit 1
fi
if [ ! -x "$binary" ]; then
	echo "binary $binary not found or not executable; build it with: make build" >&2
	exit 1
fi
case $prefix in
*[!A-Za-z0-9/._-]*) echo "--prefix may contain only letters, digits and / . _ -" >&2; exit 2 ;;
/*) ;;
*) echo "--prefix must be an absolute path" >&2; exit 2 ;;
esac

run() {
	if [ -n "$dest" ]; then
		echo "skipped (DESTDIR): $*"
	else
		"$@"
	fi
}

echo "Installing $binary to $bindir/cockpit"
install -d -m 0755 "$dest$bindir" "$dest$unitdir" "$dest$sysusersdir"
install -m 0755 "$binary" "$dest$bindir/cockpit.new"
mv -f "$dest$bindir/cockpit.new" "$dest$bindir/cockpit"

install -m 0644 "$here/sysusers.d/cockpit-monitor.conf" "$dest$sysusersdir/cockpit-monitor.conf"
for unit in cockpit-agent.service cockpit-smart.service cockpit-smart.timer; do
	# The units name /usr/local/bin/cockpit; follow --prefix.
	sed "s|/usr/local/bin/cockpit|$bindir/cockpit|g" "$here/systemd/$unit" >"$dest$unitdir/$unit.new"
	chmod 0644 "$dest$unitdir/$unit.new"
	mv -f "$dest$unitdir/$unit.new" "$dest$unitdir/$unit"
done

run systemd-sysusers "$sysusersdir/cockpit-monitor.conf"
# The agent creates its own state, runtime and configuration directories
# through systemd (StateDirectory= and friends).
run systemctl daemon-reload
if [ "$start" = yes ]; then
	run systemctl enable cockpit-agent.service
	run systemctl restart cockpit-agent.service
	if [ "$smart" = yes ]; then
		run systemctl enable --now cockpit-smart.timer
		run systemctl start cockpit-smart.service
	fi
fi

cat <<EOF

Done. Next steps:
  systemctl status cockpit-agent.service
  sudo usermod -aG cockpit-monitor "\$USER"   # then log in again to use the admin socket
  cockpit tui --mode system
EOF
if [ "$smart" = no ]; then
	echo "  # optional, reads disk health as root once an hour (needs smartmontools):"
	echo "  sudo systemctl enable --now cockpit-smart.timer"
fi
