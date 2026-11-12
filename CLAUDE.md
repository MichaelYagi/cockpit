# Cockpit — Claude Code Project Guide

## Project intent

Build **Cockpit**, a modular, API-first machine monitoring platform. Its
primary interface is a polished, keyboard-first terminal UI inspired by the
visual clarity of `btop`, but less crowded and more structured.

Cockpit is unrelated to the Red Hat Cockpit web console. System identifiers
use `cockpit-monitor` / `cockpit-agent` to avoid collisions (D1).

The product helps users understand:

- What hardware and operating system a machine has.
- What the machine is doing now.
- Whether the machine is healthy.
- What has changed or gone wrong over time.
- How other applications and machines can consume its data securely.

**Approved decisions live in `docs/DECISIONS.md` (D1–D12). They override
anything else in this file if the two ever disagree.** Propose a new
decision entry before deviating from one.

## Non-negotiable architecture principles

1. **API-first:** the TUI is an API client. It contains no monitoring or
   collection logic.
2. **Local-first:** works on one machine without a cloud account or
   external service.
3. **Modular collectors:** data is gathered by independent collectors behind
   stable interfaces.
4. **Portable contracts:** API models are normalized and do not expose
   Linux-specific implementation details.
5. **Read-only by default:** monitoring endpoints are read-only. Any future
   mutating operation must be separately authorized and explicitly
   confirmed.
6. **Secure by default:** TCP access always requires an API key, including
   from localhost (D4). Remote access additionally requires TLS. Key
   management is available only on the local Unix socket (D3). A plaintext
   key is shown only once.
7. **Graceful degradation:** unavailable sensors, permissions, commands or
   hardware are reported as unavailable, never treated as fatal.
8. **Observable:** collection errors, API activity, health findings and
   system events are diagnosable.
9. **No shell parsing as the primary architecture:** use `/proc`, `/sys`,
   netlink, system APIs and well-defined libraries. External tools are
   optional, run without a shell, use structured output (e.g.
   `smartctl --json`), and are documented.

## Names and paths (D1)

| Item | Value |
|---|---|
| Module | `github.com/MichaelYagi/cockpit` |
| Executable | `cockpit` (`agent`, `tui`, `keys`, `report`, `tls`, `smart-helper`, `config check`, `version`) |
| systemd units | `cockpit-agent.service`, `cockpit-smart.service` / `.timer` (D20) |
| System config / data / runtime | `/etc/cockpit-monitor/`, `/var/lib/cockpit-monitor/`, `/run/cockpit-monitor/` |
| User-mode dirs | `$XDG_{CONFIG,STATE,RUNTIME}_HOME/cockpit-monitor/` |
| Env prefix | `COCKPIT_` |
| API key prefix | `ckp_` |
| Default TCP address | `127.0.0.1:7878` |

Never create system files, units or users named plain `cockpit`.

## Technology

- Go **1.27.4**, pinned in `go.mod` (`toolchain` directive) (D10).
- TUI: Bubble Tea v2 (`charm.land/bubbletea/v2`) and Lip Gloss v2. Bubbles
  is not used yet (the few widgets needed are small).
- HTTP: `github.com/go-chi/chi/v5`.
- API contract: OpenAPI 3.1 in `api/openapi.yaml`, spec-first, hand-written
  types, no codegen; conformance tested with `kin-openapi` (D6).
- Config: YAML via `go.yaml.in/yaml/v3`, strict decoding.
- Logging: `log/slog`.
- Persistence: SQLite via `modernc.org/sqlite`, `CGO_ENABLED=0` (D7).
- Linux data: hand-written parsers for `/proc` and `/sys` (see D13),
  `golang.org/x/sys/unix` for `statfs`, `smartctl --json` (optional).
- Rate limiting: `golang.org/x/time/rate`.
- systemd: `github.com/godbus/dbus/v5` (read-only `ListUnits`, D15).
- Windows admin pipe: `github.com/Microsoft/go-winio` (D31).
- GPU metrics: `nvidia-smi` CSV query and `amdgpu` sysfs (optional, D16).
- Streaming: SSE (Phase 2). No WebSocket planned.
- Service integration: systemd (`cockpit-agent.service`).

Do not add a dependency without explaining why it is needed. Keep the
binary reasonably small and never require a database server.

## Repository layout

