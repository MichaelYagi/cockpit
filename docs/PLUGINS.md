# Collectors and Plugin Model

## 1. Terminology

A **collector** gathers a defined set of telemetry for one platform. A
**plugin** is a broader future extension that may register collectors,
health checks, or integrations. The MVP has only statically compiled,
trusted collectors. Dynamic third-party loading is out of scope.

## 2. Design goals

- Independent collector lifecycle.
- Capability discovery at startup (`Probe`) and status on every collection.
- Partial success is the normal case.
- Platform-specific code behind platform-neutral contracts.
- Bounded collection time and resource use.
- Testable against fixture file trees and a fake clock.
- No dependency from the TUI on collectors.

## 3. Contract

This replaces the earlier sample interface. The main changes are no
`error` return (partial failure is expressed per capability), an explicit
`Probe` step, and typed records.

```go
package collector

type Class int

const (
    Inventory Class = iota // runs at start and on rare refresh
    Sampler                // runs on an interval
)

type Descriptor struct {
    ID              string            // "linux.cpu"
    Version         string            // semver of the collector
    Capabilities    []core.Capability // "cpu.usage", "sensors.thermal", ...
    Class           Class
    DefaultInterval time.Duration
    DefaultTimeout  time.Duration
}

// Env is injected so collectors can be tested against fixture trees.
type Env struct {
    ProcRoot string // "/proc" in production
    SysRoot  string // "/sys"
    EtcRoot  string // "/etc"
    DevRoot  string // "/dev"
    Now      func() time.Time
    Logger   *slog.Logger
    Tools    ToolFinder // resolves optional external tools (e.g. smartctl)
    // SMARTResultsDir holds the privileged SMART helper's results (D20);
    // empty when no helper is configured (user mode).
    SMARTResultsDir string
}

type Collector interface {
    Descriptor() Descriptor
    // Probe runs once at startup and reports which capabilities exist
    // and whether the agent is permitted to read them.
    Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus
    // Collect returns whatever it could gather. It must respect ctx.
    Collect(ctx context.Context) Result
    Close(ctx context.Context) error
}

type Result struct {
    CollectedAt time.Time
    Records     []core.Record                              // typed, e.g. *core.CPUCounters
    Status      map[core.Capability]core.CapabilityStatus  // availability + safe reason
}
```

- `core.Record` is a sealed interface: only types in `internal/core`
  implement it. Adding a domain means adding a record type and updating the
  OpenAPI spec, which is appropriate because the contract changes too.
- For future third-party collectors, a single generic record type
  (`core.GenericMetric{Name, Labels, Value, Unit}`) will be added rather
  than opening the sealed set.
- Collectors return **raw counters**. Rates are computed by the engine.
- Collectors are stateless between calls where possible. Any state they
  keep must be bounded.
- The engine handles timeouts and panics: a collector that exceeds its
  deadline or panics yields `temporarily_unavailable` for its
  capabilities; other collectors are unaffected.
- Collectors are listed explicitly by `linux.Collectors(env)`. There is no
  `init()` self-registration.

## 4. Capabilities

Dotted identifiers: `system.identity`, `system.uptime`, `cpu.usage`,
`cpu.load`, `cpu.frequency` (not collected yet), `memory.usage`, `memory.pressure`,
`storage.devices`, `storage.filesystems`, `storage.smart`,
`network.interfaces`, `network.throughput`, `sensors.thermal`,
`sensors.fan`, later `gpu.*`, `services.systemd`, `processes.list`.

A collector declares what it can **potentially** provide. `Probe` and each
`Result` report what is **actually** available right now.

## 5. Built-in Linux collectors

