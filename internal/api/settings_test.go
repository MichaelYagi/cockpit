package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/settings"
	"github.com/MichaelYagi/cockpit/internal/storage/sqlite"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

const validSettings = `{"health":{"confirm":3,"cpu_temperature_celsius":{"warn":80,"critical":90},
"memory_available_percent":{"warn":15,"critical":5},"memory_pressure_percent":{"warn":5,"critical":20},
"storage_used_percent":{"warn":75,"critical":90},"network_errors_per_minute":{"warn":10,"critical":100},
"disk":{"warn_percentage_used":80}}}`

func TestSettingsEndpoints(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var applied []config.Health
	e.d.Settings = settings.New(db, config.Defaults().Health, func(h config.Health) { applied = append(applied, h) }, fixedNow, nil, nil)
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	tcp := router(t, e.d, Options{Listener: ListenerTCP})

	check := func(r result, want int) apiv1.SettingsResponse {
		t.Helper()
		if r.status != want {
			t.Fatalf("status %d want %d: %s", r.status, want, r.body)
		}
		spec.CheckResponse(t, r.req, r.status, r.header, r.body)
		var out apiv1.SettingsResponse
		_ = json.Unmarshal(r.body, &out)
		return out
	}
	if got := check(do(t, sock, "GET", "/api/v1/settings"), 200); got.Source != "configuration" || got.Health.Storage.Warn != 85 || got.UpdatedAt != nil {
		t.Errorf("initial %+v", got)
	}
	r := do(t, sock, "PUT", "/api/v1/settings", jsonBody(validSettings))
	spec.CheckRequest(t, r.req)
	if got := check(r, 200); got.Source != "override" || got.Health.Storage.Warn != 75 || got.UpdatedAt == nil {
		t.Errorf("after put %+v", got)
	}
	if len(applied) != 1 || applied[0].Storage.WarnPercent != 75 {
		t.Errorf("not applied: %+v", applied)
	}
	bad := strings.Replace(validSettings, `"warn":75,"critical":90`, `"warn":95,"critical":90`, 1)
	r = do(t, sock, "PUT", "/api/v1/settings", jsonBody(bad))
	check(r, 400)
	if !strings.Contains(string(r.body), "health.storage") {
		t.Errorf("validation message: %s", r.body)
	}
	check(do(t, sock, "PUT", "/api/v1/settings", jsonBody(`{"health":{"confirm":3},"extra":1}`)), 400)
	if got := check(do(t, sock, "DELETE", "/api/v1/settings"), 200); got.Source != "configuration" || got.Health.Storage.Warn != 85 {
		t.Errorf("after reset %+v", got)
	}
	// Never reachable over TCP, even with a valid key.
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		if r := do(t, tcp, m, "/api/v1/settings", bearer(systemKey)); r.status != 404 && r.status != 405 {
			t.Errorf("%s over TCP: %d", m, r.status)
		}
	}
}
