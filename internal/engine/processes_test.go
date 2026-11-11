package engine

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// fakeLister returns a process whose CPU ticks grow 50 per call.
type fakeLister struct {
	*collectortest.Fake
	calls    atomic.Int32
	reusePID bool
}

func (f *fakeLister) ListProcesses(context.Context) (core.ProcessList, core.CapabilityStatus) {
	n := f.calls.Add(1)
	start := uint64(1000)
	if f.reusePID && n > 1 {
		start = 9999 // same PID, different process
	}
	return core.ProcessList{
		TakenAt: time.Now(),
		Users:   map[int]string{1000: "alice"},
		Processes: []core.ProcessSample{
			{PID: 42, Name: "worker", UID: 1000, CPUTicks: uint64(n) * 50, StartTicks: start, RSSBytes: 512},
			{PID: 43, Name: "ghost", UID: 4242, StartTicks: 1},
		},
	}, core.CapabilityStatus{Availability: core.Available}
}

func listerEngine(t *testing.T, l *fakeLister) *Engine {
	t.Helper()
	e := newEngine(t, l)
	e.current.Store(&core.Snapshot{Memory: core.Section[core.MemoryMetrics]{Data: &core.MemoryMetrics{MemoryStats: core.MemoryStats{TotalBytes: 1024}}}})
	return e
}

func TestProcessesCPUAndCache(t *testing.T) {
	l := &fakeLister{Fake: collectortest.New("test.procs", core.CapProcessesList)}
	e := listerEngine(t, l)
	start := time.Now()
	v := e.Processes(context.Background())
	if time.Since(start) < processSampleGap {
		t.Error("first listing must take two samples")
	}
	if v.Status.Availability != core.Available || l.calls.Load() != 2 || v.ID == 0 {
		t.Fatalf("view %+v calls %d", v, l.calls.Load())
	}
	w := v.Processes[0]
	// 50 ticks at USER_HZ=100 over ~0.5s is ~100% of one core.
	if w.CPUPercent == nil || math.Abs(*w.CPUPercent-0.5/v.WindowSeconds*100) > 1e-6 || w.User != "alice" || w.MemoryPercent != 50 {
		t.Errorf("worker %+v (window %v)", w, v.WindowSeconds)
	}
	if v.Processes[1].User != "4242" {
		t.Errorf("unknown uid shown as %q", v.Processes[1].User)
	}

	again := e.Processes(context.Background())
	if again.ID != v.ID || l.calls.Load() != 2 {
		t.Error("cached view not reused within the TTL")
	}
	if e.ProcessView(v.ID) != v || e.ProcessView(v.ID+100) != nil {
		t.Error("view lookup by ID")
	}
}

func TestProcessesPIDReuseHasNoCPU(t *testing.T) {
	l := &fakeLister{Fake: collectortest.New("test.procs", core.CapProcessesList), reusePID: true}
	v := listerEngine(t, l).Processes(context.Background())
	if v.Processes[0].CPUPercent != nil {
		t.Error("a reused PID was treated as the same process")
	}
}

func TestProcessesUnsupported(t *testing.T) {
	e := newEngine(t, collectortest.New("test.cpu", core.CapCPUUsage))
	if v := e.Processes(context.Background()); v.Status.Availability != core.Unsupported || v.Status.Reason == "" {
		t.Errorf("%+v", v.Status)
	}
}

func TestProcessesDisabledLister(t *testing.T) {
	l := &fakeLister{Fake: collectortest.New("test.procs", core.CapProcessesList)}
	e, err := New([]collector.Collector{l}, Options{Settings: func(d collector.Descriptor) Settings {
		return Settings{Enabled: false, Timeout: time.Second}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v := e.Processes(context.Background()); v.Status.Availability == core.Available || l.calls.Load() != 0 {
		t.Errorf("disabled lister used: %+v", v.Status)
	}
}
