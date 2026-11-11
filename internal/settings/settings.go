// Package settings manages runtime changes to health thresholds (D19).
// Changes are validated like the configuration file, stored, applied
// immediately and recorded as events. Stored settings override the file;
// Reset returns to the file's values.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/core"
)

const healthKey = "health"

// Sources of the effective settings.
const (
	SourceConfiguration = "configuration"
	SourceOverride      = "override"
)

// Store persists settings. Implemented by storage/sqlite.
type Store interface {
	LoadSetting(ctx context.Context, key string) (value []byte, updated time.Time, found bool, err error)
	SaveSetting(ctx context.Context, key string, value []byte, at time.Time) error
	DeleteSetting(ctx context.Context, key string) error
}

// Effective is the settings in force.
type Effective struct {
	Health    config.Health
	Source    string
	UpdatedAt time.Time // zero for SourceConfiguration
}

// Service manages health settings.
type Service struct {
	store   Store
	file    config.Health
	apply   func(config.Health)
	now     func() time.Time
	publish func(core.Event)
	log     *slog.Logger

	mu  sync.Mutex
	cur Effective
}

// New returns a service starting from the file's values.
func New(store Store, file config.Health, apply func(config.Health), now func() time.Time, publish func(core.Event), log *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, file: file, apply: apply, now: now, publish: publish, log: log,
		cur: Effective{Health: file, Source: SourceConfiguration}}
}

// Load applies a stored override, if any. An override that no longer
// validates (for example after an upgrade changed the rules) is ignored
// with a warning rather than blocking startup.
func (s *Service) Load(ctx context.Context) error {
	raw, at, found, err := s.store.LoadSetting(ctx, healthKey)
	if err != nil || !found {
		return err
	}
	var h config.Health
	if err := json.Unmarshal(raw, &h); err != nil {
		s.log.Warn("ignoring unreadable stored health settings", "error", err)
		return nil
	}
	h.Interval = s.file.Interval
	if err := config.ValidateHealth(h); err != nil {
		s.log.Warn("ignoring stored health settings that are no longer valid", "error", err)
		return nil
	}
	s.mu.Lock()
	s.cur = Effective{Health: h, Source: SourceOverride, UpdatedAt: at}
	s.mu.Unlock()
	s.apply(h)
	s.log.Info("applied stored health settings", "updated_at", at)
	return nil
}

// Get returns the settings in force.
func (s *Service) Get() Effective {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Put validates, stores and applies new health settings. The evaluation
// interval is not changeable at runtime and is kept from the file.
func (s *Service) Put(ctx context.Context, h config.Health) (Effective, error) {
	h.Interval = s.file.Interval
	if err := config.ValidateHealth(h); err != nil {
		return Effective{}, err
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return Effective{}, err
	}
	at := s.now().UTC().Truncate(time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.SaveSetting(ctx, healthKey, raw, at); err != nil {
		return Effective{}, err
	}
	changes := diff(s.cur.Health, h)
	s.cur = Effective{Health: h, Source: SourceOverride, UpdatedAt: at}
	s.apply(h)
	s.record("Health thresholds changed: " + summarize(changes))
	return s.cur, nil
}

// Reset removes the override and returns to the file's values.
func (s *Service) Reset(ctx context.Context) (Effective, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteSetting(ctx, healthKey); err != nil {
		return Effective{}, err
	}
	if s.cur.Source == SourceOverride {
		s.record("Health thresholds reset to the configuration file")
	}
	s.cur = Effective{Health: s.file, Source: SourceConfiguration}
	s.apply(s.file)
	return s.cur, nil
}

func (s *Service) record(msg string) {
	s.log.Info("settings changed", "detail", msg)
	if s.publish != nil {
		s.publish(core.Event{Time: s.now().UTC(), Type: core.EventSettingsChanged, Severity: core.SeverityInfo, Subject: "settings:health", Message: msg})
	}
}

// diff lists changed leaf fields as "path: old -> new".
func diff(a, b config.Health) []string {
	var out []string
	var walk func(path string, x, y reflect.Value)
	walk = func(path string, x, y reflect.Value) {
		if x.Kind() == reflect.Struct {
			for i := 0; i < x.NumField(); i++ {
				name := x.Type().Field(i).Tag.Get("yaml")
				walk(strings.TrimPrefix(path+"."+name, "."), x.Field(i), y.Field(i))
			}
			return
		}
		if x.Interface() != y.Interface() {
			out = append(out, fmt.Sprintf("%s %v -> %v", path, x.Interface(), y.Interface()))
		}
	}
	walk("", reflect.ValueOf(a), reflect.ValueOf(b))
	sort.Strings(out)
	return out
}

func summarize(changes []string) string {
	if len(changes) == 0 {
		return "no values changed"
	}
	return strings.Join(changes, ", ")
}
