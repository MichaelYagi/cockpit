# Architecture

Cockpit is a local-first monitoring agent with a versioned API and one or
more clients. The first client is a terminal UI. Future clients include
scripts, SDKs, a web UI, and a fleet console.

The API is the boundary between data collection and presentation.
Approved decisions are recorded in [DECISIONS.md](DECISIONS.md) and
referenced below as D*n*.

## 1. Processes and runtime modes

One binary, `cockpit`, with subcommands (D2):

| Command | Role |
|---|---|
| `cockpit agent` | Monitoring engine + API server. Runs in the foreground; systemd unit `cockpit-agent.service` in system mode. |
| `cockpit tui` | Terminal UI. An API client only. |
| `cockpit keys create\|list\|rotate\|revoke` | Key management through the admin socket. |
| `cockpit config check` | Validate configuration and print the effective settings (secrets omitted). |
| `cockpit version` | Build and API version. |

```text
                    ┌────────────────────────── cockpit agent ───────────────────────────┐
                    │                                                                    │
 /proc /sys /etc ─▶ │ platform/linux collectors ─▶ engine ─▶ snapshot store (immutable)  │
 smartctl (opt.)    │                                │            │                      │
                    │                                ├─▶ health ──┤                      │
                    │                                └─▶ events bus (bounded)            │
                    │                                                │                   │
                    │      auth (keys) ◀─ storage/sqlite            ▼                   │
                    │                │                  api: middleware ─▶ handlers      │
                    │                └──────────────────▶ listeners:                     │
                    │                                     • Unix socket  (admin, D3)     │
                    │                                     • TCP loopback (API key, D4)   │
                    │                                     • TCP remote   (TLS + key)     │
                    └────────────────────────────────────────────────────────────────────┘
                                         ▲ HTTP/JSON (pkg/client)
                    ┌────────────────────┴───────┐     ┌──────────────────────────┐
                    │ cockpit tui                │     │ curl / scripts / SDKs    │
                    └────────────────────────────┘     └──────────────────────────┘
```

**Embedded agent.** If `cockpit tui` cannot reach an agent on the default
socket (and no `--socket` or `--url` was given), it starts an agent
in-process. Its logs go to `<state dir>/embedded-agent.log`, its socket is
`<runtime dir>/tui-<pid>.sock`, and it stops when the TUI exits. That agent listens only on a private Unix socket
(no TCP), and the TUI connects through `pkg/client` exactly as it would to
a separate agent. Only `internal/cli` contains the code that starts it.

**Single instance.** An agent takes an exclusive lock on
`<state dir>/agent.lock`. A second agent on the same state directory fails
with a clear message.

**Future fleet mode.** Agents keep the same per-machine API. A central
service either queries agents or receives pushed data. Every resource
already carries a `machine_id` (a random UUID created by the agent, not
`/etc/machine-id`) so data from many machines can be merged.

## 2. Components and responsibilities

| Component | Package | Responsibility | Must not |
|---|---|---|---|
| Domain model | `internal/core` | Platform-neutral types: snapshot sections, availability, capabilities, findings, events | Import any other internal package |
| Collector contract | `internal/collector` | `Collector` interface, descriptor, result, injectable environment, test fakes | Contain platform code |
| Platform collectors | `internal/platform/linux` | Read `/proc`, `/sys`, `/etc`, optional tools; return raw typed records | Compute rates, keep history, know about HTTP |
| Unsupported platform | `internal/platform/unsupported` | Default for non-Linux builds: everything `unsupported` | — |
| Engine | `internal/engine` | Schedule collectors, timeouts, panic isolation, derive rates, staleness, publish snapshots | Serve HTTP |
| Health | `internal/health` | Evaluate checks on snapshots, hysteresis, finding lifecycle | Collect data itself |
| Events | `internal/events` | Event model, bounded fan-out bus, in-memory ring buffer | Block publishers |
| Auth | `internal/auth` | Key generation, parsing, verification, scopes, rotation, revocation | Depend on HTTP |
| Storage | `internal/storage/sqlite` | Implements `auth.KeyStore` (later history); migrations | Contain business rules |
| API | `internal/api/...` | Listeners, middleware, thin handlers, mapping `core` → `pkg/apiv1`, redaction | Call collectors directly |
| Wire types | `pkg/apiv1` | Public JSON types matching the OpenAPI spec | Import `internal/...` |
| Client SDK | `pkg/client` | Typed Go client over TCP/TLS or Unix socket | Import `internal/...` |
| TUI | `internal/tui` | Navigation, layout, presentation | Import anything except `pkg/client`, `pkg/apiv1`, UI libraries |
| App wiring | `internal/app` | Build the agent object graph, ordered start and shutdown | Contain domain logic |
| CLI | `internal/cli` | Parse subcommands, start agent / TUI / key commands | — |

