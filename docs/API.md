# API Contract

`api/openapi.yaml` (OpenAPI 3.1) is the authoritative contract (D6). This
document explains conventions and the planned endpoints. If the two ever
disagree, the spec wins and this document must be fixed.

## 1. Conventions

- Base path: `/api/v1`. Incompatible changes require `/api/v2`.
- JSON only (`application/json`), UTF-8. SSE uses `text/event-stream`.
- Timestamps: UTC, RFC 3339 (e.g. `2026-11-11T15:30:00Z`).
- Every response includes an `X-Request-ID` header. Error bodies repeat it.
- Every resource includes `schema_version`, `machine_id`, and
  `generated_at`.
- Field names: `snake_case`. Units are in the name (`_bytes`, `_percent`,
  `_celsius`, `_seconds`, `_per_second`).
- Unknown query parameters are rejected with `400`.
- Request bodies are limited to 64 KiB.
- Cross-origin requests are denied by default (no CORS headers).
- On loopback listeners the `Host` header must be a loopback name/address.

## 2. Transports and authentication

| Listener | Default | Authentication | Routes |
|---|---|---|---|
| Unix socket | on | Filesystem permissions → local admin principal with all scopes (D3) | All, including `/keys` |
| TCP loopback | on, `127.0.0.1:7878` | `Authorization: Bearer ckp_…` always (D4) | All except `/keys` |
| TCP remote | off | TLS 1.2+ **and** bearer key; refuses to start without TLS | All except `/keys` |

Keys are never accepted in URLs or query strings.

## 3. Scopes

| Scope | Routes |
|---|---|
| *(public)* | `GET /livez`, `GET /openapi.json` |
| `system:read` | `/status`, `/system`, `/hardware`, `/services`, `/report` (metric parts need `metrics:read` too) |
| `metrics:read` | `/metrics`, `/metrics/history…`, `/storage`, `/network`, `/sensors`, `/health`, `/events`, `/events/stream` |
| `processes:read` | `/processes` (D14) |
| `admin` | `/keys…`, `/settings`, privileged fields in `/status` and `/report` |

No scope implies another (D8). Keys may hold `system:read`,
`metrics:read` and `processes:read`; `admin` is reserved for the socket
principal.

## 4. Endpoints

### Phase 0

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/livez` | public | `{"status":"ok"}`; no other information |
| GET | `/openapi.json` | public | The embedded spec |
| GET | `/status` | `system:read` | Agent version, API version, uptime, machine ID, collectors and capability states |

### Phase 1

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/status` | `system:read` (+`admin` fields) | Collectors and capability states. With `admin`: listeners, request counters, active clients |
| GET | `/system` | `system:read` | OS, kernel, hostname, architecture, boot time, uptime |
| GET | `/hardware` | `system:read` | CPU details and caches, memory and modules, board/firmware, block devices, network interfaces, PCI and USB devices, GPUs; each section with `source` and availability |
| GET | `/services` | `system:read` | systemd services with counts; `?state=active\|failed\|inactive` (Phase 2) |
| GET | `/metrics` | `metrics:read` | Current snapshot. `?sections=cpu,memory,...,gpu` selects sections |
| GET | `/storage` | `metrics:read` | Block devices, filesystems (capacity, inodes), SMART summary |
| GET | `/network` | `metrics:read` | Interfaces (addresses, counters, rates), routes, DNS (passive; nothing is probed) |
| GET | `/sensors` | `metrics:read` | Temperatures, fans, voltages |
| GET | `/health` | `metrics:read` | Overall status, checks (D11 plus failed services and network errors), active findings |
| GET | `/events` | `metrics:read` | Events, newest first, paginated, `?severity=&since=&until=&type=`; persisted since Phase 3 |
| GET | `/events/stream` | `metrics:read` | Live events as Server-Sent Events (D17, Phase 2) |
| GET | `/processes` | `processes:read` | Processes, sorted/searchable, snapshot-bound cursors; no command lines (D14, Phase 2) |
| GET | `/keys` | `admin` (socket) | Key metadata. Never the key or its hash |
| POST | `/keys` | `admin` (socket) | Create. Response includes the full key once |
| POST | `/keys/{id}/rotate` | `admin` (socket) | Issue a replacement key (same name/scopes), revoke the old one immediately. Full key shown once |
| DELETE | `/keys/{id}` | `admin` (socket) | Revoke. Idempotent |

### Phase 3

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/metrics/history` | `metrics:read` | Stored series (`?series=` up to 10, `from`, `to` up to 90 days, `resolution=auto\|raw\|1m\|1h`); points carry avg/min/max (D18) |
| GET | `/metrics/history/series` | `metrics:read` | Recorded series names and units |
| GET | `/settings` | `admin` (socket) | Effective health thresholds and whether they come from the file or an override (D19) |
| PUT | `/settings` | `admin` (socket) | Replace the thresholds: validated like the file, persisted, applied at once, recorded as a `settings.changed` event |
| DELETE | `/settings` | `admin` (socket) | Return to the file's thresholds |
| GET | `/report` | `system:read` | Diagnostic report, `?format=json\|markdown&anonymize=true\|false` (D21; see below) |

### Phase 4

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/metrics?sections=custom` | `metrics:read` | Generic metrics from extension collectors (D22): one set per collector with availability, `dropped_series` and `problem`; each metric has `name`, `kind`, `unit`, `labels`, `value`, and for counters `rate_per_second` and `rate_unit` |

WebSocket is not planned: all streaming is server-to-client, which SSE
covers.

### Diagnostic report

`/report` is built from the other endpoints' responses, so each part has
the same schema as the endpoint it comes from:

