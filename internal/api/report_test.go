package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/report"
	"github.com/MichaelYagi/cockpit/internal/settings"
	"github.com/MichaelYagi/cockpit/internal/storage/sqlite"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

var bothKey = "ckp" + "_bothscopes12_" + strings.Repeat("D", 43)

func omittedSections(r apiv1.Report) string {
	var s []string
	for _, o := range r.Omitted {
		s = append(s, o.Section)
	}
	return strings.Join(s, ",")
}

func TestReportScopesAndFormats(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	e.d.Verifier.(fakeVerifier)[bothKey] = auth.Principal{Kind: auth.KindAPIKey, KeyID: "bothscopes12",
		Scopes: []auth.Scope{auth.ScopeSystemRead, auth.ScopeMetricsRead}}
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.d.Settings = settings.New(db, config.Defaults().Health, nil, fixedNow, nil, nil)
	tcp := router(t, e.d, Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	sock := router(t, e.d, Options{Listener: ListenerSocket})

	decode := func(r result) apiv1.Report {
		t.Helper()
		var out apiv1.Report
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(r result, want int) result {
		t.Helper()
		if r.status != want {
			t.Fatalf("status %d, want %d: %s", r.status, want, r.body)
		}
		spec.CheckResponse(t, r.req, r.status, r.header, r.body)
		return r
	}

	// system:read only: inventory parts, everything metric-based omitted.
	rep := decode(check(do(t, tcp, "GET", "/api/v1/report", bearer(systemKey)), 200))
	if rep.System == nil || rep.Hardware == nil || rep.Services == nil || rep.Metrics != nil || rep.Health != nil || rep.Events != nil || rep.Settings != nil {
		t.Errorf("system-only report has wrong parts: %+v", rep)
	}
	if got := omittedSections(rep); got != "metrics,storage,network,health,events,settings" {
		t.Errorf("omitted %s", got)
	}
	if rep.Status.API != nil {
		t.Error("TCP report must not include admin API status")
	}

	// metrics:read alone does not open the route.
	check(do(t, tcp, "GET", "/api/v1/report", bearer(metricsKey)), 403)
	check(do(t, tcp, "GET", "/api/v1/report"), 401)

	// Both read scopes: everything except settings.
	rep = decode(check(do(t, tcp, "GET", "/api/v1/report", bearer(bothKey)), 200))
	if rep.Metrics == nil || rep.Storage == nil || rep.Network == nil || rep.Health == nil || rep.Events == nil || rep.Settings != nil {
		t.Errorf("full TCP report has wrong parts")
	}
	if got := omittedSections(rep); got != "settings" {
		t.Errorf("omitted %s", got)
	}
	if rep.Metrics.Filesystems != nil || rep.Metrics.Network != nil || rep.Metrics.CPU == nil {
		t.Error("report metrics should leave filesystems and interfaces to the storage and network parts")
	}
	if len(rep.Events.Items) == 0 || rep.Health.Status != "warning" {
		t.Errorf("events %d, health %s", len(rep.Events.Items), rep.Health.Status)
	}

	// Socket: everything, including thresholds and listener status.
	rep = decode(check(do(t, sock, "GET", "/api/v1/report"), 200))
	if rep.Settings == nil || rep.Status.API == nil || len(rep.Omitted) != 0 {
		t.Errorf("socket report: settings=%v api=%v omitted=%v", rep.Settings != nil, rep.Status.API != nil, rep.Omitted)
	}
	if rep.Anonymized || rep.System.System.Data.Hostname != "test-host" {
		t.Error("plain report should not be anonymized")
	}

	// Anonymized JSON still matches the spec.
	rep = decode(check(do(t, sock, "GET", "/api/v1/report?anonymize=true"), 200))
	if !rep.Anonymized || rep.MachineID != report.AnonymousMachineID || rep.System.System.Data.Hostname != "host-1" {
		t.Errorf("anonymized report: %v %s %s", rep.Anonymized, rep.MachineID, rep.System.System.Data.Hostname)
	}

	// Markdown.
	r := check(do(t, sock, "GET", "/api/v1/report?format=markdown&anonymize=true"), 200)
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content type %q", ct)
	}
	md := string(r.body)
	if !strings.Contains(md, "# Cockpit diagnostic report") || strings.Contains(md, "test-host") || !strings.Contains(md, "host-1") {
		t.Errorf("markdown:\n%s", md)
	}

	// Sensitive values never appear, in any format (D9).
	for _, target := range []string{"/api/v1/report", "/api/v1/report?format=markdown"} {
		body := string(do(t, sock, "GET", target).body)
		for _, secret := range []string{testMAC, testSerial, testWWN} {
			if strings.Contains(body, secret) {
				t.Errorf("%s leaks %q", target, secret)
			}
		}
	}

	// Parameters.
	for _, q := range []string{"format=pdf", "anonymize=yes", "format=json&format=markdown", "verbose=1"} {
		r := check(do(t, sock, "GET", "/api/v1/report?"+q), 400)
		if errorCode(t, r.body) != apiv1.CodeInvalidParameter {
			t.Errorf("%s: code %s", q, errorCode(t, r.body))
		}
	}
}