## 3. Dependency rules

```text
core ◀── collector ◀── platform/linux, platform/unsupported
  ▲          ▲
  ├── engine ┘ ◀── health
  ├── events
  ├── auth ◀── storage/sqlite
  └── api ──▶ engine.Reader, events.Subscriber, auth.Service, pkg/apiv1
app ──▶ everything above (the only place concrete types are wired)
cli ──▶ app, tui, config, pkg/client
tui ──▶ pkg/client, pkg/apiv1               (enforced by test)
pkg/client ──▶ pkg/apiv1                    (enforced by test)
```

## 4. How the engine communicates with the API

The engine and API run in the same process and communicate through narrow,
read-only Go interfaces, never HTTP:

```go
// engine
type Reader interface {
    Current() *core.Snapshot // immutable; never nil after start
}

// events
type Subscriber interface {
    Subscribe(ctx context.Context, f Filter) (<-chan core.Event, error)
    Recent(f Filter, page Page) ([]core.Event, string /*next cursor*/, error)
}
```

- Each collection cycle builds a **new immutable snapshot** and publishes
  it with `atomic.Pointer`. Readers never lock and never see partial
  updates.
- Handlers read the current snapshot. They never trigger collection. Rates,
  uptime and staleness are computed from timestamps at read time.
- The TUI polls (default every second) and never overlaps requests for the
  same resource. Events arrive live over SSE.
- Process lists are on demand: the API asks the engine, which samples
  `/proc` only when needed and caches for 2 seconds (see PLUGINS.md).
- The engine closes `Ready()` once every scheduled collector has reported
  once; health waits for it (at most 3 seconds) so its first evaluation
  sees real data.
  Expensive on-demand data (e.g. processes, Phase 2) goes through an engine
  method with a short cache.
- Event subscribers each get a bounded queue. If a subscriber falls behind,
  its oldest events are dropped and it receives a `lagged` marker, so
  publishers never block.
- A future remote/fleet implementation can satisfy the same interfaces.

## 5. Data flow

1. On start, the engine calls `Probe` on each collector to learn which
   capabilities exist and are permitted.
2. Schedulers run each collector on its own interval (with jitter), with a
   per-collector timeout, recovering panics.
3. Collectors return raw typed records plus per-capability status.
4. The engine derives rates (CPU %, bytes/s) from consecutive counter
   samples, handles counter resets, and marks sections stale when their
   last good data is older than twice the interval.
5. The engine publishes a new snapshot; health evaluates it and emits
   finding events on state changes.
6. API handlers map snapshot data to `pkg/apiv1` types, apply redaction
   (D9), and respond.

## 6. Availability model

Every snapshot section carries metadata:

| Field | Meaning |
|---|---|
| `status` | `available`, `unsupported`, `permission_denied`, `temporarily_unavailable`, `not_configured` |
| `collected_at` | Time of the data shown; `null` if never collected |
| `stale` | Data is older than 2× the collector interval |
| `source` | Non-sensitive origin, e.g. `procfs`, `sysfs/hwmon`, `smartctl` |
| `reason` | Safe, human-readable explanation when not available |

If a transient failure occurs after a good sample, the last good data is
kept with `stale: true` and `status: temporarily_unavailable`. Inside an
available section, optional fields are nullable and documented in the spec
(e.g. load average is Linux/macOS only).

## 7. Concurrency and lifecycle

- `context.Context` everywhere; every collector has a deadline.
- One goroutine per scheduled collector, not per metric or request.
- Bounded channels everywhere; no unbounded queues.
- Startup order: config → logging → state dir + lock → storage → auth →
  collectors (probe) → engine → health → API listeners.
