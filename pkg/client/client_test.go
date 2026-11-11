package client

import (
	"context"
	"encoding/json"
	"errors"
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
