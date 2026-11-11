//go:build linux

package linux

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

const version = "1.0.0"

// base implements Probe as "collect once" and a no-op Close. Collectors
// whose collection is cheap embed it.
type base struct {
	collect func(ctx context.Context) collector.Result
}

func (b base) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return b.collect(ctx).Status
}
func (base) Close(context.Context) error { return nil }

// ---- system ----

type systemCollector struct {
	base
	env collector.Env
}

func newSystem(env collector.Env) *systemCollector {
	c := &systemCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *systemCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.system", Version: version, Source: "procfs, os-release",
		Capabilities: []core.Capability{core.CapSystemIdentity, core.CapSystemUptime},
		Class:        collector.Inventory, DefaultInterval: 5 * time.Minute, DefaultTimeout: 2 * time.Second,
	}
}

func (c *systemCollector) Collect(context.Context) collector.Result {
	now := c.env.Now()
	info := &core.SystemInfo{Architecture: runtime.GOARCH}
	res := collector.Result{CollectedAt: now, Status: map[core.Capability]core.CapabilityStatus{}}

	// os-release: /etc/os-release, falling back to /usr/lib/os-release.
	osr, err := readFile(c.env.EtcRoot, "os-release")
	if err != nil {
		osr, err = readFile(filepath.Join(filepath.Dir(c.env.EtcRoot), "usr", "lib"), "os-release")
	}
	if err == nil {
		kv := parseOSRelease(osr)
		info.OSID, info.OSName, info.OSVersion, info.OSPrettyName = kv["ID"], kv["NAME"], kv["VERSION_ID"], kv["PRETTY_NAME"]
	}
	info.Kernel, _ = readTrim(c.env.ProcRoot, "sys/kernel/osrelease")
	host, herr := readTrim(c.env.ProcRoot, "sys/kernel/hostname")
	info.Hostname = host
	if herr != nil && info.Kernel == "" && err != nil {
		res.Status[core.CapSystemIdentity] = status(herr, "system identity")
	} else {
		res.Status[core.CapSystemIdentity] = available()
	}

	if b, uerr := readFile(c.env.ProcRoot, "uptime"); uerr != nil {
		res.Status[core.CapSystemUptime] = status(uerr, "system uptime")
	} else if up, perr := parseUptime(b); perr != nil {
		res.Status[core.CapSystemUptime] = status(perr, "system uptime")
	} else {
		info.Uptime = up
		info.BootTime = now.Add(-up).UTC().Round(time.Second)
		res.Status[core.CapSystemUptime] = available()
	}
	res.Records = []core.Record{info}
	return res
}

// ---- cpu ----

type cpuCollector struct {
	base
	env   collector.Env
	model string // cached: /proc/cpuinfo is large and the model never changes
}

func newCPU(env collector.Env) *cpuCollector {
	c := &cpuCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *cpuCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.cpu", Version: version, Source: "procfs",
		Capabilities: []core.Capability{core.CapCPUUsage, core.CapCPULoad},
		Class:        collector.Sampler, DefaultInterval: 2 * time.Second, DefaultTimeout: time.Second,
	}
}

func (c *cpuCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	b, err := readFile(c.env.ProcRoot, "stat")
	if err != nil {
		res.Status[core.CapCPUUsage] = status(err, "CPU statistics")
		res.Status[core.CapCPULoad] = status(err, "CPU statistics")
		return res
	}
	total, per, err := parseCPUStat(b)
	if err != nil {
		res.Status[core.CapCPUUsage] = status(err, "CPU statistics")
		res.Status[core.CapCPULoad] = status(err, "CPU statistics")
		return res
	}
	if c.model == "" {
		if info, err := readFile(c.env.ProcRoot, "cpuinfo"); err == nil {
			c.model = parseCPUModel(info)
		}
	}
	rec := &core.CPUCounters{Total: total, PerCore: per, LogicalCores: len(per), ModelName: c.model}
	res.Status[core.CapCPUUsage] = available()

	if lb, err := readFile(c.env.ProcRoot, "loadavg"); err != nil {
		res.Status[core.CapCPULoad] = status(err, "load average")
	} else if l, err := parseLoadAvg(lb); err != nil {
		res.Status[core.CapCPULoad] = status(err, "load average")
	} else {
		rec.Load = &l
		res.Status[core.CapCPULoad] = available()
	}
	res.Records = []core.Record{rec}
	return res
}

// ---- memory ----

type memoryCollector struct {
	base
	env collector.Env
}

func newMemory(env collector.Env) *memoryCollector {
	c := &memoryCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *memoryCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.memory", Version: version, Source: "procfs",
		Capabilities: []core.Capability{core.CapMemoryUsage, core.CapMemoryPressure},
		Class:        collector.Sampler, DefaultInterval: 2 * time.Second, DefaultTimeout: time.Second,
	}
}

