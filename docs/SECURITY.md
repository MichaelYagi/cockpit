# Security Model

## 1. Threat model

Cockpit exposes sensitive operational information: process names,
hostnames, network addresses, device models, filesystem layout, and system
events. The API is a privileged observability surface even though it is
read-only.

Threats:

- Unauthorized access from another local user or a network host.
- Malicious web pages reaching the loopback API (DNS rebinding,
  cross-origin requests).
- API key theft from logs, shell history, screenshots, backups, or
  configuration files.
- Resource exhaustion through excessive requests or streams.
- A compromised or buggy collector or external tool.
- Accidental remote exposure on an untrusted network.
- Information disclosure through verbose errors, diagnostics, or
  hardware identifiers.

## 2. Listeners and network defaults

| Listener | Default | Protection |
|---|---|---|
| Unix socket | on | Filesystem permissions (D3) |
| TCP loopback `127.0.0.1:7878` | on | API key always (D4), `Host` check, CORS only for listed origins (D34); also serves the keyless dashboard files at `/ui/` (D35) |
| TCP remote | off | Explicit configuration (`api.tcp.address` + `api.tls`, or `api.lan`, D37) + TLS + API key |
| TCP LAN, plain HTTP | off | Explicit `api.lan.http` (D38) + API key; private-network clients only; no encryption |
| TCP public, plain HTTP | off | Explicit `api.lan.public` (D42) + API key; any client, including the internet; no encryption |

- Never bind to `0.0.0.0` or `::` unless explicitly configured.
  `api.lan` (or `cockpit agent --lan`) is that explicit step: it binds
  every interface and, without configured files, uses a certificate the
  agent issues itself from a private CA in `<state dir>/tls` (keys `0600`,
  owned by the agent). It never turns off the API key (D37).
- **Plain HTTP on the LAN (D38).** `api.lan.http` (`--lan-http`) is an
  explicit opt-in for plain HTTP on every interface. API keys and data then
  travel unencrypted, so anyone on the same network can read them and
  replay the key. The listener closes connections from addresses outside
  loopback, private (RFC 1918, `fc00::/7`) and link-local ranges before
  reading a byte, logs a warning at startup, and still requires a key on
  every request. Clients send a key over plain HTTP only to a
  private-network IP address, never to a host name. Prefer `--lan` on
  networks you do not fully control (shared Wi-Fi, offices).
- **Plain HTTP from anywhere (D42).** `api.lan.public` (`--http-public`)
  drops the private-network filter: any address may connect, and keys and
  data cross the internet unencrypted. An API key is still required, and
  failed keys are rate limited per client IP. Anyone on the path can
  read and replay a key, so use narrow scopes and revoke keys you do not
  need.
- A non-loopback bind without valid TLS configuration is a **startup
  error**, never a warning or fallback.
- Log a prominent warning at startup whenever a remote listener is active.
- Do not assume a private LAN is trusted. Client IP allowlists are a
  candidate future feature.
- **Browsers (D34).** By default no web page can read API responses: the
  agent answers no CORS preflight. `api.cors.allowed_origins` lists exact
  origins (scheme, host and port; no wildcards, no `null`) that may call
  the TCP listener from a browser, with `GET` only. Credentials (cookies)
  are never allowed; the page must send an API key, so a listed origin
  gets no more than the key it is given. Preflights from unlisted origins
  get `403 origin_not_allowed`. The Unix socket never answers browsers.
  Only list sites you trust with a key: the page's code sees the key.
- **Web dashboard (D35).** The TCP listener serves static files at `/ui/`
  without a key. They contain no data. The page keeps the key the user
  enters in the tab's `sessionStorage` (gone when the tab closes; never in
  cookies or `localStorage`) and sends it as an `Authorization` header to
  the same origin, so another site cannot make requests with it. Responses
  carry a strict Content Security Policy (`default-src 'none'`; scripts,
  styles and connections from the same origin only; no framing), `nosniff`,
  `no-referrer` and `X-Frame-Options: DENY`. API text is inserted as text,
  never as HTML; a test rejects `innerHTML` and similar in the page's code.
  The dashboard cannot manage keys or settings, which stay on the Unix
  socket. Turn it off with `api.web.enabled: false`. Anyone who can reach
  the listener can load the page, but sees nothing without a key.

## 3. Local administration (Unix socket)

- System mode: `/run/cockpit-monitor/agent.sock`, directory `0750`, socket
  `0660`, owner `cockpit-monitor:cockpit-monitor`.
