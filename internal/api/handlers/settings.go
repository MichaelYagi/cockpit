package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/settings"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// SettingsService manages runtime settings.
type SettingsService interface {
	Get() settings.Effective
	Put(ctx context.Context, h config.Health) (settings.Effective, error)
	Reset(ctx context.Context) (settings.Effective, error)
}

// Settings serves /settings.
type Settings struct {
	Service SettingsService
	Logger  *slog.Logger
}

func toAPISettings(e settings.Effective) apiv1.SettingsResponse {
	h := e.Health
	out := apiv1.SettingsResponse{Source: e.Source, Health: apiv1.HealthSettings{
		Confirm:        h.Confirm,
		CPUTemperature: apiv1.WarnCritical{Warn: h.CPUTemperature.WarnCelsius, Critical: h.CPUTemperature.CriticalCelsius},
		MemAvailable:   apiv1.WarnCritical{Warn: h.Memory.WarnAvailablePercent, Critical: h.Memory.CriticalAvailablePercent},
		MemPressure:    apiv1.WarnCritical{Warn: h.Memory.WarnPressurePercent, Critical: h.Memory.CriticalPressurePercent},
		Storage:        apiv1.WarnCritical{Warn: h.Storage.WarnPercent, Critical: h.Storage.CriticalPercent},
		NetworkErrors:  apiv1.WarnCritical{Warn: h.Network.WarnErrorsPerMinute, Critical: h.Network.CriticalErrorsPerMinute},
		Disk:           apiv1.DiskEnduranceThreshold{WarnPercentageUsed: h.Disk.WarnPercentageUsed},
	}}
	if !e.UpdatedAt.IsZero() {
		t := e.UpdatedAt.UTC()
		out.UpdatedAt = &t
	}
	return out
}

func fromAPISettings(a apiv1.HealthSettings) config.Health {
	return config.Health{
		Confirm:        a.Confirm,
		CPUTemperature: config.CPUTemperatureHealth{WarnCelsius: a.CPUTemperature.Warn, CriticalCelsius: a.CPUTemperature.Critical},
		Memory: config.MemoryHealth{WarnAvailablePercent: a.MemAvailable.Warn, CriticalAvailablePercent: a.MemAvailable.Critical,
			WarnPressurePercent: a.MemPressure.Warn, CriticalPressurePercent: a.MemPressure.Critical},
		Storage: config.StorageHealth{WarnPercent: a.Storage.Warn, CriticalPercent: a.Storage.Critical},
		Disk:    config.DiskHealth{WarnPercentageUsed: a.Disk.WarnPercentageUsed},
		Network: config.NetworkHealth{WarnErrorsPerMinute: a.NetworkErrors.Warn, CriticalErrorsPerMinute: a.NetworkErrors.Critical},
	}
}

// Get serves GET /settings.
func (h *Settings) Get(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAPISettings(h.Service.Get()))
}

// Put serves PUT /settings.
func (h *Settings) Put(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must be JSON (Content-Type: application/json).")
		return
	}
	var req apiv1.UpdateSettingsRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, apiv1.CodeRequestTooLarge, "The request body is too large.")
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body is not a valid settings object.")
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "The request body must contain a single JSON object.")
		return
	}
	eff, err := h.Service.Put(r.Context(), fromAPISettings(req.Health))
	var ve *config.ValidationError
	switch {
	case errors.As(err, &ve):
		msgs := make([]string, len(ve.Errors))
		for i, fe := range ve.Errors {
			msgs[i] = fe.Field + ": " + fe.Message
		}
		httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidRequest, "Invalid settings: "+strings.Join(msgs, "; ")+".")
	case err != nil:
		h.Logger.Error("saving settings failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
	default:
		httpx.WriteJSON(w, http.StatusOK, toAPISettings(eff))
	}
}

// Delete serves DELETE /settings (reset to the configuration file).
func (h *Settings) Delete(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	eff, err := h.Service.Reset(r.Context())
	if err != nil {
		h.Logger.Error("resetting settings failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAPISettings(eff))
}
