// Package history records a fixed set of metric series from engine
// snapshots and serves them at several resolutions (D18).
package history

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Tier is a storage resolution.
type Tier string

const (
	TierRaw    Tier = "raw" // one value every SampleInterval
	TierMinute Tier = "1m"
	TierHour   Tier = "1h"
)

// Step returns the tier's resolution.
func (t Tier) Step() time.Duration {
	switch t {
	case TierMinute:
		return time.Minute
	case TierHour:
		return time.Hour
	}
	return SampleInterval
}

// Recording cadence (D18).
const (
	SampleInterval = 10 * time.Second
	FlushInterval  = 30 * time.Second
	RollupInterval = time.Minute
	MaxSeries      = 200
	MaxPoints      = 2000
	MaxRange       = 90 * 24 * time.Hour
	maxBuffered    = 2000
)

// Sample is one raw value.
type Sample struct {
	Series string
	Time   time.Time
	Value  float64
}

// Point is a value or an aggregate over a bucket.
type Point struct {
	Time          time.Time
	Min, Avg, Max float64
}

// SeriesInfo describes a stored series.
type SeriesInfo struct {
	Name        string
	First, Last time.Time
}

// Retention per tier.
type Retention struct {
	Raw, Minute, Hour time.Duration
}

// DefaultRetention is D18's retention.
func DefaultRetention() Retention {
	return Retention{Raw: 24 * time.Hour, Minute: 7 * 24 * time.Hour, Hour: 90 * 24 * time.Hour}
}

// Store persists history. Implemented by storage/sqlite.
type Store interface {
	WriteSamples(ctx context.Context, samples []Sample) error
	// Rollup recomputes 1m and 1h buckets that ended before now.
	Rollup(ctx context.Context, now time.Time) error
	Prune(ctx context.Context, now time.Time, r Retention) error
	Query(ctx context.Context, series []string, tier Tier, from, to time.Time) (map[string][]Point, error)
	ListSeries(ctx context.Context) ([]SeriesInfo, error)
}

// Unit returns the unit of a series, from its name suffix.
func Unit(series string) string {
	for _, u := range unitSuffixes {
		if strings.HasSuffix(series, "_"+u) {
			return u
		}
	}
	return ""
}

// unitSuffixes are checked in order, so compound units come first.
var unitSuffixes = []string{"bytes_per_second", "seconds_per_second", "per_second", "percent", "bytes", "celsius", "watts",
	"volts", "hertz", "rpm", "seconds", "ratio", "count"}

// CustomSeries names the history series of a generic metric (D22): the
// metric name, its label values in label-name order, and a unit suffix so
// Unit can recover the unit. Characters outside [A-Za-z0-9_:/-] become "_".
func CustomSeries(name string, labels map[string]string, unit string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte('.')
		v := labels[k]
		if len(v) > 64 {
			v = v[:64]
		}
		for _, r := range v {
			if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_:/-", r)) {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
	}
	out := b.String()
	if unit != "" && !strings.HasSuffix(out, "_"+unit) {
		out += "_" + unit
	}
	return out
}

