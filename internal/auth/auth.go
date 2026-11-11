// Package auth defines principals, scopes and credential verification.
//
// It has no HTTP dependencies. Phase 0 provides the contracts and a verifier
// that rejects every key; the SQLite-backed key service arrives in Phase 1.
package auth

import (
	"context"
	"errors"
	"slices"
)

// Scope is a permission. Scopes are checked by exact membership; no scope
// implies another (D8).
type Scope string

const (
	ScopeSystemRead    Scope = "system:read"
	ScopeMetricsRead   Scope = "metrics:read"
	ScopeProcessesRead Scope = "processes:read"
	// ScopeNetworkRead covers interface addresses, routes and DNS (D27).
	ScopeNetworkRead Scope = "network:read"
	ScopeAdmin       Scope = "admin"
)

// KnownScopes is the scope registry. Adding a scope here is non-breaking.
func KnownScopes() []Scope {
	return []Scope{ScopeSystemRead, ScopeMetricsRead, ScopeNetworkRead, ScopeProcessesRead, ScopeAdmin}
}

// Known reports whether s is registered.
func (s Scope) Known() bool { return slices.Contains(KnownScopes(), s) }

// PrincipalKind distinguishes how a caller authenticated.
type PrincipalKind string

const (
	// KindAPIKey is a caller that presented a valid API key over TCP.
	KindAPIKey PrincipalKind = "api_key"
	// KindLocalAdmin is a caller on the Unix socket (D3).
	KindLocalAdmin PrincipalKind = "local_admin"
)

// Principal is an authenticated caller.
type Principal struct {
	Kind   PrincipalKind
	KeyID  string // non-secret key ID for KindAPIKey
	Scopes []Scope
	// PeerUID and PeerPID come from SO_PEERCRED on the socket, for audit
	// logging only. -1 when unknown.
	PeerUID int
	PeerPID int
}

// Has reports whether the principal holds scope s.
func (p Principal) Has(s Scope) bool { return slices.Contains(p.Scopes, s) }

// LogID identifies the principal in logs without revealing secrets.
func (p Principal) LogID() string {
	if p.Kind == KindLocalAdmin {
		return "local-admin"
	}
	return "key:" + p.KeyID
}

// LocalAdmin returns the socket principal, which holds every scope
// explicitly.
func LocalAdmin(peerUID, peerPID int) Principal {
	return Principal{Kind: KindLocalAdmin, Scopes: KnownScopes(), PeerUID: peerUID, PeerPID: peerPID}
}

// ErrInvalidCredentials covers every reason a key is rejected (malformed,
// unknown, revoked, expired). Callers must not distinguish them to clients.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Verifier checks a presented API key.
type Verifier interface {
	Verify(ctx context.Context, key string) (Principal, error)
}

// RejectAll is the Phase 0 verifier: no API keys exist yet.
type RejectAll struct{}

// Verify always fails.
func (RejectAll) Verify(context.Context, string) (Principal, error) {
	return Principal{}, ErrInvalidCredentials
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
