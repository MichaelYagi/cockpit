# Setup

This guide covers building Cockpit, running it as an ordinary user, using
the terminal UI, and giving other programs access through API keys.
Packaged system-service installation (`cockpit-agent.service`) arrives in
Phase 3.

## Requirements

- Linux (amd64 or arm64), with everything described here.
- macOS: identity, uptime, CPU usage and load, memory, disks, network,
  processes, processor and Mac model, batteries and power adapter, and
  (on Apple Silicon) temperatures (D32, D36). Without root, macOS hides
  other users' CPU and memory use, so those processes show 0 and the list
  is marked partial. Apple Silicon temperatures are shown but not used by
  the CPU temperature check, because Apple publishes no limits for them.
  Disk health, fans and services are `unsupported`.
- Windows: identity, uptime, CPU usage, memory, disks and network (D32).
  There is no load average. Everything else is `unsupported`.
- The admin connection is a Unix socket on macOS and a named pipe on
  Windows.
- Go 1.27.4 to build (pinned in `go.mod`; the `go` command downloads it if
  needed). No C compiler or database server is needed.
- Optional: `smartctl` from smartmontools 7.0 or newer, for disk health.

### macOS and Windows

`cockpit` runs the same way: `cockpit` opens the TUI with an embedded
agent, and `cockpit agent` runs one in the foreground. On macOS the admin
socket is in the state directory, because there is no `XDG_RUNTIME_DIR`. On
Windows it is a named pipe (`\\.\pipe\cockpit-monitor-<your SID>`, D31),
which `--socket` also accepts. The system service, the SMART helper and
`deploy/` are Linux only.

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
programs, run the agent separately. Keys created while the TUI runs its own
agent are saved, but nothing can use them until a separate agent with a
TCP listener is running; the API Access screen says so.

```sh
./bin/cockpit agent        # terminal 1: foreground; Ctrl-C stops it
./bin/cockpit tui          # terminal 2
```

`cockpit tui --once` prints a plain-text summary instead of opening the UI.

### Themes

The Settings screen (`S`) lists the color themes with a swatch of each.
Single-color themes (Teal, Ocean, Violet, Rose, Graphite) come first, then
mixed ones (Aurora, Lagoon, Orchid, Dusk), then retro ones (Amber,
Phosphor) that look like an old CRT terminal: tinted text and square
panel corners. `[` and `]` switch theme at once
and save it to `tui.json` in your configuration directory
(`~/.config/cockpit-monitor/` on Linux, mode `0600`); the fleet console
uses it too. Every theme keeps green, amber and red for health, and every
status also has a symbol and a word. `cockpit tui --theme ocean` picks a
theme for one session without saving it.

### TUI keys

| Key | Action |
|---|---|
| `1`–`9`, `0`, `S`, `tab`, `←`/`→`, click a tab | Switch screen (1 Overview, 2 Hardware, 3 Performance, 4 Health, 5 Storage, 6 Network, 7 Processes, 8 Services, 9 Events, 0 API Access, `S` (Shift+S) Settings; `,` also still opens Settings) |
| `r` | Refresh now |
| `?` | Help |
| `q`, `Ctrl-C` | Quit |
| `↑`/`↓` (API Access) | Select a key |
| `n` / `o` / `x` / `d` (API Access) | Create / rotate / revoke a key / remove a revoked or expired key |
| `space` / `a` (new key dialog) | Toggle a scope / select every read scope |
| `c` (new key shown) | Copy the key to the clipboard |
| `s` / `/` / `k` (Processes) | Change sort / search / show kernel threads |
| `c` (Processes) | Switch between processes and containers |
| `f` (Services) | Failed services only / all |
| `t` (Performance) | Chart range: 1h, 6h, 24h, 7d |
| `v` / `m` (Events) | Minimum severity: all, warning, critical / load older events |
| `[` / `]` (Settings) | Previous / next color theme (saved for you) |
| `+` / `-` (Settings) | Change this session's refresh interval |
| `↑`/`↓`, `enter`, `w`, `d`, `esc` (Settings) | Select / edit a health threshold / save / reset to the file / discard edits |

Performance charts come from the agent's metric history (raw samples for
recent ranges, 1-minute or 1-hour aggregates for longer ones). Health
thresholds can be changed only over the local socket. A save shows the
changes and asks for confirmation; the override persists in the database
until reset.

Rotating, revoking and removing a key ask you to type the first 4
characters of its ID (the whole ID also works). A new key is shown once.
Press `c` to copy it (the terminal must allow clipboard access through
OSC 52, as Windows Terminal, iTerm2, kitty, WezTerm and recent xterm do), or
select it with the mouse: the TUI releases the mouse while the key is
shown. `cockpit keys create --scope all` grants every read scope.
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

## Installing a release