- User mode: `$XDG_RUNTIME_DIR/cockpit-monitor/agent.sock`, directory
  `0700`, socket `0600`.
- The agent creates the directory and socket with these modes itself and
  refuses to start if an existing directory is more permissive or owned by
  someone else.
- A socket connection is the local administrator principal. **Membership
  in the `cockpit-monitor` group grants full local admin.**
- Peer credentials (`SO_PEERCRED`) are logged for audit.
- Key management routes exist only on the socket (Phase 1).

## 3a. Fleet console

The fleet console (D30) is only a client: no listener, no agent change.

- It polls each machine with that machine's own key, over pinned TLS
  (`fleet add` pins the certificate after confirmation; non-interactive
  enrollment needs an explicit fingerprint or `--yes`).
- Enrollment records the machine ID. A different ID, or a certificate
  that no longer matches the pin, makes the console refuse the machine and
  stop polling until it is enrolled again with `--replace`.
- `fleet.yaml` and every key file must be `0600`. Keys are never written
  into the list.
- All text from agents is sanitized before display, as in the TUI.
- The console host holds a key for every machine. Keys should be
  read-only and narrow (`system:read`, `metrics:read`), and revoked on the
  machine when it leaves the fleet.

## 3b. Windows admin pipe

On Windows the local admin transport (D3) is a named pipe (D31):
`\\.\pipe\cockpit-monitor` in system mode, and
`\\.\pipe\cockpit-monitor-<user SID>` in user mode.

- Its protected DACL grants access only to SYSTEM, the Administrators
  group and the agent's own account. Remote pipe clients are rejected.
- The agent refuses to start if the pipe name already exists, so the name
  cannot be claimed first.
- Clients check that the pipe's server process runs as SYSTEM, an
  administrator or themselves before sending a request.
- The client process ID is logged for audit; Windows has no UID.
- Windows has no POSIX file modes. State and configuration live in
  `%LOCALAPPDATA%` and `%APPDATA%` (user mode), protected by the profile's
  ACLs, or `%ProgramData%\cockpit-monitor` (system mode). The Unix `0600`
  checks do not apply there; restrict `%ProgramData%\cockpit-monitor` to
  SYSTEM and Administrators when running as a service.

## 4. API keys

- Format `ckp_<id>_<secret>`: 12-character random base32 ID, 256-bit
  secret from `crypto/rand` encoded as 43 base62 characters (D5).
- Only `SHA-256(full key)` is stored, with metadata: ID, name, scopes,
  created, optional expiry, last used, revoked, rotated-from.
- Verification: parse → look up ID → constant-time hash comparison →
  revocation and expiry checks. All failure reasons return the same `401`.
- The full key is displayed exactly once, at creation or rotation. It cannot
  be shown again because the server does not have it.
- Rotation issues a new key with the same name and scopes and revokes the
  old key immediately. Revocation takes effect on the next request.
- The TUI and CLI require explicit confirmation before rotation or
  revocation.
- Keys are never accepted in URLs or query strings and never written to the
  YAML configuration.
- The `ckp_` prefix allows secret scanners to detect leaked keys.

## 5. Authorization

- Each route declares one required scope or is explicitly public. A test
  walks the route table and fails on any undeclared route.
- No scope implies another. Read-only keys can never perform admin
  operations (D8).
- Phase 1 keys may hold only `system:read` and `metrics:read`.
- No remote system-control actions exist. Monitoring is read-only.

## 6. Sensitive data

- Command lines, serial numbers, MAC addresses, WWNs and hardware UUIDs
  (including GPU UUIDs) are hidden by default and redacted at the API
  boundary (D9). Process command lines are never even read.
- Process lists need the separate `processes:read` scope (D14); they reveal
  user names and running software. Container lists (D24) use the same
  scope. They come from cgroup files only. The agent never uses the
  Docker or Podman API socket, which is root-equivalent.
- UPS monitoring (off by default) opens an outbound TCP connection to the
  configured Network UPS Tools server, runs only `LIST` commands and
  never logs in. UPS names from the server are validated before being
  sent back. Replies are bounded in line length and count. UPS serial
  numbers are never read.
- Interface IP addresses, routes and DNS servers need `network:read`
  (D27). Without it, addresses are withheld and listed in `redacted`, and
  routes and DNS report `permission_denied`. Keys created before v0.5.0
  that had `metrics:read` were given `network:read` by a migration, so
  their behavior did not change. Revoke and recreate them to narrow them.
