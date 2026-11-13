# Architecture Decisions

This file records approved architectural decisions. Each entry states the
decision, the reasoning, and its consequences. Changing a decision requires
a new entry that supersedes the old one; do not silently edit history.

Status values: **Accepted**, **Superseded by D*n***, **Proposed**.

---

## D1 — Product naming and system identifiers

**Status:** Accepted (2026-11-11)

**Decision.** The product is **Cockpit**. The GitHub repository, Go module
(`github.com/MichaelYagi/cockpit`) and primary executable are `cockpit`.
Linux system identifiers use distinct names to avoid colliding with the
Red Hat Cockpit project (cockpit-project.org), which ships `cockpit.service`,
`cockpit.socket` and `/etc/cockpit/` on many distributions.

| Item | Value |
|---|---|
| Executable | `cockpit` |
| systemd unit | `cockpit-agent.service` |
| System configuration | `/etc/cockpit-monitor/` |
| System persistent data | `/var/lib/cockpit-monitor/` |
| System runtime (socket) | `/run/cockpit-monitor/` |
| System user and group | `cockpit-monitor` |
| User-mode config / state / runtime | `$XDG_CONFIG_HOME/cockpit-monitor/`, `$XDG_STATE_HOME/cockpit-monitor/`, `$XDG_RUNTIME_DIR/cockpit-monitor/` |
| Environment variable prefix | `COCKPIT_` |
| API key prefix | `ckp_` |

**Consequences.** Never create files, units, or users named plain `cockpit`
at the system level. Documentation states that this project is unrelated to
Red Hat Cockpit.

## D2 — Process layout: one binary, separate modes

**Status:** Accepted (2026-11-11)

**Decision.** A single `cockpit` binary provides subcommands:

- `cockpit agent` — runs the monitoring engine and API server.
- `cockpit tui` — runs the terminal UI, which is an API client.
- Supporting commands: `cockpit keys …`, `cockpit config check`,
  `cockpit version`.

If `cockpit tui` finds no running agent, it may start an **embedded agent**
in the same process. The embedded agent listens only on a private Unix
socket, and the TUI talks to it through the same HTTP API and Go client used
for a separate agent. The TUI never calls collectors, engine state, storage,
or platform resources directly, in either mode.

**Why.** Monitoring must not stop when the TUI exits, multiple TUIs must not
create competing engines, and the agent is the product's core. A single
binary keeps installation simple.

**Consequences.** `internal/tui` may import only `pkg/client` and
`pkg/apiv1` (plus UI libraries). This is enforced by an automated import
boundary test. Only `internal/cli` knows how to start an embedded agent.
One agent per state directory is enforced with a lock file.

## D3 — Local administrative access via Unix domain socket

**Status:** Accepted (2026-11-11)

**Decision.** Administrative operations (API key creation, listing,
rotation, revocation) are served **only** on a Unix domain socket. Access is
controlled by filesystem ownership and permissions:

| Mode | Socket | Directory | Socket mode | Group |
|---|---|---|---|---|
| System service | `/run/cockpit-monitor/agent.sock` | `0750` | `0660` | `cockpit-monitor` |
| User mode | `$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock` | `0700` | `0600` | user's own |

A connection on the socket is the **local administrator principal**, which
holds every scope. The agent also reads peer credentials (`SO_PEERCRED`) for
audit logging (UID/PID) as defense in depth, but permissions are the access
control mechanism.

**Consequences.** Key-management routes are not mounted on TCP listeners in
Phase 1. Adding users to the `cockpit-monitor` group grants local admin, and
the docs must say so plainly. Other platforms will need their own local
transport (e.g. Windows named pipes with ACLs) behind the same listener
abstraction.

## D4 — Authentication is always required on TCP

**Status:** Accepted (2026-11-11)

**Decision.** Every request on a TCP listener, including from `127.0.0.1`
or `::1`, must present a valid API key, except for the explicitly
unauthenticated routes `/api/v1/livez` and `/api/v1/openapi.json`. There
is no localhost exception.

**Why.** Loopback is reachable by every local user and, through DNS
rebinding, by web pages in local browsers.

**Consequences.** Loopback listeners also validate the `Host` header and
deny cross-origin requests by default.

## D5 — API key format and hashing

**Status:** Accepted (2026-11-11)

**Decision.**

