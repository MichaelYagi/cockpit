package contracttest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recorder captures failures instead of failing the real test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

const goodID = "req_01JCQ7Y8K2M4T6V8X0Z2B4D6F8"

func header(id string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if id != "" {
		h.Set("X-Request-ID", id)
	}
	return h
}

// The validator must catch each kind of drift, or the contract tests prove nothing.
func TestValidatorCatchesViolations(t *testing.T) {
	spec := Load(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/livez", nil)

	cases := []struct {
		name   string
		status int
		h      http.Header
		body   string
		bad    bool
	}{
		{"valid", 200, header(goodID), `{"status":"ok"}`, false},
		{"extra field", 200, header(goodID), `{"status":"ok","debug":true}`, true},
		{"wrong value", 200, header(goodID), `{"status":"fine"}`, true},
		{"undocumented status", 418, header(goodID), `{"status":"ok"}`, true},
		{"missing request id", 200, header(""), `{"status":"ok"}`, true},
		{"malformed request id", 200, header("abc"), `{"status":"ok"}`, true},
		{"bad error code", 400, header(goodID), `{"error":{"code":"oops","message":"m","request_id":"r"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{TB: t}
			spec.CheckResponse(rec, req, tc.status, tc.h, []byte(tc.body))
			if got := len(rec.errors) > 0; got != tc.bad {
				t.Errorf("violation detected = %v, want %v (%v)", got, tc.bad, rec.errors)
			}
		})
	}
}

func TestUnknownPathFails(t *testing.T) {
	rec := &recorder{TB: t}
	Load(t).CheckResponse(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil), 200, header(goodID), []byte(`{}`))
	if len(rec.errors) == 0 {
		t.Error("unknown path not reported")
	}
}

func TestEveryOperationDeclaresScope(t *testing.T) {
	for _, op := range Load(t).Operations() {
		if op.Scope == "" {
			t.Errorf("%s %s has no x-cockpit-scope", op.Method, op.Path)
		}
	}
}
