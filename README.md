# Cockpit

**A modular, API-first machine health and hardware monitoring platform
with a polished terminal dashboard.**

Cockpit combines local machine telemetry, hardware inventory, health
diagnostics, and a secure API. The terminal UI is the primary interface;
the same data is available to scripts, external applications and future
fleet dashboards through the API.

> Cockpit is unrelated to the Red Hat [Cockpit](https://cockpit-project.org)
> web console. To avoid collisions, Cockpit's system identifiers use
> `cockpit-monitor` / `cockpit-agent` (see [D1](docs/DECISIONS.md)).

## Status

**Phases 1 and 2 are implemented** (Phase 2 awaiting review). On Linux,
the agent collects CPU, memory (with PSI), filesystems, network (with
addresses, routes and DNS), disks, sensors, SMART health, processes,
systemd services, detailed hardware (CPU, PCI, USB, board, memory modules)
and GPUs (NVIDIA and AMD metrics); runs six health checks; streams live
events; serves the authenticated API; and manages API keys. The terminal
UI has nine screens. See [SETUP](docs/SETUP.md) to get started and
[ROADMAP](docs/ROADMAP.md) for what comes next.

## Quick start

```sh
make build
./bin/cockpit tui                 # starts an embedded agent if none is running
./bin/cockpit agent               # or run the agent on its own
./bin/cockpit keys create --name grafana --scope metrics:read > grafana.key
curl -H "Authorization: Bearer $(cat grafana.key)" http://127.0.0.1:7878/api/v1/health
```

## Product goals

- Understand a machine's hardware and operating system.
- Monitor CPU, memory, storage, network, thermal and (where supported) GPU
  metrics.
- Surface health findings with evidence and next steps.
- Keep optional, bounded history of metrics and events.
- Expose normalized data through a documented, authenticated API.
- Add collectors and integrations without rewriting the core.
- Work locally without a cloud account.

## Architecture at a glance

```text
platform collectors ─▶ engine ─▶ snapshots ─▶ health / events / history
                                     │
                                     ▼
                     versioned REST API (+ SSE later)
                     Unix socket (local admin) · TCP (API key, TLS remote)
                                     │
             ┌───────────────────────┼─────────────────────┐
             ▼                       ▼                     ▼
       cockpit tui            scripts / SDKs        future web / fleet
```

One binary, several modes:

```text
cockpit agent          # monitoring engine + API server
cockpit                # in a terminal: same as cockpit tui; otherwise help
cockpit tui            # terminal UI (API client)
cockpit keys ...       # create / list / rotate / revoke API keys (local admin)
cockpit report         # diagnostic report (Markdown or JSON, optionally anonymized)
cockpit fleet          # console for several machines (pinned TLS, no central server)
cockpit tls init       # certificate for remote access
cockpit config check   # validate configuration
cockpit version
```

The TUI never reads `/proc`, `/sys` or collector internals. It uses the
API like any other client.

## Feature areas

1. Overview: machine identity, health summary, live resource metrics.
2. Hardware: CPU, memory, GPU, motherboard, storage, devices.
3. Performance: utilization, load, throughput, top consumers.
4. Health: thermal state, storage health, memory pressure, findings.
5. Storage: devices, filesystems, capacity, SMART/NVMe when available,
   ZFS pools.
6. Network: interfaces, addresses, throughput, errors.
7. Processes: process list and usage, subject to permissions; containers
   from cgroup v2.
8. Services: systemd service state.
9. Events: health findings and system events over time.
10. API Access: server status, keys, scopes, clients, request statistics.
11. Settings: refresh rate and health thresholds (local admin).

Runs on Linux (full), and on macOS and Windows (identity, CPU, memory,
disks, network). Also: a fleet console for many machines, TLS tooling,
batteries and UPS (Network UPS Tools), diagnostic reports, metric
history, and extension collectors compiled in through a public SDK
(`docs/PLUGINS.md`).

## Security defaults

- TCP API binds to `127.0.0.1:7878`; remote binding is opt-in and requires
  TLS.
- Every TCP request needs an API key, including from localhost.
- Key management happens only over a permission-protected local Unix
  socket.
- Keys (`ckp_…`) have 256 bits of entropy, are shown once, and are stored
  only as SHA-256 hashes.
- Read-only by default. Serial numbers, MAC addresses and command lines are
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
