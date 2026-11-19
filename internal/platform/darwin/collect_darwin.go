//go:build darwin

package darwin

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

// ---- hardware inventory ----

func hardware(r *collector.Result) {
	str := func(name string) (string, bool) {
		v, err := unix.Sysctl(name)
		return v, err == nil && v != ""
	}
	num := func(name string) (uint64, bool) {
		if v, err := unix.SysctlUint64(name); err == nil {
			return v, true
		}
		if v, err := unix.SysctlUint32(name); err == nil {
			return uint64(v), true
		}
		return 0, false
	}
	info := cpuInfo(runtime.GOARCH, str, num)
	if info.ModelName == "" && info.Threads == 0 {
		r.Status[core.CapHardwareCPU] = portable.Unavailable(core.TemporarilyUnavailable, "The processor could not be identified.")
	} else {
		r.Status[core.CapHardwareCPU] = portable.Available()
		r.Records = append(r.Records, info)
	}
	if model, ok := str("hw.model"); ok {
		r.Status[core.CapHardwareBoard] = portable.Available()
		r.Records = append(r.Records, &core.BoardInfo{SystemVendor: "Apple", ProductName: model})
	} else {
		r.Status[core.CapHardwareBoard] = portable.Unavailable(core.TemporarilyUnavailable, "The Mac model could not be read.")
	}
}

// ---- devices: PCI, USB and disks from the IOKit registry ----

func devices(r *collector.Result, k *ioKitLib, loadErr error) {
	if loadErr != nil {
		reason := "Device lists need IOKit, which could not be loaded: " + loadErr.Error()
		for _, c := range []core.Capability{core.CapHardwarePCI, core.CapHardwareUSB, core.CapStorageDevices} {
			r.Status[c] = portable.Unavailable(core.Unsupported, reason)
		}
		return
	}
	if pci, ok := k.pciDevices(); !ok {
		r.Status[core.CapHardwarePCI] = portable.Unavailable(core.TemporarilyUnavailable, "The IOKit registry could not be read.")
	} else if len(pci) == 0 {
		r.Status[core.CapHardwarePCI] = portable.Unavailable(core.Unsupported, "No PCI devices are visible.")
	} else {
		r.Status[core.CapHardwarePCI] = portable.Available()
		r.Records = append(r.Records, pci)
	}
	if usb, ok := k.usbDevices(); !ok {
		r.Status[core.CapHardwareUSB] = portable.Unavailable(core.TemporarilyUnavailable, "The IOKit registry could not be read.")
	} else if len(usb) == 0 {
		r.Status[core.CapHardwareUSB] = portable.Unavailable(core.Unsupported, "No USB devices are connected or visible.")
	} else {
		r.Status[core.CapHardwareUSB] = portable.Available()
		r.Records = append(r.Records, usb)
	}
	if disks, ok := k.disks(); !ok {
		r.Status[core.CapStorageDevices] = portable.Unavailable(core.TemporarilyUnavailable, "The IOKit registry could not be read.")
	} else if len(disks) == 0 {
		r.Status[core.CapStorageDevices] = portable.Unavailable(core.Unsupported, "No disks were found.")
	} else {
		r.Status[core.CapStorageDevices] = portable.Available()
		r.Records = append(r.Records, disks)
	}
}

// ---- GPUs ----

func gpus(r *collector.Result, k *ioKitLib, loadErr error) {
	if loadErr != nil {
		r.Status[core.CapGPUDevices] = portable.Unavailable(core.Unsupported, "GPUs need IOKit, which could not be loaded: "+loadErr.Error())
		r.Status[core.CapGPUMetrics] = r.Status[core.CapGPUDevices]
		return
	}
	list, ok := k.gpus()
	switch {
	case !ok:
		r.Status[core.CapGPUDevices] = portable.Unavailable(core.TemporarilyUnavailable, "The IOKit registry could not be read.")
		r.Status[core.CapGPUMetrics] = r.Status[core.CapGPUDevices]
		return
	case len(list) == 0:
		r.Status[core.CapGPUDevices] = portable.Unavailable(core.Unsupported, "No GPU was found.")
		r.Status[core.CapGPUMetrics] = r.Status[core.CapGPUDevices]
		return
	}
	r.Status[core.CapGPUDevices] = portable.Available()
	r.Status[core.CapGPUMetrics] = portable.Unavailable(core.Unsupported, "This GPU's driver reports no usage statistics.")
	for _, g := range list {
		if g.MetricsSource != "" {
			r.Status[core.CapGPUMetrics] = portable.Available()
		}
	}
	r.Records = append(r.Records, list)
}

// ---- memory modules: system_profiler ----