```text
cmd/cockpit/                 # main: delegates to internal/cli
api/
  openapi.yaml               # authoritative API contract
  spec.go                    # package openapi: embeds the spec, converts to JSON
internal/
  archtest/                  # tests enforcing the package dependency rules
  buildinfo/                 # version, commit, API version
  cli/                       # subcommands; the only place that starts an embedded agent
  app/                       # agent wiring and ordered lifecycle
  config/                    # defaults, YAML/env/flags, validation, paths
  core/                      # platform-neutral domain types (imports nothing internal)
  collector/                 # Collector contract, descriptor, result, env, fakes
  platform/
    linux/                   # Linux collectors (+ testdata/ fixture trees)
    darwin/, windows/        # macOS and Windows collectors without cgo (D32)
    portable/                # shared helpers (interfaces, collector shell)
    unsupported/             # other systems, and Rest for unimplemented capabilities
  engine/                    # scheduling, timeouts, rates, staleness, snapshot store
  health/                    # health checks, hysteresis, findings
  events/                    # event bus and ring buffer
  auth/                      # API keys and scopes (no HTTP)
  storage/sqlite/            # key store (later history), migrations
  history/                   # metric history: recorder, tiers, retention (D18)
  settings/                  # runtime health thresholds (D19)
  report/                    # diagnostic report anonymization and Markdown (D21)
  logging/                   # slog setup, secret redaction, UTC timestamps
  netaddr/                   # loopback classification for listen and Host addresses
  tlsutil/                   # private CA and server certificates (cockpit tls, D29)
  ulid/                      # request and event IDs
  extension/                 # adapts collectorsdk collectors to the internal contract
  api/
    router.go                # route table: every route declares a scope or Public
    httpx/                   # request context values, JSON and error envelope helpers
    contracttest/            # test helper: validates traffic against the spec
    server/                  # listeners: Unix socket, TCP, TLS; bind guards
    middleware/              # request ID, recovery, logging, auth, scopes, rate limit, Host check
    handlers/                # thin handlers: decode → service → map → encode
    redact/                  # D9 sensitive-field policy
  web/                       # browser dashboard (D35): embedded static files, no data, API client
  tui/                       # Bubble Tea app: model.go (state, input, SSE), view.go, theme.go,
                             # sanitize.go (control characters stripped from API text)
pkg/
  apiv1/                     # public wire types matching the spec
  client/                    # public Go SDK (Unix socket, TCP, TLS)
  collectorsdk/              # public extension collector contract (D23)
custom/                      # custom.Main: build cockpit with extension collectors
examples/
  collector/                 # example extension build (own module; make examples)
  python/, typescript/       # dependency-free API clients, run against a live agent in tests
CHANGELOG.md                 # release notes; semver policy for pkg/* (D26)
deploy/                      # systemd units, sysusers.d entry, install/uninstall scripts (+ tests)
docs/
```

## Package dependency rules

- `internal/core` imports no other internal package.
- `internal/platform/*` depends on `collector` and `core` only.
- `internal/api` reads engine state through `engine.Reader` and
  `events.Subscriber`; it never calls collectors.
- `internal/app` is the only package that wires concrete implementations
  (`custom` and `cmd/cockpit` only hand collectors to the CLI).
- `internal/web` imports nothing internal: it is static files (D35).
- `pkg/collectorsdk` imports nothing; `internal/extension` imports only
  `collector`, `core` and the SDK.
- **`internal/tui` imports only `pkg/client`, `pkg/apiv1` and UI
  libraries.** `pkg/*` never imports `internal/*`. Both rules are enforced
  by a test.

## Collector contract

See `docs/PLUGINS.md` for the full contract. Summary:

```go
type Collector interface {
    Descriptor() Descriptor // ID, version, capabilities, class, default interval/timeout
    Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus
    Collect(ctx context.Context) Result // typed records + per-capability status; no all-or-nothing error
    Close(ctx context.Context) error
}
```

- Collectors read through injected roots (`Env.ProcRoot`, `SysRoot`, …) so
  they can be tested against fixtures.
- Collectors return raw counters; the engine derives rates.
- No `init()` registration; `linux.Collectors(env)` lists them explicitly.
- Extension collectors use `pkg/collectorsdk` and `custom.Main` (D23);
  they report generic metrics (D22) and are compiled in, never loaded.
- Respect context deadlines; keep any state bounded.

Propose changes before changing a public contract.

## API rules

- All endpoints are under `/api/v1`. Change `api/openapi.yaml` first, in the
  same change as the code.
