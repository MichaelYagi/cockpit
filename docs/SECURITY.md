# Security Model

## 1. Threat model

Linux Cockpit may expose sensitive operational information: process
names, hostnames, network addresses, device models, filesystem layout,
and system events. Treat the API as a privileged observability surface
even when it is read-only.

Threats include: - Unauthorized access from another local user or
network host. - API key theft from logs, shell history, screenshots,
backups, or configuration files. - Excessive API requests causing
resource exhaustion. - A compromised third-party collector. - Accidental
remote exposure on an untrusted network. - Information disclosure
through verbose errors or diagnostic reports.

## 2. Network defaults

-   Bind to `127.0.0.1` by default.
-   Do not bind to `0.0.0.0` automatically.
-   Remote binding must be an explicit configuration choice.
-   Require TLS for non-loopback connections.
-   Provide clear warnings when remote access is enabled.
-   Consider optional client IP allowlists for remote deployments.
-   Do not assume a private LAN is trusted.

## 3. API key generation and storage

-   Generate at least 256 bits of entropy using Go `crypto/rand`.
-   Use a recognizable prefix for key identification, but do not treat
    the prefix as secret.
-   Display the full key only once immediately after creation.
-   Store only a cryptographic verifier/hash, never plaintext.
-   Prefer HMAC-SHA-256 with a server-side pepper stored separately from
    the database, or a carefully selected password-hashing approach.
    Document the trade-off and key-rotation implications.
-   Use constant-time comparison for secret verification where
    applicable.
-   Store metadata: key ID, name, scopes, creation time, optional
    expiry, last-used time, and revocation time.
-   Revoke keys immediately and reject expired keys.
-   Never put API keys in URLs or query strings.

The dashboard may show a masked representation and metadata after
creation. It must not be able to reveal the original plaintext later
because the server does not retain it.

## 4. Authorization

-   Default to read-only.
-   Enforce scopes on every protected route.
-   Separate local administrative key-management actions from ordinary
    integration keys.
-   Do not let a monitoring key create, list secret material for, or
    revoke other keys.
-   Do not implement remote system-control actions in the MVP.

## 5. TLS and certificates

-   Use TLS 1.2 or newer; prefer TLS 1.3 where supported.
-   Validate certificate/key file permissions.
-   Never silently fall back to plaintext for remote binding.
-   Document certificate provisioning and renewal.
-   Consider local self-signed certificate generation only with clear
    trust/bootstrap guidance.

## 6. Logging and audit

Never log: - API keys or Authorization headers. - TLS private keys. -
Secret configuration values. - Full request bodies by default.

Log: - Request ID, method, route template, status, latency, and
authenticated key ID (not secret). - Key creation/revocation events. -
Remote access configuration changes. - Authentication failures with rate
limiting and without echoing submitted credentials.

## 7. Resource protection

-   Rate-limit per key and globally.
-   Bound request sizes, pagination, concurrent streams, and history
    query ranges.
-   Apply timeouts to handlers and collectors.
-   Avoid unbounded goroutines and queues.
-   Use safe defaults for expensive diagnostics and benchmarks.

## 8. Plugin security

The MVP should compile trusted collectors into the application. Do not
load arbitrary native shared libraries or execute third-party plugin
binaries automatically.

Future extension mechanisms should define: - Explicit trust and
installation model. - Least-privilege execution. - Version and
compatibility checks. - Signed artifacts or trusted distribution. -
Resource limits and timeout behavior. - Clear permissions for any
collector that can access sensitive system interfaces.

## 9. Operational safety

-   Monitoring is read-only by default.
-   Do not run the entire application as root merely to access one
    privileged metric.
-   Prefer narrowly scoped capabilities, groups, or helper processes
    where necessary.
-   Explain which metrics are unavailable without elevated permissions.
-   Do not silently change firewall rules, services, kernel settings, or
    system configuration.
