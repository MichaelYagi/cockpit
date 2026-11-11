# Changelog

Cockpit follows [semantic versioning](https://semver.org/) for its
releases and Go packages (D26). While the version is `0.x`, a minor
release may change `pkg/client`, `pkg/apiv1` or `pkg/collectorsdk`, and
every such change is listed here under **Breaking**. The REST API keeps
its own promise: `/api/v1` responses only ever gain fields and endpoints.

## v0.5.0 — unreleased

### Changed

- **New `network:read` scope (D27).** Interface addresses, routes and DNS
  now need it. Existing keys with `metrics:read` were migrated to include
  it, so nothing changes on upgrade. New keys must request it
  (`cockpit keys create --scope network:read`).

### Added

- **macOS and Windows (D32).** Identity, uptime, CPU (load average on
  macOS, usage on Windows), memory, filesystems and network with counters,
  without cgo. Every other capability is reported unsupported with a
  reason. CI runs the conformance suite and real-value checks on both.
- **Fleet console (D30).** `cockpit fleet add/list/remove/check` and the
  `cockpit fleet` console: one row per machine with health, CPU, memory,
  fullest disk and last contact, polled concurrently with pinned TLS.
  Enter opens the full TUI. Machines whose ID or certificate changes are
  refused.
- **TLS tooling (D29).** `cockpit tls init`, `renew` and `show` create and
  manage a private CA and server certificate. Clients pin the certificate
  (`--tls-fingerprint`, `COCKPIT_TLS_FINGERPRINT`,
  `client.WithPinnedCertificate`) or trust the CA (`--tls-ca`,
  `COCKPIT_TLS_CA`, `client.WithCAPEM`). The agent warns before expiry and
  reports `tls_expires_at`.
- Processes report their `executable` path (from `/proc/<pid>/exe`), or an
  `executable_reason` when it cannot be read. Command lines stay closed
  for good (D28).

## v0.4.0 — 2026-11-11

The first tagged release. It contains everything from Phases 1–4.

### Added

- **Extensions (D22, D23).** Third-party collectors written against the
  public SDK (`pkg/collectorsdk`, version 1) and compiled in with
  `custom.Main`. Their generic metrics, with validation, limits and
  counter rates, appear in `/metrics?sections=custom`, history, reports
  and the TUI. `/status` marks them `external`. See `docs/PLUGINS.md` §8
  and `examples/collector`.
- **Containers (D24).** Docker, Podman (rootful and rootless), containerd
  and CRI-O from cgroup v2: `GET /containers` (`processes:read`), history
  for the busiest five, and a Containers view in the TUI (`c` on
  Processes). The engine API socket is never used.
- **ZFS, batteries and UPS (D25).** Pool state and ARC, with capacity and
  errors from OpenZFS 2.3+ JSON. Batteries and mains from sysfs. UPS from a
  Network UPS Tools server (opt-in, read-only). New `zfs_pools` and `ups`
  health checks.
- **Intel GPUs.** `i915`/`xe` frequency, and power from hwmon energy
  (`frequency_mhz`, `max_frequency_mhz`).
- **Clients.** Go SDK examples (`go doc github.com/MichaelYagi/cockpit/pkg/client`),
  plus Python and TypeScript examples without dependencies, tested in CI
  against a live agent.
- **Releases.** `make dist` and a tag-triggered workflow publish Linux
  amd64/arm64 archives with `SHA256SUMS`.
- Plain `cockpit` opens the TUI in a terminal (Q2).

### Earlier phases (untagged)

- **Phase 3:** metric history with 10 s / 1 min / 1 h tiers (D18);
  persisted events; Performance charts; runtime health thresholds over the
  socket (D19); diagnostic reports with anonymization (D21); a hardened
  systemd service, install scripts and a SMART helper (D20); configurable
  filesystem filters.
- **Phase 2:** live event stream (SSE), processes, systemd services,
  hardware inventory, GPU metrics, network diagnostics, more health checks.
- **Phase 1:** the agent, the REST API with API keys and scopes, core
  collectors, four health checks and the TUI.

### Security

- Extension collectors run in the agent's process with its privileges;
  build in only code you trust.
- `cockpit tui --once` now sanitizes API text like the interactive UI,
  and the TUI also cleans map keys (labels, evidence) from the API.