- JSON with stable, documented schemas; `snake_case`; units in field names.
- `X-Request-ID` on every response and in logs.
- UTC RFC 3339 timestamps.
- Consistent error envelope (`docs/API.md` §7).
- Availability reported per section (`status`, `collected_at`, `stale`,
  `source`, `reason`).
- Paginate large collections; validate parameters and enforce limits.
- Sensitive fields (command lines, serials, MACs, WWNs, hardware UUIDs) are
  omitted by default and listed in `redacted` (D9).
- Every route declares one required scope (`system:read`, `metrics:read`,
  `admin`) or is explicitly public. No scope implies another (D8).
- Handlers are thin; business logic belongs in services.

## API key rules (D5)

- Format `ckp_<12-char base32 id>_<43-char base62 secret>`; 256-bit secret
  from `crypto/rand`.
- Store only `SHA-256(full key)` plus metadata (name, scopes, created,
  expiry, last used, revoked, rotated-from).
- Look up by ID; compare hashes with `crypto/subtle`.
- Show the full key once at creation or rotation; never again.
- Never log keys, `Authorization` headers, or request bodies containing
  keys.
- Key management (create, list, rotate, revoke) only over the Unix socket.
  Phase 1 keys may hold only read scopes.
- Rotation and revocation require explicit confirmation in the TUI and CLI.
- Never enable remote binding without TLS and an explicit configuration
  change.

## TUI design direction

Visual language:

- Dark charcoal background, not pure black. Accents come from the
  selected theme (`theme.go`); status colors are the same in every theme.
- Fine, subtle panel borders.
- Cyan/teal for neutral information; green healthy; amber warning; red
  critical.
- Use color sparingly and never rely on color alone for status.
- Compact but readable; consistent spacing.
- Live sparklines and compact graphs where the terminal supports them.
- Keyboard-first, with mouse support where practical.
- Reflow or simplify panels at narrow widths; show a minimum-size message
  instead of a broken layout.

Navigation: Overview, Hardware, Performance, Health, Storage, Network,
Processes (with a Containers view, `c`), Services, Events / Logs, API
Access, Settings. Screens without
an implementation are hidden, not shown empty (all are implemented as of
Phase 3).

Text from the API (process names, device strings, descriptions) is
untrusted: never render it without the sanitization in `sanitize.go`.

The TUI shows API server status, listeners, active client count, request
statistics and API key management. A full key is shown only immediately
after creation or rotation.

## Development workflow for Claude Code

Before implementation:

1. Inspect the repository and report its current structure.
2. Read this file and the relevant documents under `docs/`.
3. Identify the smallest coherent implementation slice.
4. State assumptions and any architectural impact before changing public
   interfaces.
5. Do not start a new phase until the previous one is reviewed and approved.

During implementation:

- Small, testable changes.
- Unit tests for domain logic and collectors; handler, auth and contract
  tests for the API (see `docs/TESTING.md`).
- Interfaces at real boundaries only.
- Handle context cancellation and goroutine shutdown.
- No global mutable state.
- No plugin sandbox or dynamic third-party code loading in the MVP.
- Keep secrets and real host identifiers out of logs, fixtures, test
  snapshots and error messages.
- Run `make check` (gofmt, vet, staticcheck, race tests, cross-compile,
  example extension build)
  and `make vuln`.

Before completing a task:

- Summarize files changed and design decisions.
- List tests run and their results.
- Identify limitations and follow-up work.
- Never claim a command or test passed unless it was actually run.
- Update docs in the same change as the behavior they describe, and
  `CHANGELOG.md` for user-visible changes.

## MVP acceptance criteria

The first usable release:

- Runs on a supported Linux distribution without root for ordinary metrics.
- Shows CPU, memory, load, uptime, filesystem capacity, network rates and
  available temperatures.
- Shows detected hardware inventory with source and availability metadata.
- Runs the four Phase 1 health checks (D11) and reports `unknown` with a
  reason when a check cannot run.
- Exposes the same information through authenticated REST endpoints.
- Binds TCP to loopback by default and requires keys on every TCP request.
- Creates, lists, rotates and revokes API keys over the local socket; shows
  a new key once.
- Provides a polished TUI that consumes the API.
- Handles missing sensors and permissions without crashing.
- Includes the OpenAPI document and setup instructions.
- Includes tests for collectors, API auth, handlers, contracts and key
  lifecycle.
