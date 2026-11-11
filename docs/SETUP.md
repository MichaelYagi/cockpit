# Setup

This guide covers building Cockpit, running it as an ordinary user, using
the terminal UI, and giving other programs access through API keys.
Packaged system-service installation (`cockpit-agent.service`) arrives in
Phase 3.

## Requirements

- Linux (amd64 or arm64). Other platforms build and run, but report every
  capability as `unsupported` until their collectors exist.
- Go 1.27.4 to build (pinned in `go.mod`; the `go` command downloads it if
  needed). No C compiler or database server is needed.
- Optional: `smartctl` from smartmontools 7.0 or newer, for disk health.

## Build

```sh
make build          # produces bin/cockpit
make check          # gofmt, vet, staticcheck, race tests, cross-compile
```

## Quick start (user mode)

```sh
./bin/cockpit              # same as "cockpit tui" in a terminal
```

Without a terminal (in a script or pipe), plain `cockpit` prints the help
text instead; `cockpit help` always does. If no agent is running, the TUI starts an embedded one for the session.
It listens only on a private socket and stops when you quit. To keep
monitoring when the TUI is closed, and to allow API access from other
programs, run the agent separately:

```sh
./bin/cockpit agent        # terminal 1: foreground; Ctrl-C stops it
./bin/cockpit tui          # terminal 2
```

`cockpit tui --once` prints a plain-text summary instead of opening the UI.

### TUI keys

| Key | Action |
|---|---|
| `1`–`9`, `0`, `,`, `tab`, `←`/`→`, click a tab | Switch screen (1 Overview, 2 Hardware, 3 Performance, 4 Health, 5 Storage, 6 Network, 7 Processes, 8 Services, 9 Events, 0 API Access, `,` Settings) |
| `r` | Refresh now |
| `?` | Help |
| `q`, `Ctrl-C` | Quit |
| `↑`/`↓` (API Access) | Select a key |
| `n` / `o` / `x` (API Access) | Create / rotate / revoke a key |
| `s` / `/` / `k` (Processes) | Change sort / search / show kernel threads |
| `c` (Processes) | Switch between processes and containers |
| `f` (Services) | Failed services only / all |
| `t` (Performance) | Chart range: 1h, 6h, 24h, 7d |
| `v` / `m` (Events) | Minimum severity: all, warning, critical / load older events |
| `+` / `-` (Settings) | Change this session's refresh interval |
| `↑`/`↓`, `enter`, `w`, `d`, `esc` (Settings) | Select / edit a health threshold / save / reset to the file / discard edits |

Performance charts come from the agent's metric history (raw samples for
recent ranges, 1-minute or 1-hour aggregates for longer ones). Health
thresholds can be changed only over the local socket. A save shows the
changes and asks for confirmation; the override persists in the database
until reset.

Rotation and revocation ask you to type the key ID. A new key is shown once.
It is erased from the screen when you press Enter and cannot be shown again.

## Files and permissions (user mode)

| Path | Contents | Mode |
|---|---|---|
| `~/.config/cockpit-monitor/config.yaml` | Optional configuration | your choice |
| `~/.local/state/cockpit-monitor/` | Database (key hashes), machine ID, lock, embedded-agent log | `0700` dir, `0600` files |
| `$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock` | Admin socket | `0700` dir, `0600` socket |

The agent refuses to start if these directories are more permissive than
shown or owned by another user. `cockpit config check` prints the effective
configuration and paths.

## System service

To keep monitoring when nobody is logged in, install the agent as a
hardened systemd service running as the unprivileged `cockpit-monitor`
user:

```sh
make build
sudo ./deploy/install.sh                  # binary to /usr/local/bin, units to /etc/systemd/system
sudo ./deploy/install.sh --smart-helper   # also enable the hourly SMART helper (needs smartmontools)
sudo usermod -aG cockpit-monitor "$USER"  # admin socket access; log in again afterwards
cockpit tui --mode system
cockpit keys list --mode system
```

`install.sh` accepts `--prefix DIR` (default `/usr/local`) and `--no-start`.
It installs:

