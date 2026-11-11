package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

var testKey = "ckp" + "_clienttest01_" + strings.Repeat("Q", 43)

// mockAgent validates every request against the spec and answers with
// spec-conforming responses.
func mockAgent(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	spec := contracttest.Load(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec.CheckRequest(t, r)
		w.Header().Set(apiv1.RequestIDHeader, "req_01JCQ7Y8K2M4T6V8X0Z2B4D6F8")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestStatusAndLivez(t *testing.T) {
	var gotAuth string
	srv := mockAgent(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/v1/livez":
			writeJSON(w, 200, apiv1.Livez{Status: "ok"})
		case "/api/v1/status":
			writeJSON(w, 200, apiv1.Status{
				Meta:  apiv1.Meta{SchemaVersion: "1", MachineID: "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10", GeneratedAt: time.Now().UTC()},
				Agent: apiv1.Agent{Version: "dev", APIVersion: "v1", StartedAt: time.Now().UTC()},
			})
		}
	})
	c, err := NewTCP(srv.URL, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Livez(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Agent.APIVersion != "v1" || gotAuth != "Bearer "+testKey {
		t.Errorf("status %+v auth %q", st, gotAuth)
	}
}

func TestAPIErrorDecoded(t *testing.T) {
	srv := mockAgent(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 403, apiv1.ErrorEnvelope{Error: apiv1.ErrorBody{Code: apiv1.CodeInsufficientScope, Message: "nope", RequestID: "req_x"}})
	})
	c, _ := NewTCP(srv.URL, testKey)
	_, err := c.Status(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || apiErr.Code != apiv1.CodeInsufficientScope || apiErr.RequestID != "req_x" {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("error message contains the key")
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var leaked bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := NewTCP(srv.URL, testKey)
	if _, err := c.Status(context.Background()); err == nil {
		t.Error("redirect treated as success")
	}
	if leaked {
		t.Error("API key forwarded on redirect")
	}
}

func TestNewTCPValidation(t *testing.T) {
	bad := map[string]string{
		"remote plain http": "http://192.168.1.5:7878",
		"hostname http":     "http://agent.example:7878",
		"bad scheme":        "ftp://127.0.0.1:7878",
		"with path":         "http://127.0.0.1:7878/api",
		"with credentials":  "http://u:p@127.0.0.1:7878",
		"no host":           "http://",
	}
	for name, u := range bad {
		if _, err := NewTCP(u, testKey); err == nil {
			t.Errorf("%s: %s accepted", name, u)
		}
	}
	for _, u := range []string{"http://127.0.0.1:7878", "http://localhost:7878", "http://[::1]:7878", "https://agent.example:7878"} {
		if _, err := NewTCP(u, testKey); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
	_, err := NewTCP("http://127.0.0.1:7878", "not-a-key with spaces")
	if err == nil || strings.Contains(err.Error(), "spaces") {
		t.Errorf("malformed key: %v", err)
	}
}

func TestNoKeyMeansNoHeader(t *testing.T) {
	srv := mockAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization sent without a key")
		}
		writeJSON(w, 200, apiv1.Livez{Status: "ok"})
	})
	c, _ := NewTCP(srv.URL, "")
	if err := c.Livez(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedResponseRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"` + strings.Repeat("x", maxResponseSize) + `"}`))
	}))
	defer srv.Close()
	c, _ := NewTCP(srv.URL, "")
	if err := c.Livez(context.Background()); err == nil {
		t.Error("oversized response accepted")
	}
}

// Every SDK method must send a request the spec accepts.
func TestEveryMethodSendsSpecValidRequests(t *testing.T) {
	var paths []string
	srv := mockAgent(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "PUT", r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/settings"), strings.HasSuffix(r.URL.Path, "/settings"):
			writeJSON(w, 200, map[string]any{"health": map[string]any{}, "source": "override"})
		case r.Method == "POST":
			writeJSON(w, 201, map[string]any{"key": "x", "api_key": map[string]any{"id": "aaaaaaaaaaaa"}})
		case strings.HasSuffix(r.URL.Path, "/keys"):
			writeJSON(w, 200, map[string]any{"items": []any{}})
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})
	c, _ := NewTCP(srv.URL, testKey)
	ctx := context.Background()
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := []func() error{
		func() error { _, err := c.System(ctx); return err },
		func() error { _, err := c.Hardware(ctx); return err },
		func() error { _, err := c.Metrics(ctx); return err },
		func() error { _, err := c.Metrics(ctx, "cpu", "memory"); return err },
		func() error { _, err := c.Storage(ctx); return err },
		func() error { _, err := c.Network(ctx); return err },
		func() error { _, err := c.Sensors(ctx); return err },
		func() error { _, err := c.Health(ctx); return err },
		func() error {
			_, err := c.Events(ctx, EventsQuery{Limit: 10, Cursor: "5", Severity: "warning", Since: exp})
			return err
		},
		func() error {
			_, err := c.Processes(ctx, ProcessesQuery{Sort: "memory", Order: "asc", Search: "ng", Limit: 5, KernelThreads: true})
			return err
		},
		func() error { _, err := c.Services(ctx, "failed"); return err },
		func() error {
			_, err := c.History(ctx, HistoryQuery{Series: []string{"cpu.usage_percent"}, From: exp.Add(-time.Hour), To: exp, Resolution: "1m"})
			return err
		},
		func() error { _, err := c.HistorySeries(ctx); return err },
		func() error { _, err := c.Settings(ctx); return err },
		func() error {
			_, err := c.UpdateSettings(ctx, apiv1.HealthSettings{Confirm: 2, CPUTemperature: apiv1.WarnCritical{Warn: 80, Critical: 90},
				MemAvailable: apiv1.WarnCritical{Warn: 10, Critical: 5}, MemPressure: apiv1.WarnCritical{Warn: 5, Critical: 20},
				Storage: apiv1.WarnCritical{Warn: 85, Critical: 95}, NetworkErrors: apiv1.WarnCritical{Warn: 10, Critical: 100},
				Disk: apiv1.DiskEnduranceThreshold{WarnPercentageUsed: 90}})
			return err
		},
		func() error { _, err := c.ResetSettings(ctx); return err },
		func() error { _, err := c.Report(ctx, true); return err },
		func() error { _, err := c.ReportMarkdown(ctx, false); return err },
		func() error { _, err := c.ListKeys(ctx); return err },
		func() error {
			_, err := c.CreateKey(ctx, apiv1.CreateAPIKeyRequest{Name: "n", Scopes: []string{"metrics:read"}, ExpiresAt: &exp})
			return err
		},
		func() error { _, err := c.RotateKey(ctx, "aaaaaaaaaaaa"); return err },
		func() error { _, err := c.RevokeKey(ctx, "aaaaaaaaaaaa"); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if len(paths) != len(calls) {
		t.Errorf("requests %v", paths)
	}
	if paths[3] != "GET /api/v1/metrics?sections=cpu%2Cmemory" || !strings.Contains(paths[9], "kernel_threads=true") {
		t.Errorf("metrics query %q", paths[3])
	}
}

func TestReportMarkdownIsRaw(t *testing.T) {
	var accept string
	srv := mockAgent(t, func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte("# Cockpit diagnostic report\n"))
	})
	c, _ := NewTCP(srv.URL, testKey)
	md, err := c.ReportMarkdown(context.Background(), true)
	if err != nil || string(md) != "# Cockpit diagnostic report\n" {
		t.Fatalf("%q %v", md, err)
	}
	if !strings.HasPrefix(accept, "text/markdown") {
		t.Errorf("accept %q", accept)
	}
}

func TestRetryAfterParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		writeJSON(w, 429, apiv1.ErrorEnvelope{Error: apiv1.ErrorBody{Code: apiv1.CodeRateLimited, Message: "slow down", RequestID: "r"}})
	}))
	defer srv.Close()
	c, _ := NewTCP(srv.URL, "")
	_, err := c.Health(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 7*time.Second || apiErr.Code != apiv1.CodeRateLimited {
		t.Fatalf("got %v", err)
	}
}

func TestStreamEventsParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("severity") != "warning" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("request %s %v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "retry: 5000\n: connected\n\n"+
			"id: evt_1\nevent: event\ndata: {\"id\":\"evt_1\",\"type\":\"finding.raised\",\"severity\":\"warning\",\"subject\":\"s\",\"message\":\"m\"}\n\n"+
			": heartbeat\n\n"+
			"event: lagged\ndata: {\"dropped\":7}\n\n"+
			"event: future-type\ndata: {}\n\n"+
			"event: closed\ndata: {\"reason\":\"credentials_revoked\"}\n\n")
	}))
	defer srv.Close()
	c, _ := NewTCP(srv.URL, testKey)
	var got []string
	err := c.StreamEvents(context.Background(), "warning", func(m StreamMessage) error {
		if m.Event != nil {
			got = append(got, "event:"+m.Event.Message)
		} else {
			got = append(got, fmt.Sprintf("lagged:%d", m.Lagged))
		}
		return nil
	})
	if !errors.Is(err, ErrStreamClosed) || !strings.Contains(err.Error(), "credentials_revoked") {
		t.Errorf("err %v", err)
	}
	if strings.Join(got, ",") != "event:m,lagged:7" {
		t.Errorf("got %v", got)
	}
}

func TestStreamEventsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 429, apiv1.ErrorEnvelope{Error: apiv1.ErrorBody{Code: apiv1.CodeRateLimited, Message: "too many", RequestID: "r"}})
	}))
	defer srv.Close()
	c, _ := NewTCP(srv.URL, "")
	err := c.StreamEvents(context.Background(), "", func(StreamMessage) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Errorf("got %v", err)
	}
}