Each release (`vX.Y.Z` on GitHub) has Linux amd64 and arm64 archives and a
`SHA256SUMS` file:

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf cockpit_v0.4.0_linux_amd64.tar.gz && cd cockpit_v0.4.0_linux_amd64
./cockpit                      # try it in user mode, or install the service:
sudo ./deploy/install.sh       # finds ./cockpit next to deploy/
```

Maintainers cut a release by renaming the `CHANGELOG.md` heading from
"unreleased" to the date, then pushing a tag: `git tag v0.4.0 && git push
origin v0.4.0`. The release workflow runs the checks, builds with
`make dist`, and publishes the archives with the changelog section as
notes.

## System service

To keep monitoring when nobody is logged in, install the agent as a
hardened systemd service running as the unprivileged `cockpit-monitor`
user:

```sh
make build
sudo ./deploy/install.sh                  # binary to /usr/local/bin, units to /etc/systemd/system
sudo usermod -aG cockpit-monitor "$USER"  # admin socket access; log in again afterwards
cockpit tui --mode system
cockpit keys list --mode system
```

`install.sh` also installs smartmontools with the system's package manager
(apt, dnf, yum, zypper, pacman or apk) when `smartctl` is missing, and
enables the hourly SMART helper, so disk health works out of the box.
`--no-smart` skips both. A failed package install only prints a warning.
It accepts `--prefix DIR` (default `/usr/local`) and `--no-start`.
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

## Web dashboard

The agent serves a read-only dashboard in the browser on its TCP port
(D35). Open <http://127.0.0.1:7878/> and enter an API key; a key with
`--scope all` shows everything, and a key with fewer scopes shows what it
may read and says what it may not. It has the same screens as the TUI
except API Access and Settings, updates live, and works at phone width.
Number keys `1`–`9` switch screens.

- The key stays in that browser tab only and is forgotten when the tab
  closes. **Sign out** forgets it at once.
- From another computer, start the agent with `--lan` (see
  [remote access](#remote-access)) and open the address it prints. The browser warns about the certificate,
  since it comes from Cockpit's own CA: compare the fingerprint it shows
  with `cockpit tls show`, or import `ca.pem` into the browser or system
  trust store to remove the warning.
- The TUI's built-in agent has no TCP port, so run `cockpit agent` (or the
  system service) to use the dashboard.
- Turn it off with `api.web.enabled: false`.

## Remote access

By default TCP binds to `127.0.0.1` only. Remote access requires TLS and
an API key.

### The simple way: `--lan`

On the machine to watch:

```sh
cockpit agent --lan
cockpit keys create --name laptop --scope all   # in another terminal; copy the key
```

`--lan` listens on every network interface and creates its own HTTPS
certificate for this machine's name and addresses (D37). At startup it
prints the addresses to open, for example `https://192.168.0.185:7878/`,
and the certificate's fingerprint. On the other computer, open that
address in a browser, check that the certificate warning shows the same
SHA-256 fingerprint, continue, and paste the key. Or run the TUI command
it prints:

```sh
export COCKPIT_API_KEY=<the key>
cockpit tui --url https://192.168.0.185:7878 --tls-fingerprint <fingerprint>
```

To keep it on, put this in the configuration file (for the system
service, `/etc/cockpit-monitor/config.yaml`, then
`sudo systemctl restart cockpit-agent`; the addresses appear in
`journalctl -u cockpit-agent`):

```yaml
api:
  lan:
    enabled: true
    # http: true   # plain HTTP instead of HTTPS (D38), see below
```

`COCKPIT_API_LAN=true` works too. The certificate is kept in
`<state dir>/tls/` and reused, so the fingerprint stays the same. When
this machine gets a new address, or the certificate is within 30 days of
expiring, the next start issues a new one from the same CA, and pinned
clients need the new fingerprint. If another computer cannot connect,
check this machine's firewall allows TCP port 7878. On WSL, Windows must
also let the connection through; see Microsoft's WSL networking guide.

### Plain HTTP on a trusted LAN: `--lan-http`

If you trust everyone on your network and do not want the certificate
step, start the agent with `--lan-http` instead (or set
`api.lan.http: true`, or `COCKPIT_API_LAN_HTTP=true`). It serves plain
`http://` on every interface, needs no certificate, and prints the
addresses:

```sh
cockpit agent --lan-http
curl -H "Authorization: Bearer $COCKPIT_API_KEY" http://192.168.0.185:7878/api/v1/health
cockpit tui --url http://192.168.0.185:7878   # prints a warning
```

The API key and all data travel **unencrypted**: anyone on the same
network can read them and use the key. The agent only accepts
connections from private-network addresses (`192.168.x.x`, `10.x.x.x`,
`172.16–31.x.x`, `fc00::/7`, link-local) and still requires a key on every
request. The Go client and `cockpit tui` accept a plain `http://` URL only
for a private IP address, not a host name. It cannot be combined with
`api.tls`.

