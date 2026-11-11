package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/history"
	"github.com/MichaelYagi/cockpit/internal/storage/sqlite"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

func historyEnv(t *testing.T) env {
	t.Helper()
	e := newEnv(t, nil)
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var samples []history.Sample
	for i := 0; i < 360; i++ { // the hour before t0
		samples = append(samples, history.Sample{Series: "cpu.usage_percent", Time: t0.Add(-time.Hour + time.Duration(i)*10*time.Second), Value: float64(i % 100)})
	}
	if err := db.WriteSamples(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	_ = db.Rollup(context.Background(), t0)
	e.d.History = db
	return e
}

func TestHistoryEndpoints(t *testing.T) {
	spec := contracttest.Load(t)
	h := router(t, historyEnv(t).d, Options{Listener: ListenerTCP})
	cases := []struct {
		name, target string
		status       int
	}{
		{"default range", "/api/v1/metrics/history?series=cpu.usage_percent,unknown.series", 200},
		{"minute", "/api/v1/metrics/history?series=cpu.usage_percent&resolution=1m&from=2026-11-11T14:00:00Z", 200},
		{"series list", "/api/v1/metrics/history/series", 200},
		{"missing series", "/api/v1/metrics/history", 400},
		{"too many", "/api/v1/metrics/history?series=" + strings.TrimSuffix(strings.Repeat("a,", 11), ","), 400},
		{"backwards", "/api/v1/metrics/history?series=a&from=2026-11-11T16:00:00Z&to=2026-11-11T15:00:00Z", 400},
		{"too long", "/api/v1/metrics/history?series=a&from=2026-01-01T00:00:00Z", 400},
		{"too many raw points", "/api/v1/metrics/history?series=a&resolution=raw&from=2026-11-10T00:00:00Z", 400},
		{"bad resolution", "/api/v1/metrics/history?series=a&resolution=5m", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := do(t, h, "GET", tc.target, bearer(metricsKey))
			if r.status != tc.status {
				t.Fatalf("%d: %s", r.status, r.body)
			}
			spec.CheckResponse(t, r.req, r.status, r.header, r.body)
		})
	}

	var resp apiv1.HistoryResponse
	_ = json.Unmarshal(do(t, h, "GET", "/api/v1/metrics/history?series=cpu.usage_percent,unknown.series", bearer(metricsKey)).body, &resp)
	if resp.Resolution != "raw" || resp.StepSeconds != 10 || len(resp.Series) != 2 || len(resp.Series[0].Points) != 360 ||
		resp.Series[0].Unit != "percent" || len(resp.Series[1].Points) != 0 {
		t.Errorf("history %+v", resp)
	}
	_ = json.Unmarshal(do(t, h, "GET", "/api/v1/metrics/history?series=cpu.usage_percent&resolution=1m&from=2026-11-11T14:00:00Z", bearer(metricsKey)).body, &resp)
	if len(resp.Series[0].Points) != 60 || resp.Series[0].Points[0].Max < resp.Series[0].Points[0].Min {
		t.Errorf("1m %+v", resp.Series[0].Points[:1])
	}
	if r := do(t, h, "GET", "/api/v1/metrics/history?series=cpu.usage_percent", bearer(systemKey)); r.status != 403 {
		t.Errorf("system:read key got %d", r.status)
	}
}

func TestHistoryDisabled(t *testing.T) {
	h := router(t, newEnv(t, nil).d, Options{Listener: ListenerSocket}) // no History source
	var resp apiv1.HistoryResponse
	r := do(t, h, "GET", "/api/v1/metrics/history?series=cpu.usage_percent")
	_ = json.Unmarshal(r.body, &resp)
	if r.status != 200 || resp.Status != "not_configured" || resp.Reason == "" || len(resp.Series[0].Points) != 0 {
		t.Errorf("%d %+v", r.status, resp)
	}
	contracttest.Load(t).CheckResponse(t, r.req, r.status, r.header, r.body)
}
