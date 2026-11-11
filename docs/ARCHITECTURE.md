# Architecture

## 1. Architectural objective

Linux Cockpit is a local-first monitoring agent and API server with one
or more clients. The initial client is a terminal UI. Future clients may
include a web interface, command-line scripts, SDKs, or a central fleet
console.

The API is the boundary between data collection and presentation.

## 2. Logical components

### Platform collectors

Collect raw operating-system and hardware information. Linux is the
first supported platform. Platform-specific implementations must be
hidden behind stable domain contracts.

### Collection service

Schedules collectors, applies timeouts, combines partial results,
timestamps samples, and publishes normalized snapshots. A failure in one
collector must not prevent unrelated collectors from reporting.

### Normalization/domain layer

Defines platform-neutral models for system identity, CPU, memory, GPU,
storage, network, sensors, processes, services, health findings, and
events. Models should include source and availability metadata where
useful.

### Health engine

Evaluates configurable rules against current and historical data. It
emits findings with severity, evidence, timestamps, and recommended next
steps. A health score, if shown, must be explainable and must not hide
individual findings.

### History service

Optionally persists samples and events to SQLite. Retention and sampling
intervals are configurable. Avoid writing high-frequency raw samples
indefinitely.

### API server

Exposes versioned REST endpoints and event streaming. It performs
authentication, authorization, validation, rate limiting, and request
logging.

### TUI

A client of the API. It handles navigation, layout, presentation, and
user interactions. It does not directly invoke collectors or access
persistence.

### Plugin/collector registry

Registers built-in collectors and future extensions. The MVP should use
statically compiled, trusted collectors. Dynamic loading of arbitrary
third-party code is explicitly out of scope.

## 3. Runtime modes

### Standalone

One process runs collectors, health evaluation, persistence, API server,
and TUI. The TUI connects through the local API. Prefer an in-process
API client using the same HTTP handlers or a loopback HTTP connection;
do not bypass API semantics.

### Headless agent

Runs collectors and API server without the TUI, suitable for remote
machines.

### Future fleet mode

Multiple headless agents report to or are queried by a central service.
Fleet aggregation, enrollment, and central identity are future
capabilities, not MVP requirements.

## 4. Data flow

1.  Scheduler triggers collectors at configured intervals.
2.  Collectors return typed snapshots or partial errors.
3.  Collection service normalizes timestamps and source metadata.
4.  Health engine evaluates rules and publishes findings/events.
5.  History service persists selected samples and events.
6.  API handlers expose current snapshots and historical queries.
7.  TUI and external clients consume API responses.

## 5. Concurrency and lifecycle

-   Use `context.Context` for cancellation and deadlines.
-   Each collector has a bounded execution time.
-   Collectors should not share mutable state unless synchronized.
-   Use bounded channels or explicit backpressure for events.
-   Shutdown should stop scheduling, cancel in-flight work, flush
    persistence, and close collectors.
-   Avoid one goroutine per metric or per API request without limits.
-   A slow collector must not stall the whole dashboard.

## 6. Configuration

Configuration should support: - API bind address and port. - TLS
certificate/key paths. - Collection intervals and per-collector
enablement. - History enablement and retention. - API key
metadata/storage location. - Health thresholds. - Logging level and
output. - TUI theme and refresh interval.

Precedence: command-line flags \> environment variables \> configuration
file \> safe defaults. Never put plaintext API keys in the main YAML
config.

## 7. Platform portability

Define capability-oriented interfaces rather than assuming every OS
exposes every metric. An API response should distinguish: - `available`:
collected successfully. - `unsupported`: platform or hardware does not
provide it. - `permission_denied`: capability exists but current
privileges are insufficient. - `temporarily_unavailable`: transient
collection failure. - `not_configured`: optional integration is
disabled.

Linux implementation may use `/proc`, `/sys`, netlink, systemd D-Bus,
and vetted libraries. External tools such as `smartctl` or `nvme` may be
optional integrations. Do not make command-line output parsing the
primary source for ordinary metrics.

## 8. Error handling

Use typed errors internally. API errors should be stable and safe for
clients. Never expose stack traces, filesystem secrets, API key
material, or raw command output containing sensitive data.

Partial data is preferable to total failure. Include per-section status
and collection timestamp so clients can tell stale data from missing
data.