- Always (`system:read`): `status`, `system`, `hardware`, `services`.
- With `metrics:read` as well: `metrics` (CPU, memory, pressure, sensors,
  GPU), `storage`, `network`, `health` and the latest 100 `events`.
- Over the socket (`admin`): `settings` and the admin part of `status`.

Parts the caller cannot read are absent and listed in `omitted` with a
reason, rather than failing the request. Sensitive fields stay redacted
(D9) in every format.

`anonymize=true` replaces, with placeholders that are stable within one
report: host names and DNS search domains (`host-1`,
`domain-1.example`), non-loopback, non-multicast IP addresses (RFC 5737 /
RFC 3849 documentation addresses, keeping prefix lengths and ports),
MAC-like strings (`02:00:00:00:00:01`), user names taken from `/home/…`
paths (`user-1`), and the machine ID. Identifiers are collected from the
fields that hold them and then replaced wherever they appear, including
event messages, so version numbers are never mistaken for addresses.
Anonymization cannot recognise identifiers that appear only in free text
(for example a host name inside a service description); review a report
before sharing it.

`format=markdown` returns `text/markdown`. Every string from the agent is
escaped, so device names or event text cannot inject Markdown or HTML.

## 5. Resource shape

```json
{
  "schema_version": "1",
  "machine_id": "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10",
  "generated_at": "2026-11-11T15:30:00Z",
  "cpu": {
    "status": "available",
    "collected_at": "2026-11-11T15:29:59Z",
    "stale": false,
    "source": "procfs",
    "data": {
      "usage_percent": 23.4,
      "window_seconds": 2.0,
      "per_core_usage_percent": [21.0, 25.8],
      "logical_cores": 2,
      "load_average": { "1m": 0.72, "5m": 0.81, "15m": 0.77 }
    }
  },
  "sensors": {
    "status": "unsupported",
    "collected_at": null,
    "stale": false,
    "source": "sysfs/hwmon",
    "reason": "No hwmon or thermal zone devices found.",
    "data": null
  }
}
```

- Availability is reported **per section**, not per number.
- Rates include the window they were computed over (`window_seconds`).
  The first sample after start has `null` rates.
- Optional fields inside an available section are `null` when the platform
  does not provide them, as documented per field in the spec.

## 6. Sensitive fields

Fields marked `x-cockpit-sensitive: true` in the spec (process command
lines, serial numbers, MAC addresses, WWNs, hardware UUIDs) are omitted by
default (D9). The containing object lists what was withheld:

```json
{ "name": "eth0", "mtu": 1500, "redacted": ["mac_address"] }
```

## 7. Errors

```json
{
  "error": {
    "code": "insufficient_scope",
    "message": "This key does not have the metrics:read scope.",
    "request_id": "req_01JCQ7Y8K2M4T6V8X0Z2B4D6F8"
  }
}
```

| HTTP | `code` examples |
|---|---|
| 400 | `invalid_request`, `invalid_parameter`, `invalid_host` (loopback listener received a non-loopback `Host` header) |
| 401 | `authentication_required`, `invalid_credentials` (missing, malformed, unknown, revoked, and expired keys are indistinguishable to the client) |
| 403 | `insufficient_scope` |
| 404 | `not_found` |
| 409 | `conflict` |
| 405 | `method_not_allowed` |
| 413 | `request_too_large` |
| 429 | `rate_limited` (with `Retry-After`) |
| 500 | `internal_error` |
| 503 | `unavailable` |

Messages are safe to show to users and never include secrets, paths to
secret files, stack traces, or raw command output.

## 8. Pagination

List responses use:

```json
{ "items": [ ... ], "next_cursor": "opaque-or-null" }
```

- `limit`: default 50, maximum 500; out-of-range values return `400`.
- `cursor`: opaque. A cursor that has expired returns `400` with code
  `invalid_cursor`; clients restart from the first page.
- For volatile collections (processes), the cursor is bound to the snapshot
  it was created from.

## 9. Key resources

Key metadata (never the key or hash):

```json
{
  "id": "k3p9x2m7q4ta",
  "name": "grafana-bridge",
  "display": "ckp_k3p9x2m7q4ta_…",
  "scopes": ["metrics:read"],
  "created_at": "2026-11-11T15:30:00Z",
  "expires_at": null,
  "last_used_at": "2026-11-11T16:02:11Z",
  "revoked_at": null,
  "rotated_from": null
}
```

Creation and rotation responses add `"key": "ckp_<id>_<secret>"` exactly
once. `last_used_at` is updated in memory and flushed periodically, so it
may lag by up to ~30 seconds.

## 10. Streaming

`GET /events/stream` (D17) authenticates at connection time, allows 4
streams per key and 64 in total, sends a heartbeat comment every 15
seconds, re-checks the key at every heartbeat (sending `event: closed`
when it was revoked or expired), uses a bounded queue per client (a slow
client gets `event: lagged` with the number dropped), and closes cleanly
on shutdown.
Browser clients (future web UI) cannot set headers on `EventSource`; they
will need a separately designed short-lived ticket. Keys in query strings
remain forbidden.

## 11. Rate limits

TCP requests are limited globally, per key, and (for failed
authentication) per client IP. A limited request gets `429` with
`code: rate_limited` and a `Retry-After` header in seconds. The Unix
socket is not limited. Defaults are in `docs/SECURITY.md` §9.

## 12. Client SDK and examples

`pkg/client` is the Go SDK and the only way the TUI talks to the agent. It
supports Unix-socket, TCP and TLS transports, never follows redirects, and
refuses to send a key over plain HTTP to a non-loopback host. `curl` and
Python examples are in `docs/SETUP.md`. Response semantics do not change
within `v1`.
