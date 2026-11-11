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
./bin/cockpit tui
```

If no agent is running, the TUI starts an embedded one for the session.
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
| `1`–`5`, `tab`, `←`/`→`, click a tab | Switch screen |
| `r` | Refresh now |
| `?` | Help |
| `q`, `Ctrl-C` | Quit |
| `↑`/`↓` (API Access) | Select a key |
| `n` / `o` / `x` (API Access) | Create / rotate / revoke a key |

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

Scopes: `system:read` (identity, hardware, status) and `metrics:read`
(metrics, storage, network, sensors, health, events). Keys can never hold
admin rights.

## Calling the API

```sh
# Public, no key:
curl http://127.0.0.1:7878/api/v1/livez

# With a key (never put keys in URLs):
curl -H "Authorization: Bearer $(cat grafana.key)" http://127.0.0.1:7878/api/v1/metrics?sections=cpu,memory

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
| Disk health | Permission denied | Reading SMART data needs elevated privileges; a least-privilege option is planned for Phase 3 |
| Memory pressure | Kernel without PSI (before 4.20, or `psi=0`) | Memory utilization is still checked |

## Configuration reference

See `docs/ARCHITECTURE.md` §8 for the schema and defaults, or run
`cockpit config check` to print the effective values.
