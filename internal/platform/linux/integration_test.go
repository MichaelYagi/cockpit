//go:build linux && integration

package linux

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

type pathFinder struct{}

func (pathFinder) Find(name string) (string, bool) {
	p, err := exec.LookPath(name)
	return p, err == nil
}

// Runs against the real host. Checks invariants, not exact values, and
// logs what is unavailable so CI output shows the degradation behavior.
func TestRealHost(t *testing.T) {
	e := collector.DefaultEnv(slog.New(slog.DiscardHandler), pathFinder{})
	for _, c := range Collectors(e) {
		d := c.Descriptor()
		r := c.Collect(context.Background())
		for _, capability := range d.Capabilities {
			s := r.Status[capability]
			if !s.Availability.Valid() {
				t.Errorf("%s %s: invalid status %+v", d.ID, capability, s)
			}
			t.Logf("%-20s %-22s %-24s %s", d.ID, capability, s.Availability, s.Reason)
		}
		for _, rec := range r.Records {
			switch v := rec.(type) {
			case *core.CPUCounters:
				if v.LogicalCores < 1 || v.Total.Total() == 0 {
					t.Errorf("cpu %+v", v)
				}
			case *core.MemoryStats:
				if v.TotalBytes == 0 || v.AvailableBytes > v.TotalBytes {
					t.Errorf("memory %+v", v)
				}
			case *core.SystemInfo:
				if v.Kernel == "" || v.BootTime.IsZero() {
					t.Errorf("system %+v", v)
				}
			case core.Filesystems:
				for _, f := range v {
					if f.AvailableBytes > f.TotalBytes {
						t.Errorf("filesystem %+v", f)
					}
				}
			}
		}
		_ = c.Close(context.Background())
	}
	// The agent's own process must be listed.
	for _, c := range Collectors(e) {
		if pl, ok := c.(interface {
			ListProcesses(context.Context) (core.ProcessList, core.CapabilityStatus)
		}); ok {
			list, st := pl.ListProcesses(context.Background())
			found := false
			for _, p := range list.Processes {
				found = found || p.PID == os.Getpid()
			}
			if st.Availability != core.Available || !found {
				t.Errorf("own process not listed (%d processes, %+v)", len(list.Processes), st)
			}
			t.Logf("listed %d processes, partial=%v", len(list.Processes), list.Partial)
		}
	}
	// The basics must work on any supported Linux without root.
	must := map[string]core.Capability{"linux.cpu": core.CapCPUUsage, "linux.memory": core.CapMemoryUsage, "linux.system": core.CapSystemIdentity}
	for _, c := range Collectors(e) {
		if capability, ok := must[c.Descriptor().ID]; ok {
			if s := c.Collect(context.Background()).Status[capability]; s.Availability != core.Available {
				t.Errorf("%s must be available: %+v", capability, s)
			}
		}
	}
}
