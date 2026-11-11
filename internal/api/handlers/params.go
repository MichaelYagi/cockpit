package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// allowParams rejects unknown or repeated query parameters with 400. It
// returns false if it wrote a response.
func allowParams(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	q := r.URL.Query()
	var unknown []string
	for k, vs := range q {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
			}
		}
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		if len(vs) > 1 {
			badParam(w, r, k, "must be given at most once")
			return false
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		for i, u := range unknown {
			unknown[i] = quote(u)
		}
		msg := "Unknown query parameter " + strings.Join(unknown, ", ") + "."
		if len(allowed) > 0 {
			msg += " Allowed: " + strings.Join(allowed, ", ") + "."
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidParameter, msg)
		return false
	}
	return true
}

func badParam(w http.ResponseWriter, r *http.Request, name, msg string) {
	httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidParameter, "Parameter "+name+": "+msg+".")
}

// quote formats user input for an error message, bounded in length and
// with control characters escaped.
func quote(s string) string {
	if len(s) > 64 {
		s = s[:64] + "…"
	}
	return strconv.Quote(s)
}

// intParam parses an optional bounded integer.
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		badParam(w, r, name, "must be an integer from "+strconv.Itoa(lo)+" to "+strconv.Itoa(hi))
		return 0, false
	}
	return n, true
}

// timeParam parses an optional RFC 3339 timestamp.
func timeParam(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		badParam(w, r, name, "must be an RFC 3339 timestamp, e.g. 2026-11-11T15:30:00Z")
		return time.Time{}, false
	}
	return t.UTC(), true
}
