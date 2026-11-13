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

// Service checks are added and removed over the socket only (D41).
func TestEndpointChecksRoutes(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.d.EndpointChecks = settings.NewEndpoints(db, []config.EndpointTarget{{Name: "Router", URL: "http://192.0.2.1/"}}, fixedNow, nil, nil)
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	tcp := router(t, e.d, Options{Listener: ListenerTCP})

	check := func(r result, want int) []byte {
		t.Helper()
		if r.status != want {
			t.Fatalf("%s %s: status %d want %d: %s", r.req.Method, r.req.URL, r.status, want, r.body)
		}
		spec.CheckRequest(t, r.req)
		spec.CheckResponse(t, r.req, r.status, r.header, r.body)
		return r.body
	}
	list := func() apiv1.EndpointChecksResponse {
		var out apiv1.EndpointChecksResponse
		_ = json.Unmarshal(check(do(t, sock, "GET", "/api/v1/endpoint-checks"), 200), &out)
		return out
	}
	if l := list(); len(l.Items) != 1 || l.Items[0].Source != "configuration" || l.Max != config.MaxEndpoints {
		t.Errorf("initial %+v", l)
	}
	var added apiv1.EndpointCheck
	_ = json.Unmarshal(check(do(t, sock, "POST", "/api/v1/endpoint-checks", jsonBody(`{"name":"Home Assistant","url":"http://192.0.2.50:8123/"}`)), 201), &added)
	if added.Name != "Home Assistant" || added.Source != "runtime" || added.AddedAt == nil {
		t.Errorf("added %+v", added)
	}
	if b := check(do(t, sock, "POST", "/api/v1/endpoint-checks", jsonBody(`{"name":"home assistant","url":"tcp://192.0.2.50:22"}`)), 409); !strings.Contains(string(b), "already exists") {
		t.Errorf("duplicate: %s", b)
	}
	if r := do(t, sock, "POST", "/api/v1/endpoint-checks", jsonBody(`{"name":"x","url":"ftp://h/"}`)); r.status != 400 || !strings.Contains(string(r.body), "url:") {
		t.Errorf("invalid: %d %s", r.status, r.body)
	} else {
		spec.CheckResponse(t, r.req, r.status, r.header, r.body)
	}
	if r := do(t, sock, "POST", "/api/v1/endpoint-checks", jsonBody(`{"name":"x","url":"tcp://h:1","extra":1}`)); r.status != 400 {
		t.Errorf("unknown field: %d", r.status)
	}
	if b := check(do(t, sock, "DELETE", "/api/v1/endpoint-checks/Router"), 409); !strings.Contains(string(b), "configuration file") {
		t.Errorf("file entry: %s", b)
	}
	check(do(t, sock, "DELETE", "/api/v1/endpoint-checks/nope"), 404)
	check(do(t, sock, "DELETE", "/api/v1/endpoint-checks/Home%20Assistant"), 204)
	if l := list(); len(l.Items) != 1 {
		t.Errorf("after remove %+v", l)
	}
	// Never reachable over TCP, even with a valid key.
	for _, m := range []string{"GET", "POST"} {
		if r := do(t, tcp, m, "/api/v1/endpoint-checks", bearer(systemKey)); r.status != 404 && r.status != 405 {
			t.Errorf("%s over TCP: %d", m, r.status)
		}
	}
	if r := do(t, tcp, "DELETE", "/api/v1/endpoint-checks/Router", bearer(systemKey)); r.status != 404 && r.status != 405 {
		t.Errorf("DELETE over TCP: %d", r.status)
	}
}
