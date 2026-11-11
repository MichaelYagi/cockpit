package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// KeyMeta is everything about a key except the secret and its hash. It is
// safe to show and log.
type KeyMeta struct {
	ID          string
	Name        string
	Scopes      []Scope
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	RotatedFrom string
}

// Active reports whether the key can authenticate at time now.
func (m KeyMeta) Active(now time.Time) bool {
	return m.RevokedAt == nil && (m.ExpiresAt == nil || now.Before(*m.ExpiresAt))
}

// StoredKey is a key as persisted: metadata plus the SHA-256 verifier.
type StoredKey struct {
	KeyMeta
	Hash []byte
}

// KeyStore persists keys. Implemented by storage/sqlite.
type KeyStore interface {
	InsertKey(ctx context.Context, k StoredKey) error
	GetKey(ctx context.Context, id string) (StoredKey, error)
	ListKeys(ctx context.Context) ([]StoredKey, error)
	RevokeKey(ctx context.Context, id string, at time.Time) error
	// RotateKey revokes oldID and inserts next atomically.
	RotateKey(ctx context.Context, oldID string, at time.Time, next StoredKey) error
	UpdateLastUsed(ctx context.Context, used map[string]time.Time) error
	CountActiveKeys(ctx context.Context, now time.Time) (int, error)
}

// ErrNotFound means no key has the given ID.
var ErrNotFound = errors.New("key not found")

// InvalidRequestError describes a rejected key request. Its message is
// safe to show clients.
type InvalidRequestError struct{ Msg string }

func (e *InvalidRequestError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &InvalidRequestError{Msg: fmt.Sprintf(format, args...)}
}

// IssuableScopes are the scopes a key may hold in Phase 1. admin is
// reserved for the local socket principal (D3, D8).
func IssuableScopes() []Scope { return []Scope{ScopeSystemRead, ScopeMetricsRead} }

// MaxActiveKeys bounds the number of usable keys.
const MaxActiveKeys = 1000

// Created is returned once, at creation or rotation. Key is the only copy
// of the plaintext key that will ever exist.
type Created struct {
	Key  string
	Meta KeyMeta
}

// ServiceOptions configure the key service.
type ServiceOptions struct {
	Now     func() time.Time
	Logger  *slog.Logger
	OnEvent func(core.Event) // optional: key lifecycle events
}

// Service manages the API key lifecycle and verifies keys.
type Service struct {
	store KeyStore
	opts  ServiceOptions

	mu       sync.Mutex
	lastUsed map[string]time.Time // pending writes, flushed periodically
}

// NewService returns a key service backed by store.
func NewService(store KeyStore, opts ServiceOptions) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, opts: opts, lastUsed: map[string]time.Time{}}
}

// CreateRequest describes a new key.
type CreateRequest struct {
	Name      string
	Scopes    []Scope
	ExpiresAt *time.Time
}

func (s *Service) validate(req CreateRequest, now time.Time) (CreateRequest, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return req, invalid("name must be 1 to 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return req, invalid("name must not contain control characters")
		}
	}
	if len(req.Scopes) == 0 {
		return req, invalid("at least one scope is required (one of %s)", joinScopes(IssuableScopes()))
	}
	set := map[Scope]bool{}
	for _, sc := range req.Scopes {
		issuable := false
		for _, ok := range IssuableScopes() {
			if sc == ok {
				issuable = true
			}
		}
		if !issuable {
			return req, invalid("scope %q cannot be granted to an API key; allowed: %s", sc, joinScopes(IssuableScopes()))
		}
		set[sc] = true
	}
	scopes := make([]Scope, 0, len(set))
	for sc := range set {
		scopes = append(scopes, sc)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i] < scopes[j] })
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(now) {
			return req, invalid("expires_at must be in the future")
		}
		t := req.ExpiresAt.UTC().Truncate(time.Second)
		req.ExpiresAt = &t
	}
	return CreateRequest{Name: name, Scopes: scopes, ExpiresAt: req.ExpiresAt}, nil
}

func joinScopes(s []Scope) string {
	parts := make([]string, len(s))
	for i, sc := range s {
		parts[i] = string(sc)
	}
	return strings.Join(parts, ", ")
}

func (s *Service) newStored(req CreateRequest, now time.Time, rotatedFrom string) (StoredKey, string) {
	key, id := generateKey()
	return StoredKey{
		KeyMeta: KeyMeta{ID: id, Name: req.Name, Scopes: req.Scopes, CreatedAt: now, ExpiresAt: req.ExpiresAt, RotatedFrom: rotatedFrom},
		Hash:    hashKey(key),
	}, key
}

// Create issues a new key. The plaintext is returned once and never stored.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Created, error) {
	now := s.opts.Now().UTC().Truncate(time.Second)
	req, err := s.validate(req, now)
	if err != nil {
		return Created{}, err
	}
	n, err := s.store.CountActiveKeys(ctx, now)
	if err != nil {
		return Created{}, err
	}
	if n >= MaxActiveKeys {
		return Created{}, invalid("too many active keys (maximum %d); revoke unused keys first", MaxActiveKeys)
	}
	stored, key := s.newStored(req, now, "")
	if err := s.store.InsertKey(ctx, stored); err != nil {
		return Created{}, err
	}
	s.opts.Logger.Info("api key created", "key_id", stored.ID, "name", stored.Name, "scopes", joinScopes(stored.Scopes))
	s.event(core.EventKeyCreated, stored.KeyMeta, "API key created")
	return Created{Key: key, Meta: stored.KeyMeta}, nil
}