- Format: `ckp_<id>_<secret>`. `<id>` is 12 lowercase base32 characters
  (random, non-secret, used for lookup and display). `<secret>` is 256 bits
  from `crypto/rand`, encoded as 43 base62 characters.
- Storage: only `SHA-256(full key string)` plus metadata. No pepper.
- Verification: parse, look up by `<id>`, compare hashes with
  `crypto/subtle.ConstantTimeCompare`, then check revocation and expiry.
- The full key is displayed exactly once, at creation or rotation.
- Keys, `Authorization` headers, and request bodies containing keys are
  never logged.

**Why.** With 256-bit random secrets, offline guessing is infeasible, so a
fast hash is sufficient. A pepper adds key-file management and rotation
cost for negligible benefit. Slow password hashes would add per-request
latency for no security gain.

## D6 — OpenAPI is the authoritative contract (spec-first, no codegen)

**Status:** Accepted (2026-11-11)

**Decision.** `api/openapi.yaml` (OpenAPI 3.1) is the source of truth.
Wire types in `pkg/apiv1` and handlers are hand-written to match it. No code
generation in the initial phases. Conformance is enforced by tests:

- Server tests validate every response (status, headers, body) against the
  spec using `kin-openapi` (a test-only dependency).
- Client tests run `pkg/client` against a mock server that validates each
  request against the spec.
- Spec examples must decode into `pkg/apiv1` types without unknown fields.

**Consequences.** Changing the API means changing the spec first in the same
change. Code generation can be reconsidered if hand-written types become a
maintenance burden.

## D7 — SQLite driver

**Status:** Accepted (2026-11-11)

**Decision.** Use `modernc.org/sqlite` (pure Go). Builds use
`CGO_ENABLED=0`. Schema migrations are embedded SQL files tracked with
`PRAGMA user_version`; no migration library.

**Why.** Keeps cross-compiling and static binaries simple.

## D8 — Authorization scopes

**Status:** Accepted (2026-11-11)

**Decision.** Initial scopes:

| Scope | Grants |
|---|---|
| `system:read` | Machine identity, OS, hardware inventory, basic agent status |
| `metrics:read` | Live metrics, storage, network, sensors, health, events |
| `admin` | Key management and privileged status fields |

Rules:

- Scopes are opaque strings of the form `<resource>:<action>` (or a bare
  reserved word such as `admin`), checked by exact membership.
- There is no implicit inheritance: `admin` does not imply read scopes, and
  read scopes never imply `admin`. The local socket principal is granted all
  scopes explicitly.
- Every route declares exactly one required scope (or is explicitly public).
- A central scope registry lists valid scopes. Adding finer scopes later
  (e.g. `processes:read`, `sensitive:read`) is a non-breaking change.
- In Phase 1, keys created for TCP use cannot include `admin`, because admin
  routes are socket-only (D3).

## D9 — Sensitive fields hidden by default

**Status:** Accepted (2026-11-11)

**Decision.** Process command lines, hardware serial numbers, and MAC
addresses (plus other unique hardware identifiers such as WWNs and UUIDs)
are hidden by default on every listener.

- The spec marks such fields with `x-cockpit-sensitive: true`.
- Redaction happens in one place at the API boundary, not in collectors or
  handlers, so it is testable.
- Hidden fields are omitted, and the containing object lists them in a
  `redacted` array so clients know data exists but was withheld.
- Exposure can later be enabled via a dedicated scope and/or configuration.
  No such switch exists in Phase 1.
- Sensitive values are never logged.

## D10 — Go toolchain

**Status:** Accepted (2026-11-11)

**Decision.** Use the latest stable Go at initialization: **Go 1.27.4**.
`go.mod` declares `go 1.27` and `toolchain go1.27.4`. Developer tools
(staticcheck, govulncheck) are pinned with `go.mod` `tool` directives.
Toolchain upgrades are explicit commits.

UI libraries target Bubble Tea v2 (`charm.land/bubbletea/v2`) and its
matching Lip Gloss and Bubbles releases.

## D11 — Phase 1 health checks

**Status:** Accepted (2026-11-11)

**Decision.** Phase 1 ships four health checks:

1. **CPU temperature**, when sensors are available.
2. **Memory** pressure (PSI) and utilization.
3. **Storage capacity** (space and inodes) per filesystem.
4. **Disk health** via SMART/NVMe where supported.