- Process command lines are never read or exposed (D28).
- `machine_id` is a random identifier generated by the agent, never
  `/etc/machine-id` or DMI UUIDs.
- Fixtures and test snapshots must not contain real host identifiers.
- Diagnostic reports (D21) apply the same redaction. A report over TCP
  contains only what the key's scopes allow and lists what it left out;
  thresholds and listener details are socket only. `cockpit report -o`
  writes files with mode `0600`. Anonymization replaces identifiers found
  in identifying fields (host name, search domains, addresses, `/home`
  paths) wherever they appear, plus MAC-like strings; it is a convenience
  for sharing, not a guarantee, and identifiers that appear only in free
  text are not recognised. Markdown output escapes all agent text.

## 7. Untrusted text in the terminal UI

Process names (any local user), USB product strings (the device itself)
and service descriptions are controlled by others. The API returns them
JSON-escaped. The TUI replaces control characters (C0, C1, ESC) and
invalid UTF-8 in every response before rendering, so they cannot inject
terminal escape sequences. Tests use a hostile process name and USB
product string.

## 8. TLS

- TLS 1.2 minimum; TLS 1.3 preferred. Go's default cipher suites.
- The certificate and key are file paths. The agent refuses to start if the
  key file is readable by group or others.
- No silent plaintext fallback.
- `cockpit tls init` (D29) creates a private CA (ECDSA P-256, 10 years)
  and a server certificate (1 year). Keys are written `0600` atomically, the
  directory is `0700`, and the CA key never leaves root in system mode. The
  CA is limited to one level (`MaxPathLenZero`).
- Clients trust a server either through a CA (`--tls-ca`, system roots by
  default) or by pinning the leaf certificate's SHA-256 fingerprint
  (`--tls-fingerprint`). A pinned connection accepts exactly that
  certificate, and only within its validity period. Pinning and private CAs
  require `https`.
- `client.ServerCertificate` reads a server's certificate without
  trusting it, so its fingerprint can be confirmed before pinning. It never
  sends a key.
- The agent logs a warning daily from 30 days before its certificate
  expires, and an error once it has expired.

## 9. Logging and audit

Never log:

- API keys, `Authorization` headers, TLS private keys.
- Request bodies (by default).
- Sensitive hardware identifiers or command lines.

Log:

- Request ID, method, route template, status, latency, principal (key ID or
  `local-admin` + peer UID).
- Key creation, rotation and revocation (key ID and name only).
- Listener configuration at startup, with warnings for remote listeners.
- Authentication failures, rate-limited, without echoing credentials.

Logging goes through a redaction helper. Tests capture log output and fail
if any key material appears.

## 10. Resource protection

- Rate limiting on TCP (`golang.org/x/time/rate`), `429` with
  `Retry-After`: global (default 200/s, burst 400), per key (20/s, burst
  40), and failed authentication per client IP (1/s, burst 10). Keys are
  always verified before the failure budget is consulted, so another
  local process sending bad keys cannot lock out a valid key. Limiter
  memory is bounded (4096 tracked IDs). The socket is not rate limited.
- A storage failure while verifying a key returns `500`, never `401`, so
  outages are not mistaken for revoked keys.
- Event streams: 4 per key, 64 in total; bounded per-client queues; the
  key is re-checked every 15 seconds; shutdown closes streams first.
- Process listing is sampled at most once per 2 seconds for everyone,
  however often clients ask.
- External tools (`smartctl`, `nvidia-smi`) run without a shell, with
  fixed arguments, a minimal environment, bounded output and a timeout.
- D-Bus access is limited to systemd's read-only `ListUnits` call.
- `ReadHeaderTimeout`, idle timeouts, and maximum header size on every
  server. Write deadlines are set per route, so SSE is not cut off.
- Request body limit 64 KiB; bounded pagination and query ranges.
- Collector and external-tool timeouts. External tools run without a shell,
  with absolute paths resolved at startup and fixed arguments.
- No unbounded goroutines or queues.
- **Endpoint checks (D40, D41)** connect only to targets in the
  configuration file or added by the local admin over the Unix socket (at
  most 64 in total); API keys can never add one: 8 at a time, one
  timeout each, no proxy, no redirects, a new connection per check and at
  most 64 KiB read per response. Target URLs may name internal hosts, so
  they need `network:read` and are dropped from anonymized reports. URLs
  with a user name or password are refused.
