# Collectors and Plugin Model

## 1. Terminology

A **collector** is a module that gathers a defined set of telemetry. A
**plugin** is a broader extension that may register collectors, health
rules, or integrations. In the MVP, use statically compiled, trusted
collector modules. Dynamic third-party loading is deferred.

## 2. Design goals

-   Independent collector lifecycle.
-   Capability discovery.
-   Partial success.
-   Platform-specific implementation behind portable contracts.
-   Bounded collection time and resource use.
-   Testable collectors with fake system inputs.
-   No direct dependency from the TUI on collectors.

## 3. Proposed contract

``` go
type Collector interface {
    ID() string
    Version() string
    Capabilities() []Capability
    Collect(ctx context.Context) (Snapshot, error)
    Close(ctx context.Context) error
}
```

A `Snapshot` should include: - Collector ID and version. - Collection
timestamp. - Measurements or inventory records. - Per-capability
availability. - Partial errors that are safe to expose.

The final model should make partial results explicit. Avoid a single
all-or-nothing error when one sensor fails but other data is available.

## 4. Capability model

Examples: - `cpu.usage` - `cpu.frequency` - `cpu.temperature` -
`memory.usage` - `gpu.utilization` - `gpu.memory` - `storage.devices` -
`storage.filesystems` - `storage.smart` - `network.interfaces` -
`network.throughput` - `sensors.thermal` - `services.systemd`

A collector should declare capabilities it can potentially provide.
Runtime responses indicate whether each capability is currently
available.

## 5. Initial built-in collectors

  -----------------------------------------------------------------------
  Collector               Initial responsibility  Possible Linux source
  ----------------------- ----------------------- -----------------------
  system                  OS, kernel, uptime,     `/etc/os-release`,
                          hostname                `uname`, `/proc/uptime`

  cpu                     Model, cores,           `/proc/stat`,
                          utilization, load       `/proc/cpuinfo`, sysfs

  memory                  RAM, swap, pressure     `/proc/meminfo`, PSI

  storage                 Devices, filesystems,   `/sys`, `/proc/mounts`,
                          capacity                statfs

  network                 Interfaces, counters,   netlink,
                          rates                   `/sys/class/net`

  sensors                 Temperatures and fans   hwmon, libsensors

  gpu                     GPU identity and        vendor APIs/tools
                          metrics                 

  services                systemd service state   systemd D-Bus

  processes               Process inventory and   `/proc`
                          resource use            
  -----------------------------------------------------------------------

Optional external tools such as `smartctl` and `nvme-cli` should be
detected at runtime and reported as optional dependencies.

## 6. Collection scheduling

-   Configure interval per collector or collector group.
-   Use timeouts and cancellation.
-   Keep last-known-good samples with timestamps.
-   Mark stale data explicitly.
-   Apply bounded concurrency.
-   Avoid synchronized spikes by staggering collectors when useful.
-   Do not poll high-cost sources at the same frequency as cheap
    counters.

## 7. Testing

Every collector should have: - Unit tests for parsing/normalization. -
Tests for missing files, permission errors, malformed input, and
unsupported hardware. - Tests for cancellation and timeout. - Fixture
data that does not contain real host identifiers. - A small integration
test on Linux where practical.

## 8. Future extension model

Potential future collectors: - NVIDIA / AMD GPU. - Docker / Podman. -
Kubernetes node and workload metrics. - ZFS / mdraid / hardware RAID. -
UPS devices. - Fan controllers. - Virtual machines. - SMART/NVMe
endurance and health. - Custom enterprise sensors.

Before implementing dynamic plugins, write a separate security design.
Do not equate a Go interface with a safe third-party plugin boundary.