Each check reports `ok`, `warning`, `critical`, or `unknown`. `unknown`
always carries the underlying availability state (`unsupported`,
`permission_denied`, …) and a reason. Missing hardware, sensors or
permissions never cause failures.

**Consequences.** Disk health in Phase 1 uses `smartctl --json`
(smartmontools ≥ 7.0) as an optional, runtime-detected external
dependency: invoked without a shell, with fixed arguments, a timeout and a
low polling frequency. Reading SMART data usually requires elevated
privileges; an unprivileged agent reports `permission_denied`. How to
provide privilege without running the agent as root is an open question
(see ROADMAP "Open questions").

## D12 — Documentation baseline

**Status:** Accepted (2026-11-11)

**Decision.** The revised architecture and documentation are the first
Phase 0 commit. All documents use the name "Cockpit" and reflect D1–D11.
Documentation is updated in the same change as any behavior it describes.

## D13 — Hand-written /proc and /sys parsers instead of prometheus/procfs

**Status:** Accepted (2026-11-11)

**Decision.** Linux collectors parse `/proc` and `/sys` with small,
hand-written pure functions (`internal/platform/linux/parse.go`) instead of
depending on `github.com/prometheus/procfs`, which CLAUDE.md originally
listed.

**Why.** Phase 1 needs only a few simple formats (`stat`, `meminfo`,
`loadavg`, `uptime`, `net/dev`, `mountinfo`, PSI, `os-release`, sysfs
attributes). Each parser is 20–60 lines, is fuzz-tested, and lets every
read error be classified per capability (missing, permission denied,
malformed) without translating a library's errors. It also avoids a
dependency.

**Consequences.** If later phases need complex formats (per-process
`/proc/[pid]` details, `/proc/net/*` socket tables), adopting procfs for
those is reasonable and needs a new entry.

## D14 — Scopes for processes and services

**Status:** Accepted (2026-11-11)

**Decision.** Add an issuable `processes:read` scope for `GET /processes`.
Process lists reveal user names, running software and resource use, so
they are not covered by `metrics:read`. Services (`GET /services`) use
`system:read`: the unit list is system inventory, comparable to hardware.
Command lines remain hidden regardless of scope (D9).

**Consequences.** Existing keys do not gain process access. The scope
registry and spec gain one entry; this is a non-breaking addition (D8).

## D15 — systemd access through godbus/dbus

**Status:** Accepted (2026-11-11)

**Decision.** Read systemd unit state over the system D-Bus with
`github.com/godbus/dbus/v5` (pure Go, the library underneath
`coreos/go-systemd`). Only read-only `ListUnits` calls are made.

**Why.** D-Bus is systemd's supported interface; parsing `systemctl`
output is excluded by principle 9. Using godbus directly avoids the
larger go-systemd dependency for one call.

## D16 — Vendor GPU metrics as optional tools

**Status:** Accepted (2026-11-11)

**Decision.** GPU inventory comes from PCI and DRM in sysfs. Metrics come
from vendor sources when present: NVIDIA via `nvidia-smi
--query-gpu=<fixed fields> --format=csv,noheader,nounits` (a documented,
structured output mode), AMD via `amdgpu` sysfs files. Missing tools are
`not_configured`; GPUs without a metrics source are listed without
metrics. NVML (cgo) is not used.

*Extension (4G):* Intel GPUs (`i915`, `xe`) report their current and
maximum frequency from sysfs, and discrete cards report power from the
hwmon energy counter and the power limit. Utilization needs perf events,
which the unprivileged agent cannot open, so it stays unavailable with
that reason.

## D17 — Server-Sent Events for the event stream

**Status:** Accepted (2026-11-11)

**Decision.** `GET /api/v1/events/stream` (`metrics:read`) sends events as
SSE with a heartbeat every 15 seconds. Each subscriber has a bounded
queue (256 events); when a client falls behind, its oldest events are
dropped and it receives a `lagged` event with the number missed. At most
4 streams per key and 64 in total. The key is re-verified at every
heartbeat, so revoked or expired keys are disconnected within 15 seconds.
Streams close cleanly on shutdown. `Last-Event-ID` is not supported in
Phase 2; clients resume with `GET /events?since=`.

## D18 — Metric history design

**Status:** Accepted (2026-11-11)