- **Notifications (D39)** are the agent's only other outbound HTTP requests.
  Destinations come only from the configuration file (at most 8), never
  from the API. Each webhook has a 64-message queue, a rate limit (20 a
  minute), a 10-second timeout and at most two retries; redirects are not
  followed. A slow or failing webhook never blocks health evaluation.
  Messages contain finding summaries (mount points, service and disk
  names) and the machine name, which go to the service behind the URL.

## 11. Privileges

- The agent never needs root for ordinary metrics.
- Do not run the agent as root to get one privileged metric.
- SMART/NVMe data requires raw device access. The agent never gets it.
  In system mode, the optional `cockpit-smart.service` (D20) runs
  `cockpit smart-helper` as root once an hour, with only
  `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN`, `CAP_DAC_OVERRIDE` and `CAP_CHOWN`, no
  network and a read-only file system apart from
  `/var/lib/cockpit-monitor`. It runs fixed `smartctl` arguments found in
  the system directories (never through `$PATH`) and writes results the
  agent reads. Because the parent directory belongs to the agent's user, the
  helper opens its directory without following symlinks, requires it to be
  root-owned and not writable by others, and writes only through that
  directory handle (create-exclusive temporary file, then rename). The
  agent parses the files exactly like `smartctl` output, with the same size
  bound. Without the helper, disk health reports `permission_denied`.
- The system service runs as `cockpit-monitor` with no capabilities and
  systemd hardening (see SETUP).
- The TUI and setup docs list which capabilities are unavailable without
  elevated permissions.
- Never change firewall rules, services, kernel settings or system
  configuration.

## 12. Files and modes

| File | Mode | Notes |
|---|---|---|
| State directory | `0700` | Refused if more permissive or owned by another user |
| `cockpit.db` (+ WAL) | `0600` | Key SHA-256 hashes and metadata only |
| `machine-id`, `agent.lock` | `0600` | Not secret |
| `embedded-agent.log` (+ `.1`) | `0600` | Same redaction as agent logs; capped at 10 MiB, one older copy kept |
| Runtime directory | `0700` user / `0750` system | Holds the socket |
| `agent.sock` | `0600` user / `0660` system | Access = local admin |
| `smart/` (system mode) | `0750` `root:cockpit-monitor` | Written by the SMART helper only; refused if not root-owned |
| `smart/*.json` | `0640` | `smartctl` output per disk, bounded to 1 MiB |
| Report files (`cockpit report -o`) | `0600` | Tightened if the file existed |
| TLS private key | must be `0600` | Agent refuses group/other access |
| Webhook URL files (`notifications.webhooks[].url_file`) | must be `0600` | The URL is a credential; never logged, and removed from delivery errors (D39) |

A newly created key is printed once: on stdout by `cockpit keys create`
(so it can be redirected to a file), or in a dialog in the TUI. The TUI
uses the alternate screen, so the key does not remain in terminal
scrollback, and the key is erased from memory when the dialog closes.
`c` in the dialog sends the key to the terminal's clipboard with OSC 52; it
is never written to a file or log. The terminal decides whether to accept
the request (and over SSH, the clipboard is the one on the machine running
the terminal).

## 13. Extensions

Cockpit never loads code at runtime: no shared libraries, Go plugins,
scripts or plugin binaries. Third-party collectors (D23) are Go code
compiled into a custom build with `custom.Main`.

- **Trust.** Extension code runs in the agent process with the agent's
  privileges. It can read everything the agent can, including the key
  database. Treat it like any dependency: review it, pin its version, and
  build only what you trust. A Go interface is not a sandbox.
- **What the agent enforces.** The ID and capability namespace
  (`custom.<name>`); the SDK major version, so an incompatible collector
  stops the build from starting; interval and timeout bounds; per-call
  deadlines and panic recovery; and the generic-metric rules (names, units,
  at most 8 labels of 128 bytes each, finite values, at most 500 series per
  collector, D22). Metrics that break these rules are dropped and
  reported, never fatal.
- **What it cannot enforce.** It can't stop extension code from leaking
  goroutines, using memory or reading files. It also can't stop secrets
  being put into metric names, labels or status reasons. Those go to every
  `metrics:read` client, so the collector guide forbids them.
- **Display.** Labels, names and reasons are untrusted text in the TUI
  (sanitized, including map keys) and in Markdown reports (escaped).

Runtime loading would need its own security design first: trust and
installation, least privilege, signing, version checks and resource
limits. It is not planned.
