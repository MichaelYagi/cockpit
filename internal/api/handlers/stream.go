package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/events"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Subscriber opens event subscriptions.
type Subscriber interface {
	Subscribe(f events.Filter) *events.Subscription
}

// KeyChecker reports whether a key can still authenticate.
type KeyChecker interface {
	IsActive(ctx context.Context, id string) (bool, error)
}

// Stream limits (D17).
const (
	MaxStreamsPerKey = 4
	MaxStreams       = 64
)

// StreamLimits counts open event streams. Safe for concurrent use.
type StreamLimits struct {
	mu    sync.Mutex
	total int
	byKey map[string]int
}

// NewStreamLimits returns empty counters.
func NewStreamLimits() *StreamLimits { return &StreamLimits{byKey: map[string]int{}} }

func (l *StreamLimits) acquire(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= MaxStreams || (id != "" && l.byKey[id] >= MaxStreamsPerKey) {
		return false
	}
	l.total++
	if id != "" {
		l.byKey[id]++
	}
	return true
}

func (l *StreamLimits) release(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if id != "" {
		if l.byKey[id]--; l.byKey[id] <= 0 {
			delete(l.byKey, id)
		}
	}
}

// Open returns the number of open streams.
func (l *StreamLimits) Open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// EventStream serves GET /events/stream as Server-Sent Events (D17).
type EventStream struct {
	Events    Subscriber
	Keys      KeyChecker // nil: keys are not re-checked
	Limits    *StreamLimits
	Heartbeat time.Duration
	Logger    *slog.Logger
}

func (h *EventStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r, "severity") {
		return
	}
	var f events.Filter
	switch sev := r.URL.Query().Get("severity"); sev {
	case "":
	case "info", "warning", "critical":
		f.MinSeverity = core.Severity(sev)
	default:
		badParam(w, r, "severity", "must be info, warning or critical")
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	keyID := ""
	if p.Kind == auth.KindAPIKey {
		keyID = p.KeyID
	}
	if !h.Limits.acquire(keyID) {
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, r, http.StatusTooManyRequests, apiv1.CodeRateLimited,
			fmt.Sprintf("Too many open event streams (at most %d per key and %d in total).", MaxStreamsPerKey, MaxStreams))
		return
	}
	defer h.Limits.release(keyID)

	rc := http.NewResponseController(w)
	// Streams live long: drop the per-request write deadline and set one
	// per write instead, so a stalled client is disconnected.
	_ = rc.SetWriteDeadline(time.Time{})
	sub := h.Events.Subscribe(f)
	defer sub.Close()

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(s string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 5000\n: connected\n\n") {
		return
	}
	beat := time.NewTicker(h.Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return // client gone or agent shutting down
		case <-beat.C:
			if keyID != "" && h.Keys != nil {
				ok, err := h.Keys.IsActive(r.Context(), keyID)
				if err == nil && !ok {
					write("event: closed\ndata: {\"reason\":\"credentials_revoked\"}\n\n")
					return
				}
			}
			if !write(": heartbeat\n\n") {
				return
			}
		case <-sub.Ready():
			evs, dropped := sub.Drain()
			var b []byte
			if dropped > 0 {
				b = fmt.Appendf(b, "event: lagged\ndata: {\"dropped\":%d}\n\n", dropped)
			}
			for _, e := range evs {
				data, _ := json.Marshal(apiv1.Event{ID: e.ID, Time: e.Time.UTC(), Type: string(e.Type),
					Severity: string(e.Severity), Subject: e.Subject, Message: e.Message})
				b = fmt.Appendf(b, "id: %s\nevent: event\ndata: %s\n\n", e.ID, data)
			}
			if !write(string(b)) {
				return
			}
		}
	}
}
