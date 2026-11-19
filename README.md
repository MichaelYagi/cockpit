# Cockpit

**A modular, API-first machine health and hardware monitoring platform
with a polished terminal dashboard.**

Cockpit collects machine telemetry, hardware inventory and health
diagnostics, and serves them through a secure, documented API. The
terminal UI, the built-in web dashboard, the fleet console and your own
scripts all read the same API.

> Cockpit is unrelated to the Red Hat [Cockpit](https://cockpit-project.org)
> web console. To avoid collisions, Cockpit's system identifiers use
> `cockpit-monitor` / `cockpit-agent` (see [D1](docs/DECISIONS.md)).

## Quick start

```sh
make build
./bin/cockpit tui                 # starts an embedded agent if none is running
./bin/cockpit agent               # or run the agent on its own
./bin/cockpit keys create --name grafana --scope metrics:read > grafana.key
curl -H "Authorization: Bearer $(cat grafana.key)" http://127.0.0.1:7878/api/v1/health
```

With the agent running, the web dashboard is at <http://127.0.0.1:7878/>.
See [SETUP](docs/SETUP.md) for configuration, remote access and services.

## What it shows

1. **Overview:** machine identity, health summary, live CPU, memory,
   temperatures, power and GPU.
2. **Hardware:** CPU, memory modules, board, GPUs, PCI and USB devices,
   network adapters.
3. **Performance:** CPU, memory, storage and network charts, live or
   from stored history (1 hour to 7 days).
4. **Health:** checks for CPU temperature, memory, storage capacity, disk
   health (SMART), services, network errors, ZFS pools, UPS and watched
   endpoints, with evidence and thresholds.
5. **Storage:** disks, filesystems, capacity, SMART/NVMe health and ZFS
   pools.
6. **Network:** interfaces, addresses, throughput, errors, routes, DNS and
   endpoint checks.
7. **Processes:** process list and usage, and containers (from cgroup v2).
8. **Services:** systemd services on Linux, launchd jobs on macOS.
9. **Events:** health findings and system events over time.
10. **API Access:** listeners, clients, request statistics and API keys
    (TUI only).
11. **Settings:** refresh rate and health thresholds (TUI only).

GPU metrics come from `nvidia-smi` (NVIDIA) and sysfs (AMD, Intel).
Batteries and UPS (Network UPS Tools) are included, and health findings
can be sent to webhooks.

**Platforms.** Linux has everything above. macOS has CPU, memory, disks,
network, processes, services, hardware inventory (memory, disks, PCI and
USB devices), routes and DNS, UPS, batteries and Apple Silicon
temperatures. Windows has identity, CPU, memory, disks, network and UPS. Under WSL, Cockpit monitors the
WSL virtual machine; Windows passes no CPU sensors into it.

## Architecture at a glance

```text
platform collectors ─▶ engine ─▶ snapshots ─▶ health / events / history
                                     │
                                     ▼
                     versioned REST API + Server-Sent Events
                     Unix socket (local admin) · TCP (API key)
                                     │
        ┌──────────────┬─────────────┼──────────────┬──────────────┐
        ▼              ▼             ▼              ▼              ▼
   cockpit tui   web dashboard  cockpit fleet  scripts / SDKs  webhooks
```

One binary, several modes:

```text
cockpit agent          # monitoring engine + API server + web dashboard
cockpit                # in a terminal: same as cockpit tui; otherwise help
cockpit tui            # terminal UI (API client)
cockpit keys ...       # create / list / rotate / revoke / remove API keys (local admin)
cockpit fleet ...      # one console for several machines; fleet scan finds them on the LAN
cockpit report         # diagnostic report (Markdown or JSON, optionally anonymized)
cockpit tls ...        # private CA and certificate for remote access
cockpit notify test    # send a test message to the configured webhooks
cockpit config check   # validate configuration and print the effective settings
cockpit version        # version (also in the TUI, web dashboard and X-Cockpit-Version header)
```

The TUI and web dashboard never read `/proc`, `/sys` or collector
internals. They use the API like any other client. Extra collectors can
be compiled in through a public SDK ([PLUGINS](docs/PLUGINS.md)), and
`pkg/client` is a Go client for the API.

## Security defaults

- The TCP API binds to `127.0.0.1:7878`. Remote access is opt-in: `--lan`
  uses TLS, and `--lan-http` (private-network clients only) or
  `--http-public` allow plain HTTP when you choose it.
- Every TCP request needs an API key, including from localhost.
- Key management happens only over a permission-protected local Unix
  socket (a named pipe on Windows).
- Keys (`ckp_…`) have 256 bits of entropy, are shown once, and are stored
  only as SHA-256 hashes.
- The API is read-only. Serial numbers, MAC addresses and command lines are
  hidden by default.
- Secrets and `Authorization` headers are never logged.

## Documentation

- [Claude Code project guide](CLAUDE.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Decisions](docs/DECISIONS.md)
- [API contract](docs/API.md)
- [Security model](docs/SECURITY.md)
- [Collectors and plugins](docs/PLUGINS.md)
- [Setup and usage](docs/SETUP.md)
- [Testing strategy](docs/TESTING.md)
- [Roadmap](docs/ROADMAP.md)

## License

MIT. See [LICENSE](LICENSE).
