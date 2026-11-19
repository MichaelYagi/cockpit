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
