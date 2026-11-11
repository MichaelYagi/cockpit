# Roadmap

## Guiding principle

Build a reliable vertical slice before broadening hardware coverage. Prove
early that collectors, normalized models, API authentication, and the TUI
work together through the API boundary.

Each phase requires review and approval before the next one starts.

## Phase 0 — Foundation

**Status: implemented, awaiting review.** All milestones below are done and
every exit criterion has been checked with tests and against the built
binary.

Goal: a running, well-structured agent skeleton with configuration,
logging, lifecycle, both listeners, the API conventions, and CI. No real
collectors yet.

| # | Milestone | Depends on | Deliverables |
|---|---|---|---|
| 0.1 | Module and tooling | — | `go.mod` (`github.com/MichaelYagi/cockpit`, `go 1.27`, `toolchain go1.27.4`, `tool` directives for staticcheck and govulncheck); package skeleton; `Makefile` (`fmt`, `vet`, `lint`, `test`, `test-integration`, `cross`, `vuln`); GitHub Actions CI; `.gitignore`; `.editorconfig` |
| 0.2 | Configuration | 0.1 | `internal/config`: defaults, strict YAML decoding, `COCKPIT_*` env, flags, precedence, validation reporting all errors, path resolution for system vs user mode; tests |
| 0.3 | Logging and request IDs | 0.1 | `log/slog` setup (text/JSON, level); redaction helper; request ID generator (`req_` + ULID); tests proving keys are redacted |
| 0.4 | Domain and collector contracts | 0.1 | `internal/core` (availability, capability status, section metadata, snapshot skeleton, finding, event, machine ID); `internal/collector` (interface, descriptor, result, env, fakes); `internal/platform/unsupported` |
| 0.5 | Agent lifecycle | 0.2, 0.3 | `internal/app`: state/runtime directories with permission checks, single-instance lock, machine ID file, signal handling, ordered start and shutdown with deadlines |
| 0.6 | API skeleton | 0.4, 0.5 | `api/openapi.yaml` (conventions, error envelope, `livez`, `openapi.json`, `status`); `pkg/apiv1` types; `internal/api` server with Unix socket and loopback TCP listeners, remote-bind/TLS guard, middleware (request ID, recovery, access log, `Host` check, body limit, no CORS); auth middleware wired with a stub that rejects all keys; contract-test harness |
| 0.7 | CLI and client skeleton | 0.5, 0.6 | `cmd/cockpit` + `internal/cli`: `agent`, `tui` (placeholder that connects via `pkg/client` and prints status), `config check`, `version`; `pkg/client` with Unix socket and TCP transports |
| 0.8 | Guard rails | 0.6, 0.7 | Import-boundary test; route-table scope test; cross-compile job; `goleak` in lifecycle tests |

**Phase 0 dependencies (runtime):** `github.com/go-chi/chi/v5`,
`go.yaml.in/yaml/v3`, `golang.org/x/sys`. **Test-only:**
`github.com/getkin/kin-openapi`, `go.uber.org/goleak`. **Tools:**
`honnef.co/go/tools` (staticcheck), `golang.org/x/vuln` (govulncheck).
SQLite and Bubble Tea arrive in Phase 1.

**Exit criteria:**

- `cockpit agent` starts with validated configuration, creates its socket
  with correct permissions, serves `/api/v1/livez`, `/api/v1/openapi.json`
  and `/api/v1/status` on the socket and loopback TCP, and shuts down
  cleanly on SIGINT/SIGTERM within the configured deadline.
- TCP requests to `/status` without a key return `401` in the standard
  error envelope with a request ID.
- A second agent on the same state directory fails with a clear message.
- A non-loopback bind without TLS fails at startup.
- `cockpit config check` reports all validation errors.
- `cockpit tui` (placeholder) reaches the agent only through `pkg/client`.
- `gofmt`, `go vet`, staticcheck, `go test -race ./...` and govulncheck
  pass in CI; Linux, macOS and Windows cross-compile.

## Phase 1 — MVP vertical slice

**Status: implemented, awaiting review.** Every milestone below is done.
The exit criteria were checked with automated tests and against the built
binary on WSL2 (no sensors, no smartctl: both reported as unavailable with
reasons). Every TUI screen was also rendered from live data and reviewed at
80×24 and 120×40.

