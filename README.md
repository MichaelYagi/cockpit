# Linux Cockpit

**A modular, API-first machine health and hardware monitoring platform
with a polished terminal dashboard.**

Linux Cockpit combines local machine telemetry, hardware inventory,
health diagnostics, and a secure API. The terminal interface is the
primary user experience, while the API makes the same data available to
scripts, external applications, and future fleet dashboards.

## Product goals

-   Understand machine hardware and operating-system configuration.
-   Monitor CPU, memory, GPU (where supported), storage, network, and
    thermal metrics.
-   Identify health warnings and useful diagnostic context.
-   Retain optional historical metrics and system events.
-   Expose normalized data through a documented, authenticated API.
-   Allow additional collectors and integrations without rewriting the
    core.
-   Work locally without a cloud account.

## Architecture at a glance

``` text
Platform collectors
       |
       v
Collection + normalization services
       |
       +------> Health rules / events
       |
       +------> Optional local history
       |
       v
Versioned REST API + event stream
       |
       +------> Terminal UI (API client)
       +------> External applications
       +------> Future web / fleet clients
```

The TUI must not read `/proc`, `/sys`, or collector internals directly.
It consumes the API just like any other client.

## Initial feature areas

1.  Overview --- machine identity, health summary, live resource
    metrics.
2.  Hardware --- CPU, memory, GPU, motherboard, storage, and detected
    devices.
3.  Performance --- utilization, load, throughput, and process
    consumers.
4.  Health --- thermal state, storage health, memory pressure, failed
    services, and findings.
5.  Storage --- devices, filesystems, mount points, capacity, and
    SMART/NVMe data when available.
6.  Network --- interfaces, addresses, throughput, errors, and
    connectivity diagnostics.
7.  Processes --- process list and resource usage, subject to platform
    permissions.
8.  Services --- systemd service state on Linux.
9.  Events --- timestamped health findings and relevant system events.
10. API Access --- server status, keys, scopes, clients, and request
    statistics.

## Security defaults

-   API binds to `127.0.0.1` by default.
-   Remote binding is opt-in and requires TLS.
-   API keys are generated with a cryptographically secure random
    source.
-   Plaintext keys are displayed only once.
-   Only a hash of each key is stored.
-   Read-only access is the default.
-   Secrets and authorization headers must never be logged.

## Build status

This repository documentation describes the intended architecture and
product requirements. It does not imply that the application has already
been implemented.

## Documentation

-   [Claude Code project instructions](CLAUDE.md)
-   [Architecture](docs/ARCHITECTURE.md)
-   [API contract](docs/API.md)
-   [Security model](docs/SECURITY.md)
-   [Collector and plugin model](docs/PLUGINS.md)
-   [Roadmap](docs/ROADMAP.md)

## Proposed implementation sequence

Start with a thin vertical slice: 1. Go module, configuration, logging,
and lifecycle. 2. CPU/memory/system collectors. 3. Normalized domain
models and collection service. 4. Read-only REST API and OpenAPI. 5. API
key lifecycle and authentication. 6. Bubble Tea TUI consuming the API.
7. Storage, sensors, network, and health expansion.

See `docs/ROADMAP.md` for detailed acceptance criteria.
