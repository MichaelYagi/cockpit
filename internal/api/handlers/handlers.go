// Package handlers contains thin HTTP handlers: they read from services,
// map domain types to pkg/apiv1 wire types, and encode the response.
package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/buildinfo"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// StatusSource provides agent status. Implemented by the app.
type StatusSource interface {
	MachineID() core.MachineID
	StartedAt() time.Time
	Collectors() []core.CollectorStatus
}

// Livez reports that the process is serving requests. It reveals nothing else.
func Livez(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, apiv1.Livez{Status: "ok"})
}

// OpenAPI serves the embedded specification as JSON.
func OpenAPI(specJSON []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(specJSON)
	}
}

// AdminStatus provides the admin-only part of /status.
type AdminStatus func() *apiv1.APIServerStatus

// Status reports the agent version, uptime and collector capabilities.
// Admin principals (the local socket) also get listener and request data.
func Status(src StatusSource, admin AdminStatus, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowParams(w, r) {
			return
		}
		p, ok := auth.PrincipalFrom(r.Context())
		httpx.WriteJSON(w, http.StatusOK, statusResponse(src, admin, ok && p.Has(auth.ScopeAdmin), now().UTC()))
	}
}

// statusResponse builds /status; withAdmin adds listener and request data.
func statusResponse(src StatusSource, admin AdminStatus, withAdmin bool, t time.Time) apiv1.Status {
	started := src.StartedAt().UTC()
	out := apiv1.Status{
		Meta: apiv1.Meta{
			SchemaVersion: apiv1.SchemaVersion,
			MachineID:     string(src.MachineID()),
			GeneratedAt:   t,
		},
		Agent: apiv1.Agent{
			Version:       buildinfo.Version,
			APIVersion:    buildinfo.APIVersion,
			StartedAt:     started,
			UptimeSeconds: t.Sub(started).Seconds(),
		},
		Collectors: mapCollectors(src.Collectors()),
	}
	if withAdmin && admin != nil {
		out.API = admin()
	}
	return out
}

func mapCollectors(in []core.CollectorStatus) []apiv1.Collector {
	out := make([]apiv1.Collector, 0, len(in))
	for _, c := range in {
		caps := make([]apiv1.Capability, 0, len(c.Capabilities))
		for name, s := range c.Capabilities {
			caps = append(caps, apiv1.Capability{Name: string(name), Status: string(s.Availability), Reason: s.Reason})
		}
		sort.Slice(caps, func(i, j int) bool { return caps[i].Name < caps[j].Name })
		out = append(out, apiv1.Collector{ID: c.ID, Version: c.Version, Capabilities: caps, External: c.External, SDKVersion: c.SDKVersion})
	}
	return out
}