**Decision.** A recorder samples the current snapshot every 10 seconds
(not every collection) into SQLite. Series are a fixed, documented set
named by dotted keys (e.g. `cpu.usage_percent`,
`net.eth0.rx_bytes_per_second`, `fs./home.used_percent`). Three tiers:

| Tier | Resolution | Kept | Stored |
|---|---|---|---|
| raw | 10 s | 24 hours | value |
| 1m | 1 minute | 7 days | min, avg, max |
| 1h | 1 hour | 90 days | min, avg, max |

Writes are batched (one transaction per 30 seconds). Rollups and
retention run every minute. History is enabled by default and can be
disabled (`history.enabled: false`). Queries pick the finest tier that
covers the range within 2,000 points per series.

**Why.** Fixed series keep storage predictable (roughly 30 MB at steady
state for a typical machine) and avoid an open-ended metrics database.

## D19 — Settings changes through the admin socket

**Status:** Accepted (2026-11-11)

**Decision.** Health thresholds can be changed at runtime through
`GET /settings` and `PUT /settings`, mounted only on the Unix socket
(`admin`, like key management). Changes are validated with the same rules
as the configuration file, stored in SQLite, applied immediately, logged
and recorded as events. Stored settings override the file; `DELETE
/settings` returns to the file's values. The TUI requires confirmation
before saving.

**Why.** Principle 5 requires mutating operations to be separately
authorized and confirmed; the socket is the existing admin boundary.

## D20 — SMART data through a privileged helper

**Status:** Accepted (2026-11-11)

**Decision.** In system mode, `cockpit-smart.service` (run by
`cockpit-smart.timer`, hourly) executes `cockpit smart-helper` as root,
which runs `smartctl --json` for each disk and writes the results to
`/var/lib/cockpit-monitor/smart/` (directory `0750`, files `0640`, group
`cockpit-monitor`). The agent stays unprivileged and reads these files;
results older than 3 hours are treated as stale. Without the helper, the
agent falls back to running `smartctl` itself (Phase 1 behavior).
Because `/var/lib/cockpit-monitor` belongs to the agent's user, the helper
creates `smart/` itself (not through `StateDirectory=`, which would chown
it), refuses a symlink or a directory not owned by root or writable by
others, and writes only relative to the opened directory.

**Why.** Resolves Q1 without giving the long-running, network-facing
agent elevated privileges.

**Amended (2026-11-13, proposed).** `deploy/install.sh` installs
smartmontools from the distribution's packages when `smartctl` is
missing and enables the timer by default; `--no-smart` opts out. The
agent still detects `smartctl` at runtime and works without it.

## D21 — Diagnostic reports

**Status:** Accepted (2026-11-11)

**Decision.** `GET /report` and `cockpit report` produce a point-in-time
report: status, system, hardware, services, health, recent events,
current metrics and the effective health thresholds (no secrets). The
route requires `system:read`, keeping D8's one scope per route; the
metric-based parts are included only when the caller also holds
`metrics:read`, and the thresholds only over the socket. Parts left out
are listed in `omitted` with a reason.
Formats: JSON and Markdown. Sensitive fields stay redacted (D9).
`anonymize=true` additionally replaces host names, IP addresses, MAC-like
strings, user names and mount points under `/home` with stable
placeholders. Files are written `0600`.

## D22 — Generic metrics for extension collectors

**Status:** Accepted (2026-11-11)

**Decision.** Add one open record type, `core.GenericMetric`, alongside the
sealed built-in records (PLUGINS §3):

| Field | Rules |
|---|---|
| `name` | `<collector namespace>.<metric>`, lower-case dotted (`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`), at most 128 characters |
| `kind` | `gauge` or `counter`; the engine turns counters into per-second rates, as for built-in counters |
| `unit` | from a fixed list (`bytes`, `bytes_per_second`, `percent`, `celsius`, `watts`, `seconds`, `count`, `ratio`, …) |
| `labels` | at most 8; keys `^[a-z][a-z0-9_]{0,31}$`; values at most 128 characters, sanitized like all API text |
| `value` | finite float64; NaN and infinities are dropped |

Each collector may emit at most 500 series (name + labels); extra series
are dropped and the collector's status says so. Generic metrics are served
in a new `custom` section of `GET /metrics` (`metrics:read`) with the
usual per-collector availability. They enter history (D18) only when the
collector's descriptor asks for it, within the existing 200-series limit.

