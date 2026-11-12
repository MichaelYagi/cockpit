package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// memStore is an in-memory KeyStore for service tests.
type memStore struct {
	mu   sync.Mutex
	keys map[string]StoredKey
	fail error
}

func newMemStore() *memStore { return &memStore{keys: map[string]StoredKey{}} }

func (m *memStore) InsertKey(_ context.Context, k StoredKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[k.ID] = k
	return m.fail
}
func (m *memStore) GetKey(_ context.Context, id string) (StoredKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return StoredKey{}, m.fail
	}
	k, ok := m.keys[id]
	if !ok {
		return k, ErrNotFound
	}
	return k, nil
}
func (m *memStore) ListKeys(context.Context) ([]StoredKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredKey
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}
func (m *memStore) RevokeKey(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.keys[id]
	k.RevokedAt = &at
	m.keys[id] = k
	return nil
}
func (m *memStore) DeleteKey(_ context.Context, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok || k.Active(now) {
		return ErrNotFound
	}
	delete(m.keys, id)
	return nil
}
func (m *memStore) RotateKey(ctx context.Context, oldID string, at time.Time, next StoredKey) error {
	_ = m.RevokeKey(ctx, oldID, at)
	return m.InsertKey(ctx, next)
}
func (m *memStore) UpdateLastUsed(_ context.Context, used map[string]time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	for id, t := range used {
		k := m.keys[id]
		k.LastUsedAt = &t
		m.keys[id] = k
	}
	return nil
}
func (m *memStore) CountActiveKeys(_ context.Context, now time.Time) (int, error) {
	n := 0
	for _, k := range m.keys {
		if k.Active(now) {
			n++
		}
	}
	return n, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(t *testing.T) (*Service, *memStore, *clock, *bytes.Buffer, *[]core.Event) {
	t.Helper()
	store := newMemStore()
	clk := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	var logs bytes.Buffer
	var events []core.Event
	svc := NewService(store, ServiceOptions{
		Now:     clk.now,
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
		OnEvent: func(e core.Event) { events = append(events, e) },
	})
	return svc, store, clk, &logs, &events
}

func TestKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, store, clk, logs, events := newTestService(t)

	c, err := svc.Create(ctx, CreateRequest{Name: " grafana ", Scopes: []Scope{ScopeMetricsRead, ScopeMetricsRead, ScopeSystemRead}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Meta.Name != "grafana" || len(c.Meta.Scopes) != 2 || c.Meta.Scopes[0] != ScopeMetricsRead {
		t.Errorf("meta %+v", c.Meta)
	}
	stored := store.keys[c.Meta.ID]
	if bytes.Contains(stored.Hash, []byte(c.Key)) || string(stored.Hash) != string(hashKey(c.Key)) {
		t.Error("store must hold only the SHA-256 of the key")
	}

	p, err := svc.Verify(ctx, c.Key)
	if err != nil || p.KeyID != c.Meta.ID || !p.Has(ScopeMetricsRead) || p.Has(ScopeAdmin) || p.Kind != KindAPIKey {
		t.Fatalf("verify: %+v %v", p, err)
	}

	clk.t = clk.t.Add(time.Minute)
	list, _ := svc.List(ctx)
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Errorf("pending last-used not visible: %+v", list)
	}
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if store.keys[c.Meta.ID].LastUsedAt == nil {
		t.Error("last used not flushed")
	}

	r, err := svc.Rotate(ctx, c.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Key == c.Key || r.Meta.RotatedFrom != c.Meta.ID || r.Meta.Name != "grafana" {
		t.Errorf("rotated %+v", r.Meta)
	}
	if _, err := svc.Verify(ctx, c.Key); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("old key still works after rotation")
	}
	if _, err := svc.Verify(ctx, r.Key); err != nil {
		t.Error("new key does not work")
	}
	if _, err := svc.Rotate(ctx, c.Meta.ID); err == nil {
		t.Error("revoked key rotated")
	}

	m, err := svc.Revoke(ctx, r.Meta.ID)
	if err != nil || m.RevokedAt == nil {
		t.Fatalf("revoke %+v %v", m, err)
	}
	if _, err := svc.Verify(ctx, r.Key); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("revoked key works")
	}
	if _, err := svc.Revoke(ctx, r.Meta.ID); err != nil {
		t.Error("revoke not idempotent")
	}
	if _, err := svc.Revoke(ctx, "nosuchkeyxxx"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown revoke: %v", err)
	}

	// Removal (D33): only inactive keys, and the record is gone after.
	live, err := svc.Create(ctx, CreateRequest{Name: "live", Scopes: []Scope{ScopeSystemRead}})
	if err != nil {
		t.Fatal(err)
	}
	var ire *InvalidRequestError
	if err := svc.Remove(ctx, live.Meta.ID); !errors.As(err, &ire) {
		t.Errorf("removed an active key: %v", err)
	}
	if err := svc.Remove(ctx, r.Meta.ID); err != nil {
		t.Fatalf("remove revoked: %v", err)
	}
	if _, err := svc.Get(ctx, r.Meta.ID); !errors.Is(err, ErrNotFound) {
		t.Error("removed key still listed")
	}
	if err := svc.Remove(ctx, r.Meta.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}

	// Lifecycle events and logs never contain key material.
	// created, rotated, revoked, created (live), removed
	if len(*events) != 5 || (*events)[4].Type != core.EventKeyRemoved {
		t.Errorf("events %d %+v", len(*events), *events)
	}
	all := logs.String() + fmt.Sprint(*events)
	for _, k := range []string{c.Key, r.Key, c.Key[len(KeyPrefix)+keyIDLen+1:]} {
		if strings.Contains(all, k) {
			t.Error("key material in logs or events")
		}
	}
}

