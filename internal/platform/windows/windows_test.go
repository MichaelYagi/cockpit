//go:build windows

package windows

import (
	"context"
	"log/slog"
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
