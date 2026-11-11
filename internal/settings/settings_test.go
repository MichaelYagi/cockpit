package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
)

type memStore struct {
	vals map[string][]byte
	at   time.Time
}

func (m *memStore) LoadSetting(_ context.Context, k string) ([]byte, time.Time, bool, error) {
	v, ok := m.vals[k]
	return v, m.at, ok, nil
}
func (m *memStore) SaveSetting(_ context.Context, k string, v []byte, at time.Time) error {
	m.vals[k], m.at = v, at
	return nil
}
func (m *memStore) DeleteSetting(_ context.Context, k string) error { delete(m.vals, k); return nil }

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*Service, *memStore, *[]config.Health, *[]core.Event) {
	t.Helper()
	store := &memStore{vals: map[string][]byte{}}
	var applied []config.Health
	var evs []core.Event
	s := New(store, config.Defaults().Health, func(h config.Health) { applied = append(applied, h) },
		func() time.Time { return t0 }, func(e core.Event) { evs = append(evs, e) }, nil)
	return s, store, &applied, &evs
}

func TestPutValidatesAppliesAndRecords(t *testing.T) {
	s, store, applied, evs := newService(t)
	h := config.Defaults().Health
	h.Storage.WarnPercent, h.Storage.CriticalPercent = 70, 90
	h.Interval = time.Hour // not changeable at runtime: kept from the file
	eff, err := s.Put(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Source != SourceOverride || eff.Health.Storage.WarnPercent != 70 || eff.Health.Interval != config.Defaults().Health.Interval || !eff.UpdatedAt.Equal(t0) {
		t.Errorf("%+v", eff)
	}
	if len(*applied) != 1 || store.vals[healthKey] == nil {
		t.Error("not applied or stored")
	}
	if len(*evs) != 1 || (*evs)[0].Type != core.EventSettingsChanged || !strings.Contains((*evs)[0].Message, "storage.warn_percent 85 -> 70") ||
		!strings.Contains((*evs)[0].Message, "storage.critical_percent 95 -> 90") {
		t.Errorf("event %+v", *evs)
	}

	bad := h
	bad.Storage.WarnPercent = 99 // above critical
	var ve *config.ValidationError
	if _, err := s.Put(context.Background(), bad); !errors.As(err, &ve) {
		t.Fatalf("invalid settings accepted: %v", err)
	}
	if s.Get().Health.Storage.WarnPercent != 70 || len(*applied) != 1 {
		t.Error("rejected settings changed the effective values")
	}
}

func TestResetAndLoad(t *testing.T) {
	s, store, applied, evs := newService(t)
	h := config.Defaults().Health
	h.Confirm = 5
	if _, err := s.Put(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	// A new service (restart) picks the override up.
	s2 := New(store, config.Defaults().Health, func(config.Health) {}, nil, nil, nil)
	if err := s2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := s2.Get(); g.Source != SourceOverride || g.Health.Confirm != 5 {
		t.Errorf("after restart %+v", g)
	}

	eff, err := s.Reset(context.Background())
	if err != nil || eff.Source != SourceConfiguration || eff.Health.Confirm != 2 || store.vals[healthKey] != nil {
		t.Fatalf("reset %+v %v", eff, err)
	}
	if len(*applied) != 2 || len(*evs) != 2 || !strings.Contains((*evs)[1].Message, "reset") {
		t.Errorf("applied %d events %+v", len(*applied), *evs)
	}
}

func TestInvalidStoredOverrideIgnored(t *testing.T) {
	s, store, applied, _ := newService(t)
	store.vals[healthKey] = []byte(`{"Confirm": 0}`)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Get().Source != SourceConfiguration || len(*applied) != 0 {
		t.Error("invalid stored settings were applied")
	}
	store.vals[healthKey] = []byte(`not json`)
	if err := s.Load(context.Background()); err != nil || s.Get().Source != SourceConfiguration {
		t.Error("unreadable stored settings")
	}
}
