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
| `system:read` | `/status`, `/system`, `/hardware` |
| `metrics:read` | `/metrics`, `/storage`, `/network`, `/sensors`, `/health`, `/events` |
| `admin` | `/keys…`, privileged fields in `/status` |

No scope implies another (D8). Keys created in Phase 1 may hold
`system:read` and/or `metrics:read`; `admin` is reserved for the socket
principal.

## 4. Endpoints

### Phase 0

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/livez` | public | `{"status":"ok"}`; no other information |
| GET | `/openapi.json` | public | The embedded spec |
| GET | `/status` | `system:read` | Agent version, API version, uptime, collector list (stub in Phase 0) |

### Phase 1

| Method | Path | Scope | Description |
|---|---|---|---|
| GET | `/status` | `system:read` (+`admin` fields) | Collectors and capability states. With `admin`: listeners, request counters, active clients |
| GET | `/system` | `system:read` | OS, kernel, hostname, architecture, boot time, uptime |
| GET | `/hardware` | `system:read` | CPU model/topology, memory total, block devices, network interfaces; each with `source` |
| GET | `/metrics` | `metrics:read` | Current snapshot. `?sections=cpu,memory,...` selects sections |
| GET | `/storage` | `metrics:read` | Block devices, filesystems (capacity, inodes), SMART summary |
| GET | `/network` | `metrics:read` | Interfaces, addresses, counters, rates |
| GET | `/sensors` | `metrics:read` | Temperatures, fans, voltages |
| GET | `/health` | `metrics:read` | Overall status, the four checks (D11), active findings |
| GET | `/events` | `metrics:read` | Recent events (in-memory), paginated, `?severity=&since=` |
| GET | `/keys` | `admin` (socket) | Key metadata. Never the key or its hash |
| POST | `/keys` | `admin` (socket) | Create. Response includes the full key once |
| POST | `/keys/{id}/rotate` | `admin` (socket) | Issue a replacement key (same name/scopes), revoke the old one immediately. Full key shown once |
| DELETE | `/keys/{id}` | `admin` (socket) | Revoke. Idempotent |

### Later phases

`/events/stream` (SSE, Phase 2), `/processes` (Phase 2), `/services`
(Phase 2), `/metrics/history` (Phase 3). WebSocket is not planned: all
streaming is server-to-client, which SSE covers.

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
| 400 | `invalid_request`, `invalid_parameter` |
| 401 | `authentication_required`, `invalid_credentials` (missing, malformed, unknown, revoked, and expired keys are indistinguishable to the client) |
| 403 | `insufficient_scope` |
| 404 | `not_found` |
| 409 | `conflict` |
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

## 10. Streaming (Phase 2)

SSE connections authenticate at connection time, are limited per key and
globally, send heartbeats, close cleanly on shutdown, use bounded
per-client queues, and are closed when the key is revoked or expires.
Browser clients (future web UI) cannot set headers on `EventSource`; they
will need a separately designed short-lived ticket. Keys in query strings
remain forbidden.

## 11. Client SDK and examples

`pkg/client` is the Go SDK and the only way the TUI talks to the agent. It
supports Unix-socket, TCP and TLS transports. Documentation includes `curl`
and Python examples. Response semantics do not change within `v1`.