const systemProfiler = "/usr/sbin/system_profiler"

// memoryModulesFrom runs Apple's system_profiler, which ships with
// macOS: memory module details have no public library interface.
func memoryModulesFrom(ctx context.Context, r *collector.Result) {
	out, err := exec.CommandContext(ctx, systemProfiler, "-json", "SPMemoryDataType").Output()
	var mods core.MemoryModules
	if err == nil {
		mods, err = memoryModules(out)
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.Status[core.CapHardwareMemory] = portable.Unavailable(core.Unsupported, "Memory modules need "+systemProfiler+", which is missing.")
	case ctx.Err() != nil:
		r.Status[core.CapHardwareMemory] = portable.Unavailable(core.TemporarilyUnavailable, "system_profiler took too long.")
	case err != nil:
		r.Status[core.CapHardwareMemory] = portable.Unavailable(core.TemporarilyUnavailable, "system_profiler failed: "+err.Error())
	case len(mods) == 0:
		r.Status[core.CapHardwareMemory] = portable.Unavailable(core.Unsupported, "This Mac reports no memory modules.")
	default:
		r.Status[core.CapHardwareMemory] = portable.Available()
		r.Records = append(r.Records, mods)
	}
}

// ---- network configuration: routes and DNS ----

func netConfig(r *collector.Result) {
	raw, err := syscall.RouteRIB(syscall.NET_RT_DUMP, 0) //lint:ignore SA1019 the replacement (x/net/route) would be a new dependency for one call
	if err != nil {
		r.Status[core.CapNetworkRoutes] = portable.Status(err, "the routing table")
	} else {
		names := map[int]string{}
		if ifs, err := net.Interfaces(); err == nil {
			for _, i := range ifs {
				names[i.Index] = i.Name
			}
		}
		routes := parseRouteDump(raw, func(i int) string { return names[i] })
		sortRoutes(routes)
		r.Status[core.CapNetworkRoutes] = portable.Available()
		r.Records = append(r.Records, routes)
	}
	// macOS keeps /etc/resolv.conf in step with the primary resolver.
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		r.Status[core.CapNetworkDNS] = portable.Status(err, "the resolver configuration (no network is connected)")
		return
	}
	cfg := portable.ParseResolvConf(b)
	cfg.Resolver = "resolv.conf"
	r.Status[core.CapNetworkDNS] = portable.Available()
	r.Records = append(r.Records, &cfg)
}

// ---- services (launchd, D48) ----

func services(r *collector.Result, m *smLib, loadErr error) {
	if loadErr != nil {
		r.Status[core.CapServicesLaunchd] = portable.Unavailable(core.Unsupported, "Services need ServiceManagement, which could not be loaded: "+loadErr.Error())
		return
	}
	jobs, ok := m.jobs()
	if !ok {
		r.Status[core.CapServicesLaunchd] = portable.Unavailable(core.TemporarilyUnavailable, "launchd did not return its job list.")
		return
	}
	r.Status[core.CapServicesLaunchd] = portable.Available()
	r.Records = append(r.Records, launchdServices(jobs))
}

// ---- power sources and temperatures ----

func power(r *collector.Result, k *ioKitLib, loadErr error) {
	if loadErr != nil {
		r.Status[core.CapPowerSupplies] = portable.Unavailable(core.Unsupported, "Power sources need IOKit, which could not be loaded: "+loadErr.Error())
		return
	}
	ps := k.powerSources()
	if len(ps) == 0 {
		r.Status[core.CapPowerSupplies] = portable.Unavailable(core.Unsupported,
			"No batteries or power adapters are reported (common on desktops and virtual machines).")
		return
	}
	r.Status[core.CapPowerSupplies] = portable.Available()
	r.Records = append(r.Records, ps)
}

func thermal(r *collector.Result, k *ioKitLib, loadErr error) {
	switch {
	case runtime.GOARCH != "arm64":
		r.Status[core.CapSensorsThermal] = portable.Unavailable(core.Unsupported, "Temperatures are read on Apple Silicon Macs only.")
		return
	case loadErr != nil:
		r.Status[core.CapSensorsThermal] = portable.Unavailable(core.Unsupported, "Temperatures need IOKit, which could not be loaded: "+loadErr.Error())
		return
	}
	s := thermalSensors(k.temperatures())
	if len(s) == 0 {
		r.Status[core.CapSensorsThermal] = portable.Unavailable(core.Unsupported,
			"No temperature sensors are reported. This is normal in virtual machines.")
		return
	}
	r.Status[core.CapSensorsThermal] = portable.Available()
	r.Records = append(r.Records, s)
}

// ---- processes ----

// maxProcesses bounds one listing.
const maxProcesses = 32768