// List returns metadata for every key, newest first.
func (s *Service) List(ctx context.Context) ([]KeyMeta, error) {
	keys, err := s.store.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]KeyMeta, len(keys))
	s.mu.Lock()
	for i, k := range keys {
		out[i] = s.withPending(k.KeyMeta)
	}
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Get returns one key's metadata.
func (s *Service) Get(ctx context.Context, id string) (KeyMeta, error) {
	k, err := s.store.GetKey(ctx, id)
	if err != nil {
		return KeyMeta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withPending(k.KeyMeta), nil
}

// withPending overlays an unflushed last-used time. Caller holds s.mu.
func (s *Service) withPending(m KeyMeta) KeyMeta {
	if t, ok := s.lastUsed[m.ID]; ok && (m.LastUsedAt == nil || t.After(*m.LastUsedAt)) {
		m.LastUsedAt = &t
	}
	return m
}

// Rotate issues a replacement key with the same name, scopes and expiry,
// and revokes the old key immediately.
func (s *Service) Rotate(ctx context.Context, id string) (Created, error) {
	now := s.opts.Now().UTC().Truncate(time.Second)
	old, err := s.store.GetKey(ctx, id)
	if err != nil {
		return Created{}, err
	}
	if !old.Active(now) {
		return Created{}, invalid("only active keys can be rotated")
	}
	req := CreateRequest{Name: old.Name, Scopes: old.Scopes, ExpiresAt: old.ExpiresAt}
	stored, key := s.newStored(req, now, old.ID)
	if err := s.store.RotateKey(ctx, old.ID, now, stored); err != nil {
		return Created{}, err
	}
	s.opts.Logger.Info("api key rotated", "key_id", old.ID, "new_key_id", stored.ID, "name", stored.Name)
	s.event(core.EventKeyRotated, stored.KeyMeta, "API key rotated (replaces "+old.ID+")")
	return Created{Key: key, Meta: stored.KeyMeta}, nil
}

// Revoke disables a key immediately. Revoking a revoked key is a no-op.
func (s *Service) Revoke(ctx context.Context, id string) (KeyMeta, error) {
	k, err := s.store.GetKey(ctx, id)
	if err != nil {
		return KeyMeta{}, err
	}
	if k.RevokedAt == nil {
		now := s.opts.Now().UTC().Truncate(time.Second)
		if err := s.store.RevokeKey(ctx, id, now); err != nil {
			return KeyMeta{}, err
		}
		k.RevokedAt = &now
		s.opts.Logger.Info("api key revoked", "key_id", id, "name", k.Name)
		s.event(core.EventKeyRevoked, k.KeyMeta, "API key revoked")
	}
	return k.KeyMeta, nil
}

// IsActive reports whether the key with this ID can still authenticate.
// Long-lived connections use it to drop revoked or expired keys.
func (s *Service) IsActive(ctx context.Context, id string) (bool, error) {
	k, err := s.store.GetKey(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return k.Active(s.opts.Now().UTC()), nil
}

// dummyHash keeps verification time similar when the ID is unknown.
var dummyHash = hashKey(KeyPrefix + "aaaaaaaaaaaa_" + strings.Repeat("0", keySecretLen))

// Verify authenticates a presented key. Every failure returns
// ErrInvalidCredentials; the reason is never revealed.
func (s *Service) Verify(ctx context.Context, key string) (Principal, error) {
	id, ok := parseKey(key)
	if !ok {
		return Principal{}, ErrInvalidCredentials
	}
	presented := hashKey(key)
	k, err := s.store.GetKey(ctx, id)
	if err != nil {
		subtle.ConstantTimeCompare(presented, dummyHash)
		if errors.Is(err, ErrNotFound) {
			return Principal{}, ErrInvalidCredentials
		}
		return Principal{}, err // storage failure: surfaced as 500, not 401
	}
	if subtle.ConstantTimeCompare(presented, k.Hash) != 1 {
		return Principal{}, ErrInvalidCredentials
	}
	now := s.opts.Now().UTC()
	if !k.Active(now) {
		return Principal{}, ErrInvalidCredentials
	}
	s.mu.Lock()
	s.lastUsed[id] = now.Truncate(time.Second)
	s.mu.Unlock()
	return Principal{Kind: KindAPIKey, KeyID: id, Scopes: append([]Scope(nil), k.Scopes...), PeerUID: -1, PeerPID: -1}, nil
}

// Flush writes pending last-used times.
func (s *Service) Flush(ctx context.Context) error {
	s.mu.Lock()
	pending := s.lastUsed
	s.lastUsed = map[string]time.Time{}
	s.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	if err := s.store.UpdateLastUsed(ctx, pending); err != nil {
		// Put them back so the next flush retries.
		s.mu.Lock()
		for id, t := range pending {
			if cur, ok := s.lastUsed[id]; !ok || t.After(cur) {
				s.lastUsed[id] = t
			}
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// RunFlusher flushes last-used times every interval until ctx is done,
// then flushes once more.
func (s *Service) RunFlusher(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.Flush(fctx); err != nil {
				s.opts.Logger.Warn("could not save key last-used times", "error", err)
			}
			cancel()
			return
		case <-t.C:
			if err := s.Flush(ctx); err != nil {
				s.opts.Logger.Warn("could not save key last-used times", "error", err)
			}
		}
	}
}

func (s *Service) event(t core.EventType, m KeyMeta, msg string) {
	if s.opts.OnEvent == nil {
		return
	}
	s.opts.OnEvent(core.Event{Time: s.opts.Now().UTC(), Type: t, Severity: core.SeverityInfo,
		Subject: "api_key:" + m.ID, Message: msg + ": " + m.Name})
}
