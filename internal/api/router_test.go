package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	openapi "github.com/MichaelYagi/cockpit/api"
	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Test keys are assembled at runtime so no key-shaped literal is committed.
var (
	systemKey  = "ckp" + "_systemreader_" + strings.Repeat("A", 43)
	metricsKey = "ckp" + "_metricsonly1_" + strings.Repeat("B", 43)
)

type fakeVerifier map[string]auth.Principal

func (f fakeVerifier) Verify(_ context.Context, key string) (auth.Principal, error) {
	if p, ok := f[key]; ok {
		return p, nil
	}
	return auth.Principal{}, auth.ErrInvalidCredentials
}

type fakeStatus struct{ panics bool }

func (fakeStatus) MachineID() core.MachineID { return "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10" }
func (fakeStatus) StartedAt() time.Time      { return time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC) }
func (f fakeStatus) Collectors() []core.CollectorStatus {
	if f.panics {
		panic("status source exploded")
	}
	return []core.CollectorStatus{{
		ID: "test.cpu", Version: "1.0.0",
		Capabilities: map[core.Capability]core.CapabilityStatus{
			core.CapCPUUsage: {Availability: core.Available},
			core.CapCPULoad:  {Availability: core.PermissionDenied, Reason: "Not permitted."},
		},
	}}
}

func deps(t *testing.T, logOut io.Writer) Deps {
	t.Helper()
	spec, err := openapi.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if logOut == nil {
		logOut = io.Discard
	}
	return Deps{
		Logger: slog.New(slog.NewTextHandler(logOut, nil)),
		Verifier: fakeVerifier{
			systemKey:  {Kind: auth.KindAPIKey, KeyID: "systemreader", Scopes: []auth.Scope{auth.ScopeSystemRead}},
			metricsKey: {Kind: auth.KindAPIKey, KeyID: "metricsonly1", Scopes: []auth.Scope{auth.ScopeMetricsRead}},
		},
		Status:   fakeStatus{},
		SpecJSON: spec,
		Now:      func() time.Time { return time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC) },
	}
}