func TestVerifyRejections(t *testing.T) {
	ctx := context.Background()
	svc, store, clk, _, _ := newTestService(t)
	exp := clk.t.Add(time.Hour)
	c, err := svc.Create(ctx, CreateRequest{Name: "short-lived", Scopes: []Scope{ScopeSystemRead}, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := generateKey()
	tampered := c.Key[:len(c.Key)-1] + map[bool]string{true: "A", false: "B"}[c.Key[len(c.Key)-1] != 'A']
	for name, k := range map[string]string{"malformed": "ckp_nope", "unknown": other, "wrong secret": tampered} {
		if _, err := svc.Verify(ctx, k); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
	clk.t = exp
	if _, err := svc.Verify(ctx, c.Key); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("expired key accepted")
	}

	// A storage failure is not reported as bad credentials.
	store.fail = errors.New("disk on fire")
	if _, err := svc.Verify(ctx, c.Key); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("storage failure: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	svc, _, clk, _, _ := newTestService(t)
	past := clk.t.Add(-time.Second)
	cases := map[string]CreateRequest{
		"empty name":    {Name: "  ", Scopes: []Scope{ScopeSystemRead}},
		"long name":     {Name: strings.Repeat("x", 65), Scopes: []Scope{ScopeSystemRead}},
		"control char":  {Name: "a\nb", Scopes: []Scope{ScopeSystemRead}},
		"no scopes":     {Name: "x"},
		"admin scope":   {Name: "x", Scopes: []Scope{ScopeAdmin}},
		"unknown scope": {Name: "x", Scopes: []Scope{"processes:write"}},
		"past expiry":   {Name: "x", Scopes: []Scope{ScopeSystemRead}, ExpiresAt: &past},
	}
	for name, req := range cases {
		_, err := svc.Create(ctx, req)
		var ire *InvalidRequestError
		if !errors.As(err, &ire) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFlushRetriesOnFailure(t *testing.T) {
	ctx := context.Background()
	svc, store, _, _, _ := newTestService(t)
	c, _ := svc.Create(ctx, CreateRequest{Name: "x", Scopes: []Scope{ScopeSystemRead}})
	if _, err := svc.Verify(ctx, c.Key); err != nil {
		t.Fatal(err)
	}
	store.fail = errors.New("busy")
	if err := svc.Flush(ctx); err == nil {
		t.Fatal("flush error swallowed")
	}
	store.fail = nil
	if err := svc.Flush(ctx); err != nil || store.keys[c.Meta.ID].LastUsedAt == nil {
		t.Error("pending last-used lost after a failed flush")
	}
}

func TestRunFlusherFlushesOnStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	svc, store, _, _, _ := newTestService(t)
	c, _ := svc.Create(context.Background(), CreateRequest{Name: "x", Scopes: []Scope{ScopeSystemRead}})
	_, _ = svc.Verify(context.Background(), c.Key)
	done := make(chan struct{})
	go func() { svc.RunFlusher(ctx, time.Hour); close(done) }()
	cancel()
	<-done
	if store.keys[c.Meta.ID].LastUsedAt == nil {
		t.Error("final flush missing")
	}
}