- Shutdown is the reverse: stop accepting connections, close streams,
  drain in-flight requests (bounded), stop schedulers, cancel collection,
  flush storage (e.g. `last_used_at`), close collectors, release lock.
- No global mutable state. Collectors are listed explicitly by
  `linux.Collectors(env)`, not registered via `init()`.

## 8. Configuration

YAML file, strictly decoded (unknown keys are errors), with validated
defaults.

- **Precedence:** command-line flags > `COCKPIT_*` environment variables >
  config file > defaults.
- **Locations:** system mode `/etc/cockpit-monitor/config.yaml`; user
  mode `$XDG_CONFIG_HOME/cockpit-monitor/config.yaml`; or `--config`.
- **Validation** reports all errors at once. `cockpit config check`
  prints them and the effective configuration.
- **No secrets** in the YAML. TLS key and certificate are file paths whose
  permissions are checked at startup.
- Remote (non-loopback) TCP binding without TLS is a startup error.
- The socket's parent directory must be private (`0700` user mode, `0750`
  system mode) and owned by the agent's user. A custom `api.socket.path`
  in a shared directory such as `/tmp` is refused.
- No hot reload. Health thresholds can be changed at runtime over the
  admin socket (D19); everything else needs a restart.

Current schema with defaults. Run `cockpit config check` to print the
effective configuration.

```yaml
agent:
  state_dir: ""                # default: per mode (see DECISIONS D1)
  runtime_dir: ""              # default: per mode; holds the socket
  shutdown_timeout: 10s
api:
  socket:
    enabled: true
    path: ""                   # default: <runtime_dir>/agent.sock
  tcp:
    enabled: true
    address: 127.0.0.1:7878
  tls:
    cert_file: ""
    key_file: ""
  rate_limit:                  # TCP only; the socket is never limited
    per_key_rps: 20
    per_key_burst: 40
    global_rps: 200
    global_burst: 400
collection:
  defaults: { interval: 2s, timeout: 1s }   # samplers; inventory collectors keep their own
  collectors:                  # per-collector overrides, e.g.
    linux.smart: { interval: 1h, timeout: 30s }
  filesystems:                 # Q5; excluded mounts leave the API, history and health
    exclude_types: []          # added to the built-in pseudo/in-memory list
    include_types: []          # e.g. [tmpfs]; network filesystems cannot be included
    exclude_mount_points: []   # path.Match globs; "/dir/**" matches below /dir
  ups:                         # D25; Network UPS Tools, read-only
    enabled: false
    address: 127.0.0.1:3493
health:
  interval: 5s
  confirm: 2                   # evaluations a change must hold (anti-flapping)
  cpu_temperature: { warn_celsius: 85, critical_celsius: 95 }   # used only without hardware limits
  memory: { warn_available_percent: 10, critical_available_percent: 5, warn_pressure_percent: 5, critical_pressure_percent: 20 }
  storage: { warn_percent: 85, critical_percent: 95 }
  disk: { warn_percentage_used: 90 }
  network: { warn_errors_per_minute: 10, critical_errors_per_minute: 100 }
  # Thresholds can be overridden at runtime over the socket (D19).
history:                       # D18
  enabled: true
  retention: { raw: 24h, minute: 168h, hour: 2160h }
logging:
  level: info
  format: text                 # text | json
tui:
  refresh: 1s
```

## 9. Platform portability

- Platform code lives in `internal/platform/<os>` behind build tags.
- Non-Linux builds use `internal/platform/unsupported`, so the binary
  always compiles and runs, reporting capabilities as `unsupported`. CI
  cross-compiles for Linux, macOS, and Windows on every change.
- Local admin transport is behind a listener abstraction: Unix sockets on
  Linux/macOS, named pipes with ACLs on Windows later.
- Linux sources: `/proc` and `/sys` (hand-written parsers, D13), `statfs`,
  later systemd D-Bus and netlink where needed.
  `smartctl --json` is an optional external dependency. Parsing
  human-readable command output is not used.

## 10. Error handling

Typed errors internally. The API returns stable error codes in a consistent
envelope and never exposes stack traces, file contents, secrets or raw
command output. Partial data is always preferred to total failure.
