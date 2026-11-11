// Package httpx holds request context values and response helpers shared by
// API middleware and handlers.
package httpx

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

type requestIDKey struct{}
type peerKey struct{}

// WithRequestID stores the request ID in ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID from ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Peer is the credential of a Unix socket client (from SO_PEERCRED).
type Peer struct {
	UID int
	PID int
}

// WithPeer stores socket peer credentials in ctx.
func WithPeer(ctx context.Context, p Peer) context.Context {
	return context.WithValue(ctx, peerKey{}, p)
}

// PeerFrom returns socket peer credentials, if known.
func PeerFrom(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(peerKey{}).(Peer)
	return p, ok
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the standard error envelope. message must be safe to
// show clients: no secrets, internal paths or stack traces.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, apiv1.ErrorEnvelope{Error: apiv1.ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: RequestID(r.Context()),
	}})
}