**Why.** Third-party collectors need a way to report data without
changing `internal/core` and the OpenAPI schema for every new domain, and
without opening the sealed record set that keeps built-in data typed.
The limits keep a misbehaving collector from flooding memory, the API or
history.

## D23 — Compile-time extensions through a public collector SDK

**Status:** Accepted (2026-11-11)

**Decision.** Third-party collectors are Go code compiled into a custom
build. Nothing is loaded at runtime: no Go plugins, scripts or
subprocess protocol.

- `pkg/collectorsdk` (public, imports nothing internal) defines a reduced
  contract: `Descriptor` (ID in the `custom.` namespace, version, SDK
  version, interval, timeout, capabilities), `Probe`, `Collect` returning
  `[]GenericMetric` plus per-capability status, and `Close`.
- The public package `github.com/MichaelYagi/cockpit/custom` provides
  `custom.Main(collectors ...collectorsdk.Collector) int`, which runs the
  normal CLI with the extra collectors added. A custom build is a five-line
  `main.go` in the author's own module.
- An internal adapter wraps SDK collectors in the internal contract, so
  they get the same engine treatment: timeouts, panic recovery, staleness,
  and status in `/status`.
- **Compatibility:** `collectorsdk.Version` starts at `1`. A collector
  declares the SDK version it was written against, and the agent refuses
  to start one with a different major version. `/status` marks extension
  collectors with `external: true` and their SDK version.
- Extension code runs in the agent process with the agent's privileges.
  It is trusted code, like any other compiled-in dependency, and the docs
  say so plainly.

**Why.** It keeps the single static binary and the "no dynamic code
loading" rule while letting people add collectors without forking.
`internal/` stays private and the public surface is small enough to keep
stable.

## D24 — Containers from cgroup v2, without the engine socket

**Status:** Accepted (2026-11-11)

**Decision.** A `linux.containers` collector finds Docker, Podman
(rootful and rootless) and containerd containers from the cgroup v2
hierarchy (`docker-<id>.scope`, `libpod-<id>.scope`, …) and reads their
CPU, memory, I/O and process counts from `/sys/fs/cgroup`. It never talks
to the Docker or Podman API socket: access to that socket is equivalent to
root, and the agent must not hold it. Containers are identified by
runtime and short ID; names and images are reported as unavailable with
that reason. `GET /containers` requires `processes:read` (D14), because a
container list reveals running software in the same way. cgroup v1 hosts
report `unsupported`.

*Clarification (4E):* in the TUI, containers are a view of the Processes
screen (`c` toggles it) rather than a separate tab. A twelfth tab does not
fit an 80-column tab bar, and the view needs the same scope.

**Why.** Per-container resource use is the most asked-for addition and
cgroup v2 provides it without privileges. See Q8 for names and images.

## D25 — ZFS, batteries and UPS

**Status:** Accepted (2026-11-11)

**Decision.**

- **ZFS** (`linux.zfs`): pool state from `/proc/spl/kstat/zfs/<pool>/state`
  and ARC statistics from `/proc/spl/kstat/zfs/arcstats`, without
  privileges. Capacity and errors come from `zpool list -j` and
  `zpool status -j` (OpenZFS 2.3+, JSON output) as an optional tool;
  older versions report those parts as unsupported rather than having
  their text parsed. A new health check raises a warning for a `DEGRADED`
  pool and a critical finding for `FAULTED`, `UNAVAIL` or `SUSPENDED`.
- **Batteries** (`linux.power`): `/sys/class/power_supply` gives charge,
  status, health and power draw for batteries, and the mains status.
- **UPS** (`linux.ups`, off by default): a read-only client for the
  Network UPS Tools protocol (`LIST UPS`, `LIST VAR`; default
  `127.0.0.1:3493`, configurable). It never sends commands that change
  state. A health check raises a warning when running on battery and a
  critical finding at low battery.
- Batteries and UPS appear in new `power` and `ups` sections of
  `/metrics` (two sections because two collectors provide them), and ZFS
  in `/storage`, all under `metrics:read`.

**Why.** These are the remaining common "is my machine healthy" questions
on servers and laptops. All sources are documented interfaces with
structured data.

## D26 — Releases and SDK versioning

**Status:** Accepted (2026-11-11)

