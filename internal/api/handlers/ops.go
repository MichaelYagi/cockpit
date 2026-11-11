package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/events"
	"github.com/MichaelYagi/cockpit/internal/health"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// ---- /health ----

// HealthSource provides the latest health report.
type HealthSource interface {
	Current() *health.Report
}

// Health serves GET /health.
func Health(src HealthSource, machineID core.MachineID, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowParams(w, r) {
			return
		}
		httpx.WriteJSON(w, http.StatusOK, healthResponse(src.Current(), machineID, now().UTC()))
	}
}

func healthResponse(rep *health.Report, machineID core.MachineID, now time.Time) apiv1.HealthResponse {
	out := apiv1.HealthResponse{
		Meta:        apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(machineID), GeneratedAt: now},
		Status:      string(rep.Status),
		EvaluatedAt: rep.EvaluatedAt.UTC(),
		Checks:      make([]apiv1.HealthCheck, 0, len(rep.Checks)),
	}
	for _, c := range rep.Checks {
		hc := apiv1.HealthCheck{ID: c.ID, Name: c.Name, Status: string(c.Status), Availability: string(c.Availability),
			Reason: c.Reason, Summary: c.Summary, Findings: make([]apiv1.Finding, 0, len(c.Findings))}
		for _, f := range c.Findings {
			ev := f.Evidence
			if ev == nil {
				ev = map[string]string{}
			}
			hc.Findings = append(hc.Findings, apiv1.Finding{ID: f.ID, Check: f.CheckID, Severity: string(f.Severity),
				Subject: f.Subject, Summary: f.Summary, Evidence: ev, Recommendation: f.Recommendation, Since: f.Since.UTC()})
		}
		out.Checks = append(out.Checks, hc)
	}
	return out
}

// ---- /events ----

// EventSource lists recent events.
type EventSource interface {
	List(f events.Filter, cursor string, limit int) ([]core.Event, string, error)
}

// Events serves GET /events.
func Events(src EventSource, machineID core.MachineID, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowParams(w, r, "limit", "cursor", "severity", "since", "until", "type") {
			return
		}
		limit, ok := intParam(w, r, "limit", 50, 1, 500)
		if !ok {
			return
		}
		since, ok := timeParam(w, r, "since")
		if !ok {
			return
		}
		until, ok := timeParam(w, r, "until")
		if !ok {
			return
		}
		f := events.Filter{Since: since, Until: until}
		switch typ := core.EventType(r.URL.Query().Get("type")); typ {
		case "", core.EventFindingRaised, core.EventFindingResolved, core.EventCollectorDegraded, core.EventCollectorRecovered,
			core.EventKeyCreated, core.EventKeyRotated, core.EventKeyRevoked, core.EventSettingsChanged:
			f.Type = typ
		default:
			badParam(w, r, "type", "is not a known event type")
			return
		}
		switch sev := r.URL.Query().Get("severity"); sev {
		case "":
		case "info", "warning", "critical":
			f.MinSeverity = core.Severity(sev)
		default:
			badParam(w, r, "severity", "must be info, warning or critical")
			return
		}
		cursor := r.URL.Query().Get("cursor")
		if len(cursor) > 64 {
			httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidCursor, "The cursor is invalid or has expired. Start again from the first page.")
			return
		}
		evs, next, err := src.List(f, cursor, limit)
		if errors.Is(err, events.ErrInvalidCursor) {
			httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidCursor, "The cursor is invalid or has expired. Start again from the first page.")
			return
		}
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, eventsResponse(evs, next, machineID, now().UTC()))
	}
}

func eventsResponse(evs []core.Event, next string, machineID core.MachineID, now time.Time) apiv1.EventsResponse {
	out := apiv1.EventsResponse{
		Meta:  apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(machineID), GeneratedAt: now},
		Items: make([]apiv1.Event, 0, len(evs)),
	}
	for _, e := range evs {
		out.Items = append(out.Items, apiv1.Event{ID: e.ID, Time: e.Time.UTC(), Type: string(e.Type),
			Severity: string(e.Severity), Subject: e.Subject, Message: e.Message})
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out
}

// ---- /keys (socket only) ----

// KeyService manages API keys.
type KeyService interface {
	Create(ctx context.Context, req auth.CreateRequest) (auth.Created, error)
	List(ctx context.Context) ([]auth.KeyMeta, error)
	Rotate(ctx context.Context, id string) (auth.Created, error)
	Revoke(ctx context.Context, id string) (auth.KeyMeta, error)
}

// Keys serves the key management routes.
type Keys struct {
	Service KeyService
	Now     func() time.Time
	Logger  *slog.Logger
}

var keyIDPattern = regexp.MustCompile(`^[a-z2-7]{12}$`)

func (h *Keys) toAPI(m auth.KeyMeta) apiv1.APIKey {
	k := apiv1.APIKey{ID: m.ID, Name: m.Name, Display: auth.DisplayKey(m.ID), Scopes: make([]string, len(m.Scopes)),
		Active: m.Active(h.Now()), CreatedAt: m.CreatedAt.UTC(), ExpiresAt: m.ExpiresAt, LastUsedAt: m.LastUsedAt, RevokedAt: m.RevokedAt}
	for i, s := range m.Scopes {
		k.Scopes[i] = string(s)
	}
	if m.RotatedFrom != "" {
		rf := m.RotatedFrom
		k.RotatedFrom = &rf
	}
	return k
}

func (h *Keys) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ire *auth.InvalidRequestError
	switch {
	case errors.As(err, &ire):
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, ire.Msg+".")
	case errors.Is(err, auth.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, apiv1.CodeNotFound, "No API key has that ID.")
	default:
		h.Logger.Error("key operation failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
	}
}

// List serves GET /keys.
func (h *Keys) List(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	keys, err := h.Service.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := apiv1.APIKeyList{Items: make([]apiv1.APIKey, 0, len(keys))}
	for _, k := range keys {
		out.Items = append(out.Items, h.toAPI(k))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Create serves POST /keys.
func (h *Keys) Create(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must be JSON (Content-Type: application/json).")
		return
	}
	var req apiv1.CreateAPIKeyRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, apiv1.CodeRequestTooLarge, "The request body is too large.")
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body is not a valid key request.")
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must contain a single JSON object.")
		return
	}
	cr := auth.CreateRequest{Name: req.Name, ExpiresAt: req.ExpiresAt}
	for _, s := range req.Scopes {
		cr.Scopes = append(cr.Scopes, auth.Scope(s))
	}
	created, err := h.Service.Create(r.Context(), cr)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, apiv1.CreatedAPIKey{Key: created.Key, APIKey: h.toAPI(created.Meta)})
}

func (h *Keys) id(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !keyIDPattern.MatchString(id) {
		httpx.WriteError(w, r, http.StatusNotFound, apiv1.CodeNotFound, "No API key has that ID.")
		return "", false
	}
	return id, true
}

// Rotate serves POST /keys/{id}/rotate.
func (h *Keys) Rotate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.id(w, r)
	if !ok || !allowParams(w, r) {
		return
	}
	created, err := h.Service.Rotate(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, apiv1.CreatedAPIKey{Key: created.Key, APIKey: h.toAPI(created.Meta)})
}

// Revoke serves DELETE /keys/{id}.
func (h *Keys) Revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := h.id(w, r)
	if !ok || !allowParams(w, r) {
		return
	}
	m, err := h.Service.Revoke(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, apiv1.APIKeyResponse{APIKey: h.toAPI(m)})
}
