package history

import (
	"context"
	"errors"
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