**Decision.** Releases are git tags `vX.Y.Z`; Phase 4 ships `v0.4.0`. A
release workflow (GitHub Actions, no new tools) builds `linux/amd64` and
`linux/arm64` binaries with `CGO_ENABLED=0 -trimpath`, a tarball with
`deploy/` and the docs, and `SHA256SUMS`, and attaches them to a GitHub
release. `pkg/client`, `pkg/apiv1` and `pkg/collectorsdk` follow semantic
versioning: while in `v0`, a minor release may break them, but only with
a note in `CHANGELOG.md`. From `v1`, never. The REST API keeps its own
promise (`/api/v1` is not broken, API.md §1), independent of these
version numbers.

**Why.** "Published SDK" needs a version people can pin, release notes,
and binaries they can verify.

## D27 — A `network:read` scope for network layout

**Status:** Accepted (2026-11-11)

**Decision.** Interface addresses, routes and DNS configuration need a new
issuable scope, `network:read`. Interface names, state, counters, rates
and errors stay under `metrics:read`, so throughput dashboards still work.
Without `network:read`:

- `addresses` in `/metrics` and `/network` interfaces is an empty list,
  and `redacted` lists `addresses`;
- the `routes` and `dns` sections of `/network` report `permission_denied`
  with a reason naming the scope;
- reports leave out the network part the same way.

The socket principal holds every scope. A storage migration gives every
existing key that has `metrics:read` the new scope as well, so no
integration changes behavior on upgrade. Only new keys have to ask for it.

**Why.** Resolves Q6. One metrics key per machine is harmless; a fleet
console holding keys for many machines would otherwise hold a map of the
whole network.

## D28 — Process command lines are never exposed

**Status:** Accepted (2026-11-11)

**Decision.** Command lines are never read or returned, under any scope
or configuration. This closes Q7. Process items gain an `executable` field,
the resolved path of `/proc/<pid>/exe` under `processes:read`. It is null
when the kernel does not allow reading it (other users' processes,
without privileges), and `executable_reason` explains why.

**Why.** Arguments routinely carry passwords, tokens and connection
strings, and no redaction can recognize them reliably. The executable
path identifies the program without its arguments.

## D29 — TLS tooling and certificate pinning

**Status:** Accepted (2026-11-11)

**Decision.**

- `cockpit tls init` creates a private CA and a server certificate (ECDSA
  P-256, CA 10 years, server 1 year, SANs from `--host`, which may be
  repeated). Keys are written `0600` into the configuration directory. It
  prints the configuration lines to add and the certificate's SHA-256
  fingerprint. `cockpit tls renew` issues a new server certificate from
  the same CA. `cockpit tls show` prints the fingerprint and expiry.
- Clients can pin a server instead of trusting a CA:
  `client.WithPinnedCertificate(sha256)`, `--tls-fingerprint`, or
  `COCKPIT_TLS_FINGERPRINT`. A pinned connection checks only the leaf
  certificate's fingerprint, and expiry, and refuses everything else.
  `--tls-ca FILE` trusts a private CA.
- The agent logs a warning 30 days before its certificate expires, and
  `/status` (admin part) reports `tls_expires_at`.
- Bringing your own certificate (for example ACME) stays supported and
  documented.

**Why.** Remote access already requires TLS (D4). This makes it practical
without a public CA, and pinning works without one.

## D30 — Fleet model: a pull-only console with pinned identities

**Status:** Accepted (2026-11-11)

**Decision.** The fleet console is a client. It needs no agent changes, no
new listeners and no central server.

- `cockpit fleet add NAME URL` enrolls a machine. The key is read from a
  file or an environment variable, and TLS pins its fingerprint by
  default. Enrollment contacts the agent and records its `machine_id` and
  certificate fingerprint. `fleet list`, `fleet remove NAME` and
  `fleet check` manage the list.
- The list lives in `fleet.yaml` in the user's configuration directory,
  mode `0600` and refused otherwise. Keys are not stored in it; it refers
  to key files, which must also be `0600`.
- `cockpit fleet` (the console) polls each machine with its own key and
  shows a table: health, CPU, memory, fullest filesystem, uptime and last
  contact. Pressing Enter opens the normal TUI for one machine. Machines
  are polled concurrently with per-machine timeouts. One slow or
  unreachable machine never blocks the others.
- **Identity.** A changed `machine_id` or certificate fingerprint makes
  the console refuse that machine and say so, until the operator runs
  `fleet add --replace`. A reinstalled or impersonated host cannot slip
  in silently.