### Your own certificate

Cockpit can create a certificate for names you choose (D29):

```sh
cockpit tls init --host box.example --host 192.0.2.7   # system mode: sudo cockpit tls init --mode system ...
# paste the printed api: block into the configuration file, restart the agent
```

`tls init` writes a private CA and a server certificate into
`<config dir>/tls/`. Keys are `0600`; in system mode the server key goes to
the `cockpit-monitor` user and the CA key stays root-only. It prints the
certificate's SHA-256 fingerprint. On the client:

```sh
export COCKPIT_API_KEY="$(cat box.key)"
cockpit tui --url https://box.example:7878 --tls-fingerprint <fingerprint>   # pin this certificate
cockpit tui --url https://box.example:7878 --tls-ca ca.pem                    # or trust the CA
```

Both also work as `COCKPIT_TLS_FINGERPRINT` and `COCKPIT_TLS_CA`, and in
the Go SDK as `client.WithPinnedCertificate` and `client.WithCAPEM`.
`cockpit tls show` prints the fingerprint again. Server certificates last
a year, and the agent warns in its log 30 days before expiry. `/status`
shows `tls_expires_at` to the local admin. `cockpit tls renew` issues a new
one from the same CA. Clients that trust the CA need no change; pinned
clients need the new fingerprint.

A certificate from your own CA or ACME works too; set the two paths:

```yaml
api:
  tcp:
    address: 0.0.0.0:7878
  tls:
    cert_file: /etc/cockpit-monitor/tls/server.pem
    key_file: /etc/cockpit-monitor/tls/server-key.pem   # must be chmod 600
```

Without both files (or `api.lan.http`), the agent refuses to start on a
non-loopback address. Clients refuse to send a key over plain HTTP to a
non-loopback host, except to a private-network address of an agent
started with `--lan-http`.

## Watching several machines

`cockpit fleet` is a console for many machines (D30). Nothing central is
installed. Each machine keeps its own agent, and the console polls them
with a key per machine.

```sh
# on each machine: enable TLS (see Remote access) and create a key
sudo cockpit keys create --mode system --name fleet --scope system:read --scope metrics:read > web01.key
# on the console machine
chmod 600 web01.key
cockpit fleet add web01 https://web01.example:7878 --key-file ./web01.key
cockpit fleet                    # the console; Enter opens the full TUI for a machine
cockpit fleet check              # one-line status per machine, for scripts
cockpit fleet list               # remove with: cockpit fleet remove web01
```

`fleet add` shows the server certificate's fingerprint and pins it once
you confirm. Compare it with `cockpit tls show` on that machine, or pass
`--tls-fingerprint` (or `--tls-ca`) to skip the question. It also records
the machine ID. If either changes, for example after a reinstall or an
impersonation, the console marks the machine **REFUSED** and stops
polling it until you run `fleet add NAME ... --replace`. The list is
`~/.config/cockpit-monitor/fleet.yaml` (mode `0600`). It names key files
but never contains keys. Keys can also come from an environment variable
(`--key-env`).

Give fleet keys `system:read` and `metrics:read`. Add `network:read` only
if you want addresses on the console host. Protect the console host as
well as the machines it watches, because it holds their keys.

## What you may see as "unavailable"

Unavailable data is reported with a reason, never as an error:

| Area | Typical reason | What to do |
|---|---|---|
| Temperatures, fans | No hwmon/thermal sensors (VMs, containers, WSL) | Nothing; this is expected |
| Disk health | `smartctl` not installed | Install smartmontools 7.0+ (`install.sh` does this for the system service). Virtual disks in VMs and WSL have no SMART data even with it |
| Disk health | Permission denied | Reading SMART data needs raw device access; in system mode enable the SMART helper (see System service) |
| Disk health | Results more than 3 hours old | The SMART helper stopped running: `systemctl status cockpit-smart.timer` |
| Memory pressure | Kernel without PSI (before 4.20, or `psi=0`) | Memory utilization is still checked |
| GPU metrics | `nvidia-smi` not installed (NVIDIA) or no metrics source | Install the NVIDIA driver utilities; the GPU is still listed |
| GPU busy % (Intel) | Needs perf events | Frequency and power are shown instead; busy % is not available to an unprivileged agent |
| Memory modules (DIMMs) | SMBIOS tables are root-only | Expected without root |
| Board / firmware, USB | Not exposed (VMs, containers, WSL) | Nothing; this is expected |
| Services | Not running systemd | Nothing; other features still work |
| Batteries | None present | Expected on desktops, servers and VMs |
| UPS | Off by default | Set `collection.ups.enabled: true` (see UPS monitoring) |
| UPS | No server answered | Start `upsd` or check `collection.ups.address` |
| ZFS capacity and errors | zpool older than OpenZFS 2.3 | Pool state and ARC still work; capacity needs zpool's JSON output |
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

