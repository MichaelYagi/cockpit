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

**Status: implemented, awaiting review.** Every milestone below is done,
verified with tests and against a real WSL2 host (systemd, NVIDIA GPU via
`nvidia-smi`; no USB, DMI or sensors, all reported with reasons). D14–D17
accepted.

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

**Status: done, approved and merged (PR #4).** Every milestone below is done,
verified with tests and against a real WSL2 host: history and charts, a
real diagnostic report checked for leaks, and the agent run under the
unit's seccomp and memory restrictions in a transient systemd unit. The
units pass `systemd-analyze verify`; nothing was installed system-wide,
so the full system service has not been run as root here. D18–D21 are
accepted (D20 and D21 with the clarifications noted there).

| # | Milestone | Depends on |
|---|---|---|
| 3A | Metric history: recorder sampling the snapshot every 10s into SQLite, batched writes, rollups to 1-minute and 1-hour tiers, retention per tier (D18) | 1C |
| 3B | `GET /metrics/history` and `/metrics/history/series`: series, time range, automatic resolution, point limits | 3A |
| 3C | Event persistence: events written to SQLite asynchronously, loaded on start, filtered and paginated from storage, bounded retention | 1E |
| 3D | TUI: Performance screen with charts over 1h/6h/24h/7d; sparklines seeded from history; event filtering and paging | 3B, 3C |
| 3E | Settings: health thresholds editable through a socket-only admin API (`GET`/`PUT /settings`), persisted, applied without restart, confirmed in the TUI (D19) | 1E |
| 3F | Diagnostic report: `cockpit report` and `GET /report` produce a redacted JSON or Markdown snapshot; `--anonymize` hides host names and addresses (D21) | 2* |
| 3G | Packaging: `cockpit-agent.service` with systemd hardening, `cockpit-monitor` user/group (sysusers.d), install/uninstall script, upgrade docs; SMART helper timer running `smartctl` as root and writing results the unprivileged agent reads (Q1, D20) | all |
| 3H | Configurable filesystem skip lists (Q5); docs, spec and security review | all |

**Exit criteria:** the agent runs unattended as a hardened system service
without root, keeps bounded history and events across restarts, serves
history through the API, shows charts in the TUI, lets the local admin
adjust thresholds, and exports a redacted diagnostic report.

## Phase 4 — Extensibility

**Status: in progress.** Decisions D22–D26 accepted.

| # | Milestone | Depends on |
|---|---|---|
| 4A ✓ | Bare `cockpit` opens the TUI in a terminal and prints help otherwise (Q2) | — |
| 4B ✓ | Generic metrics (D22): `core.GenericMetric`, counter rates, name/label/unit validation, per-collector series cap, `custom` section of `/metrics`, opt-in history, shown on the Performance screen | — |
| 4C ✓ | Collector SDK (D23): `pkg/collectorsdk`, the internal adapter, `custom.Main`, SDK version checks, `external` and `sdk_version` in `/status`; PLUGINS.md rewritten as a guide for collector authors | 4B |
| 4D ✓ | Example extension: a collector in its own module under `examples/collector/` (open file handles and entropy, from `/proc/sys`), built with `custom.Main` in CI and tested end to end | 4C |
| 4E | Containers (D24): `linux.containers` from cgroup v2, `GET /containers` (`processes:read`), Containers screen, history series for the busiest containers | — |
| 4F | ZFS, batteries and UPS (D25): `linux.zfs`, `linux.power`, `linux.ups` (opt-in), health checks for pools and UPS, Storage and Overview additions | — |
| 4G | More GPU metrics: Intel (`i915`/`xe`) frequency and power from sysfs; utilization reported unavailable where it needs perf privileges (extends D16) | — |
| 4H | SDKs and releases (D26): `pkg/client` examples as Go example tests, stability policy, `CHANGELOG.md`, Python and TypeScript examples (no dependencies) run in CI against a live agent, release workflow with checksums, tag `v0.4.0` | 4C |
| 4I | Docs, spec and security review; check the extension trust model and the new data for sensitive fields | all |

**Exit criteria:** someone can build Cockpit with their own collector
without forking, its metrics appear in the API, TUI and history with the
same availability reporting as built-ins; containers, ZFS pools,
batteries and UPS are monitored where present; and a versioned release
with checksums and a documented Go SDK is published.

**Not in Phase 4:** runtime plugin loading, the Docker/Podman API socket
(Q8), write operations of any kind, Windows and macOS (Phase 5).

## Phase 5 — Multi-machine and cross-platform

- Remote TLS guidance and certificate tooling.
- Agent identity and enrollment design; fleet console.
- Windows and macOS collectors; Windows named-pipe admin transport.
- Platform capability conformance tests.

## Open questions

| # | Question | Needed by | Current recommendation |
|---|---|---|---|
| Q1 | How does disk health (D11) get SMART privileges without running the agent as root? | 3 | **Phase 1 done as approved:** the agent runs `smartctl` directly and reports `permission_denied` when unprivileged. **Phase 3 done (D20):** `cockpit-smart.timer` runs `cockpit smart-helper` as root hourly and writes results to `/var/lib/cockpit-monitor/smart/` (group-readable), which the agent reads. |
| Q2 | Should bare `cockpit` (no subcommand) print help or launch the TUI? | 4 | **Done (4A):** launches the TUI when stdin and stdout are terminals; otherwise prints help. `cockpit help` and `-h` keep printing help. |
| Q5 | Should the filesystem skip lists become configurable? | 3 | **Done (Phase 3):** `collection.filesystems` with `exclude_types`, `include_types` and `exclude_mount_points`. Network filesystems stay excluded. |
| Q6 | Should a dedicated scope cover network addresses, routes and DNS? | 3 | No for now: documented as visible with `metrics:read`. Revisit with fleet mode. |
| Q7 | Should command lines ever be exposed (behind a new scope)? | 3+ | Not until there is a clear need; they often contain secrets. |
| Q8 | Should container names and images come from the Docker/Podman API socket? | 4+ | No in Phase 4: that socket is root-equivalent (D24). Revisit if a read-only, unprivileged source appears, or as an explicit opt-in with a documented risk. |
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
