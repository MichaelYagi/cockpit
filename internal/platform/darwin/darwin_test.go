//go:build darwin

package darwin

import (
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
	"github.com/MichaelYagi/cockpit/internal/core"
	"golang.org/x/sys/unix"
)

func collectors() []collector.Collector {
	return Collectors(collector.DefaultEnv(slog.New(slog.DiscardHandler), nil))
}

func TestConformance(t *testing.T) { conformance.Run(t, collectors()) }

// On a real Mac the values must be plausible, which checks the sysctl
// names and the struct offsets this package relies on.
func TestRealValues(t *testing.T) {
	got := map[string]collector.Result{}
	for _, c := range collectors() {
		got[c.Descriptor().ID] = c.Collect(context.Background())
	}
	sys := got["darwin.system"].Records[0].(*core.SystemInfo)
	if sys.Hostname == "" || sys.OSVersion == "" || sys.BootTime.IsZero() || sys.Uptime <= 0 {
		t.Errorf("system %+v", sys)
	}
	if st := got["darwin.cpu"].Status[core.CapCPULoad]; st.Availability != core.Available {
		t.Fatalf("load %+v", st)
	}
	load := got["darwin.cpu"].Records[0].(*core.CPUCounters).Load
	if load.One < 0 || load.One > 1000 || load.Fifteen < 0 {
		t.Errorf("load %+v", load)
	}
	for _, name := range append([]string{"vm.page_free_count"}, reclaimable...) {
		t.Logf("%s = %d", name, sysctlCount(name))
	}
	if st := got["darwin.memory"].Status[core.CapMemoryUsage]; st.Availability != core.Available {
		t.Fatalf("memory %+v", st)
	}
	mem := got["darwin.memory"].Records[0].(*core.MemoryStats)
	if mem.TotalBytes < 1<<30 || mem.AvailableBytes == 0 || mem.AvailableBytes > mem.TotalBytes {
		t.Errorf("memory %+v", mem)
	}
	fss := got["darwin.filesystems"].Records[0].(core.Filesystems)
	root := false
	for _, f := range fss {
		root = root || f.MountPoint == "/"
		if f.AvailableBytes > f.TotalBytes {
			t.Errorf("filesystem %+v", f)
		}
	}
	if !root {
		t.Errorf("no root filesystem in %+v", fss)
	}
	nets := got["darwin.network"].Records[0].(core.NetInterfaces)
	raw, err := ifList()
	t.Logf("NET_RT_IFLIST2: %d bytes, %d interfaces, err %v", len(raw), len(parseIfList(raw)), err)
	if st := got["darwin.network"].Status[core.CapNetworkThroughput]; st.Availability != core.Available {
		t.Fatalf("throughput %+v", st)
	}
	for _, n := range nets {
		if n.Loopback && n.RxBytes == 0 && n.TxBytes == 0 && n.RxPackets == 0 {
			t.Logf("loopback %s has no traffic yet", n.Name)
		}
		if n.RxBytes > 1<<56 || n.TxBytes > 1<<56 || n.MTU > 1<<20 {
			t.Errorf("implausible counters for %s: %+v (struct offsets wrong?)", n.Name, n)
		}
	}
}

func TestParseIfList(t *testing.T) {
	msg := make([]byte, ifmMinLen+8)
	binary.LittleEndian.PutUint16(msg, uint16(len(msg)))
	msg[3] = unix.RTM_IFINFO2
	binary.LittleEndian.PutUint16(msg[ifmIndex:], 4)
	binary.LittleEndian.PutUint32(msg[ifdMTU:], 1500)
	binary.LittleEndian.PutUint64(msg[ifdIbytes:], 1000)
	binary.LittleEndian.PutUint64(msg[ifdObytes:], 2000)
	other := []byte{8, 0, 5, 0x0e, 0, 0, 0, 0} // another message type
	got := parseIfList(append(append(other, msg...), 0, 0))
	if len(got) != 1 || got[0].index != 4 || got[0].mtu != 1500 || got[0].ibytes != 1000 || got[0].obytes != 2000 {
		t.Errorf("%+v", got)
	}
}

// CPU tick counters advance and cover every CPU (host_processor_info
// through purego, D36).
func TestRealCPUUsage(t *testing.T) {
	var cpu collector.Collector
	for _, c := range collectors() {
		if c.Descriptor().ID == "darwin.cpu" {
			cpu = c
		}
	}
	first := cpu.Collect(context.Background())
	if st := first.Status[core.CapCPUUsage]; st.Availability != core.Available {
		t.Fatalf("usage %+v", st)
	}
	busy := make([]byte, 1<<20)
	for i := 0; i < 2000; i++ { // a little work so the counters move
		for j := range busy {
			busy[j]++
		}
	}
	second := cpu.Collect(context.Background())
	a, b := first.Records[0].(*core.CPUCounters), second.Records[0].(*core.CPUCounters)
	if len(b.PerCore) != runtime.NumCPU() {
		t.Errorf("%d per-core counters, %d CPUs", len(b.PerCore), runtime.NumCPU())
	}
	if b.Total.Total() <= a.Total.Total() || b.Total.Busy() < a.Total.Busy() {
		t.Errorf("counters did not advance: %+v then %+v", a.Total, b.Total)
	}
	t.Logf("model %q, total %+v", b.ModelName, b.Total)
}

