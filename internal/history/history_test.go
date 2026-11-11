package history

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func meta() core.SectionMeta {
	return core.SectionMeta{Status: core.Available, CollectedAt: t0, StaleAfter: t0.Add(time.Hour)}
}

func snapshot() *core.Snapshot {
	return &core.Snapshot{
		CPU:    core.Section[core.CPUMetrics]{Meta: meta(), Data: &core.CPUMetrics{UsagePercent: ptr(12.5), Load: &core.LoadAverage{One: 0.5}}},
		Memory: core.Section[core.MemoryMetrics]{Meta: meta(), Data: &core.MemoryMetrics{MemoryStats: core.MemoryStats{AvailableBytes: 600, SwapTotalBytes: 0}, UsedPercent: 40}},
		Network: core.Section[[]core.NetInterfaceMetrics]{Meta: meta(), Data: &[]core.NetInterfaceMetrics{
			{NetInterface: core.NetInterface{Name: "eth0"}, RxBytesPerSecond: ptr(100.0), TxBytesPerSecond: ptr(50.0)},
			{NetInterface: core.NetInterface{Name: "lo", Loopback: true}, RxBytesPerSecond: ptr(9.0)},
		}},
		Filesystems: core.Section[core.Filesystems]{Meta: meta(), Data: &core.Filesystems{
			{MountPoint: "/", TotalBytes: 100, FreeBytes: 50, AvailableBytes: 50},
			{MountPoint: "/snap/x", ReadOnly: true, TotalBytes: 10},
		}},
		Sensors: core.Section[core.Sensors]{Meta: core.SectionMeta{Status: core.Unsupported}},
		GPUs:    core.Section[core.GPUs]{Meta: meta(), Data: &core.GPUs{{UtilizationPercent: ptr(30.0), MemoryUsedBytes: ptr(uint64(1)), MemoryTotalBytes: ptr(uint64(4))}}},
	}
}

func TestExtract(t *testing.T) {
	got := Extract(snapshot(), t0)
	want := map[string]float64{
		"cpu.usage_percent": 12.5, "cpu.load_1m": 0.5, "memory.used_percent": 40, "memory.available_bytes": 600,
		"net.eth0.rx_bytes_per_second": 100, "net.eth0.tx_bytes_per_second": 50, "fs./.used_percent": 50,
		"gpu.0.utilization_percent": 30, "gpu.0.memory_used_percent": 25,
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["swap.used_percent"]; ok {
		t.Error("swap recorded without swap")
	}
}

func TestExtractSkipsStale(t *testing.T) {
	if got := Extract(snapshot(), t0.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("stale data recorded: %v", got)
	}
}

func TestUnit(t *testing.T) {
	for s, u := range map[string]string{"net.eth0.rx_bytes_per_second": "bytes_per_second", "cpu.usage_percent": "percent",
		"memory.available_bytes": "bytes", "gpu.0.temperature_celsius": "celsius", "gpu.0.power_watts": "watts", "cpu.load_1m": ""} {
		if Unit(s) != u {
			t.Errorf("%s: %q", s, Unit(s))
		}
	}
}

func TestChooseTier(t *testing.T) {
	cases := map[time.Duration]Tier{time.Hour: TierRaw, 5 * time.Hour: TierRaw, 6 * time.Hour: TierMinute, 24 * time.Hour: TierMinute, 7 * 24 * time.Hour: TierHour}
	for span, want := range cases {
		if got := ChooseTier(t0.Add(-span), t0, MaxPoints); got != want {
			t.Errorf("%v: %s, want %s", span, got, want)
		}
	}
}

type fakeStore struct {
	written []Sample
	fail    error
}

func (f *fakeStore) WriteSamples(_ context.Context, s []Sample) error {
	if f.fail != nil {
		return f.fail
	}
	f.written = append(f.written, s...)
	return nil
}
func (f *fakeStore) Rollup(context.Context, time.Time) error           { return nil }
func (f *fakeStore) Prune(context.Context, time.Time, Retention) error { return nil }
func (f *fakeStore) Query(context.Context, []string, Tier, time.Time, time.Time) (map[string][]Point, error) {
	return nil, nil
}
func (f *fakeStore) ListSeries(context.Context) ([]SeriesInfo, error) { return nil, nil }

type src struct{ s *core.Snapshot }

func (s src) Current() *core.Snapshot { return s.s }

func TestRecorderFlushRetriesAndBounds(t *testing.T) {
	store := &fakeStore{fail: errors.New("disk full")}
	r := NewRecorder(store, src{snapshot()}, DefaultRetention(), func() time.Time { return t0 }, nil)
	r.Sample()
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("error swallowed")
	}
	store.fail = nil
	if err := r.Flush(context.Background()); err != nil || len(store.written) != 9 {
		t.Fatalf("samples lost after a failed flush: %d %v", len(store.written), err)
	}
	// The buffer is bounded when storage keeps failing.
	store.fail = errors.New("still failing")
	for i := 0; i < maxBuffered; i++ {
		r.Sample()
	}
	if r.Dropped() == 0 {
		t.Error("buffer not bounded")
	}
	r.mu.Lock()
	n := len(r.buf)
	r.mu.Unlock()
	if n > maxBuffered {
		t.Errorf("buffer %d > %d", n, maxBuffered)
	}
}

