package settings

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestEndpointsAddRemovePersist(t *testing.T) {
	ctx := context.Background()
	store := &memStore{vals: map[string][]byte{}}
	file := []config.EndpointTarget{{Name: "Router", URL: "http://192.168.0.1/"}}
	var evs []core.Event
	now := func() time.Time { return t0 }
	e := NewEndpoints(store, file, now, func(ev core.Event) { evs = append(evs, ev) }, nil)
	if err := e.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.Add(ctx, config.EndpointTarget{Name: " Home Assistant ", URL: " http://192.168.0.50:8123/ "})
	if err != nil || got.Target.Name != "Home Assistant" || got.Target.URL != "http://192.168.0.50:8123/" || got.Source != SourceRuntime || !got.AddedAt.Equal(t0) {
		t.Fatalf("add: %+v %v", got, err)
	}
	if _, err := e.Add(ctx, config.EndpointTarget{Name: "NAS", URL: "tcp://192.168.0.10:22"}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		t    config.EndpointTarget
		want error
	}{
		"duplicate of runtime": {config.EndpointTarget{Name: "nas", URL: "tcp://h:1"}, ErrEndpointExists},
		"duplicate of file":    {config.EndpointTarget{Name: "router", URL: "tcp://h:1"}, ErrEndpointExists},
	} {
		if _, err := e.Add(ctx, c.t); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var ve *config.ValidationError
	if _, err := e.Add(ctx, config.EndpointTarget{Name: "bad", URL: "https://user:pw@h/"}); !errors.As(err, &ve) {
		t.Errorf("invalid: %v", err)
	}
	if l := e.List(); len(l) != 3 || l[0].Source != SourceConfiguration || l[1].Target.Name != "Home Assistant" || l[2].Target.Name != "NAS" {
		t.Errorf("list %+v", l)
	}
	// A restart keeps runtime checks.
	again := NewEndpoints(store, file, now, nil, nil)
	if err := again.Load(ctx); err != nil || len(again.Targets()) != 3 {
		t.Fatalf("reload: %+v %v", again.Targets(), err)
	}
	if err := e.Remove(ctx, "router"); !errors.Is(err, ErrEndpointInFile) {
		t.Errorf("remove file entry: %v", err)
	}
	if err := e.Remove(ctx, "missing"); !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("remove missing: %v", err)
	}
	if err := e.Remove(ctx, "home assistant"); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(ctx, "NAS"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.vals[endpointsKey]; ok || len(e.Targets()) != 1 {
		t.Errorf("after removing all: %v %+v", store.vals, e.Targets())
	}
	if len(evs) != 4 || evs[0].Type != core.EventSettingsChanged || evs[0].Message != "Service check added: Home Assistant" || evs[3].Message != "Service check removed: NAS" {
		t.Errorf("events %+v", evs)
	}
}

func TestEndpointsLimitAndBadStoredData(t *testing.T) {
	ctx := context.Background()
	store := &memStore{vals: map[string][]byte{}}
	e := NewEndpoints(store, nil, nil, nil, nil)
	for i := range config.MaxEndpoints {
		if _, err := e.Add(ctx, config.EndpointTarget{Name: fmt.Sprintf("s%d", i), URL: "tcp://h:1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Add(ctx, config.EndpointTarget{Name: "one more", URL: "tcp://h:1"}); !errors.Is(err, ErrEndpointLimit) {
		t.Errorf("limit: %v", err)
	}
	// A file entry that now uses a stored name wins; unreadable data is ignored.
	again := NewEndpoints(store, []config.EndpointTarget{{Name: "S0", URL: "tcp://x:2"}}, nil, nil, nil)
	if err := again.Load(ctx); err != nil || len(again.Targets()) != config.MaxEndpoints || again.Targets()[0].URL != "tcp://x:2" {
		t.Errorf("reload with clash: %d %v", len(again.Targets()), err)
	}
	store.vals[endpointsKey] = []byte("{not json")
	if err := NewEndpoints(store, nil, nil, nil, nil).Load(ctx); err != nil {
		t.Errorf("bad data should be ignored: %v", err)
	}
}
