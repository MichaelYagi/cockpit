# API Contract

## 1. General conventions

-   Base path: `/api/v1`
-   Format: JSON
-   Time: UTC RFC 3339 timestamps
-   Authentication: `Authorization: Bearer <api-key>`
-   Content type: `application/json`
-   All responses should include or be traceable to a request ID.
-   API contract is versioned independently from the TUI.

The following endpoint list is a proposed MVP contract. Implementations
should publish a matching OpenAPI 3.x document.

## 2. Endpoints

  -----------------------------------------------------------------------
  Method                  Path                    Description
  ----------------------- ----------------------- -----------------------
  GET                     `/system`               OS identity, hostname,
                                                  kernel, uptime

  GET                     `/hardware`             Hardware inventory

  GET                     `/metrics`              Latest normalized
                                                  metrics

  GET                     `/metrics/history`      Historical metric
                                                  samples

  GET                     `/health`               Overall status and
                                                  findings

  GET                     `/temperatures`         Thermal sensors

  GET                     `/storage`              Devices, partitions,
                                                  filesystems, health
                                                  where supported

  GET                     `/network`              Interfaces and counters

  GET                     `/processes`            Paginated process list

  GET                     `/services`             Service state where
                                                  supported

  GET                     `/events`               Paginated event history

  GET                     `/events/stream`        Server-Sent Events

  GET                     `/stream`               Optional WebSocket live
                                                  metrics

  GET                     `/api-keys`             List key metadata;
                                                  never return
                                                  secret/hash

  POST                    `/api-keys`             Create a key; return
                                                  plaintext once

  DELETE                  `/api-keys/{id}`        Revoke a key

  GET                     `/status`               API/agent status and
                                                  capabilities
  -----------------------------------------------------------------------

Do not expose key creation or revocation to a read-only key. The local
owner/admin workflow should be separately protected.

## 3. Example metric response

``` json
{
  "schema_version": "1.0",
  "collected_at": "2026-11-11T15:30:00Z",
  "system": {
    "hostname": "workstation",
    "os": "linux",
    "architecture": "amd64"
  },
  "cpu": {
    "usage_percent": 23.4,
    "logical_cores": 16,
    "load_average": [0.72, 0.81, 0.77],
    "temperature_celsius": {
      "value": 48.0,
      "status": "available"
    }
  },
  "memory": {
    "total_bytes": 34359738368,
    "available_bytes": 17179869184,
    "used_percent": 50.0
  }
}
```

This is illustrative, not a promise that every field is available on
every machine. Optional fields should be omitted or accompanied by an
explicit availability state.

## 4. Availability model

Use a shared status vocabulary: - `available` - `unsupported` -
`permission_denied` - `temporarily_unavailable` - `not_configured`

Include `observed_at` or `collected_at` for measurements. Clients should
be able to identify stale samples.

## 5. Error envelope

Use a consistent shape:

``` json
{
  "error": {
    "code": "authentication_required",
    "message": "A valid API key is required.",
    "request_id": "req_01J..."
  }
}
```

Do not return internal stack traces or secrets. Suggested HTTP status
mapping: - 400 invalid request - 401 missing/invalid credentials - 403
insufficient scope - 404 unknown resource - 409 conflict - 429 rate
limit - 500 unexpected server error - 503 required subsystem temporarily
unavailable

## 6. Pagination and filtering

Large collections must be paginated. Use bounded `limit` and an opaque
`cursor` where practical. Support filters such as: - Process sort and
search. - Event severity and time range. - History metric name and time
range.

Reject unreasonable limits and malformed timestamps.

## 7. Authentication and scopes

Suggested initial scopes: - `system:read` - `hardware:read` -
`metrics:read` - `health:read` - `events:read` - `processes:read` -
`services:read` - `admin:keys`

Default newly created integration keys should receive only explicitly
selected read scopes. `admin:keys` should be restricted to a local
owner/admin workflow and never be included in a default integration key.

## 8. Streaming

SSE is the preferred first streaming mechanism for one-way health and
event updates. WebSocket may be added for high-frequency metric
streaming if there is a clear requirement.

Streaming connections must: - Authenticate at connection
establishment. - Enforce connection limits. - Send heartbeats. - Close
cleanly on shutdown. - Avoid unbounded per-client queues. - Respect key
revocation and expiry.

## 9. SDK compatibility

Publish the OpenAPI document and a small Go client SDK. Keep generated
clients optional. Document examples with curl and Python. Avoid changing
response semantics in a minor release; introduce incompatible changes
under a new API version.
