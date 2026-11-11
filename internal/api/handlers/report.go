package handlers

import (
	"log/slog"
	"net/http"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/events"
	"github.com/MichaelYagi/cockpit/internal/report"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// reportEvents is how many recent events a report includes.
const reportEvents = 100

// Report serves GET /report (D21). The route requires system:read; the
// sections built from metrics need metrics:read as well and the
// thresholds need admin (the local socket). Sections the caller cannot
// read are listed as omitted instead of failing the request.
type Report struct {
	Resources *Resources
	Status    StatusSource
	Admin     AdminStatus
	Health    HealthSource // nil: omitted
	Events    EventSource  // nil: omitted
	Settings  SettingsService
	Logger    *slog.Logger
}

// Get serves GET /report.
func (h *Report) Get(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r, "format", "anonymize") {
		return
	}
	format := r.URL.Query().Get("format")
	switch format {
	case "":
		format = "json"
	case "json", "markdown":
	default:
		badParam(w, r, "format", "must be json or markdown")
		return
	}
	anonymize := false
	switch r.URL.Query().Get("anonymize") {
	case "", "false":
	case "true":
		anonymize = true
	default:
		badParam(w, r, "anonymize", "must be true or false")
		return
	}

	p, _ := auth.PrincipalFrom(r.Context())
	rep := h.build(p)
	if anonymize {
		anon, err := report.Anonymize(rep)
		if err != nil {
			h.Logger.Error("anonymize report", "error", err)
			httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
			return
		}
		rep = anon
	}
	if format == "markdown" {
		hd := w.Header()
		hd.Set("Content-Type", "text/markdown; charset=utf-8")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(report.Markdown(rep))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

func (h *Report) build(p auth.Principal) *apiv1.Report {
	now := h.Resources.Now().UTC()
	s := h.Resources.Snapshots.Current()
	out := &apiv1.Report{
		Meta:    h.Resources.meta(now),
		Status:  statusResponse(h.Status, h.Admin, p.Has(auth.ScopeAdmin), now),
		Omitted: []apiv1.ReportOmission{},
	}
	omit := func(section, reason string) {
		out.Omitted = append(out.Omitted, apiv1.ReportOmission{Section: section, Reason: reason})
	}

	sys := h.Resources.system(s, now)
	hw := h.Resources.hardware(s, now)
	svc := h.Resources.services(s, now, "")
	out.System, out.Hardware, out.Services = &sys, &hw, &svc

	if p.Has(auth.ScopeMetricsRead) {
		// Filesystems and interfaces are in the storage and network parts.
		m := h.Resources.metrics(s, now, map[string]bool{"cpu": true, "memory": true, "memory_pressure": true, "sensors": true, "gpu": true, "custom": true})
		st := h.Resources.storage(s, now)
		nw := h.Resources.networkResponse(s, now)
		out.Metrics, out.Storage, out.Network = &m, &st, &nw
		if h.Health != nil {
			hr := healthResponse(h.Health.Current(), h.Resources.MachineID, now)
			out.Health = &hr
		} else {
			omit("health", "Health checks are not running.")
		}
		if h.Events != nil {
			evs, next, err := h.Events.List(events.Filter{}, "", reportEvents)
			if err != nil {
				h.Logger.Warn("report: list events", "error", err)
				omit("events", "Events could not be read.")
			} else {
				er := eventsResponse(evs, next, h.Resources.MachineID, now)
				out.Events = &er
			}
		} else {
			omit("events", "Events are not recorded.")
		}
	} else {
		for _, sec := range []string{"metrics", "storage", "network", "health", "events"} {
			omit(sec, "Requires the metrics:read scope.")
		}
	}

	switch {
	case !p.Has(auth.ScopeAdmin):
		omit("settings", "Health thresholds are included only over the local socket.")
	case h.Settings == nil:
		omit("settings", "Runtime settings are not available.")
	default:
		st := toAPISettings(h.Settings.Get())
		out.Settings = &st
	}
	return out
}