func TestExtractCustomMetrics(t *testing.T) {
	s := &core.Snapshot{Custom: []core.CustomSet{
		{Collector: "custom.demo", Meta: meta(), Metrics: []core.CustomMetric{
			{Name: "custom.demo.open", Kind: core.Gauge, Unit: "count", Value: 42, History: true},
			{Name: "custom.demo.temp_celsius", Kind: core.Gauge, Unit: "celsius", Labels: map[string]string{"zone": "cpu 0", "chip": "k10"}, Value: 50, History: true},
			{Name: "custom.demo.read", Kind: core.Counter, Unit: "bytes", Value: 900, RatePerSecond: ptr(30.0), History: true},
			{Name: "custom.demo.first", Kind: core.Counter, Unit: "count", Value: 1, History: true}, // no rate yet
			{Name: "custom.demo.quiet", Kind: core.Gauge, Unit: "count", Value: 1},                  // not opted in
		}},
		{Collector: "custom.stale", Meta: core.SectionMeta{Status: core.Available, CollectedAt: t0, StaleAfter: t0.Add(-time.Minute)},
			Metrics: []core.CustomMetric{{Name: "custom.stale.x", Kind: core.Gauge, Unit: "count", History: true}}},
	}}
	got := Extract(s, t0)
	want := map[string]float64{
		"custom.demo.open_count":                     42,
		"custom.demo.temp_celsius.k10.cpu_0_celsius": 50, // labels in name order, then the unit
		"custom.demo.read_bytes_per_second":          30,
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	for series, unit := range map[string]string{"custom.demo.open_count": "count", "custom.demo.read_bytes_per_second": "bytes_per_second", "custom.demo.temp_celsius.k10.cpu_0_celsius": "celsius",
		"x.rate_per_second": "per_second", "x.busy_seconds_per_second": "seconds_per_second", "cpu.load_1m": ""} {
		if u := Unit(series); u != unit {
			t.Errorf("Unit(%s) = %q, want %q", series, u, unit)
		}
	}
}

func TestExtractBusiestContainers(t *testing.T) {
	var cs []core.ContainerMetrics
	for i := 0; i < 8; i++ {
		id := strings.Repeat(string(rune('a'+i)), 64)
		c := core.ContainerMetrics{ContainerSample: core.ContainerSample{Runtime: "docker", ID: id, MemoryBytes: ptr(uint64(i))}, CPUPercent: ptr(float64(i))}
		cs = append(cs, c)
	}
	cs = append(cs, core.ContainerMetrics{ContainerSample: core.ContainerSample{Runtime: "podman", ID: strings.Repeat("z", 64)}}) // no rate yet
	got := Extract(&core.Snapshot{Containers: core.Section[[]core.ContainerMetrics]{Meta: meta(), Data: &cs}}, t0)
	if len(got) != 2*HistoryContainers {
		t.Errorf("series %v", got)
	}
	if got["containers.docker.hhhhhhhhhhhh.cpu_percent"] != 7 || got["containers.docker.dddddddddddd.memory_bytes"] != 3 {
		t.Errorf("busiest missing: %v", got)
	}
	if _, ok := got["containers.docker.aaaaaaaaaaaa.cpu_percent"]; ok {
		t.Error("idle container recorded")
	}
}

func TestExtractZFSPowerUPS(t *testing.T) {
	s := &core.Snapshot{
		ZFS:   core.Section[core.ZFSInfo]{Meta: meta(), Data: &core.ZFSInfo{Pools: []core.ZFSPool{{Name: "tank", SizeBytes: ptr(uint64(200)), AllocatedBytes: ptr(uint64(50))}, {Name: "nocap"}}}},
		Power: core.Section[core.PowerSupplies]{Meta: meta(), Data: &core.PowerSupplies{{Name: "BAT0", Type: "battery", CapacityPercent: ptr(80.0)}, {Name: "AC", Type: "mains"}}},
		UPS:   core.Section[core.UPSList]{Meta: meta(), Data: &core.UPSList{{Name: "rack", ChargePercent: ptr(90.0), LoadPercent: ptr(30.0)}}},
	}
	got := Extract(s, t0)
	want := map[string]float64{"zfs.tank.used_percent": 25, "power.BAT0.capacity_percent": 80, "ups.rack.charge_percent": 90, "ups.rack.load_percent": 30}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
