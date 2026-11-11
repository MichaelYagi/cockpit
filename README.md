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

**Phase 0 (foundation) is implemented.** The agent starts, serves
`/api/v1/livez`, `/api/v1/openapi.json` and `/api/v1/status` over a Unix
socket and loopback TCP, and shuts down cleanly. No metrics are collected
yet; Linux collectors, API keys and the interactive TUI arrive in Phase 1.
See [ROADMAP](docs/ROADMAP.md).

## Development quick start

Requires Go 1.27.4 (pinned in `go.mod`; the `go` command downloads it if
needed).

```sh
make build                    # builds bin/cockpit
make check                    # gofmt, vet, staticcheck, race tests, cross-compile
./bin/cockpit config check    # print effective configuration and paths
./bin/cockpit agent           # run the agent in the foreground (Ctrl-C to stop)
./bin/cockpit tui             # in another terminal: placeholder client over the socket
```

Talking to the agent directly:

```sh
# Unix socket: local administrator, no key needed
curl --unix-socket "$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock" http://localhost/api/v1/status

# TCP: always needs an API key (key creation arrives in Phase 1)
curl http://127.0.0.1:7878/api/v1/livez
```

User mode keeps state in `~/.local/state/cockpit-monitor/` and the socket in
`$XDG_RUNTIME_DIR/cockpit-monitor/`. `--mode system` uses
`/etc/cockpit-monitor/`, `/var/lib/cockpit-monitor/` and
`/run/cockpit-monitor/`.

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
cockpit tui            # terminal UI (API client)
cockpit keys ...       # create / list / rotate / revoke API keys (local admin)
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
5. Storage: devices, filesystems, capacity, SMART/NVMe when available.
6. Network: interfaces, addresses, throughput, errors.
7. Processes: process list and usage, subject to permissions.
8. Services: systemd service state.
9. Events: health findings and system events over time.
10. API Access: server status, keys, scopes, clients, request statistics.

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
- [Testing strategy](docs/TESTING.md)
- [Roadmap](docs/ROADMAP.md)
