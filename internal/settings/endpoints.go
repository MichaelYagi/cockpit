package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Service checks added at runtime (D41) are stored under this key, next
// to the ones in the configuration file, which they never replace.
const endpointsKey = "endpoints"

// Errors from Endpoints.Add and Endpoints.Remove.
var (
	ErrEndpointExists   = errors.New("a service check with that name already exists")
	ErrEndpointLimit    = fmt.Errorf("at most %d service checks are allowed", config.MaxEndpoints)
	ErrEndpointNotFound = errors.New("no service check has that name")
	ErrEndpointInFile   = errors.New("that service check is defined in the configuration file; remove it there and restart the agent")
)

// EndpointEntry is one service check and where it comes from.
type EndpointEntry struct {
	Target  config.EndpointTarget
	Source  string    // SourceConfiguration or SourceRuntime
	AddedAt time.Time // zero for SourceConfiguration
}

// SourceRuntime marks checks added through the admin API.
const SourceRuntime = "runtime"

type storedEndpoint struct {
	Target  config.EndpointTarget `json:"target"`
	AddedAt time.Time             `json:"added_at"`
}

// Endpoints manages the list of service checks: the configuration
// file's, then the ones added at runtime, in the order they were added.
type Endpoints struct {
	store   Store
	file    []config.EndpointTarget
	now     func() time.Time
	publish func(core.Event)
	log     *slog.Logger

	mu      sync.Mutex
	runtime []storedEndpoint
}

// NewEndpoints returns the service starting with the file's checks.
func NewEndpoints(store Store, file []config.EndpointTarget, now func() time.Time, publish func(core.Event), log *slog.Logger) *Endpoints {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Endpoints{store: store, file: file, now: now, publish: publish, log: log}
}

// Load reads checks added at runtime. Entries that no longer validate, or
// whose name is now taken by the configuration file, are skipped with a
// warning rather than blocking startup.
func (e *Endpoints) Load(ctx context.Context) error {
	raw, _, found, err := e.store.LoadSetting(ctx, endpointsKey)
	if err != nil || !found {
		return err
	}
	var stored []storedEndpoint
	if err := json.Unmarshal(raw, &stored); err != nil {
		e.log.Warn("ignoring unreadable stored service checks", "error", err)
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range stored {
		switch {
		case config.ValidateEndpointTarget(s.Target) != nil:
			e.log.Warn("ignoring an invalid stored service check", "name", s.Target.Name)
		case e.taken(s.Target.Name):
			e.log.Warn("ignoring a stored service check whose name is already used", "name", s.Target.Name)
		case len(e.file)+len(e.runtime) >= config.MaxEndpoints:
			e.log.Warn("ignoring stored service checks over the limit", "limit", config.MaxEndpoints)
		default:
			e.runtime = append(e.runtime, s)
		}
	}
	return nil
}

// taken reports whether a check already uses name (case-insensitive).
// The caller holds e.mu.
func (e *Endpoints) taken(name string) bool {
	for _, t := range e.file {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	for _, s := range e.runtime {
		if strings.EqualFold(s.Target.Name, name) {
			return true
		}
	}
	return false
}

// List returns every check, file entries first.
func (e *Endpoints) List() []EndpointEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]EndpointEntry, 0, len(e.file)+len(e.runtime))
	for _, t := range e.file {
		out = append(out, EndpointEntry{Target: t, Source: SourceConfiguration})
	}
	for _, s := range e.runtime {
		out = append(out, EndpointEntry{Target: s.Target, Source: SourceRuntime, AddedAt: s.AddedAt})
	}
	return out
}

// Targets returns every check in List order, for the checker.
func (e *Endpoints) Targets() []config.EndpointTarget {
	list := e.List()
	out := make([]config.EndpointTarget, len(list))
	for i, l := range list {
		out[i] = l.Target
	}
	return out
}

// Add validates, stores and applies a new check. The checker picks it up
// within a few seconds.
func (e *Endpoints) Add(ctx context.Context, t config.EndpointTarget) (EndpointEntry, error) {
	t.Name = strings.TrimSpace(t.Name)
	t.URL = strings.TrimSpace(t.URL)
	if err := config.ValidateEndpointTarget(t); err != nil {
		return EndpointEntry{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.taken(t.Name) {
		return EndpointEntry{}, ErrEndpointExists
	}
	if len(e.file)+len(e.runtime) >= config.MaxEndpoints {
		return EndpointEntry{}, ErrEndpointLimit
	}
	s := storedEndpoint{Target: t, AddedAt: e.now().UTC()}
	next := append(append([]storedEndpoint(nil), e.runtime...), s)
	if err := e.save(ctx, next); err != nil {
		return EndpointEntry{}, err
	}
	e.runtime = next
	e.event("Service check added: " + t.Name)
	return EndpointEntry{Target: t, Source: SourceRuntime, AddedAt: s.AddedAt}, nil
}

// Update replaces a check added at runtime, keeping its place in the
// list and its added time. The name may change if the new one is free.
func (e *Endpoints) Update(ctx context.Context, name string, t config.EndpointTarget) (EndpointEntry, error) {
	t.Name = strings.TrimSpace(t.Name)
	t.URL = strings.TrimSpace(t.URL)
	if err := config.ValidateEndpointTarget(t); err != nil {
		return EndpointEntry{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.file {
		if strings.EqualFold(f.Name, name) {
			return EndpointEntry{}, ErrEndpointInFile
		}
	}
	i := -1
	for j, s := range e.runtime {
		if strings.EqualFold(s.Target.Name, name) {
			i = j
		}
	}
	if i < 0 {
		return EndpointEntry{}, ErrEndpointNotFound
	}
	old := e.runtime[i]
	if !strings.EqualFold(t.Name, old.Target.Name) && e.taken(t.Name) {
		return EndpointEntry{}, ErrEndpointExists
	}
	next := append([]storedEndpoint(nil), e.runtime...)
	next[i] = storedEndpoint{Target: t, AddedAt: old.AddedAt}
	if err := e.save(ctx, next); err != nil {
		return EndpointEntry{}, err
	}
	e.runtime = next
	msg := "Service check changed: " + t.Name
	if t.Name != old.Target.Name {
		msg = "Service check changed: " + old.Target.Name + " is now " + t.Name
	}
	e.event(msg)
	return EndpointEntry{Target: t, Source: SourceRuntime, AddedAt: old.AddedAt}, nil
}

// Remove deletes a check added at runtime.
func (e *Endpoints) Remove(ctx context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range e.file {
		if strings.EqualFold(t.Name, name) {
			return ErrEndpointInFile
		}
	}
	for i, s := range e.runtime {
		if !strings.EqualFold(s.Target.Name, name) {
			continue
		}
		next := append(append([]storedEndpoint(nil), e.runtime[:i]...), e.runtime[i+1:]...)
		if err := e.save(ctx, next); err != nil {
			return err
		}
		e.runtime = next
		e.event("Service check removed: " + s.Target.Name)
		return nil
	}
	return ErrEndpointNotFound
}

func (e *Endpoints) save(ctx context.Context, list []storedEndpoint) error {
	if len(list) == 0 {
		return e.store.DeleteSetting(ctx, endpointsKey)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return e.store.SaveSetting(ctx, endpointsKey, raw, e.now().UTC())
}

func (e *Endpoints) event(msg string) {
	e.log.Info("service checks changed", "message", msg)
	if e.publish != nil {
		e.publish(core.Event{Time: e.now().UTC(), Type: core.EventSettingsChanged, Severity: core.SeverityInfo, Subject: "endpoints", Message: msg})
	}
}
