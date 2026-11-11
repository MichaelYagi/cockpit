# Linux Cockpit --- Claude Code Project Guide

## Project intent

Build **Linux Cockpit**, a modular, API-first machine monitoring
platform. Its primary interface is a polished, keyboard-first terminal
UI inspired by the visual clarity of `btop`, but less crowded and more
structured.

The product is not just a hardware information screen. It should help
users understand: - What hardware and operating system a machine has. -
What the machine is doing now. - Whether the machine is healthy. - What
has changed or gone wrong over time. - How other applications and
machines can consume its data securely.

## Non-negotiable architecture principles

1.  **API-first:** the TUI is an API client. It must not contain
    monitoring or collection business logic.
2.  **Local-first:** the MVP works on one machine without a cloud
    account or external service.
3.  **Modular collectors:** hardware and operating-system data is
    gathered by independent collectors behind stable interfaces.
4.  **Portable contracts:** API models are normalized and do not expose
    Linux-specific implementation details.
5.  **Read-only by default:** monitoring endpoints are read-only. Any
    future mutating operation must be separately authorized and
    explicitly confirmed.
6.  **Secure by default:** remote API access requires TLS and
    authentication. Never expose a plaintext API key after its one-time
    creation display.
7.  **Graceful degradation:** unavailable sensors, permissions,
    commands, or hardware are reported as unsupported/unavailable, not
    treated as fatal errors.
8.  **Observable:** collection errors, API activity, health findings,
    and system events should be diagnosable.
9.  **No shell parsing as the primary architecture:** prefer `/proc`,
    `/sys`, netlink, system APIs, and well-defined libraries. Use
    external utilities only where appropriate and document the
    dependency.

## Initial technology direction

-   Language: Go.
-   TUI: Bubble Tea, Lip Gloss, and Bubbles.
-   HTTP API: Chi (preferred) or another small, well-supported router.
-   API description: OpenAPI 3.x.
-   Configuration: YAML with validated defaults.
-   Local persistence: SQLite for API keys metadata, settings, event
    history, and optional metric history.
-   Transport: HTTP on loopback by default; HTTPS for remote binding.
-   Streaming: Server-Sent Events for event/alert streams; WebSocket
    only when bidirectional streaming is justified.
-   Service integration: systemd on Linux.

Do not add a dependency without explaining why it is needed. Keep the
core binary reasonably small and avoid requiring a database server.

## Repository boundaries

Recommended layout:

``` text
cmd/
  cockpit/             # TUI executable
  agent/               # optional headless agent executable
internal/
  app/                 # application wiring and lifecycle
  config/              # config loading, defaults, validation
  core/                # domain models and interfaces
  collectors/          # platform-specific metric collectors
  health/               # rules and health evaluation
  metrics/              # sampling and normalization
  history/              # persistence and query service
  events/               # event model and event bus
  api/
    auth/
    handlers/
    middleware/
    routes/
    openapi/
  plugins/              # plugin registry and contracts
  storage/              # persistence implementations
  platform/
    linux/
pkg/
  client/               # public Go API client SDK
  models/               # only if models are intentionally public
docs/
  ARCHITECTURE.md
  API.md
  SECURITY.md
  PLUGINS.md
  ROADMAP.md
```

Keep package dependencies pointing inward toward domain contracts. UI
packages must depend on the API client, not collectors or storage
implementations.

## Collector contract

A collector should: - Have a stable identifier and version. - Declare
the metrics/capabilities it can provide. - Initialize and shut down
cleanly. - Respect context cancellation and deadlines. - Return
timestamped, typed data. - Report unsupported capabilities explicitly. -
Avoid blocking the whole collection cycle. - Avoid retaining unbounded
history in memory.

A simple starting interface:

``` go
type Collector interface {
    ID() string
    Version() string
    Capabilities() []Capability
    Collect(ctx context.Context) (Snapshot, error)
    Close(ctx context.Context) error
}
```

Do not blindly adopt a sample interface if it makes testing, partial
results, or platform differences harder. Propose changes before changing
a public contract.

## API rules

-   Prefix all endpoints with `/api/v1`.
-   Return JSON using stable, documented schemas.
-   Include a request ID in responses/logs.
-   Use UTC RFC 3339 timestamps.
-   Use consistent error envelopes.
-   Paginate potentially large collections such as processes and events.
-   Validate query parameters and enforce sensible limits.
-   Do not return secrets, raw API key hashes, or sensitive hardware
    identifiers by default.
-   Keep API handlers thin; business logic belongs in services.

## API key rules

-   Generate high-entropy random keys using `crypto/rand`.
-   Display the full key only once at creation/rotation time.
-   Persist only a secure hash (prefer a keyed HMAC-SHA-256 using a
    server-side pepper stored separately from the database, or a
    suitable password-hashing scheme); never persist plaintext.
-   Store a short non-secret key prefix/ID for identification.
-   Support name, scopes, creation time, optional expiry, last-used
    time, and revocation.
-   Compare secrets in constant time where applicable.
-   Never log keys, Authorization headers, or request bodies containing
    secrets.
-   Default to read-only scopes.
-   Require explicit user confirmation for revocation and rotation.
-   Do not enable remote binding without TLS and an explicit
    configuration change.

## TUI design direction

Visual language: - Dark charcoal background, not pure black. - Fine,
subtle panel borders. - Cyan/teal for neutral information; green for
healthy; amber for warnings; red for critical states. - Use color
sparingly and never rely on color alone to communicate status. - Compact
but readable typography and consistent spacing. - Live sparklines and
compact graphs where supported by terminal capabilities. -
Keyboard-first navigation, with mouse support where practical. -
Responsive layout: reflow or simplify panels at narrow terminal
widths. - Provide a useful minimum-size message rather than rendering
broken layouts.

Initial navigation: - Overview - Hardware - Performance - Health -
Storage - Network - Processes - Services - Events / Logs - API Access -
Settings

The TUI must display API server status, bind address, port, active
client count where available, request statistics, and API key
management. The full API key is shown only immediately after creation.

## Development workflow for Claude Code

Before implementation: 1. Inspect the existing repository and report its
current structure. 2. Read this file and the relevant documents under
`docs/`. 3. Identify the smallest coherent implementation slice. 4.
State assumptions and any architectural impact before changing public
interfaces.

During implementation: - Prefer small, testable changes. - Add unit
tests for domain logic and collectors. - Add HTTP handler and
authentication tests. - Use interfaces at real boundaries, not
everywhere. - Handle context cancellation and goroutine shutdown. -
Avoid global mutable state. - Do not introduce a plugin execution
sandbox or dynamic third-party code loading in the MVP. - Keep secrets
out of logs, test snapshots, fixtures, and error messages. - Run
`gofmt`, `go vet`, and relevant tests.

Before completing a task: - Summarize files changed and design
decisions. - List tests run and their results. - Identify limitations
and follow-up work. - Do not claim a command or test passed unless it
was actually run.

## MVP acceptance criteria

The first usable release should: - Run on a supported Linux distribution
without requiring root for ordinary metrics. - Show CPU, memory, load,
uptime, filesystem capacity, network rates, and available
temperatures. - Show detected hardware inventory with
source/availability metadata. - Expose the same information through
authenticated REST endpoints. - Bind to loopback by default. - Generate,
list, and revoke API keys; show a newly generated key once. - Provide a
polished TUI that consumes the API. - Handle missing sensors and
permissions without crashing. - Include OpenAPI documentation and setup
instructions. - Include tests for collectors, API auth, API handlers,
and key lifecycle.
