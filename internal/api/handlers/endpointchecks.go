package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/settings"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// EndpointChecksService manages service checks at runtime (D41).
type EndpointChecksService interface {
	List() []settings.EndpointEntry
	Add(ctx context.Context, t config.EndpointTarget) (settings.EndpointEntry, error)
	Update(ctx context.Context, name string, t config.EndpointTarget) (settings.EndpointEntry, error)
	Remove(ctx context.Context, name string) error
}

// EndpointChecks serves /endpoint-checks (Unix socket only).
type EndpointChecks struct {
	Service   EndpointChecksService
	MachineID core.MachineID
	Now       func() time.Time
	Logger    *slog.Logger
}

func toAPICheck(e settings.EndpointEntry) apiv1.EndpointCheck {
	c := apiv1.EndpointCheck{Name: e.Target.Name, URL: e.Target.URL, ExpectStatus: append([]int{}, e.Target.ExpectStatus...),
		TLSSkipVerify: e.Target.TLSSkipVerify, Source: e.Source}
	if !e.AddedAt.IsZero() {
		t := e.AddedAt.UTC()
		c.AddedAt = &t
	}
	return c
}

// List serves GET /endpoint-checks.
func (h *EndpointChecks) List(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	out := apiv1.EndpointChecksResponse{
		Meta:  apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(h.MachineID), GeneratedAt: h.Now().UTC()},
		Items: []apiv1.EndpointCheck{}, Max: config.MaxEndpoints,
	}
	for _, e := range h.Service.List() {
		out.Items = append(out.Items, toAPICheck(e))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Add serves POST /endpoint-checks.
func (h *EndpointChecks) Add(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	e, err := h.Service.Add(r.Context(), config.EndpointTarget{Name: req.Name, URL: req.URL, ExpectStatus: req.ExpectStatus, TLSSkipVerify: req.TLSSkipVerify})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toAPICheck(e))
}

// Update serves PUT /endpoint-checks/{name}: replaces a runtime check.
func (h *EndpointChecks) Update(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	e, err := h.Service.Update(r.Context(), pathName(r), config.EndpointTarget{Name: req.Name, URL: req.URL, ExpectStatus: req.ExpectStatus, TLSSkipVerify: req.TLSSkipVerify})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAPICheck(e))
}

func (h *EndpointChecks) decode(w http.ResponseWriter, r *http.Request) (apiv1.AddEndpointCheckRequest, bool) {
	var req apiv1.AddEndpointCheckRequest
	if !allowParams(w, r) {
		return req, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must be JSON (Content-Type: application/json).")
		return req, false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, apiv1.CodeRequestTooLarge, "The request body is too large.")
			return req, false
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body is not a valid service check.")
		return req, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must contain a single JSON object.")
		return req, false
	}
	return req, true
}

func pathName(r *http.Request) string {
	name := chi.URLParam(r, "name")
	if n, err := url.PathUnescape(name); err == nil {
		name = n
	}
	return name
}

// Remove serves DELETE /endpoint-checks/{name}.
func (h *EndpointChecks) Remove(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	if err := h.Service.Remove(r.Context(), pathName(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EndpointChecks) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *config.ValidationError
	switch {
	case errors.As(err, &ve):
		msgs := make([]string, len(ve.Errors))
		for i, fe := range ve.Errors {
			// Fields are "endpoint.url" and similar; the message says enough.
			msgs[i] = strings.TrimPrefix(fe.Field, "endpoint.") + ": " + fe.Message
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "Invalid service check: "+strings.Join(msgs, "; ")+".")
	case errors.Is(err, settings.ErrEndpointExists), errors.Is(err, settings.ErrEndpointLimit), errors.Is(err, settings.ErrEndpointInFile):
		httpx.WriteError(w, r, http.StatusConflict, apiv1.CodeConflict, capitalize(err.Error())+".")
	case errors.Is(err, settings.ErrEndpointNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, apiv1.CodeNotFound, "No service check has that name.")
	default:
		h.Logger.Error("service check change failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