// Our own process is listed with its path, memory, threads, start time
// and user name (sysctl kern.proc, libproc, getpwuid_r).
func TestRealProcesses(t *testing.T) {
	sys, err := loadSys()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("timebase %d/%d", sys.numer, sys.denom)
	pc := newProcesses(sys, nil, time.Now)
	list, st := pc.ListProcesses(context.Background())
	if st.Availability != core.Available {
		t.Fatalf("status %+v", st)
	}
	if len(list.Processes) < 10 {
		t.Fatalf("only %d processes", len(list.Processes))
	}
	boot, ok := pc.BootTime()
	if !ok {
		t.Fatal("no boot time")
	}
	var me, launchd *core.ProcessSample
	for i := range list.Processes {
		switch list.Processes[i].PID {
		case os.Getpid():
			me = &list.Processes[i]
		case 1:
			launchd = &list.Processes[i]
		}
	}
	if me == nil || launchd == nil {
		t.Fatalf("own process %v, launchd %v", me, launchd)
	}
	exe, _ := os.Executable()
	if me.Executable == "" || filepath.Base(me.Executable) != filepath.Base(exe) {
		t.Errorf("executable %q (%s), want %q", me.Executable, me.ExecutableReason, exe)
	}
	if me.RSSBytes < 1<<20 || me.VirtualBytes < me.RSSBytes || me.Threads < 1 || me.State == "?" {
		t.Errorf("own process %+v", me)
	}
	started := boot.Add(time.Duration(me.StartTicks) * time.Second / ticksPerSecond)
	if d := time.Since(started); d < 0 || d > 30*time.Minute {
		t.Errorf("started %v ago (boot %v, ticks %d)", d, boot, me.StartTicks)
	}
	if name := list.Users[os.Getuid()]; name == "" {
		t.Errorf("no name for uid %d", os.Getuid())
	}
	if launchd.Name != "launchd" || launchd.UID != 0 || list.Users[0] != "root" {
		t.Errorf("launchd %+v, root %q", launchd, list.Users[0])
	}
	t.Logf("%d processes, partial %v, own CPU ticks %d", len(list.Processes), list.Partial, me.CPUTicks)
}

func TestRealHardware(t *testing.T) {
	var r collector.Result
	r.Status = map[core.Capability]core.CapabilityStatus{}
	hardware(&r)
	if st := r.Status[core.CapHardwareCPU]; st.Availability != core.Available {
		t.Fatalf("cpu %+v", st)
	}
	cpu := r.Records[0].(*core.CPUInfo)
	if cpu.ModelName == "" || cpu.Threads != runtime.NumCPU() || cpu.Cores < 1 {
		t.Errorf("cpu %+v", cpu)
	}
	board := r.Records[1].(*core.BoardInfo)
	if board.ProductName == "" {
		t.Errorf("board %+v", board)
	}
	t.Logf("%s, %d cores, %s, hypervisor %v, caches %+v", cpu.ModelName, cpu.Cores, board.ProductName, cpu.Hypervisor, cpu.Caches)
}

// Power sources and temperatures must work or say why; CI runners are
// virtual machines, so neither is required to be present.
func TestRealPowerAndThermal(t *testing.T) {
	for _, c := range collectors() {
		id := c.Descriptor().ID
		if id != "darwin.power" && id != "darwin.thermal" {
			continue
		}
		r := c.Collect(context.Background())
		for cp, st := range r.Status {
			t.Logf("%s %s: %s %s", id, cp, st.Availability, st.Reason)
		}
		for _, rec := range r.Records {
			t.Logf("%s: %+v", id, rec)
		}
	}
}

// launchd jobs come from ServiceManagement (D48); every Mac, including a
// CI virtual machine, runs Apple daemons.
func TestRealServices(t *testing.T) {
	for _, c := range collectors() {
		if c.Descriptor().ID != "darwin.services" {
			continue
		}
		r := c.Collect(context.Background())
		st := r.Status[core.CapServicesLaunchd]
		if st.Availability != core.Available {
			t.Fatalf("services: %s %s", st.Availability, st.Reason)
		}
		svcs := r.Records[0].(core.Services)
		running, apple := 0, 0
		for i, s := range svcs {
			if s.ActiveState == "active" {
				running++
			}
			if strings.HasPrefix(s.Name, "com.apple.") {
				apple++
			}
			if i < 15 || strings.HasPrefix(s.SubState, "exited (") || strings.HasPrefix(s.SubState, "killed") {
				if i < 40 {
					t.Logf("%s | %s | %s | %s", s.Name, s.ActiveState, s.SubState, s.Description)
				}
			}
		}
		t.Logf("%d services, %d running, %d com.apple", len(svcs), running, apple)
		if running == 0 || apple == 0 {
			t.Errorf("implausible job list: %d services, %d running, %d com.apple", len(svcs), running, apple)
		}
		return
	}
	t.Fatal("no darwin.services collector")
}

