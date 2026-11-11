package middleware

import (
	"context"

	"github.com/MichaelYagi/cockpit/internal/auth"
)

// principalHolder lets AccessLog see the principal that inner middleware
// attaches to a derived request context.
type principalHolder struct {
	p   auth.Principal
	set bool
}

type holderKey struct{}

func withHolder(ctx context.Context, h *principalHolder) context.Context {
	return context.WithValue(ctx, holderKey{}, h)
}

// setPrincipal stores p in ctx and reports it to the access log holder.
func setPrincipal(ctx context.Context, p auth.Principal) context.Context {
	if h, ok := ctx.Value(holderKey{}).(*principalHolder); ok {
		h.p, h.set = p, true
	}
	return auth.WithPrincipal(ctx, p)
}