func (c *memoryCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	if b, err := readFile(c.env.ProcRoot, "meminfo"); err != nil {
		res.Status[core.CapMemoryUsage] = status(err, "memory statistics")
	} else if m, err := parseMeminfo(b); err != nil {
		res.Status[core.CapMemoryUsage] = status(err, "memory statistics")
	} else {
		res.Records = append(res.Records, &m)
		res.Status[core.CapMemoryUsage] = available()
	}
	// PSI is absent on kernels before 4.20 or when booted with psi=0.
	if b, err := readFile(c.env.ProcRoot, "pressure/memory"); err != nil {
		res.Status[core.CapMemoryPressure] = status(err, "memory pressure (PSI)")
	} else if p, err := parsePSI(b); err != nil {
		res.Status[core.CapMemoryPressure] = status(err, "memory pressure (PSI)")
	} else {
		res.Records = append(res.Records, &p)
		res.Status[core.CapMemoryPressure] = available()
	}
	return res
}

// ---- network ----

type networkCollector struct {
	base
	env collector.Env
	// addrs returns interface addresses (CIDR) by name. Uses the kernel's
	// address list (netlink via the standard library); injectable for tests.
	addrs func() map[string][]string
}

func newNetwork(env collector.Env) *networkCollector {
	c := &networkCollector{env: env, addrs: interfaceAddrs}
	c.base = base{collect: c.Collect}
	return c
}

func (c *networkCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.network", Version: version, Source: "procfs, sysfs",
		Capabilities: []core.Capability{core.CapNetworkInterfaces, core.CapNetworkThroughput},
		Class:        collector.Sampler, DefaultInterval: 2 * time.Second, DefaultTimeout: time.Second,
	}
}

// arphrdLoopback is ARPHRD_LOOPBACK from <linux/if_arp.h>.
const arphrdLoopback = 772

func (c *networkCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	b, err := readFile(c.env.ProcRoot, "net/dev")
	if err == nil {
		var counters map[string]netDevCounters
		var order []string
		if counters, order, err = parseNetDev(b); err == nil {
			nets := make(core.NetInterfaces, 0, len(order))
			addrs := c.addrs()
			for _, name := range order {
				n := c.iface(name, counters[name])
				n.Addresses = addrs[name]
				nets = append(nets, n)
			}
			res.Records = []core.Record{nets}
		}
	}
	st := status(err, "network interface statistics")
	res.Status[core.CapNetworkInterfaces] = st
	res.Status[core.CapNetworkThroughput] = st
	return res
}

func (c *networkCollector) iface(name string, n netDevCounters) core.NetInterface {
	dir := filepath.Join("class", "net", name)
	out := core.NetInterface{
		Name:    name,
		RxBytes: n.rxBytes, TxBytes: n.txBytes,
		RxPackets: n.rxPackets, TxPackets: n.txPackets,
		RxErrors: n.rxErrs, TxErrors: n.txErrs,
		RxDropped: n.rxDrop, TxDropped: n.txDrop,
		OperState: "unknown",
	}
	out.MACAddress, _ = readTrim(c.env.SysRoot, filepath.Join(dir, "address"))
	if mtu, err := readInt(c.env.SysRoot, filepath.Join(dir, "mtu")); err == nil {
		out.MTU = int(mtu)
	}
	if s, err := readTrim(c.env.SysRoot, filepath.Join(dir, "operstate")); err == nil && s != "" {
		out.OperState = s
	}
	if t, err := readInt(c.env.SysRoot, filepath.Join(dir, "type")); err == nil && t == arphrdLoopback {
		out.Loopback = true
	} else if name == "lo" {
		out.Loopback = true
	}
	// "speed" fails with EINVAL when the link is down and reports -1 for
	// virtual devices; both mean unknown.
	if s, err := readInt(c.env.SysRoot, filepath.Join(dir, "speed")); err == nil && s > 0 && s < 10_000_000 {
		v := int(s)
		out.SpeedMbps = &v
	}
	return out
}

// Collectors returns the Linux collector set, listed explicitly (no init()
// registration).
func Collectors(env collector.Env) []collector.Collector {
	return []collector.Collector{
		newSystem(env),
		newCPU(env),
		newMemory(env),
		newFilesystems(env),
		newNetwork(env),
		newBlockDevices(env),
		newSensors(env),
		newSMART(env),
		newProcesses(env),
		newServices(env),
		newCPUInfo(env),
		newPCI(env),
		newUSB(env),
		newDMI(env),
		newGPU(env),
		newNetConfig(env),
	}
}

func interfaceAddrs() map[string][]string {
	out := map[string][]string{}
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifs {
		as, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok {
				out[ifc.Name] = append(out[ifc.Name], ipn.String())
			}
		}
	}
	return out
}