| Path | Contents | Mode |
|---|---|---|
| `/usr/local/bin/cockpit` | The binary | `0755` |
| `/etc/systemd/system/cockpit-agent.service` | The agent (`cockpit agent --mode system`) | `0644` |
| `/etc/systemd/system/cockpit-smart.{service,timer}` | SMART helper, hourly (D20) | `0644` |
| `/etc/sysusers.d/cockpit-monitor.conf` | The `cockpit-monitor` user and group | `0644` |

systemd creates the directories when the agent starts:

| Path | Contents | Mode |
|---|---|---|
| `/etc/cockpit-monitor/config.yaml` | Optional configuration | `0750` dir |
| `/var/lib/cockpit-monitor/` | Database, machine ID, lock | `0700` dir, `0600` files |
| `/var/lib/cockpit-monitor/smart/` | SMART helper results | `0750` dir, `0640` files, `root:cockpit-monitor` |
| `/run/cockpit-monitor/agent.sock` | Admin socket | `0750` dir, `0660` socket, group `cockpit-monitor` |

Membership of the `cockpit-monitor` group gives full local admin access,
including key management. Grant it as you would `sudo`.

The unit applies systemd hardening: no capabilities, `NoNewPrivileges`, a
read-only file system apart from its own directories, read-only home
directories, a system-call filter and restricted address families
(`systemd-analyze security cockpit-agent.service` rates it about 1.9,
"OK"). Process listing and GPU metrics keep working.

**SMART helper (D20).** Disk health needs raw device access, which the
agent deliberately lacks. `cockpit-smart.timer` runs `cockpit smart-helper`
as root once an hour (and two minutes after boot) with only the
capabilities `smartctl` needs and no network. It writes one file per disk
to `/var/lib/cockpit-monitor/smart/`, and the agent reads them. Results
older than three hours are reported as stale. Without the helper, the
agent runs `smartctl` itself, which usually reports `permission_denied`.
Run it now with `sudo systemctl start cockpit-smart.service`; check it with
`systemctl list-timers cockpit-smart.timer` and
`journalctl -u cockpit-smart.service`. The helper refuses to write if its
directory is a symlink, is not owned by root or is writable by others;
remove the directory and run it again.

**WSL.** `nvidia-smi` lives in `/usr/lib/wsl/lib`, which is not on a
service's `PATH`. Add it with a drop-in:

```sh
sudo systemctl edit cockpit-agent.service
# [Service]
# Environment=PATH=/usr/lib/wsl/lib:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
```

**Upgrade.** Build the new version and run `sudo ./deploy/install.sh`
again. It replaces the binary and units, reloads systemd and restarts the
agent. Data, keys and history in `/var/lib/cockpit-monitor` are kept, and
database migrations run on start. Local unit changes belong in drop-ins
(`systemctl edit`), which upgrades do not overwrite.

**Uninstall.** `sudo ./deploy/uninstall.sh` stops and removes the units and
binary but keeps data, configuration and the user. `--purge` also deletes
them, including every API key; this cannot be undone.

## Diagnostic reports

```sh
./bin/cockpit report > report.md                        # Markdown, from the local socket
./bin/cockpit report --format json -o report.json       # file is created with mode 0600
./bin/cockpit report --anonymize -o report-shareable.md # placeholders for host names, IPs, MACs, users
```

Over the socket the report includes everything, including the health
thresholds. Over TCP (`--url`, key in `COCKPIT_API_KEY`) it needs
`system:read`; parts built from metrics are added when the key also has
`metrics:read`, and the report lists anything it left out. Serial numbers,
MAC addresses and other sensitive fields are never included. Check an
anonymized report before sharing it: identifiers that appear only in free
text, such as a host name in a service description, are not recognised.

## API keys

Key management works only through the admin socket, so only the local
user who owns the agent can do it.

```sh
# Create a read-only key. Only the key is printed on stdout, once.
./bin/cockpit keys create --name grafana --scope metrics:read --expires 2160h > grafana.key
chmod 600 grafana.key

./bin/cockpit keys list
./bin/cockpit keys rotate <id>      # new key printed once; old key stops working
./bin/cockpit keys revoke <id>      # asks you to type the ID; --yes to skip
```

Scopes: `system:read` (identity, hardware, services, status),
`metrics:read` (metrics, storage, network, sensors, health, events and the
live event stream) and `processes:read` (the process list). Keys can never
hold admin rights.

