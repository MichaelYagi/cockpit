package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/history"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// HistorySource queries stored history.
type HistorySource interface {
	Query(ctx context.Context, series []string, tier history.Tier, from, to time.Time) (map[string][]history.Point, error)
	ListSeries(ctx context.Context) ([]history.SeriesInfo, error)
}

// History serves the history routes. Source is nil when history is
// disabled.
type History struct {
	Source    HistorySource
	MachineID core.MachineID
	Now       func() time.Time
	Logger    *slog.Logger
}

const maxHistorySeries = 10

func validSeriesName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ',' {
			return false
		}
	}
	return true
}

func (h *History) meta() apiv1.Meta {
	return apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(h.MachineID), GeneratedAt: h.Now().UTC()}
}

const historyDisabled = "Metric history is disabled in the configuration (history.enabled)."

// Query serves GET /metrics/history.
func (h *History) Query(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r, "series", "from", "to", "resolution") {
		return
	}
	raw := r.URL.Query().Get("series")
	if raw == "" {
		badParam(w, r, "series", "is required (a comma-separated list; see /metrics/history/series)")
		return
	}
	names := strings.Split(raw, ",")
	if len(names) > maxHistorySeries {
		badParam(w, r, "series", "must name at most 10 series")
		return
	}
	for _, n := range names {
		if !validSeriesName(n) {
			badParam(w, r, "series", "contains an invalid series name")
			return
		}
	}
	now := h.Now().UTC().Truncate(time.Second)
	to, ok := timeParam(w, r, "to")
	if !ok {
		return
	}
	if to.IsZero() {
		to = now
	}
	from, ok := timeParam(w, r, "from")
	if !ok {
		return
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if !from.Before(to) {
		badParam(w, r, "from", "must be before to")
		return
	}
	if to.Sub(from) > history.MaxRange {
		badParam(w, r, "from", "the range must be at most 90 days")
		return
	}
	res := r.URL.Query().Get("resolution")
	var tier history.Tier
	switch res {
	case "", "auto":
		tier = history.ChooseTier(from, to, history.MaxPoints)
	case "raw", "1m", "1h":
		tier = history.Tier(res)
		if int(to.Sub(from)/tier.Step()) > history.MaxPoints {
			badParam(w, r, "resolution", "would return more than 2000 points per series; use a coarser resolution or a shorter range")
			return
		}
	default:
		badParam(w, r, "resolution", "must be auto, raw, 1m or 1h")
		return
	}
	out := apiv1.HistoryResponse{Meta: h.meta(), Status: string(core.Available), From: from, To: to,
		Resolution: string(tier), StepSeconds: int(tier.Step().Seconds()), Series: []apiv1.HistorySeries{}}
	if h.Source == nil {
		out.Status, out.Reason = string(core.NotConfigured), historyDisabled
		for _, n := range names {
			out.Series = append(out.Series, apiv1.HistorySeries{Name: n, Unit: history.Unit(n), Points: []apiv1.HistoryPoint{}})
		}
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	data, err := h.Source.Query(r.Context(), names, tier, from, to)
	if err != nil {
		h.Logger.Error("history query failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
		return
	}
	for _, n := range names {
		s := apiv1.HistorySeries{Name: n, Unit: history.Unit(n), Points: make([]apiv1.HistoryPoint, 0, len(data[n]))}
		for _, p := range data[n] {
			s.Points = append(s.Points, apiv1.HistoryPoint{Time: p.Time, Min: p.Min, Avg: p.Avg, Max: p.Max})
		}
		out.Series = append(out.Series, s)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Series serves GET /metrics/history/series.
func (h *History) Series(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	out := apiv1.HistorySeriesList{Meta: h.meta(), Status: string(core.Available), Items: []apiv1.HistorySeriesInfo{}}
	if h.Source == nil {
		out.Status, out.Reason = string(core.NotConfigured), historyDisabled
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	list, err := h.Source.ListSeries(r.Context())
	if err != nil {
		h.Logger.Error("history series failed", "request_id", httpx.RequestID(r.Context()), "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
		return
	}
	for _, s := range list {
		out.Items = append(out.Items, apiv1.HistorySeriesInfo{Name: s.Name, Unit: history.Unit(s.Name), First: s.First, Last: s.Last})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