## UPS monitoring

Cockpit reads UPS state from a Network UPS Tools server (`upsd`). It only
lists UPS units and their variables and never sends commands. Enable it in
the configuration file:

```yaml
collection:
  ups:
    enabled: true
    address: 127.0.0.1:3493   # upsd's default
```

The Overview shows each UPS. The `ups` health check warns when a UPS runs
on battery and reports critical at low battery. Batteries and power adapters
(laptops) and ZFS pools need no setup.

## Checking your services

Cockpit can check that your services answer: web apps, APIs, or
anything listening on a TCP port (D40).

**From the TUI** (on the agent's machine, so it uses the admin socket):
open the Network screen (`6`), press `a`, type a name, then the address
(`http://192.168.0.50:8123/`, `https://nas.lan/` or
`tcp://192.168.0.10:22`). For `https://` addresses it asks whether to
accept a self-signed certificate. The check starts within a few seconds
and is kept across restarts. Select a check with `↑`/`↓` and press `d`
to remove it (D41).

**In the configuration file**, for checks you want under version
control or need `expect_status` for:

```yaml
collection:
  endpoints:
    interval: 30s               # default; 10s to 1h
    timeout: 5s                 # default; at most half the interval
    targets:
      - name: Home Assistant
        url: http://192.168.0.50:8123/
      - name: Proxmox
        url: https://192.168.0.20:8006/
        tls_skip_verify: true     # self-signed certificate
      - name: Jellyfin
        url: http://192.168.0.30:8096/health
        expect_status: [200]      # default: any 2xx or 3xx
      - name: NAS SSH
        url: tcp://192.168.0.10:22    # just the port must accept connections
```

Restart the agent. The Network screen (TUI key `6`, and the web
dashboard's Network page) shows each service as up, failing (one failed
check) or down (two in a row), with its response time and the reason it
failed. The `endpoints` health check turns critical when a service is
down and warns when an HTTPS certificate expires within 14 days, so
notifications cover your services too.

Each service is checked in its own background loop, so one that hangs
only waits out its own timeout and never delays the others or the rest
of Cockpit. Results show what *this* machine can reach. Don't put passwords or
tokens in these URLs: the configuration file is not secret. API keys
need the `network:read` scope to see the URLs; without it they see names
and states only.

## Notifications

Cockpit can send its health findings to your phone or chat: when a
problem appears or gets worse, and again when it is resolved (D39). It
works with [ntfy](https://ntfy.sh) (phone push, no account needed),
Discord, Slack, or any service that accepts JSON.

1. Put the webhook URL in a private file. The URL is the password, so it
   does not go in the configuration file:

   ```sh
   # user mode
   echo 'https://ntfy.sh/pick-a-long-random-topic' > ~/.config/cockpit-monitor/ntfy.url
   chmod 600 ~/.config/cockpit-monitor/ntfy.url
   # system mode: the agent's user must be able to read it
   echo 'https://ntfy.sh/pick-a-long-random-topic' | sudo tee /etc/cockpit-monitor/ntfy.url >/dev/null
   sudo chown cockpit-monitor: /etc/cockpit-monitor/ntfy.url && sudo chmod 600 /etc/cockpit-monitor/ntfy.url
   ```

2. List it in the configuration file (`~/.config/cockpit-monitor/config.yaml`
   or `/etc/cockpit-monitor/config.yaml`). `url_file` must be an absolute
   path:

   ```yaml
   notifications:
     machine_name: nas            # optional; default is the host name
     webhooks:
       - name: phone
         url_file: /home/me/.config/cockpit-monitor/ntfy.url
         format: ntfy             # ntfy | discord | slack | json (default)
         min_severity: warning    # warning (default) | critical
   ```

3. Send a test message, then restart the agent:

   ```sh
   cockpit notify test
   ```

   It prints `phone: sent`, or the reason it failed. Subscribe to the same
   topic in the ntfy app to receive it.

For Discord, create a webhook in the channel's settings (Integrations) and
use `format: discord`; for Slack, an incoming webhook with
`format: slack`. With `format: json` Cockpit POSTs an object with
`machine`, `title`, `status` (`warning`, `critical` or `resolved`),
`subject`, `message` and `time`.

A problem is announced once and again when it clears, not repeatedly. A
problem still present when the agent restarts is announced again. If a
webhook is down, the agent retries twice and logs the failure; it never
affects monitoring.

## Configuration reference

See `docs/ARCHITECTURE.md` §8 for the schema and defaults, or run
`cockpit config check` to print the effective values.
