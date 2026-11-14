# Changelog

Cockpit follows [semantic versioning](https://semver.org/) for its
releases and Go packages (D26). While the version is `0.x`, a minor
release may change `pkg/client`, `pkg/apiv1` or `pkg/collectorsdk`, and
every such change is listed here under **Breaking**. The REST API keeps
its own promise: `/api/v1` responses only ever gain fields and endpoints.

## Unreleased

### Changed

- **Disk health works after install.** `deploy/install.sh` installs
  smartmontools with the system's package manager when `smartctl` is
  missing and enables the hourly SMART helper by default. `--no-smart`
  skips both; `--smart-helper` is still accepted and does nothing extra.
- **Shorter key confirmations.** Rotating, revoking and removing a key
  (TUI and CLI) need only the first 4 characters of the key ID; the whole
  ID still works.
- **Settings is on `S` (Shift+S).** The tab used to show `, Settings`,
  which read like a stray comma. `,` still works.
- **Line charts.** Performance charts and Overview sparklines are drawn
  as connected lines in braille characters (2 points across and 4 levels
  up per character) instead of block bars. Gaps in history still show.
- **Health checks that cannot run here show as N/A.** A check whose
  hardware or feature is absent or turned off (no temperature sensors,
  no ZFS, UPS monitoring off, smartctl not installed) is shown as `– N/A`
  in the TUI and the web dashboard and listed after the real results, so
  it no longer looks like a problem. The API still reports it as
  `unknown` with its availability and reason.
- **Storage shows used / total.** Filesystem rows on the Overview and
  Storage screens show used and total space; they showed free space.

### Added

- **`config.yaml` in the current folder.** In user mode, `cockpit` reads
  `config.yaml` from the folder it is run from when one is there, before
  `~/.config/cockpit-monitor/config.yaml`. `--config` and `COCKPIT_CONFIG`
  still take precedence; the system service never reads the current
  folder. `config.yaml` is now in `.gitignore`.
- **Plain HTTP from outside the network, opt-in (D42).**
  `cockpit agent --http-public` (or `api.lan.public: true`) works like
  `--lan-http` but accepts clients from any address, so the API and
  dashboard can be reached over the internet through a router port
  forward without HTTPS. API keys are still required; they and all data
  travel unencrypted. `pkg/client` adds `WithPlainHTTP()`.
- **Edit service checks.** On the Network screen, `e` opens the selected
  check with its name and address filled in. Changes apply within a few
  seconds. API: `PUT /endpoint-checks/{name}` (admin socket);
  `pkg/client` adds `UpdateEndpointCheck`.
- **Services that ask for a login count as up.** A service check that
  gets 401 or 403 now reports the service up (it answered; it just wants
  a password), so login-protected apps no longer show as failing.
  `expect_status` still overrides the default.
- **Paste into TUI fields.** Service check names and addresses, API key
  names and confirmations, thresholds and the process search accept
  pasted text (Ctrl+V or Shift+Insert, depending on the terminal). Line
  breaks and control characters are removed, and the text is trimmed.
- **Add and remove service checks from the TUI (D41).** On the agent's
  machine, the Network screen's Endpoints panel takes `a` to add a check
  (name, address, and for `https://` whether to accept a self-signed
  certificate) and `d` to remove one, with no restart. Checks from the
  configuration file are shown as `[config.yaml]` and stay read-only.
  New admin-socket routes: `GET`/`POST /endpoint-checks` and
  `DELETE /endpoint-checks/{name}`; `pkg/client` adds `EndpointChecks`,
  `AddEndpointCheck` and `RemoveEndpointCheck`. Errors gain the
  `conflict` code (409).
- **Service checks (D40).** List services under
  `collection.endpoints.targets` (`https://…`, `http://…` or
  `tcp://host:port`) and the agent checks them every 30 seconds. The TUI
  and web dashboard Network pages show each one as up, failing or down
  with its response time. A new `endpoints` health check reports a
  service down after two failed checks in a row and warns when its HTTPS
  certificate expires within 14 days, so notifications cover them.
  `GET /network` gains `endpoints`; targets need `network:read`.
- **Notifications (D39).** The agent can send health findings to ntfy
  (phone push), Discord, Slack or any JSON webhook when a problem appears,
  gets worse or is resolved. Configure `notifications.webhooks`, with each
  URL in a private file, and check the setup with `cockpit notify test`.
  `cockpit config check` also reads the URL files.
- **Plain HTTP on the LAN, opt-in (D38).** `cockpit agent --lan-http`
  (or `api.lan.http: true`) serves `http://` on the network with no
  certificate, accepts only private-network clients and still requires an
  API key. Keys and data travel unencrypted; `--lan` (HTTPS) remains the
  recommended way. `pkg/client` adds `WithPlainHTTPOnPrivateNetwork()`
  and `IsPrivateAddress()`; `cockpit tui --url http://<private IP>` uses
  them with a warning.
- **One-step LAN access (D37).** `cockpit agent --lan` (or
  `api.lan.enabled: true`) listens on the network, creates and renews
  its own HTTPS certificate for this machine's addresses, and prints the
  address to open in a browser, the certificate fingerprint and the TUI
  command. API keys are still required.
- **Retro themes.** Amber and Phosphor give the TUI an old CRT terminal
  look: tinted text and square panel corners.
- **Web dashboard (D35).** The agent serves a read-only dashboard at
  `http://127.0.0.1:7878/` (or your TLS address): Overview, Performance
  (live and stored history), Health, Storage, Network, Processes,
  Services, Events (live) and Hardware. Sign in with an API key, which is
  kept in that browser tab only. No third-party code; strict Content
  Security Policy. On by default; `api.web.enabled: false` turns it off.
- **More on macOS (D36).** CPU usage (total and per core), processes with
  CPU, memory, threads and executable path, the processor and Mac model,
  batteries and the power adapter, and temperatures on Apple Silicon.
  System libraries are called through `github.com/ebitengine/purego`
  (Apache-2.0, macOS builds only), so builds stay without cgo and macOS
  binaries can still be built on Linux.
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

- The footer highlighted only part of keys that contain a space: `0-9 S`
  showed `S` as a label, and `[ ]` showed `]` as one.
- **Embedded agent log is capped.** When the TUI runs its own agent, its
  log (`embedded-agent.log`) grew by every API request and was never
  trimmed (about 2 MB an hour). It is now capped at 10 MiB, with one older
  copy kept as `embedded-agent.log.1`, so it never uses more than about
  20 MiB. An existing oversized log is rotated at the next start.

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