// Extract returns the recorded series values from a snapshot. Only
// available, non-stale data is recorded.
func Extract(s *core.Snapshot, now time.Time) map[string]float64 {
	out := map[string]float64{}
	ok := func(m core.SectionMeta) bool { return m.Status == core.Available && !m.IsStale(now) }
	if c := s.CPU.Data; c != nil && ok(s.CPU.Meta) {
		if c.UsagePercent != nil {
			out["cpu.usage_percent"] = *c.UsagePercent
		}
		if c.Load != nil {
			out["cpu.load_1m"] = c.Load.One
		}
	}
	if m := s.Memory.Data; m != nil && ok(s.Memory.Meta) {
		out["memory.used_percent"] = m.UsedPercent
		out["memory.available_bytes"] = float64(m.AvailableBytes)
		if m.SwapTotalBytes > 0 {
			out["swap.used_percent"] = m.SwapUsedPercent
		}
	}
	if p := s.MemoryPressure.Data; p != nil && ok(s.MemoryPressure.Meta) {
		out["memory.pressure_full_avg10_percent"] = p.FullAvg10
	}
	if n := s.Network.Data; n != nil && ok(s.Network.Meta) {
		for _, ni := range *n {
			if ni.Loopback {
				continue
			}
			if ni.RxBytesPerSecond != nil {
				out["net."+ni.Name+".rx_bytes_per_second"] = *ni.RxBytesPerSecond
			}
			if ni.TxBytesPerSecond != nil {
				out["net."+ni.Name+".tx_bytes_per_second"] = *ni.TxBytesPerSecond
			}
		}
	}
	if f := s.Filesystems.Data; f != nil && ok(s.Filesystems.Meta) {
		for _, fs := range *f {
			if fs.ReadOnly || fs.TotalBytes == 0 || fs.FreeBytes > fs.TotalBytes {
				continue
			}
			used := fs.TotalBytes - fs.FreeBytes
			if d := used + fs.AvailableBytes; d > 0 {
				out["fs."+fs.MountPoint+".used_percent"] = float64(used) / float64(d) * 100
			}
		}
	}
	if sn := s.Sensors.Data; sn != nil && ok(s.Sensors.Meta) {
		hottest, found := 0.0, false
		for _, x := range *sn {
			if x.Kind == core.SensorTemperature && x.CPU && (!found || x.Value > hottest) {
				hottest, found = x.Value, true
			}
		}
		if found {
			out["sensors.cpu_temperature_celsius"] = hottest
		}
	}
	if g := s.GPUs.Data; g != nil && ok(s.GPUs.Meta) {
		for i, gpu := range *g {
			p := fmt.Sprintf("gpu.%d.", i)
			if gpu.UtilizationPercent != nil {
				out[p+"utilization_percent"] = *gpu.UtilizationPercent
			}
			if gpu.MemoryUsedBytes != nil && gpu.MemoryTotalBytes != nil && *gpu.MemoryTotalBytes > 0 {
				out[p+"memory_used_percent"] = float64(*gpu.MemoryUsedBytes) / float64(*gpu.MemoryTotalBytes) * 100
			}
			if gpu.TemperatureCelsius != nil {
				out[p+"temperature_celsius"] = *gpu.TemperatureCelsius
			}
			if gpu.PowerWatts != nil {
				out[p+"power_watts"] = *gpu.PowerWatts
			}
		}
	}
	for _, set := range s.Custom {
		if !ok(set.Meta) {
			continue
		}
		for _, m := range set.Metrics {
			if !m.History {
				continue
			}
			v, unit := m.Value, m.Unit
			if m.Kind == core.Counter {
				if m.RatePerSecond == nil {
					continue
				}
				v, unit = *m.RatePerSecond, core.RateUnit(m.Unit)
			}
			out[CustomSeries(m.Name, m.Labels, unit)] = v
		}
	}
	if len(out) > MaxSeries { // keep storage bounded on unusual machines
		names := make([]string, 0, len(out))
		for k := range out {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names[MaxSeries:] {
			delete(out, k)
		}
	}
	return out
}

// SnapshotSource provides the latest snapshot.
type SnapshotSource interface {
	Current() *core.Snapshot
}

// Recorder samples snapshots into a Store.
type Recorder struct {
	store     Store
	src       SnapshotSource
	retention Retention
	now       func() time.Time
	log       *slog.Logger

	mu     sync.Mutex
	buf    []Sample
	lostMu sync.Mutex
	lost   int // samples dropped because the store fell behind
}

// NewRecorder returns a recorder.
func NewRecorder(store Store, src SnapshotSource, r Retention, now func() time.Time, log *slog.Logger) *Recorder {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Recorder{store: store, src: src, retention: r, now: now, log: log}
}

// Sample records the current snapshot into the buffer.
func (r *Recorder) Sample() {
	now := r.now().UTC().Truncate(time.Second)
	vals := Extract(r.src.Current(), now)
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, v := range vals {
		r.buf = append(r.buf, Sample{Series: name, Time: now, Value: v})
	}
	if over := len(r.buf) - maxBuffered; over > 0 {
		r.buf = r.buf[over:]
		r.lostMu.Lock()
		r.lost += over
		r.lostMu.Unlock()
	}
}

// Flush writes buffered samples. On failure they are kept for the next
// attempt (within the buffer bound).
func (r *Recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	batch := r.buf
	r.buf = nil
	r.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := r.store.WriteSamples(ctx, batch); err != nil {
		r.mu.Lock()
		r.buf = append(batch, r.buf...)
		if over := len(r.buf) - maxBuffered; over > 0 {
			r.buf = r.buf[over:]
		}
		r.mu.Unlock()
		return err
	}
	return nil
}

// Maintain rolls up and prunes.
func (r *Recorder) Maintain(ctx context.Context) error {
	now := r.now().UTC()
	if err := r.store.Rollup(ctx, now); err != nil {
		return err
	}
	return r.store.Prune(ctx, now, r.retention)
}

// Run samples, flushes and maintains until ctx is cancelled, then
// flushes once more.
func (r *Recorder) Run(ctx context.Context) {
	sample := time.NewTicker(SampleInterval)
	flush := time.NewTicker(FlushInterval)
	maintain := time.NewTicker(RollupInterval)
	defer sample.Stop()
	defer flush.Stop()
	defer maintain.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.Flush(fctx); err != nil {
				r.log.Warn("could not save metric history on shutdown", "error", err)
			}
			cancel()
			return
		case <-sample.C:
			r.Sample()
		case <-flush.C:
			if err := r.Flush(ctx); err != nil {
				r.log.Warn("could not save metric history", "error", err)
			}
		case <-maintain.C:
			if err := r.Maintain(ctx); err != nil {
				r.log.Warn("metric history maintenance failed", "error", err)
			}
		}
	}
}

// Dropped returns how many samples were discarded because storage fell
// behind.
func (r *Recorder) Dropped() int {
	r.lostMu.Lock()
	defer r.lostMu.Unlock()
	return r.lost
}

// ChooseTier picks the finest tier that keeps the range within maxPoints.
func ChooseTier(from, to time.Time, maxPoints int) Tier {
	span := to.Sub(from)
	for _, t := range []Tier{TierRaw, TierMinute, TierHour} {
		if int(span/t.Step()) <= maxPoints {
			return t
		}
	}
	return TierHour
}
