package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/history"
)

var h0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestHistoryWriteRollupQuery(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	// 2 hours of 10s samples: cpu ramps 0..719, mem constant 50.
	var samples []history.Sample
	for i := 0; i < 720; i++ {
		ts := h0.Add(time.Duration(i) * 10 * time.Second)
		samples = append(samples, history.Sample{Series: "cpu.usage_percent", Time: ts, Value: float64(i)},
			history.Sample{Series: "memory.used_percent", Time: ts, Value: 50})
	}
	if err := db.WriteSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}
	now := h0.Add(2*time.Hour + 30*time.Second)
	if err := db.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}
	// Running it again must not change anything.
	if err := db.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}

	raw, err := db.Query(ctx, []string{"cpu.usage_percent"}, history.TierRaw, h0, h0.Add(time.Minute))
	if err != nil || len(raw["cpu.usage_percent"]) != 7 {
		t.Fatalf("raw %v %v", raw, err)
	}
	m, _ := db.Query(ctx, []string{"cpu.usage_percent", "memory.used_percent"}, history.TierMinute, h0, now)
	cpu := m["cpu.usage_percent"]
	if len(cpu) != 120 || cpu[0].Min != 0 || cpu[0].Max != 5 || cpu[0].Avg != 2.5 || !cpu[1].Time.Equal(h0.Add(time.Minute)) {
		t.Fatalf("1m buckets: %d %+v", len(cpu), cpu[:2])
	}
	if len(m["memory.used_percent"]) != 120 || m["memory.used_percent"][0].Avg != 50 {
		t.Error("memory 1m")
	}
	hr, _ := db.Query(ctx, []string{"cpu.usage_percent"}, history.TierHour, h0, now)
	if len(hr["cpu.usage_percent"]) != 2 {
		t.Fatalf("1h buckets %+v", hr)
	}
	// First hour holds samples 0..359: weighted average 179.5.
	if p := hr["cpu.usage_percent"][0]; p.Min != 0 || p.Max != 359 || math.Abs(p.Avg-179.5) > 1e-9 {
		t.Errorf("1h bucket %+v", p)
	}

	series, err := db.ListSeries(ctx)
	if err != nil || len(series) != 2 || series[0].Name != "cpu.usage_percent" || !series[0].First.Equal(h0) {
		t.Errorf("series %+v %v", series, err)
	}
}

func TestHistoryPrune(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	old, recent := h0.Add(-48*time.Hour), h0.Add(-time.Hour)
	_ = db.WriteSamples(ctx, []history.Sample{{Series: "a", Time: old, Value: 1}, {Series: "a", Time: recent, Value: 2}, {Series: "gone", Time: old, Value: 3}})
	if err := db.Prune(ctx, h0, history.DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Query(ctx, []string{"a", "gone"}, history.TierRaw, old.Add(-time.Hour), h0)
	if len(got["a"]) != 1 || got["a"][0].Avg != 2 || len(got["gone"]) != 0 {
		t.Errorf("after prune %+v", got)
	}
	series, _ := db.ListSeries(ctx)
	if len(series) != 1 {
		t.Errorf("empty series not removed: %+v", series)
	}
}

func TestHistoryMigrationVersion(t *testing.T) {
	db, _ := open(t)
	if v, _ := db.SchemaVersion(context.Background()); v != migrationCount(t) {
		t.Errorf("schema version %d", v)
	}
}
