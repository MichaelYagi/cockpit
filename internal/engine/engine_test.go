package engine

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func newEngine(t *testing.T, cs ...collector.Collector) *Engine {
	t.Helper()
	e, err := New(cs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e
}

// waitFor polls the snapshot until cond holds.
func waitFor(t *testing.T, e *Engine, cond func(*core.Snapshot) bool) *core.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := e.Current(); cond(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
	return nil
}

func fast(f *collectortest.Fake) *collectortest.Fake {
	f.Desc.DefaultInterval = 20 * time.Millisecond
	f.Desc.DefaultTimeout = 50 * time.Millisecond
	return f
}

func TestNewRejectsInvalidAndDuplicate(t *testing.T) {
	if _, err := New([]collector.Collector{collectortest.New("Bad ID")}, Options{}); err == nil {
		t.Error("invalid descriptor accepted")
	}
	if _, err := New([]collector.Collector{collectortest.New("test.a"), collectortest.New("test.a")}, Options{}); err == nil {
		t.Error("duplicate id accepted")
	}
}

func TestCurrentNeverNil(t *testing.T) {
	e := newEngine(t)
	if e.Current() == nil {
		t.Fatal("nil snapshot before start")
	}
}

func TestProbeIsolatesFailures(t *testing.T) {
	ok := collectortest.New("test.ok", core.CapCPUUsage, core.CapCPULoad)
	ok.ProbeStatus = map[core.Capability]core.CapabilityStatus{core.CapCPULoad: {Availability: core.PermissionDenied, Reason: "no access"}}
	panics := collectortest.New("test.panics", core.CapMemoryUsage)
	panics.ProbePanics = true
	slow := collectortest.New("test.slow", core.CapSensorsThermal)
	slow.Desc.DefaultTimeout = 20 * time.Millisecond
	slow.ProbeDelay = time.Hour

	e := newEngine(t, slow, panics, ok)
	start := time.Now()
	e.Start(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Start took %v", d)
	}
	got := map[string]core.CollectorStatus{}
	for _, s := range e.Collectors() {
		got[s.ID] = s
	}
	if s := got["test.ok"].Capabilities[core.CapCPULoad]; s.Availability != core.PermissionDenied {
		t.Errorf("ok load %+v", s)
	}
	if s := got["test.panics"].Capabilities[core.CapMemoryUsage]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("panicking probe %+v", s)
	}
	if s := got["test.slow"].Capabilities[core.CapSensorsThermal]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("slow probe %+v", s)
	}
	if st := e.Current().Memory.Meta.Status; st != core.TemporarilyUnavailable {
		t.Errorf("memory section after failed probe: %s", st)
	}
}

func cpuSample(busy, idle uint64) *core.CPUCounters {
	return &core.CPUCounters{
		Total:        core.CPUTimes{User: busy, Idle: idle},
		PerCore:      []core.CPUTimes{{User: busy, Idle: idle}},
		LogicalCores: 1,
	}
}

func TestScheduledCollectionDerivesCPUUsage(t *testing.T) {
	var n atomic.Uint64
	f := fast(collectortest.New("test.cpu", core.CapCPUUsage))
	f.CollectFunc = func(context.Context) collector.Result {
		i := n.Add(1)
		return collector.Result{
			CollectedAt: time.Now(),
			Records:     []core.Record{cpuSample(i*25, i*75)}, // 25% busy every step
			Status:      map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Available}},
		}
	}
	e := newEngine(t, f)
	e.Start(context.Background())
	s := waitFor(t, e, func(s *core.Snapshot) bool { return s.CPU.Data != nil && s.CPU.Data.UsagePercent != nil })
	if u := *s.CPU.Data.UsagePercent; math.Abs(u-25) > 0.001 {
		t.Errorf("usage %v", u)
	}
	if s.CPU.Meta.Status != core.Available || s.CPU.Meta.CollectedAt.IsZero() || s.CPU.Meta.StaleAfter.IsZero() {
		t.Errorf("meta %+v", s.CPU.Meta)
	}
	if s.CPU.Data.WindowSeconds <= 0 || len(s.CPU.Data.PerCoreUsagePercent) != 1 {
		t.Errorf("data %+v", s.CPU.Data)
	}
}