- **Recommended keys.** `system:read` and `metrics:read`. `network:read`
  only if the operator wants network detail (D27). The docs say a fleet
  host holding many keys should be protected like those machines.
- Plain-HTTP machines are allowed only on loopback, as for every client.

**Why.** It meets the "fleet console" goal with the existing API, keeps
every machine local-first, and avoids a server that would need its own
availability, storage and trust model. Push-based enrollment can be
designed later if pull stops scaling (hundreds of machines).

## D31 — Windows admin transport: a named pipe with an explicit ACL

**Status:** Accepted (2026-11-11)

**Decision.** On Windows the admin listener is the named pipe
`\\.\pipe\cockpit-monitor` (user mode: `cockpit-monitor-<user SID>`). Its
security descriptor grants access only to SYSTEM, the Administrators
group and the user running the agent. Remote pipe clients are rejected
(`PIPE_REJECT_REMOTE_CLIENTS`). The listener and client use
`github.com/Microsoft/go-winio`. This is a new dependency, maintained by
Microsoft and widely used, because correct overlapped named-pipe I/O and
security descriptors are not available in the standard library.

*Clarification (5H):* there is no separate `--pipe` flag. `--socket`
and `api.socket.path` accept a pipe name, and on Windows they default to
the pipe. Clients also check the pipe's server: it must run as SYSTEM, an
administrator or the current user. Otherwise a process that created the
name first could impersonate the agent.

**Why.** The Unix socket gives local admin access through file
permissions (D3). A named pipe with an ACL is Windows' equivalent, and the
roadmap asked for it.

## D32 — macOS and Windows collectors without cgo

**Status:** Accepted (2026-11-11)

**Decision.** New `internal/platform/darwin` and `internal/platform/windows`
packages use only `golang.org/x/sys` and the standard library, so builds
stay `CGO_ENABLED=0`:

- **macOS:** identity and uptime, load average, memory (`sysctl`),
  filesystems (`getfsstat`), and interfaces with addresses. CPU usage,
  sensors and network counters need Mach or IOKit calls that require cgo.
  They are reported `unsupported` with that reason.
- **Windows:** identity and version (`RtlGetVersion`), uptime, CPU usage
  (`GetSystemTimes`), memory (`GlobalMemoryStatusEx`), fixed drives
  (`GetDiskFreeSpaceExW`), and interfaces with addresses. Load average does
  not exist on Windows and is reported `unsupported`.

Anything not listed is reported `unsupported`, as today. CI runs the
platform tests and the conformance suite on `macos-latest` and
`windows-latest` runners, since these collectors cannot be exercised on
Linux.

**Why.** Basic monitoring on the two other common desktop systems, while
keeping the single static binary and the "unsupported, not fatal" rule.

## D33 — Removing revoked and expired API keys

**Status:** Proposed (2026-11-12)

**Decision.** `POST /api/v1/keys/{id}/remove` (admin, local socket only)
deletes the record of a key that is revoked or expired. A key that still
works is refused with `invalid_request`; it must be revoked first, so
removal never cuts off a client. `DELETE /keys/{id}` keeps meaning
"revoke" (the `/api/v1` promise). Each removal is logged and published as
a `key.removed` event. The TUI removes the selected key with `d`, and the
CLI with `cockpit keys remove ID`, both after the first 4 characters of the key
ID are typed, like revoke and rotate. A newer key's `rotated_from` may then name a key that
no longer exists; it stays a plain ID.

**Why.** Revoked keys stayed in the list for good and cluttered it. Their
history is kept by the `key.revoked` and `key.removed` events and the logs.

## D34 — Opt-in browser access (CORS) to the TCP listener

**Status:** Proposed (2026-11-12)

**Decision.** A new setting, `api.cors.allowed_origins`, lists exact web
origins (`scheme://host[:port]`, lower case, http or https; no wildcards,
paths or `null`; at most 32) that may call the TCP listener from a
browser. It is empty by default, which keeps today's behavior. For a
listed origin the agent answers preflights with `204`, allowing `GET` and
the `Authorization`, `Accept` and `Last-Event-ID` headers (cached 10
minutes), and adds `Access-Control-Allow-Origin` (with `Vary: Origin`) to
every response, errors included, so pages can read them. Credentials are
never allowed: the page must send an API key, and auth, scopes, rate
limits and the `Host` check apply as before. A preflight from an unlisted
origin gets `403 origin_not_allowed`. The Unix socket never answers
browsers. The setting is in the configuration file only.

