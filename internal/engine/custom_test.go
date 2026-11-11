package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/core"
)

const demoCap core.Capability = "custom.demo.files"

func demoCollector(collect func(i uint64) []core.GenericMetric) *collectortest.Fake {
	f := fast(collectortest.New("custom.demo", demoCap))
	f.Desc.GenericMetrics = true
	f.Desc.HistoryMetrics = []string{"custom.demo.open"}
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var n atomic.Uint64
	f.CollectFunc = func(context.Context) collector.Result {
		i := n.Add(1)
		return collector.Result{
			CollectedAt: base.Add(time.Duration(i) * time.Second),
			Records:     []core.Record{core.GenericMetrics(collect(i))},
			Status:      map[core.Capability]core.CapabilityStatus{demoCap: {Availability: core.Available}},
		}
	}
	return f
}

func TestGenericMetrics(t *testing.T) {
	f := demoCollector(func(i uint64) []core.GenericMetric {
		ms := []core.GenericMetric{
			{Name: "custom.demo.open", Kind: core.Gauge, Unit: "count", Value: 42},
			{Name: "custom.demo.read", Kind: core.Counter, Unit: "bytes", Labels: map[string]string{"dev": "a"}, Value: float64(i * 100)},
			{Name: "other.namespace", Kind: core.Gauge, Unit: "count"},               // wrong namespace
			{Name: "custom.demo.open", Kind: core.Gauge, Unit: "count", Value: 1},    // duplicate
			{Name: "custom.demo.temp", Kind: core.Gauge, Unit: "furlongs", Value: 1}, // unknown unit
			{Name: "custom.demo.gauge_counter", Kind: core.Counter, Unit: "percent"}, // counter in percent
		}
		for j := 0; j < core.MaxSeriesPerCollector; j++ { // 2 valid already: 2 of these overflow
			ms = append(ms, core.GenericMetric{Name: "custom.demo.n", Kind: core.Gauge, Unit: "count", Labels: map[string]string{"i": fmt.Sprint(j)}})
		}
		return ms
	})
	e := newEngine(t, f)
	e.Start(context.Background())
	s := waitFor(t, e, func(s *core.Snapshot) bool {
		return len(s.Custom) == 1 && len(s.Custom[0].Metrics) > 0 && rateOf(s.Custom[0], "custom.demo.read") != nil
	})
	set := s.Custom[0]
	if set.Collector != "custom.demo" || set.Meta.Status != core.Available || set.Meta.CollectedAt.IsZero() {
		t.Errorf("set %+v", set.Meta)
	}
	if len(set.Metrics) != core.MaxSeriesPerCollector {
		t.Errorf("kept %d series", len(set.Metrics))
	}
	if set.Dropped != 6 || !strings.Contains(set.Problem, "must start with the collector ID") {
		t.Errorf("dropped %d, problem %q", set.Dropped, set.Problem)
	}
	if r := rateOf(set, "custom.demo.read"); *r != 100 {
		t.Errorf("counter rate %v, want 100/s", *r)
	}
	for _, m := range set.Metrics {
		if m.History != (m.Name == "custom.demo.open") {
			t.Errorf("%s history=%v", m.Name, m.History)
		}
	}
}

func rateOf(set core.CustomSet, name string) *float64 {
	for _, m := range set.Metrics {
		if m.Name == name {
			return m.RatePerSecond
		}
	}
	return nil
}

func TestGenericMetricsDescriptorRules(t *testing.T) {
	f := collectortest.New("custom.demo", demoCap)
	if _, err := New([]collector.Collector{f}, Options{}); err == nil {
		t.Error("own capability accepted without GenericMetrics")
	}
	f.Desc.GenericMetrics = true
	f.Desc.HistoryMetrics = []string{"custom.other.x"}
	if _, err := New([]collector.Collector{f}, Options{}); err == nil {
		t.Error("history metric outside the namespace accepted")
	}
	g := collectortest.New("custom.demo", "custom.elsewhere.cap")
	g.Desc.GenericMetrics = true
	if _, err := New([]collector.Collector{g}, Options{}); err == nil {
		t.Error("capability outside the namespace accepted")
	}
}

func TestGenericMetricsWithoutRecordGoUnavailable(t *testing.T) {
	f := fast(collectortest.New("custom.demo", demoCap))
	f.Desc.GenericMetrics = true
	f.CollectFunc = func(context.Context) collector.Result {
		return collector.Result{CollectedAt: time.Now(), Status: map[core.Capability]core.CapabilityStatus{demoCap: {Availability: core.Available}}}
	}
	e := newEngine(t, f)
	e.Start(context.Background())
	waitFor(t, e, func(s *core.Snapshot) bool {
		return len(s.Custom) == 1 && s.Custom[0].Meta.Status == core.TemporarilyUnavailable
	})
}