func router(t *testing.T, d Deps, o Options) http.Handler {
	t.Helper()
	h, err := NewRouter(d, o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type result struct {
	req    *http.Request
	status int
	header http.Header
	body   []byte
}

func do(t *testing.T, h http.Handler, method, target string, mutate func(*http.Request)) result {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:7878"
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return result{req: req, status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func bearer(key string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
}

var requestIDPattern = regexp.MustCompile(`^req_[0-9A-HJKMNP-TV-Z]{26}$`)

func TestContract(t *testing.T) {
	spec := contracttest.Load(t)
	d := deps(t, nil)
	tcp := router(t, d, Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	sock := router(t, d, Options{Listener: ListenerSocket})

	panicky := d
	panicky.Status = fakeStatus{panics: true}
	broken := router(t, panicky, Options{Listener: ListenerSocket})

	tests := []struct {
		name     string
		h        http.Handler
		path     string
		mutate   func(*http.Request)
		status   int
		wantCode string
	}{
		{"livez", tcp, "/api/v1/livez", nil, 200, ""},
		{"openapi", tcp, "/api/v1/openapi.json", nil, 200, ""},
		{"status without key", tcp, "/api/v1/status", nil, 401, apiv1.CodeAuthenticationRequired},
		{"status unknown key", tcp, "/api/v1/status", bearer("ckp_nope_nope"), 401, apiv1.CodeInvalidCredentials},
		{"status wrong scheme", tcp, "/api/v1/status", func(r *http.Request) { r.Header.Set("Authorization", "Basic "+systemKey) }, 401, apiv1.CodeInvalidCredentials},
		{"status empty bearer", tcp, "/api/v1/status", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, 401, apiv1.CodeInvalidCredentials},
		{"status wrong scope", tcp, "/api/v1/status", bearer(metricsKey), 403, apiv1.CodeInsufficientScope},
		{"status ok over tcp", tcp, "/api/v1/status", bearer(systemKey), 200, ""},
		{"status lowercase bearer", tcp, "/api/v1/status", func(r *http.Request) { r.Header.Set("Authorization", "bearer "+systemKey) }, 200, ""},
		{"status ok over socket", sock, "/api/v1/status", nil, 200, ""},
		{"rebinding host", tcp, "/api/v1/livez", func(r *http.Request) { r.Host = "attacker.example:7878" }, 400, apiv1.CodeInvalidHost},
		{"internal error", broken, "/api/v1/status", nil, 500, apiv1.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, tt.h, http.MethodGet, tt.path, tt.mutate)
			if r.status != tt.status {
				t.Fatalf("status %d, want %d: %s", r.status, tt.status, r.body)
			}
			spec.CheckResponse(t, r.req, r.status, r.header, r.body)
			if id := r.header.Get(apiv1.RequestIDHeader); !requestIDPattern.MatchString(id) {
				t.Errorf("bad request id %q", id)
			}
			if tt.wantCode != "" {
				var env apiv1.ErrorEnvelope
				if err := json.Unmarshal(r.body, &env); err != nil {
					t.Fatal(err)
				}
				if env.Error.Code != tt.wantCode || env.Error.RequestID != r.header.Get(apiv1.RequestIDHeader) {
					t.Errorf("envelope %+v", env)
				}
			}
			if r.status == 500 && bytes.Contains(r.body, []byte("exploded")) {
				t.Error("panic detail leaked to client")
			}
		})
	}
}

func TestStatusBody(t *testing.T) {
	r := do(t, router(t, deps(t, nil), Options{Listener: ListenerSocket}), http.MethodGet, "/api/v1/status", nil)
	dec := json.NewDecoder(bytes.NewReader(r.body))
	dec.DisallowUnknownFields()
	var s apiv1.Status
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Agent.UptimeSeconds != 1800 {
		t.Errorf("uptime %v", s.Agent.UptimeSeconds)
	}
	caps := s.Collectors[0].Capabilities
	if len(caps) != 2 || caps[0].Name != "cpu.load" || caps[0].Status != apiv1.PermissionDenied || caps[1].Reason != "" {
		t.Errorf("capabilities %+v", caps)
	}
}

func TestOpenAPIServesEmbeddedSpec(t *testing.T) {
	r := do(t, router(t, deps(t, nil), Options{Listener: ListenerTCP}), http.MethodGet, "/api/v1/openapi.json", nil)
	var doc map[string]any
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi version %v", doc["openapi"])
	}
}

// Every route must be in the spec with the same scope, and vice versa.
func TestRoutesMatchSpec(t *testing.T) {
	spec := contracttest.Load(t)
	var fromTable []contracttest.Operation
	for _, rt := range Routes(deps(t, nil)) {
		scope := string(rt.Scope)
		if rt.Public {
			scope = "public"
		}
		fromTable = append(fromTable, contracttest.Operation{Method: rt.Method, Path: BasePath + rt.Path, Scope: scope})
	}
	sort.Slice(fromTable, func(i, j int) bool {
		if fromTable[i].Path != fromTable[j].Path {
			return fromTable[i].Path < fromTable[j].Path
		}
		return fromTable[i].Method < fromTable[j].Method
	})
	fromSpec := spec.Operations()
	if len(fromSpec) != len(fromTable) {
		t.Fatalf("spec has %d operations, route table has %d:\nspec:  %v\ntable: %v", len(fromSpec), len(fromTable), fromSpec, fromTable)
	}
	for i := range fromSpec {
		if fromSpec[i] != fromTable[i] {
			t.Errorf("mismatch: spec %+v, table %+v", fromSpec[i], fromTable[i])
		}
	}
}

// Walk the router actually built and confirm nothing is registered outside
// the table, and every non-public route enforces authentication.
func TestEveryRegisteredRouteIsDeclared(t *testing.T) {
	d := deps(t, nil)
	declared := map[string]Route{}
	for _, rt := range Routes(d) {
		declared[rt.Method+" "+BasePath+rt.Path] = rt
	}
	h := router(t, d, Options{Listener: ListenerTCP})
	walked := 0
	err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked++
		rt, ok := declared[method+" "+route]
		if !ok {
			t.Errorf("route %s %s is registered but not declared", method, route)
			return nil
		}
		r := do(t, h, method, route, nil)
		if !rt.Public && r.status != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d without a key", method, route, r.status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if walked != len(declared) {
		t.Errorf("walked %d routes, declared %d", walked, len(declared))
	}
}

func TestInvalidRouteDeclarations(t *testing.T) {
	ok := func(http.ResponseWriter, *http.Request) {}
	for name, rt := range map[string]Route{
		"no scope":      {Method: "GET", Path: "/x", Handler: ok},
		"public+scope":  {Method: "GET", Path: "/x", Public: true, Scope: auth.ScopeAdmin, Handler: ok},
		"unknown scope": {Method: "GET", Path: "/x", Scope: "x:write", Handler: ok},
		"no handler":    {Method: "GET", Path: "/x", Public: true},
	} {
		if _, err := newRouter(deps(t, nil), Options{Listener: ListenerTCP}, []Route{rt}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSocketOnlyRoutesAbsentOnTCP(t *testing.T) {
	routes := []Route{{Method: "GET", Path: "/keys", Scope: auth.ScopeAdmin, SocketOnly: true,
		Handler: func(w http.ResponseWriter, r *http.Request) { httpx.WriteJSON(w, 200, map[string]string{}) }}}
	d := deps(t, nil)
	tcp, err := newRouter(d, Options{Listener: ListenerTCP}, routes)
	if err != nil {
		t.Fatal(err)
	}
	sock, err := newRouter(d, Options{Listener: ListenerSocket}, routes)
	if err != nil {
		t.Fatal(err)
	}
	if r := do(t, tcp, "GET", "/api/v1/keys", bearer(systemKey)); r.status != http.StatusNotFound {
		t.Errorf("tcp: %d", r.status)
	}
	if r := do(t, sock, "GET", "/api/v1/keys", nil); r.status != http.StatusOK {
		t.Errorf("socket: %d", r.status)
	}
}

func TestNoCORS(t *testing.T) {
	h := router(t, deps(t, nil), Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		r := do(t, h, method, "/api/v1/livez", func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set("Access-Control-Request-Method", "GET")
		})
		for k := range r.header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s present", method, k)
			}
		}
	}
}

func TestNotFoundAndMethodEnvelope(t *testing.T) {
	h := router(t, deps(t, nil), Options{Listener: ListenerTCP})
	for target, want := range map[string]int{"/api/v1/nope": 404, "/": 404} {
		r := do(t, h, http.MethodGet, target, nil)
		if r.status != want || !bytes.Contains(r.body, []byte(`"request_id"`)) {
			t.Errorf("%s: %d %s", target, r.status, r.body)
		}
	}
	if r := do(t, h, http.MethodPost, "/api/v1/livez", nil); r.status != 405 {
		t.Errorf("POST livez: %d", r.status)
	}
}

func TestIncomingRequestIDIgnored(t *testing.T) {
	h := router(t, deps(t, nil), Options{Listener: ListenerTCP})
	r := do(t, h, http.MethodGet, "/api/v1/livez", func(r *http.Request) { r.Header.Set("X-Request-ID", "evil\nline") })
	if got := r.header.Get(apiv1.RequestIDHeader); got == "evil\nline" || !requestIDPattern.MatchString(got) {
		t.Errorf("request id %q", got)
	}
}

func TestBodyLimit(t *testing.T) {
	h := router(t, deps(t, nil), Options{Listener: ListenerTCP})
	r := do(t, h, http.MethodGet, "/api/v1/livez", func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 70<<10)))
		r.ContentLength = 70 << 10
	})
	if r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", r.status)
	}
}

// The access log must never contain credentials, even with a plain handler
// that has no redaction.
func TestAccessLogHasNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	d := deps(t, &buf)
	tcp := router(t, d, Options{Listener: ListenerTCP})
	sock := router(t, d, Options{Listener: ListenerSocket})
	do(t, tcp, http.MethodGet, "/api/v1/status?token=secretquery", bearer(systemKey))
	do(t, tcp, http.MethodGet, "/api/v1/status", bearer("ckp_unknownkeyx_"+strings.Repeat("C", 43)))
	do(t, sock, http.MethodGet, "/api/v1/status", nil)

	out := buf.String()
	for _, leak := range []string{systemKey, "CCCCCCCC", "secretquery", "Bearer", "Authorization"} {
		if strings.Contains(out, leak) {
			t.Errorf("access log leaked %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"principal=key:systemreader", "route=/api/v1/status", "status=200", "status=401", "principal=local-admin", "listener=socket"} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q:\n%s", want, out)
		}
	}
}