| # | Milestone | Depends on |
|---|---|---|
| 1A | Engine: scheduler with jitter, timeouts, panic isolation, rate derivation, staleness, snapshot store | 0.4 |
| 1B | Linux collectors: system, cpu, memory, filesystems, block devices, network, sensors, smart (with fixtures) | 0.4 (in parallel with 1A) |
| 1C | SQLite + migrations; `auth` key service (create, list, rotate, revoke, verify); `cockpit keys` CLI over the socket | 0.5 |
| 1D | API: real auth and scope middleware, rate limiting, redaction, Phase 1 endpoints, contract tests | 1A, 1C |
| 1E | Health: the four D11 checks with hysteresis; in-memory events | 1A, 1B |
| 1F | `pkg/client` coverage for all Phase 1 endpoints | spec from 1D |
| 1G | TUI (Bubble Tea v2): frame, navigation, minimum-size message, theme; Overview, Health, Storage, Network, API Access screens. Other screens hidden until implemented | 1F (can start against a mock server once the spec is fixed) |
| 1H | Embedded agent mode in `cockpit tui` | 1G |
| 1I | Setup docs, OpenAPI review, security review, docs match behavior | all |

**Exit criteria:** on a supported Linux host, without root, the TUI shows
live CPU, memory, load, uptime, filesystem, network, temperature (where
available) and health from the API; `curl` with a `metrics:read` key gets
the same data; keys can be created (shown once), listed, rotated and
revoked; missing sensors and permissions are reported, not fatal.

## Phase 2 — Hardware and diagnostics

**Status: in progress.**

| # | Milestone | Depends on |
|---|---|---|
| 2A | Event streaming: bounded per-subscriber queues with a lag marker; `GET /events/stream` (SSE) with heartbeats, stream limits, and re-verification of the key so revoked or expired keys are disconnected (D17) | 1E |
| 2B | Processes: on-demand sampler with a short cache and per-process CPU %; `GET /processes` with sorting, search and snapshot-bound cursors; new `processes:read` scope; command lines redacted (D9, D14) | 1A |
| 2C | systemd services over D-Bus (`godbus/dbus`, D15); `GET /services`; unavailable when systemd or the bus is missing | 1A |
| 2D | Hardware inventory: CPU topology, caches and flags; PCI and USB devices; board/firmware where readable (DMI, often root-only); GPUs from PCI/DRM with vendor metrics (NVIDIA via `nvidia-smi` CSV, AMD via sysfs; D16) | 1B |
| 2E | Health rules: failed services, rising network errors; collector degraded/recovered events | 2C, 1E |
| 2F | Passive network diagnostics: addresses, default routes, DNS servers (no probing) | 1B |
| 2G | TUI: Hardware, Processes, Services and Events screens; GPU on Overview; live events via SSE | 2A–2F |
| 2H | Docs, spec review, security review | all |

**Exit criteria:** on a supported host without root, the TUI and API show
processes, services, detailed hardware (with unavailable parts explained),
GPU metrics where a supported GPU exists, live events, and the new health
findings; unprivileged limits (other users' process I/O, DMI tables,
SMART) are reported, not fatal.

## Phase 3 — History and operational polish

- SQLite metric history with retention and downsampling; `/metrics/history`.
- Historical sparklines and charts in the TUI.
- Event persistence, filtering and pagination.
- Configurable thresholds and refresh intervals in the TUI.
- Diagnostic report export (redacted).
- `cockpit-agent.service`, `cockpit-monitor` user/group, packaging,
  installation and upgrade docs.

## Phase 4 — Extensibility

- Collector SDK documentation, `core.GenericMetric`, compatibility metadata.
- Example external collector compiled into a development build.
- Published Go SDK; Python and TypeScript examples.
- Docker/Podman, ZFS, UPS, additional GPU collectors.

## Phase 5 — Multi-machine and cross-platform

- Remote TLS guidance and certificate tooling.
- Agent identity and enrollment design; fleet console.
- Windows and macOS collectors; Windows named-pipe admin transport.
- Platform capability conformance tests.

## Open questions

| # | Question | Needed by | Current recommendation |
|---|---|---|---|
| Q1 | How does disk health (D11) get SMART privileges without running the agent as root? | 3 | **Phase 1 done as approved:** the agent runs `smartctl` directly and reports `permission_denied` when unprivileged. Phase 3: a root-owned systemd timer runs `smartctl --json` and writes results to `/var/lib/cockpit-monitor/smart/` (group-readable), which the agent reads. |
| Q2 | Should bare `cockpit` (no subcommand) print help or launch the TUI? | 2 | Still prints help. Launching the TUI is now reasonable since it can embed an agent. |
| Q5 | Should the filesystem skip lists become configurable? | 3 | Yes, with the history work. |
| Q3 | Default TCP port | 0.6 | `7878` (avoids 9090, used by Red Hat Cockpit) |

## Non-goals for the MVP

Cloud control plane; user accounts or billing; runtime plugin loading;
remote shell or command execution; automated remediation; full Kubernetes
observability; firewall or network changes; AI diagnosis that sends data
off the machine.

## Quality gates

A milestone is complete only when `gofmt`, `go vet`, staticcheck and
`go test -race ./...` pass; the OpenAPI spec and docs match behavior;
security implications are reviewed; and missing-permission and
unsupported-hardware cases are tested.
