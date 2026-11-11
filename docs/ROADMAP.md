# Product Roadmap

## Guiding principle

Build a reliable vertical slice before broadening hardware coverage. The
first milestone should prove that collectors, normalized models, API
authentication, and the TUI work together through the API boundary.

## Phase 0 --- Foundation

-   [ ] Initialize Go module and repository structure.
-   [ ] Add `CLAUDE.md` and project documentation.
-   [ ] Implement configuration loading, defaults, and validation.
-   [ ] Add structured logging and request IDs.
-   [ ] Define core domain models and availability states.
-   [ ] Add graceful startup and shutdown.

**Exit criteria:** application starts with validated configuration, logs
clearly, and shuts down cleanly.

## Phase 1 --- MVP vertical slice

-   [ ] System identity and uptime collector.
-   [ ] CPU utilization/load collector.
-   [ ] Memory and swap collector.
-   [ ] Filesystem capacity collector.
-   [ ] Network interface counters and throughput.
-   [ ] Thermal sensor discovery where available.
-   [ ] Normalized snapshot service with partial-result handling.
-   [ ] REST endpoints for system, hardware, metrics, health, and
    status.
-   [ ] OpenAPI 3.x document.
-   [ ] API key creation, one-time display, listing metadata, and
    revocation.
-   [ ] Authentication middleware and read scopes.
-   [ ] Bubble Tea dashboard consuming the API.
-   [ ] API Access screen with server status and masked key metadata.
-   [ ] Loopback-only default binding.
-   [ ] Unit and integration tests.

**Exit criteria:** on a supported Linux host, the TUI displays live
system metrics from the authenticated API; a separate client can
retrieve the same data.

## Phase 2 --- Hardware and diagnostics

-   [ ] Detailed CPU inventory.
-   [ ] DIMM/memory inventory where available.
-   [ ] PCI and USB device inventory.
-   [ ] GPU inventory and vendor-specific metrics.
-   [ ] Storage device inventory.
-   [ ] SMART/NVMe health integration when tools and permissions allow.
-   [ ] Health rules for thermal thresholds, memory pressure, filesystem
    capacity, and storage warnings.
-   [ ] Events endpoint and SSE stream.
-   [ ] Processes endpoint with pagination.
-   [ ] systemd service status.
-   [ ] Network diagnostics.

**Exit criteria:** useful health findings explain the evidence and
identify unavailable capabilities without crashing.

## Phase 3 --- History and operational polish

-   [ ] SQLite persistence.
-   [ ] Configurable retention.
-   [ ] Historical metrics API.
-   [ ] Historical charts/sparklines in TUI.
-   [ ] Event filtering and pagination.
-   [ ] API request statistics.
-   [ ] Configurable thresholds and refresh intervals.
-   [ ] Diagnostic report export.
-   [ ] systemd service packaging.
-   [ ] Installation and upgrade documentation.

**Exit criteria:** the application can run unattended as a headless
service and retain bounded history.

## Phase 4 --- Extensibility

-   [ ] Public collector SDK/interface documentation.
-   [ ] Collector capability registry.
-   [ ] Compatibility/version metadata.
-   [ ] Example third-party collector compiled into a development build.
-   [ ] Go API client SDK.
-   [ ] Python and TypeScript examples.
-   [ ] Docker/Podman, ZFS, UPS, and additional GPU collectors.

**Exit criteria:** an external developer can build and test an
additional collector without changing core collection or TUI packages.

## Phase 5 --- Multi-machine and cross-platform

-   [ ] Headless agent mode.
-   [ ] Remote TLS configuration and certificate guidance.
-   [ ] Agent identity and enrollment design.
-   [ ] Central fleet dashboard.
-   [ ] Fleet health and comparison views.
-   [ ] Windows collector implementation.
-   [ ] macOS collector implementation.
-   [ ] Platform capability conformance tests.

**Exit criteria:** multiple agents can be monitored through a common API
contract with secure enrollment and transport.

## Explicit non-goals for MVP

-   Cloud-hosted control plane.
-   User accounts or SaaS billing.
-   Arbitrary runtime plugin loading.
-   Remote shell or command execution.
-   Automated system remediation.
-   Full Kubernetes observability.
-   Automatic firewall or network configuration changes.
-   AI diagnosis requiring external data transfer.

## Quality gates

Before a milestone is considered complete: - `gofmt` passes. -
`go vet ./...` passes. - `go test ./...` passes. - API contract is
updated. - Security implications are reviewed. - Missing permissions and
unsupported hardware are tested. - Documentation reflects actual
behavior.