**Why.** Browser-based API tools and small web dashboards could not call
the agent: the preflight got `405` and the browser never sent the request.
An exact allowlist enables them without letting arbitrary sites read the
API.

## D35 — Web dashboard served by the agent

**Status:** Proposed (2026-11-12)

**Decision.** The agent serves a read-only web dashboard on its TCP
listener at `/ui/`; `/` redirects there. It is on by default and turned off
with `api.web.enabled: false`. It is an API client like the TUI:

- The files (HTML, CSS, JavaScript) are embedded in the binary, with no
  third-party code, no CDN and no build step. They are outside `/api/v1`
  and not part of the OpenAPI contract, contain no data, and need no key.
- The page asks for an API key and keeps it in the tab's
  `sessionStorage`, so it is gone when the tab closes. Every API call is
  same-origin with an `Authorization` header; no cookies are used, so
  there is nothing for another site to forge. Scopes, rate limits, the
  `Host` check and TLS rules apply exactly as for any other client.
  Sections a key may not read say so instead of failing.
- It is read-only: no key management or settings (those stay on the Unix
  socket, D3), and no write operations.
- Responses carry a strict Content Security Policy (`default-src 'none'`,
  scripts, styles and connections from the same origin only, no framing),
  `nosniff` and `no-referrer`. API text is inserted only as text, never
  as HTML.
- Live updates poll the API and read `/events/stream` with `fetch`
  (`EventSource` cannot send an `Authorization` header).

**Why.** A browser view is the easiest way to look at a machine from
another computer, and it reuses the TCP listener, TLS setup and keys that
already exist. Serving it from the same origin needs no CORS (D34).

## D36 — macOS system libraries through purego

**Status:** Proposed (2026-11-12)

**Decision.** This amends D32. The macOS collectors may call system
libraries (`libSystem`, IOKit, CoreFoundation) through
`github.com/ebitengine/purego`, which loads them at run time without cgo.
Builds stay `CGO_ENABLED=0`, and macOS binaries can still be built on
Linux. The library is imported only by `internal/platform/darwin`, so Linux
and Windows binaries do not contain it. This adds:

- CPU usage, total and per core (`host_processor_info`), and the CPU
  model and core counts (`sysctl`);
- processes with CPU, memory, threads and executable path (`sysctl`
  `kern.proc`, `proc_pidinfo`, `proc_pidpath`);
- batteries and power adapters (IOKit power sources);
- temperatures on Apple Silicon (IOKit HID sensors). Intel Macs report
  temperatures `unsupported` for now.

A library or symbol that fails to load makes only its capabilities
unavailable, with a reason; it is never fatal. These calls cannot run on
Linux, so they are tested on the `macos-latest` CI runner and by the
conformance suite.

**Why.** Without CPU usage and processes, the macOS agent misses the most
basic monitoring. cgo would have meant building macOS binaries on a Mac;
purego keeps the single static binary and cross-compilation (D7, D32).

## D37 — One-step LAN access

**Status:** Proposed (2026-11-13)

**Decision.** `api.lan.enabled` (also `cockpit agent --lan` and
`COCKPIT_API_LAN`) opens the TCP listener to the local network in one
step. It changes a loopback `api.tcp.address` to `0.0.0.0` on the same
port; an explicit non-loopback address is kept. When `api.tls` names no
files, the agent keeps its own private CA and server certificate in
`<state dir>/tls` (D29 format, keys `0600`, owned by the agent's user)
for the host name, `localhost`, the loopback addresses and every IPv4
address other machines can reach. At each start it reuses the
certificate, or issues a new one from the same CA when an address is not
covered or expiry is within 30 days. It prints the URLs, the SHA-256
fingerprint and the commands to create a key and connect; it never
prints or creates a key. TLS and API keys stay required (D4); the
startup warning for remote listeners stays.

**Why.** Remote access needed `tls init` with the right `--host` values,
a pasted configuration block and a restart. `--lan` does the same in one
flag, with a certificate for the addresses the machine really has.

**Trade-off.** The agent can read its own CA key, so a compromised agent
could issue certificates under that CA. Clients that pin the fingerprint
are unaffected; the documentation recommends pinning, and `cockpit tls
init` (root-only CA key in system mode) remains for anyone who wants to
import the CA.