## Calling the API

```sh
# Public, no key:
curl http://127.0.0.1:7878/api/v1/livez

# With a key (never put keys in URLs):
curl -H "Authorization: Bearer $(cat grafana.key)" http://127.0.0.1:7878/api/v1/metrics?sections=cpu,memory

# Live events (Server-Sent Events):
curl -N -H "Authorization: Bearer $(cat grafana.key)" http://127.0.0.1:7878/api/v1/events/stream

# As the local admin over the socket (no key):
curl --unix-socket "$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock" http://localhost/api/v1/status
```

Python (standard library only):

```python
import json, os, urllib.request

key = open("grafana.key").read().strip()
req = urllib.request.Request(
    "http://127.0.0.1:7878/api/v1/health",
    headers={"Authorization": f"Bearer {key}"},
)
with urllib.request.urlopen(req) as resp:
    health = json.load(resp)
print(health["status"])
for check in health["checks"]:
    print(f'{check["id"]:18} {check["status"]:8} {check.get("summary") or check.get("reason")}')
```

The TUI accepts TCP too, for example to watch another machine:

```sh
COCKPIT_API_KEY="$(cat grafana.key)" ./bin/cockpit tui --url http://127.0.0.1:7878
```

The full contract is served at `/api/v1/openapi.json` and lives in
`api/openapi.yaml`.

## Remote access

By default TCP binds to `127.0.0.1` only. Remote access requires TLS:

```yaml
# ~/.config/cockpit-monitor/config.yaml
api:
  tcp:
    address: 0.0.0.0:7878
  tls:
    cert_file: /home/you/.config/cockpit-monitor/tls/cert.pem
    key_file: /home/you/.config/cockpit-monitor/tls/key.pem   # must be chmod 600
```

Without both files, the agent refuses to start. Clients refuse to send a
key over plain HTTP to a non-loopback host.

## What you may see as "unavailable"

Unavailable data is reported with a reason, never as an error:

| Area | Typical reason | What to do |
|---|---|---|
| Temperatures, fans | No hwmon/thermal sensors (VMs, containers, WSL) | Nothing; this is expected |
| Disk health | `smartctl` not installed | Install smartmontools 7.0+ |
| Disk health | Permission denied | Reading SMART data needs raw device access; in system mode enable the SMART helper (see System service) |
| Disk health | Results more than 3 hours old | The SMART helper stopped running: `systemctl status cockpit-smart.timer` |
| Memory pressure | Kernel without PSI (before 4.20, or `psi=0`) | Memory utilization is still checked |
| GPU metrics | `nvidia-smi` not installed (NVIDIA) or no metrics source (e.g. Intel) | Install the NVIDIA driver utilities; the GPU is still listed |
| Memory modules (DIMMs) | SMBIOS tables are root-only | Expected without root |
| Board / firmware, USB | Not exposed (VMs, containers, WSL) | Nothing; this is expected |
| Services | Not running systemd | Nothing; other features still work |
| Containers | cgroup v1 host | Containers are found only through cgroup v2 |
| Container names and images | Never shown | They come from the engine's API socket, which is root-equivalent and not used (D24) |
| Container memory or I/O (rootless) | Controller not delegated to the user | Enable cgroup delegation for the user's systemd instance |
| Processes (partial) | `/proc` mounted with `hidepid` | Run the agent with access to other users' processes, if appropriate |

## Choosing filesystems

Pseudo and in-memory filesystems (`proc`, `tmpfs`, `squashfs`, container
overlays, …) are skipped, and network filesystems (NFS, SMB, sshfs, Ceph, …)
are never inspected because a hung server can block collection. Adjust the
rest in the configuration file:

```yaml
collection:
  filesystems:
    exclude_types: [fuse.mergerfs]
    include_types: [tmpfs]                       # e.g. to watch /dev/shm
    exclude_mount_points: ["/snap/**", "/mnt/[de]"]
```

Excluded filesystems disappear from the API, the TUI, history and the
storage health check. Changes need an agent restart.

## Configuration reference

See `docs/ARCHITECTURE.md` §8 for the schema and defaults, or run
`cockpit config check` to print the effective values.
