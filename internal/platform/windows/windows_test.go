//go:build windows

package windows

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func collectors() []collector.Collector {
	return Collectors(collector.DefaultEnv(slog.New(slog.DiscardHandler), nil))
}

func TestConformance(t *testing.T) { conformance.Run(t, collectors()) }

// On a real Windows host the values must be plausible, which checks the
// system calls and struct layouts.
func TestRealValues(t *testing.T) {
	got := map[string]collector.Result{}
	for _, c := range collectors() {
		got[c.Descriptor().ID] = c.Collect(context.Background())
	}
	sys := got["windows.system"].Records[0].(*core.SystemInfo)
	if sys.Hostname == "" || !strings.HasPrefix(sys.OSPrettyName, "Windows") || sys.Uptime <= 0 || sys.BootTime.After(time.Now()) {
		t.Errorf("system %+v", sys)
	}
	if st := got["windows.cpu"].Status[core.CapCPUUsage]; st.Availability != core.Available {
		t.Fatalf("cpu %+v", st)
	}
	c := got["windows.cpu"].Records[0].(*core.CPUCounters)
	if c.Total.Total() == 0 || c.Total.Idle > c.Total.Total() || c.LogicalCores < 1 {
		t.Errorf("cpu %+v", c)
	}
	mem := got["windows.memory"].Records[0].(*core.MemoryStats)
	if mem.TotalBytes < 1<<30 || mem.AvailableBytes == 0 || mem.AvailableBytes > mem.TotalBytes {
		t.Errorf("memory %+v", mem)
	}
	fss := got["windows.filesystems"].Records[0].(core.Filesystems)
	sysDrive := false
	for _, f := range fss {
		sysDrive = sysDrive || strings.EqualFold(f.MountPoint, `C:\`)
		if f.AvailableBytes > f.TotalBytes || f.Type == "" {
			t.Errorf("drive %+v", f)
		}
	}
	if !sysDrive {
		t.Errorf("no C: drive in %+v", fss)
	}
	if st := got["windows.network"].Status[core.CapNetworkThroughput]; st.Availability != core.Available {
		t.Errorf("throughput %+v", st)
	}
}

// Two samples a moment apart give a usage the engine can derive.
func TestCPUTicksAdvance(t *testing.T) {
	c := collectors()[1]
	a := c.Collect(context.Background()).Records[0].(*core.CPUCounters)
	time.Sleep(200 * time.Millisecond)
	b := c.Collect(context.Background()).Records[0].(*core.CPUCounters)
	if b.Total.Total() <= a.Total.Total() {
		t.Errorf("ticks did not advance: %+v -> %+v", a.Total, b.Total)
	}
}

func collectorByID(t *testing.T, id string) collector.Collector {
	t.Helper()
	for _, c := range collectors() {
		if c.Descriptor().ID == id {
			return c
		}
	}
	t.Fatalf("no %s collector", id)
	return nil
}

// Every Windows host runs System, its own test process and services, and
// the test's own process must have a path, an owner and CPU time.
func TestRealProcesses(t *testing.T) {
	pl := collectorByID(t, "windows.processes").(collector.ProcessLister)
	list, st := pl.ListProcesses(context.Background())
	if st.Availability != core.Available {
		t.Fatalf("%s %s", st.Availability, st.Reason)
	}
	states := map[string]int{}
	var self, system *core.ProcessSample
	withExe, withUser := 0, 0
	for i := range list.Processes {
		p := &list.Processes[i]
		states[p.State]++
		if p.Executable != "" {
			withExe++
		}
		if list.Users[p.UID] != "" {
			withUser++
		}
		switch p.PID {
		case os.Getpid():
			self = p
		case systemPID:
			system = p
		}
	}
	t.Logf("%d processes, %d with a path, %d with a user, states %v, users %v", len(list.Processes), withExe, withUser, states, list.Users)
	if self == nil || system == nil {
		t.Fatalf("own process %v or System %v missing", self, system)
	}
	t.Logf("self %+v", *self)
	t.Logf("system %+v", *system)
	if !strings.HasSuffix(strings.ToLower(self.Executable), ".exe") || self.Threads < 1 || self.RSSBytes == 0 || self.PPID == 0 ||
		self.UID < 0 || list.Users[self.UID] == "" || self.StartTicks == 0 {
		t.Errorf("implausible own process %+v", *self)
	}
	if !system.KernelThread || system.Threads < 10 {
		t.Errorf("implausible System %+v", *system)
	}
	if len(list.Processes) < 20 || states["R"]+states["S"] < len(list.Processes)/2 {
		t.Errorf("implausible list: %d processes, states %v", len(list.Processes), states)
	}
}

func TestRealHardware(t *testing.T) {
	r := collectorByID(t, "windows.hardware").Collect(context.Background())
	for c, st := range r.Status {
		t.Logf("%s: %s %s", c, st.Availability, st.Reason)
	}
	for _, rec := range r.Records {
		switch v := rec.(type) {
		case *core.CPUInfo:
			t.Logf("cpu %+v", *v)
			if v.ModelName == "" || v.Cores < 1 || v.Threads < v.Cores || v.Sockets < 1 || len(v.Caches) == 0 {
				t.Errorf("implausible CPU %+v", *v)
			}
		case *core.BoardInfo:
			t.Logf("board %+v", *v)
		case core.MemoryModules:
			for _, m := range v {
				t.Logf("module %+v", m)
				if m.SizeBytes == 0 {
					t.Errorf("implausible module %+v", m)
				}
			}
		}
	}
	if st := r.Status[core.CapHardwareCPU]; st.Availability != core.Available {
		t.Errorf("cpu: %s", st.Reason)
	}
	p := collectorByID(t, "windows.power").Collect(context.Background())
	t.Logf("power %v %+v", p.Status[core.CapPowerSupplies], p.Records)
}

func TestRealNetConfig(t *testing.T) {
	r := collectorByID(t, "windows.netconfig").Collect(context.Background())
	var routes core.Routes
	var dns *core.DNSConfig
	for _, rec := range r.Records {
		switch v := rec.(type) {
		case core.Routes:
			routes = v
		case *core.DNSConfig:
			dns = v
		}
	}
	for c, st := range r.Status {
		t.Logf("%s: %s %s", c, st.Availability, st.Reason)
	}
	for _, rt := range routes {
		t.Logf("route %+v", rt)
	}
	t.Logf("dns %+v", dns)
	hasDefault := false
	for _, rt := range routes {
		hasDefault = hasDefault || rt.Default && rt.Gateway != "" && rt.Interface != ""
	}
	if !hasDefault {
		t.Error("no default route with a gateway and interface")
	}
	if dns == nil || len(dns.Nameservers) == 0 || dns.Resolver != "windows" {
		t.Errorf("dns %+v", dns)
	}
}

// Every Windows host runs core services such as the RPC server and the
// event log, started automatically.
func TestRealServices(t *testing.T) {
	c := collectorByID(t, "windows.services")
	start := time.Now()
	r := c.Collect(context.Background())
	first := time.Since(start)
	start = time.Now()
	c.Collect(context.Background())
	t.Logf("first collection %v, cached %v", first, time.Since(start))
	st := r.Status[core.CapServicesWindows]
	if st.Availability != core.Available {
		t.Fatalf("%s %s", st.Availability, st.Reason)
	}
	svcs := r.Records[0].(core.Services)
	counts := map[string]int{}
	found := map[string]core.Service{}
	for _, s := range svcs {
		counts[s.ActiveState+"/"+s.LoadState]++
		if s.Name == "RpcSs" || s.Name == "EventLog" {
			found[s.Name] = s
		}
		if s.ActiveState == "failed" {
			t.Logf("failed: %+v", s)
		}
	}
	t.Logf("%d services: %v", len(svcs), counts)
	for _, n := range []string{"RpcSs", "EventLog"} {
		s, ok := found[n]
		t.Logf("%s %+v", n, s)
		if !ok || s.ActiveState != "active" || s.LoadState != "auto" || !strings.HasPrefix(s.SubState, "running (pid ") {
			t.Errorf("%s: %+v", n, s)
		}
	}
}

// Every Windows host has PCI devices and a disk; USB and GPU lists depend
// on the hardware, so they are logged.
func TestRealDevices(t *testing.T) {
	start := time.Now()
	r := collectorByID(t, "windows.devices").Collect(context.Background())
	t.Logf("collected in %v", time.Since(start))
	for c, st := range r.Status {
		t.Logf("%s: %s %s", c, st.Availability, st.Reason)
	}
	for _, rec := range r.Records {
		switch v := rec.(type) {
		case core.PCIDevices:
			for _, d := range v {
				t.Logf("pci %+v", d)
				if d.VendorID == "" || d.Address == "" {
					t.Errorf("implausible PCI device %+v", d)
				}
			}
		case core.USBDevices:
			for _, d := range v {
				t.Logf("usb %+v", d)
			}
		case core.BlockDevices:
			for _, d := range v {
				t.Logf("disk %+v rotational=%v", d, d.Rotational != nil && *d.Rotational)
				if d.SizeBytes == 0 || d.Model == "" {
					t.Errorf("implausible disk %+v", d)
				}
			}
		}
	}
	for _, c := range []core.Capability{core.CapHardwarePCI, core.CapStorageDevices} {
		if st := r.Status[c]; st.Availability != core.Available {
			t.Errorf("%s: %s %s", c, st.Availability, st.Reason)
		}
	}
	g := collectorByID(t, "windows.gpu").Collect(context.Background())
	for c, st := range g.Status {
		t.Logf("%s: %s %s", c, st.Availability, st.Reason)
	}
	for _, rec := range g.Records {
		for _, x := range rec.(core.GPUs) {
			t.Logf("gpu %+v", x)
		}
	}
}
