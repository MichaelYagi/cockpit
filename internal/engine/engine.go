// Package engine runs collectors and publishes their results.
//
// Phase 0 only validates and probes collectors at startup. Scheduling,
// rate derivation, staleness and snapshot publication arrive in Phase 1.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Engine owns the collector set for the agent's lifetime.
type Engine struct {
	collectors []collector.Collector
	logger     *slog.Logger

	mu       sync.RWMutex
	statuses []core.CollectorStatus
}

// New validates the collectors' descriptors and returns an engine.
func New(collectors []collector.Collector, logger *slog.Logger) (*Engine, error) {
	seen := make(map[string]bool, len(collectors))
	var errs []error
	for _, c := range collectors {
		d := c.Descriptor()
		if err := d.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[d.ID] {
			errs = append(errs, fmt.Errorf("duplicate collector id %q", d.ID))
		}
		seen[d.ID] = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Engine{collectors: collectors, logger: logger}, nil
}

// Start probes every collector concurrently, each bounded by its default
// timeout. A collector that panics or times out is reported as temporarily
// unavailable; it never prevents the others from starting.
func (e *Engine) Start(ctx context.Context) {
	statuses := make([]core.CollectorStatus, len(e.collectors))
	var wg sync.WaitGroup
	for i, c := range e.collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = e.probe(ctx, c)
		}()
	}
	wg.Wait()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })

	e.mu.Lock()
	e.statuses = statuses
	e.mu.Unlock()
}

func (e *Engine) probe(ctx context.Context, c collector.Collector) core.CollectorStatus {
	d := c.Descriptor()
	st := core.CollectorStatus{ID: d.ID, Version: d.Version, Capabilities: map[core.Capability]core.CapabilityStatus{}}

	pctx, cancel := context.WithTimeout(ctx, d.DefaultTimeout)
	defer cancel()

	type outcome struct {
		res      map[core.Capability]core.CapabilityStatus
		panicked bool
	}
	done := make(chan outcome, 1) // buffered: a late probe must not leak a blocked goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.logger.Error("collector probe panicked", "collector", d.ID, "panic", fmt.Sprint(r))
				done <- outcome{panicked: true}
			}
		}()
		done <- outcome{res: c.Probe(pctx)}
	}()

	var out outcome
	select {
	case out = <-done:
	case <-pctx.Done():
		e.logger.Warn("collector probe timed out", "collector", d.ID, "timeout", d.DefaultTimeout)
		out = outcome{panicked: true}
	}

	for _, capability := range d.Capabilities {
		s, ok := out.res[capability]
		switch {
		case out.panicked:
			s = core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The collector failed to start."}
		case !ok || !s.Availability.Valid():
			s = core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The collector did not report this capability."}
		}
		st.Capabilities[capability] = s
	}
	return st
}

// Collectors returns the probed status of every collector, sorted by ID.
// The returned slice must not be modified.
func (e *Engine) Collectors() []core.CollectorStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.statuses
}

// Close closes every collector and returns their combined errors.
func (e *Engine) Close(ctx context.Context) error {
	var errs []error
	for _, c := range e.collectors {
		if err := c.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", c.Descriptor().ID, err))
		}
	}
	return errors.Join(errs...)
}
