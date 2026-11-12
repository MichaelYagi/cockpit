# Changelog

Cockpit follows [semantic versioning](https://semver.org/) for its
releases and Go packages (D26). While the version is `0.x`, a minor
release may change `pkg/client`, `pkg/apiv1` or `pkg/collectorsdk`, and
every such change is listed here under **Breaking**. The REST API keeps
its own promise: `/api/v1` responses only ever gain fields and endpoints.

## Unreleased

### Changed

- **Shorter key confirmations.** Rotating, revoking and removing a key
  (TUI and CLI) need only the first 4 characters of the key ID; the whole
  ID still works.
- **Settings is on `S` (Shift+S).** The tab used to show `, Settings`,
  which read like a stray comma. `,` still works.
- **Line charts.** Performance charts and Overview sparklines are drawn
  as connected lines in braille characters (2 points across and 4 levels
  up per character) instead of block bars. Gaps in history still show.
- **Storage shows used / total.** Filesystem rows on the Overview and
  Storage screens show used and total space; they showed free space.

### Added

- **Browser access (D34).** `api.cors.allowed_origins` lists exact web
  origins that may call the TCP listener from a browser (GET only, API key
  still required). Off by default. Preflights from other origins get
  `403 origin_not_allowed`.

- **Color themes.** Settings lists nine themes with a swatch each: five
  single-color (Teal, the default, Ocean, Violet, Rose, Graphite) and four
  mixed (Aurora, Lagoon, Orchid, Dusk). `[` and `]` switch live; the choice
  is saved per user in `tui.json`. `cockpit tui --theme NAME` overrides it
  for one session. Health colors stay green, amber and red in every theme.
- **All read scopes at once.** In the TUI's new-key dialog, `a` selects
  every read scope (or clears them); `cockpit keys create --scope all`
  does the same. Keys still never get `admin`.
- **Removing old keys (D33).** Revoked and expired keys can be deleted
  from the list: `d` on the API Access screen, `cockpit keys remove ID`,
  or `POST /api/v1/keys/{id}/remove`. Active keys must be revoked first.
  Removal is recorded as a `key.removed` event.
- **Key usability hints.** When the TUI runs its own private agent (no
  agent was running), the API Access screen and the new-key dialog say
  that this agent has no network port and that keys work once
  `cockpit agent` runs. With a TCP listener, the new-key dialog shows a
  ready `curl` command for `/api/v1/health`.
- **Copying a new key.** In the TUI's new-key dialog, `c` copies the key
  to the clipboard (OSC 52; most modern terminals, including Windows
  Terminal). The mouse is released while the dialog is open, so the key
  can also be selected and copied with the terminal's own selection.

### Fixed

- The TUI's new-key dialog was missing `network:read`, so keys created
  there could not read interface addresses, routes or DNS.

## v0.5.0 — 2026-11-11

### Changed

- **New `network:read` scope (D27).** Interface addresses, routes and DNS
  now need it. Existing keys with `metrics:read` were migrated to include
  it, so nothing changes on upgrade. New keys must request it
  (`cockpit keys create --scope network:read`).

### Added

- **Windows admin pipe (D31).** The local admin API is a named pipe with a
  DACL for SYSTEM, Administrators and the agent's user. Clients check the
  pipe server's account before using it. Uses `github.com/Microsoft/go-winio`.
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
