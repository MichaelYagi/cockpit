# Testing Strategy

All tests run with `go test ./...`. CI additionally runs `-race`, the
integration tag on Linux, and cross-compilation. A test or command is
reported as passing only after it was actually run.

## 1. Layers

| Layer | What | How |
|---|---|---|
| Unit | `core`, `config`, `auth`, `health`, rate math, redaction | Table-driven tests, fake clock |
| Collector | Each Linux collector | Fixture trees via `collector.Env` roots; error, permission, malformed and cancellation cases; fuzzing |
| Engine | Scheduling, timeouts, panic isolation, staleness, snapshot publication | Fake collectors (slow, panicking, partial, flapping); `goleak` |
| Storage | SQLite key store, migrations | Temporary database per test |
| API integration | Handlers + middleware + real engine with fake collectors | `httptest`, over TCP **and** Unix socket |
| Contract | Server responses and client requests match `api/openapi.yaml` | `kin-openapi` validation (test-only) |
| Client | `pkg/client` | Against a spec-validating mock server |
| TUI | Views and layouts | `teatest` golden output at several terminal sizes, against a mock API server |
| Platform | Real `/proc`, `/sys` | `//go:build linux && integration`; checks invariants only |
| Architecture | Package boundaries | Test using `go list -deps` |

## 2. Contract testing (D6)

- Every route test validates the response (status, headers, body schema)
  against the spec.
- Every documented response code for a route has at least one test.
- Spec examples must decode into `pkg/apiv1` types with unknown fields
  disallowed.
- A test fails if a route is registered but missing from the spec, or the
  reverse.

## 3. Security testing

- Missing, malformed, unknown, revoked and expired keys → identical `401`.
- Wrong scope → `403`. A read-only key cannot reach admin routes.
- Key routes are absent on TCP listeners.
- Phase 1 key creation rejects the `admin` scope.
- The full key appears in exactly one response (creation/rotation).
- Captured logs never contain key material, `Authorization` values or
  sensitive fields.
- Route-table walk: every route has a declared scope or is on the public
  allowlist.
- Non-loopback bind without TLS → startup error.
- Socket directory or TLS key with permissive mode → startup error.
- Spoofed `Host` on loopback → rejected. Cross-origin preflight → no CORS
  headers.
- Rate limiting → `429` with `Retry-After`.
- Oversized body, `limit` out of range, malformed timestamps → `400`/`413`.
- Sensitive fields are omitted and listed in `redacted`.
- Fuzz tests for key parsing and query parameter parsing.
- `govulncheck` and `staticcheck` in CI.

## 4. Platform testing

- CI on Ubuntu (GitHub Actions) runs the integration tag as a non-root user.
- Fixture trees cover other distributions and environments without hardware
  (containers, WSL, VMs).
- Cross-compile on every change: `linux/amd64`, `linux/arm64`,
  `darwin/arm64`, `windows/amd64`, with `CGO_ENABLED=0`.
- Permission-denied tests are skipped when running as root, since root
  bypasses file modes.

## 5. Conventions

- Inject time (`func() time.Time`) and filesystem roots; never sleep in
  tests.
- No real host identifiers, keys or secrets in fixtures or golden files.
  Test keys are generated at runtime.
- Tests create files, sockets and databases under the OS temp directory,
  never in the repository. Socket tests use short `os.MkdirTemp` paths
  because Unix socket paths are limited to 107 bytes.
- Working from a WSL2 checkout on a Windows drive (`/mnt/c/...`) is
  supported. Unix permission tests need that drive mounted with the
  `metadata` option (set in `/etc/wsl.conf`).
- `internal/archtest` inspects packages through `go list`; it stats source
  files so Go's test cache invalidates when imports change.

## 6. Commands

| Command | Runs |
|---|---|
| `make check` | gofmt check, `go vet`, staticcheck, `go test -race`, cross-compile + vet for linux/amd64, linux/arm64, darwin/arm64, windows/amd64 |
| `make test` | `go test ./...` |
| `make test-integration` | Tests with the `integration` build tag |
| `make vuln` | govulncheck |