// Aggregator tests drive apply() directly with fixed times.
func testAggregator(t *testing.T, f *collectortest.Fake) (*aggregator, *runner) {
	t.Helper()
	e, err := New([]collector.Collector{f}, Options{Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	probe := normalize(f.Desc, f.Probe(context.Background()))
	return newAggregator(e, []map[core.Capability]core.CapabilityStatus{probe}), e.runners[0]
}

func avail(caps ...core.Capability) map[core.Capability]core.CapabilityStatus {
	m := map[core.Capability]core.CapabilityStatus{}
	for _, c := range caps {
		m[c] = core.CapabilityStatus{Availability: core.Available}
	}
	return m
}

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestCPUUsageAndCounterReset(t *testing.T) {
	a, r := testAggregator(t, collectortest.New("test.cpu", core.CapCPUUsage))
	apply := func(at time.Time, c *core.CPUCounters) *core.CPUMetrics {
		a.apply(outcome{runner: r, result: collector.Result{CollectedAt: at, Records: []core.Record{c}, Status: avail(core.CapCPUUsage)}})
		return a.publish().CPU.Data
	}
	if m := apply(t0, cpuSample(100, 100)); m.UsagePercent != nil {
		t.Error("first sample must have no usage")
	}
	m := apply(t0.Add(2*time.Second), cpuSample(160, 140)) // +60 busy, +40 idle
	if m.UsagePercent == nil || math.Abs(*m.UsagePercent-60) > 1e-9 || m.WindowSeconds != 2 {
		t.Errorf("usage %+v", m)
	}
	if m := apply(t0.Add(4*time.Second), cpuSample(10, 10)); m.UsagePercent != nil {
		t.Error("counter reset must yield no usage")
	}
	if m := apply(t0.Add(6*time.Second), cpuSample(20, 30)); m.UsagePercent == nil || math.Abs(*m.UsagePercent-(10.0/30*100)) > 1e-9 {
		t.Errorf("after reset %+v", m)
	}
}

func TestNetworkRates(t *testing.T) {
	a, r := testAggregator(t, collectortest.New("test.net", core.CapNetworkInterfaces))
	apply := func(at time.Time, nets core.NetInterfaces) []core.NetInterfaceMetrics {
		a.apply(outcome{runner: r, result: collector.Result{CollectedAt: at, Records: []core.Record{nets}, Status: avail(core.CapNetworkInterfaces)}})
		return *a.publish().Network.Data
	}
	apply(t0, core.NetInterfaces{{Name: "eth0", RxBytes: 1000, TxBytes: 500}})
	got := apply(t0.Add(2*time.Second), core.NetInterfaces{{Name: "eth0", RxBytes: 3000, TxBytes: 400}, {Name: "wlan0", RxBytes: 9}})
	if got[0].RxBytesPerSecond == nil || *got[0].RxBytesPerSecond != 1000 {
		t.Errorf("rx rate %+v", got[0])
	}
	if got[0].TxBytesPerSecond != nil {
		t.Error("tx counter went backwards; rate must be nil")
	}
	if got[1].RxBytesPerSecond != nil {
		t.Error("new interface must have no rate yet")
	}
}

func TestTransientFailureKeepsStaleData(t *testing.T) {
	f := collectortest.New("test.mem", core.CapMemoryUsage)
	f.Desc.DefaultInterval = time.Second
	a, r := testAggregator(t, f)
	a.apply(outcome{runner: r, result: collector.Result{CollectedAt: t0,
		Records: []core.Record{&core.MemoryStats{TotalBytes: 100, AvailableBytes: 25}}, Status: avail(core.CapMemoryUsage)}})
	s := a.publish()
	if s.Memory.Data == nil || s.Memory.Data.UsedPercent != 75 {
		t.Fatalf("memory %+v", s.Memory.Data)
	}
	if !s.Memory.Meta.StaleAfter.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("stale deadline %v", s.Memory.Meta.StaleAfter)
	}

	a.apply(outcome{runner: r, failed: "timed out"})
	s2 := a.publish()
	if s2.Memory.Meta.Status != core.TemporarilyUnavailable || s2.Memory.Meta.Reason != "timed out" {
		t.Errorf("meta after failure %+v", s2.Memory.Meta)
	}
	if s2.Memory.Data == nil || !s2.Memory.Meta.CollectedAt.Equal(t0) {
		t.Error("last good data not kept")
	}
	if s2.Memory.Meta.IsStale(t0.Add(time.Second)) || !s2.Memory.Meta.IsStale(t0.Add(3*time.Second)) {
		t.Error("staleness deadline wrong")
	}
	// Earlier snapshots are not mutated.
	if s.Memory.Meta.Status != core.Available || s.Generation >= s2.Generation {
		t.Error("published snapshot was modified")
	}
}

func TestAvailableWithoutRecord(t *testing.T) {
	a, r := testAggregator(t, collectortest.New("test.mem", core.CapMemoryUsage))
	a.apply(outcome{runner: r, result: collector.Result{CollectedAt: t0, Status: avail(core.CapMemoryUsage)}})
	if st := a.publish().Memory.Meta.Status; st != core.TemporarilyUnavailable {
		t.Errorf("status %s", st)
	}
}

func TestUnownedRecordIgnored(t *testing.T) {
	cpu := collectortest.New("test.cpu", core.CapCPUUsage)
	mem := collectortest.New("test.mem", core.CapMemoryUsage)
	e, err := New([]collector.Collector{cpu, mem}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	probes := []map[core.Capability]core.CapabilityStatus{avail(core.CapCPUUsage), avail(core.CapMemoryUsage)}
	a := newAggregator(e, probes)
	// The cpu collector sends a memory record; the memory section belongs to test.mem.
	a.apply(outcome{runner: e.runners[0], result: collector.Result{CollectedAt: t0,
		Records: []core.Record{&core.MemoryStats{TotalBytes: 1}}, Status: avail(core.CapCPUUsage)}})
	if a.publish().Memory.Data != nil {
		t.Error("record from a non-owning collector was applied")
	}
}

func TestSectionsWithoutCollectorAreUnsupported(t *testing.T) {
	e := newEngine(t, collectortest.New("test.cpu", core.CapCPUUsage))
	e.Start(context.Background())
	if st := e.Current().DiskHealth.Meta.Status; st != core.Unsupported {
		t.Errorf("disk health %s", st)
	}
}

func TestCollectTimeoutAndOverlap(t *testing.T) {
	var calls, concurrent, maxConcurrent atomic.Int32
	block := make(chan struct{})
	f := fast(collectortest.New("test.hang", core.CapMemoryUsage))
	f.CollectFunc = func(ctx context.Context) collector.Result {
		calls.Add(1)
		c := concurrent.Add(1)
		for {
			m := maxConcurrent.Load()
			if c <= m || maxConcurrent.CompareAndSwap(m, c) {
				break
			}
		}
		<-block // ignores ctx on purpose
		concurrent.Add(-1)
		return collector.Result{}
	}
	e := newEngine(t, f)
	e.Start(context.Background())
	s := waitFor(t, e, func(s *core.Snapshot) bool {
		return s.Memory.Meta.Status == core.TemporarilyUnavailable && s.Memory.Meta.Reason != ""
	})
	if s.Memory.Meta.Reason == "" {
		t.Error("no reason")
	}
	time.Sleep(150 * time.Millisecond) // several intervals while the first call hangs
	if maxConcurrent.Load() != 1 {
		t.Errorf("overlapping collections: %d", maxConcurrent.Load())
	}
	close(block)
	_ = e.Close(context.Background())
	if calls.Load() < 1 {
		t.Error("collector never called")
	}
}

func TestCollectPanicIsolated(t *testing.T) {
	bad := fast(collectortest.New("test.bad", core.CapMemoryUsage))
	bad.CollectFunc = func(context.Context) collector.Result { panic("boom") }
	good := fast(collectortest.New("test.good", core.CapCPUUsage))
	var n atomic.Uint64
	good.CollectFunc = func(context.Context) collector.Result {
		i := n.Add(1)
		return collector.Result{CollectedAt: time.Now(), Records: []core.Record{cpuSample(i, i)}, Status: avail(core.CapCPUUsage)}
	}
	e := newEngine(t, bad, good)
	e.Start(context.Background())
	waitFor(t, e, func(s *core.Snapshot) bool {
		return s.CPU.Data != nil && s.CPU.Data.UsagePercent != nil && s.Memory.Meta.Status == core.TemporarilyUnavailable
	})
}

func TestDisabledCollector(t *testing.T) {
	var calls atomic.Int32
	f := fast(collectortest.New("test.mem", core.CapMemoryUsage))
	f.CollectFunc = func(context.Context) collector.Result { calls.Add(1); return collector.Result{} }
	e, err := New([]collector.Collector{f}, Options{Settings: func(d collector.Descriptor) Settings {
		return Settings{Enabled: false, Interval: d.DefaultInterval, Timeout: d.DefaultTimeout}
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Error("disabled collector ran")
	}
	if st := e.Current().Memory.Meta.Status; st != core.NotConfigured {
		t.Errorf("status %s", st)
	}
}

func TestUnsupportedOnlyCollectorNotScheduled(t *testing.T) {
	var calls atomic.Int32
	f := fast(collectortest.New("test.none", core.CapSensorsThermal))
	f.ProbeStatus = map[core.Capability]core.CapabilityStatus{core.CapSensorsThermal: {Availability: core.Unsupported, Reason: "no sensors"}}
	f.CollectFunc = func(context.Context) collector.Result { calls.Add(1); return collector.Result{} }
	e := newEngine(t, f)
	e.Start(context.Background())
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != 0 {
		t.Error("collector with nothing to provide was scheduled")
	}
	if m := e.Current().Sensors.Meta; m.Status != core.Unsupported || m.Reason != "no sensors" {
		t.Errorf("sensors %+v", m)
	}
}

func TestInventoryRunsOnce(t *testing.T) {
	var calls atomic.Int32
	f := collectortest.New("test.inv", core.CapStorageDevices)
	f.Desc.Class = collector.Inventory
	f.Desc.DefaultInterval = 0
	f.CollectFunc = func(context.Context) collector.Result {
		calls.Add(1)
		return collector.Result{CollectedAt: time.Now(), Records: []core.Record{core.BlockDevices{{Name: "sda"}}}, Status: avail(core.CapStorageDevices)}
	}
	e := newEngine(t, f)
	e.Start(context.Background())
	s := waitFor(t, e, func(s *core.Snapshot) bool { return s.BlockDevices.Data != nil })
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("inventory ran %d times", calls.Load())
	}
	if !s.BlockDevices.Meta.StaleAfter.IsZero() {
		t.Error("run-once inventory must never go stale")
	}
}

func TestCloseClosesAll(t *testing.T) {
	a := collectortest.New("test.a")
	b := collectortest.New("test.b")
	b.CloseErr = errors.New("boom")
	e, err := New([]collector.Collector{a, b}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.Start(context.Background())
	if err := e.Close(context.Background()); err == nil {
		t.Error("close error not reported")
	}
	if !a.Closed() || !b.Closed() {
		t.Error("not all collectors closed")
	}
}
