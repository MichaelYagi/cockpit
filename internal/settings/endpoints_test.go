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

func TestEndpointsUpdate(t *testing.T) {
	ctx := context.Background()
	store := &memStore{vals: map[string][]byte{}}
	var evs []core.Event
	e := NewEndpoints(store, []config.EndpointTarget{{Name: "Router", URL: "http://192.168.0.1/"}}, func() time.Time { return t0 },
		func(ev core.Event) { evs = append(evs, ev) }, nil)
	for _, n := range []string{"A", "B", "C"} {
		if _, err := e.Add(ctx, config.EndpointTarget{Name: n, URL: "tcp://h:1"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.Update(ctx, "b", config.EndpointTarget{Name: " B2 ", URL: " https://nas.lan/ ", TLSSkipVerify: true})
	if err != nil || got.Target.Name != "B2" || got.Target.URL != "https://nas.lan/" || !got.Target.TLSSkipVerify || !got.AddedAt.Equal(t0) {
		t.Fatalf("update: %+v %v", got, err)
	}
	if names := e.Targets(); names[2].Name != "B2" || names[3].Name != "C" {
		t.Errorf("order not kept: %+v", names)
	}
	if evs[len(evs)-1].Message != "Service check changed: B is now B2" {
		t.Errorf("event %q", evs[len(evs)-1].Message)
	}
	// Same name, new address; a case change of its own name is allowed.
	if _, err := e.Update(ctx, "B2", config.EndpointTarget{Name: "b2", URL: "tcp://h:2"}); err != nil {
		t.Errorf("rename case: %v", err)
	}
	for name, c := range map[string]struct {
		from string
		to   config.EndpointTarget
		want error
	}{
		"taken by runtime": {"A", config.EndpointTarget{Name: "c", URL: "tcp://h:1"}, ErrEndpointExists},
		"taken by file":    {"A", config.EndpointTarget{Name: "router", URL: "tcp://h:1"}, ErrEndpointExists},
		"file entry":       {"Router", config.EndpointTarget{Name: "Router", URL: "tcp://h:1"}, ErrEndpointInFile},
		"missing":          {"Z", config.EndpointTarget{Name: "Z", URL: "tcp://h:1"}, ErrEndpointNotFound},
	} {
		if _, err := e.Update(ctx, c.from, c.to); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var ve *config.ValidationError
	if _, err := e.Update(ctx, "A", config.EndpointTarget{Name: "A", URL: "ftp://x/"}); !errors.As(err, &ve) {
		t.Errorf("invalid: %v", err)
	}
	again := NewEndpoints(store, nil, nil, nil, nil)
	if err := again.Load(ctx); err != nil || again.Targets()[1].Name != "b2" || again.Targets()[1].URL != "tcp://h:2" {
		t.Errorf("reload: %+v %v", again.Targets(), err)
	}
}