| ID | Phase | Capabilities | Sources |
|---|---|---|---|
| `linux.system` | 1 | `system.identity`, `system.uptime` | `/etc/os-release`, `uname(2)`, `/proc/uptime`, `/proc/sys/kernel/hostname` |
| `linux.cpu` | 1 | `cpu.usage`, `cpu.load` | `/proc/stat`, `/proc/loadavg`, `/proc/cpuinfo` |
| `linux.memory` | 1 | `memory.usage`, `memory.pressure` | `/proc/meminfo`, `/proc/pressure/memory` |
| `linux.filesystems` | 1 | `storage.filesystems` | `/proc/self/mountinfo`, `statfs(2)` |
| `linux.blockdevices` | 1 | `storage.devices` | `/sys/block`, `/sys/class/nvme` |
| `linux.network` | 1 | `network.interfaces`, `network.throughput` | `/sys/class/net`, `/proc/net/dev` |
| `linux.sensors` | 1 | `sensors.thermal`, `sensors.fan` | `/sys/class/hwmon`, `/sys/class/thermal` |
| `linux.smart` | 1 | `storage.smart` | `smartctl --json` (optional, ≥ 7.0), hourly |
| `linux.processes` | 2 | `processes.list` | `/proc/[pid]`, `/etc/passwd`; on demand, never scheduled |
| `linux.systemd` | 2 | `services.systemd` | systemd D-Bus `ListUnits` (godbus, D15), every 10s |
| `linux.cpuinfo` | 2 | `hardware.cpu` | `/proc/cpuinfo`, `/sys/devices/system/cpu` |
| `linux.pci` | 2 | `hardware.pci` | `/sys/bus/pci`, `pci.ids` when installed |
| `linux.usb` | 2 | `hardware.usb` | `/sys/bus/usb`, `usb.ids` when installed |
| `linux.dmi` | 2 | `hardware.board`, `hardware.memory_modules` | `/sys/class/dmi/id`, SMBIOS type 17 (root-only) |
| `linux.gpu` | 2 | `gpu.devices`, `gpu.metrics` | PCI/DRM; `nvidia-smi` CSV query; `amdgpu` sysfs (D16); at least every 5s |
| `linux.netconfig` | 2 | `network.routes`, `network.dns` | `/proc/net/route`, `/proc/net/ipv6_route`, `resolv.conf` |

Filesystem collection skips pseudo, in-memory and image filesystems
(`proc`, `sysfs`, `cgroup2`, `tmpfs`, `squashfs`, `rootfs`, …), overlays
other than the root filesystem, and network filesystems (`nfs`, `cifs`,
`sshfs`, …) whose `statfs` can hang. Bind mounts of the same device are
reported once, at the shortest mount point. The skip lists are fixed in
Phase 1.

### External tools

Optional tools such as `smartctl` are:

- Detected at startup by absolute path; missing → `not_configured`.
- Run without a shell, with fixed arguments, a timeout, and a low
  frequency.
- Used only through structured output (`--json`). Human-readable output is
  never parsed.
- Reported in `/status` as optional dependencies.

Collectors that run an external tool declare `MinInterval` so
configuration cannot schedule them more often than the tool tolerates.

### On-demand collectors

`linux.processes` implements `collector.ProcessLister` and is never
scheduled. The engine samples it only when a client asks, caches the
result for 2 seconds, takes two samples 0.5 seconds apart when no recent
one exists (CPU usage needs a delta), and keeps a few recent views so
cursors stay valid while paging.

## 6. Scheduling

- Per-collector interval and timeout from configuration, with defaults
  from the descriptor.
- Start times are staggered with jitter.
- At most one in-flight `Collect` per collector; a slow collector skips
  ticks rather than piling up.
- Last good data is kept and marked stale (2× interval).
- Costly sources (SMART) run far less often than cheap counters.

## 7. Testing

Every collector has:

- Parsing/normalization tests against sanitized fixture trees in
  `internal/platform/linux/testdata/` (several kernels and distros, Alpine,
  WSL, containers, machines without sensors).
- Tests for missing files, permission errors (skipped when running as
  root), malformed input, unsupported hardware.
- Cancellation and timeout tests.
- Fuzz tests for each parser.
- A `linux && integration` test against the real host that checks
  invariants, not exact values.

Fixture data must not contain real host identifiers.

## 8. Future extension model

Candidates: NVIDIA/AMD GPU, Docker/Podman, Kubernetes node metrics,
ZFS/mdraid, UPS, fan controllers, VMs, enterprise sensors.

Before implementing dynamic plugins, write a separate security design. A Go
interface is not a safe boundary for untrusted code.