type processesCollector struct {
	sys     *sysLib
	loadErr error
	now     func() time.Time
	euid    int

	mu      sync.Mutex
	users   map[int]string
	usersAt time.Time
}

func newProcesses(sys *sysLib, loadErr error, now func() time.Time) *processesCollector {
	return &processesCollector{sys: sys, loadErr: loadErr, now: now, euid: os.Geteuid()}
}

func (c *processesCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "darwin.processes", Version: version, Source: "sysctl kern.proc, libproc",
		Capabilities: []core.Capability{core.CapProcessesList},
		// Listed on demand by the engine; never scheduled.
		Class: collector.Inventory, DefaultTimeout: 3 * time.Second,
	}
}

func (c *processesCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	if c.loadErr != nil {
		return map[core.Capability]core.CapabilityStatus{core.CapProcessesList: portable.Unavailable(core.Unsupported,
			"Process details need the system library, which could not be loaded: "+c.loadErr.Error())}
	}
	return map[core.Capability]core.CapabilityStatus{core.CapProcessesList: portable.Available()}
}

func (c *processesCollector) Collect(ctx context.Context) collector.Result {
	return collector.Result{CollectedAt: c.now(), Status: c.Probe(ctx)}
}

func (c *processesCollector) Close(context.Context) error { return nil }

// BootTime lets the engine turn start ticks into times.
func (c *processesCollector) BootTime() (time.Time, bool) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil || tv.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}

func (c *processesCollector) ListProcesses(ctx context.Context) (core.ProcessList, core.CapabilityStatus) {
	if c.loadErr != nil {
		return core.ProcessList{}, c.Probe(ctx)[core.CapProcessesList]
	}
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return core.ProcessList{}, portable.Status(err, "the process list")
	}
	boot, _ := c.BootTime()
	list := core.ProcessList{TakenAt: c.now()}
	info := make([]byte, taskInfoSize)
	pathBuf := make([]byte, 4096) // PROC_PIDPATHINFO_MAXSIZE
	nameBuf := make([]byte, 64)
	uids := map[int]bool{}
	for i, kp := range kps {
		if i >= maxProcesses {
			list.Partial = true
			break
		}
		if i%256 == 0 && ctx.Err() != nil {
			return core.ProcessList{}, portable.Unavailable(core.TemporarilyUnavailable, "The process listing was cancelled.")
		}
		pid := kp.Proc.P_pid
		uid := int(kp.Eproc.Ucred.Uid)
		p := core.ProcessSample{PID: int(pid), PPID: int(kp.Eproc.Ppid), UID: uid, State: procState(kp.Proc.P_stat)}
		p.Name = c.sys.name(pid, nameBuf)
		if p.Name == "" {
			p.Name = cString(kp.Proc.P_comm[:], int32(len(kp.Proc.P_comm)))
		}
		if st := kp.Proc.P_starttime; !boot.IsZero() && st.Sec > 0 {
			if d := time.Unix(st.Sec, int64(st.Usec)*1000).Sub(boot); d > 0 {
				p.StartTicks = uint64(d / (time.Second / ticksPerSecond))
			}
		}
		ours := c.euid == 0 || c.euid == uid
		if pid == 0 { // kernel_task
			p.KernelThread = true
			p.ExecutableReason = core.ExeKernelThread
		} else if path := c.sys.path(pid, pathBuf); path != "" {
			p.Executable = path
		} else if ours {
			p.ExecutableReason = core.ExeUnavailable
		} else {
			p.ExecutableReason = core.ExeDenied
		}
		if t, ok := c.sys.taskInfo(pid, info); ok {
			p.RSSBytes, p.VirtualBytes, p.Threads = t.residentBytes, t.virtualBytes, t.threads
			p.CPUTicks = absToTicks(t.userTime+t.systemTime, c.sys.numer, c.sys.denom)
		} else if !ours {
			// macOS shows other users' CPU and memory use to root only.
			list.Partial = true
		}
		uids[uid] = true
		list.Processes = append(list.Processes, p)
	}
	list.Users = c.userNames(uids)
	return list, portable.Available()
}

// userNames resolves UIDs, caching names for five minutes.
func (c *processesCollector) userNames(uids map[int]bool) map[int]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.users == nil || c.now().Sub(c.usersAt) > 5*time.Minute {
		c.users, c.usersAt = map[int]string{}, c.now()
	}
	out := make(map[int]string, len(uids))
	for uid := range uids {
		name, ok := c.users[uid]
		if !ok && uid >= 0 {
			name = c.sys.userName(uint32(uid))
			c.users[uid] = name
		}
		if name != "" {
			out[uid] = name
		}
	}
	return out
}
