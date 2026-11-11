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

**Status:** Proposed (2026-11-11)

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

**Status:** Proposed (2026-11-11)

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

**Status:** Proposed (2026-11-11)

**Decision.** In system mode, `cockpit-smart.service` (run by
`cockpit-smart.timer`, hourly) executes `cockpit smart-helper` as root,
which runs `smartctl --json` for each disk and writes the results to
`/var/lib/cockpit-monitor/smart/` (directory `0750`, files `0640`, group
`cockpit-monitor`). The agent stays unprivileged and reads these files;
results older than 3 hours are treated as stale. Without the helper, the
agent falls back to running `smartctl` itself (Phase 1 behavior).

**Why.** Resolves Q1 without giving the long-running, network-facing
agent elevated privileges.

## D21 — Diagnostic reports

**Status:** Proposed (2026-11-11)

**Decision.** `GET /report` (requires both `system:read` and
`metrics:read`; always available to the socket) and `cockpit report`
produce a point-in-time report: status, system, hardware, health, recent
events, current metrics and the effective configuration (no secrets).
Formats: JSON and Markdown. Sensitive fields stay redacted (D9).
`anonymize=true` additionally replaces host names, IP addresses, MAC-like
strings, user names and mount points under `/home` with stable
placeholders. Files are written `0600`.