// PCI, USB and disks come from the IOKit registry, memory modules from
// system_profiler. Every Mac, including a CI virtual machine, has a boot
// disk; the other lists depend on the hardware, so they are logged.
func TestRealDevices(t *testing.T) {
	got := map[string]collector.Result{}
	for _, c := range collectors() {
		if id := c.Descriptor().ID; id == "darwin.devices" || id == "darwin.memory_modules" {
			got[id] = c.Collect(context.Background())
		}
	}
	for id, r := range got {
		for c, st := range r.Status {
			t.Logf("%s %s: %s %s", id, c, st.Availability, st.Reason)
		}
		for _, rec := range r.Records {
			t.Logf("%s: %+v", id, rec)
		}
	}
	dev := got["darwin.devices"]
	if st := dev.Status[core.CapStorageDevices]; st.Availability != core.Available {
		t.Fatalf("disks: %s %s", st.Availability, st.Reason)
	}
	for _, rec := range dev.Records {
		switch v := rec.(type) {
		case core.BlockDevices:
			for _, d := range v {
				if !strings.HasPrefix(d.Name, "disk") || d.SizeBytes == 0 {
					t.Errorf("implausible disk %+v", d)
				}
			}
		case core.PCIDevices:
			for _, d := range v {
				if d.VendorID == "" || d.ClassCode == "" || d.Address == "" {
					t.Errorf("implausible PCI device %+v", d)
				}
			}
		case core.USBDevices:
			for _, d := range v {
				if len(d.VendorID) != 4 {
					t.Errorf("implausible USB device %+v", d)
				}
			}
		}
	}
	mem := got["darwin.memory_modules"]
	st := mem.Status[core.CapHardwareMemory]
	if st.Availability == core.TemporarilyUnavailable {
		t.Errorf("memory modules: %s", st.Reason)
	}
	if st.Availability == core.Available {
		for _, m := range mem.Records[0].(core.MemoryModules) {
			if m.SizeBytes == 0 || m.Locator == "" {
				t.Errorf("implausible module %+v", m)
			}
		}
	}
}

// Every networked Mac, including a CI virtual machine, has a default
// route and a resolver.
func TestRealNetConfig(t *testing.T) {
	for _, c := range collectors() {
		if c.Descriptor().ID != "darwin.netconfig" {
			continue
		}
		r := c.Collect(context.Background())
		for cp, st := range r.Status {
			t.Logf("%s: %s %s", cp, st.Availability, st.Reason)
		}
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
		for _, rt := range routes {
			t.Logf("route %+v", rt)
			if (strings.HasSuffix(rt.Destination, "/32") || strings.HasSuffix(rt.Destination, "/128")) && rt.Gateway == "" {
				t.Errorf("local host route listed: %+v", rt)
			}
		}
		t.Logf("dns %+v", dns)
		if !slices.ContainsFunc(routes, func(rt core.Route) bool { return rt.Default && rt.Gateway != "" && rt.Interface != "" }) {
			t.Error("no default route with a gateway and interface")
		}
		if dns == nil || len(dns.Nameservers) == 0 {
			t.Error("no nameservers")
		}
		return
	}
	t.Fatal("no darwin.netconfig collector")
}

// GPUs come from IOAccelerator; a CI virtual machine may have only a
// paravirtual GPU, so the result is logged and only has to be coherent.
func TestRealGPU(t *testing.T) {
	for _, c := range collectors() {
		if c.Descriptor().ID != "darwin.gpu" {
			continue
		}
		r := c.Collect(context.Background())
		for cp, st := range r.Status {
			t.Logf("%s: %s %s", cp, st.Availability, st.Reason)
		}
		if st := r.Status[core.CapGPUDevices]; st.Availability == core.TemporarilyUnavailable {
			t.Errorf("gpu: %s", st.Reason)
		}
		for _, rec := range r.Records {
			for _, g := range rec.(core.GPUs) {
				t.Logf("gpu %+v util=%v mem=%v", g, deref(g.UtilizationPercent), deref(g.MemoryUsedBytes))
				if model, _ := unix.Sysctl("hw.model"); g.Name == "" || g.Driver == "" || g.Name == model {
					t.Errorf("implausible GPU %+v (Mac model %q)", g, model)
				}
			}
		}
		return
	}
	t.Fatal("no darwin.gpu collector")
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// Disk health needs smartctl (D54); runners and machines without it
// report not_configured, which is logged.
func TestRealSMART(t *testing.T) {
	for _, c := range collectors() {
		if c.Descriptor().ID != "darwin.smart" {
			continue
		}
		r := c.Collect(context.Background())
		st := r.Status[core.CapStorageSMART]
		t.Logf("%s %s", st.Availability, st.Reason)
		for _, rec := range r.Records {
			for _, h := range rec.(core.DiskHealthList) {
				t.Logf("%+v passed=%v", h, h.Passed != nil && *h.Passed)
			}
		}
		if st.Availability == core.Available && len(r.Records) == 0 {
			t.Error("available without records")
		}
		return
	}
	t.Fatal("no darwin.smart collector")
}
