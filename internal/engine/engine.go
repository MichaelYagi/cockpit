// Package engine runs collectors on their schedules, derives rates from raw
// counters, and publishes immutable snapshots.
//
// Concurrency model: one goroutine per collector runs it on its interval;
// results flow over a bounded channel to a single aggregator goroutine,
// which owns all mutable state and publishes each new snapshot with an
// atomic pointer swap. Readers never lock and never see partial updates.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Settings are the effective scheduling settings for one collector.
type Settings struct {
	Enabled  bool
	Interval time.Duration // 0 for an inventory collector means "once"
	Timeout  time.Duration
}

// Options configure the engine.
type Options struct {
	Logger *slog.Logger
	Now    func() time.Time
	// Settings resolves configuration for a collector. Nil uses the
	// descriptor defaults with the collector enabled.
	Settings func(collector.Descriptor) Settings
}

// Reader gives read-only access to the latest snapshot.
type Reader interface {
	Current() *core.Snapshot
}

// Engine owns the collector set for the agent's lifetime.
type Engine struct {
	opts    Options
	runners []*runner

	current atomic.Pointer[core.Snapshot]
	results chan outcome

	mu      sync.Mutex // guards started/closed
	started bool
	cancel  context.CancelFunc
	loops   sync.WaitGroup
	aggDone chan struct{}
}

type runner struct {
	c        collector.Collector
	desc     collector.Descriptor
	settings Settings
	inFlight atomic.Bool
}

type outcome struct {
	runner *runner
	result collector.Result
	failed string // non-empty: timeout or panic; reason shown to clients
}

// New validates the collectors' descriptors and returns an engine.
func New(collectors []collector.Collector, opts Options) (*Engine, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	seen := make(map[string]bool, len(collectors))
	var errs []error
	var runners []*runner
	for _, c := range collectors {
		d := c.Descriptor()
		if err := d.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[d.ID] {
			errs = append(errs, fmt.Errorf("duplicate collector id %q", d.ID))
			continue
		}
		seen[d.ID] = true
		s := Settings{Enabled: true, Interval: d.DefaultInterval, Timeout: d.DefaultTimeout}
		if opts.Settings != nil {
			s = opts.Settings(d)
		}
		runners = append(runners, &runner{c: c, desc: d, settings: s})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sort.Slice(runners, func(i, j int) bool { return runners[i].desc.ID < runners[j].desc.ID })
	e := &Engine{opts: opts, runners: runners, results: make(chan outcome, 2*len(runners)+1)}
	e.current.Store(&core.Snapshot{GeneratedAt: opts.Now().UTC()})
	return e, nil
}

// Current returns the latest snapshot. It never returns nil.
func (e *Engine) Current() *core.Snapshot { return e.current.Load() }

// Collectors returns the status of every collector, sorted by ID.
func (e *Engine) Collectors() []core.CollectorStatus { return e.Current().Collectors }

// Start probes every collector (concurrently, each bounded by its
// timeout), publishes an initial snapshot, then starts the schedules.
// It returns once probing is done.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return
	}
	e.started = true

	probes := make([]map[core.Capability]core.CapabilityStatus, len(e.runners))
	var wg sync.WaitGroup
	for i, r := range e.runners {
		if !r.settings.Enabled {
			probes[i] = allStatus(r.desc, core.NotConfigured, "Disabled in configuration.")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			probes[i] = e.probe(ctx, r)
		}()
	}
	wg.Wait()

	agg := newAggregator(e, probes)
	e.current.Store(agg.publish())

	runCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.aggDone = make(chan struct{})
	go func() {
		defer close(e.aggDone)
		agg.run(runCtx)
	}()
	for i, r := range e.runners {
		if !r.settings.Enabled || !anyAvailableOrTransient(probes[i]) {
			continue // nothing this collector can ever provide here
		}
		e.loops.Add(1)
		go func() {
			defer e.loops.Done()
			e.loop(runCtx, r)
		}()
	}
}

// anyAvailableOrTransient reports whether collecting could yield data.
// Capabilities that are unsupported or not configured never change at
// runtime, so a collector with only those is not scheduled.
func anyAvailableOrTransient(m map[core.Capability]core.CapabilityStatus) bool {
	for _, s := range m {
		switch s.Availability {
		case core.Available, core.TemporarilyUnavailable, core.PermissionDenied:
			return true
		}
	}
	return false
}

func allStatus(d collector.Descriptor, a core.Availability, reason string) map[core.Capability]core.CapabilityStatus {
	m := make(map[core.Capability]core.CapabilityStatus, len(d.Capabilities))
	for _, c := range d.Capabilities {
		m[c] = core.CapabilityStatus{Availability: a, Reason: reason}
	}
	return m
}

// call runs fn with a deadline and panic isolation. It returns ok=false on
// timeout or panic, with a client-safe reason.
func call[T any](ctx context.Context, r *runner, log *slog.Logger, what string, fn func(context.Context) T) (T, string) {
	var zero T
	cctx, cancel := context.WithTimeout(ctx, r.settings.Timeout)
	defer cancel()
	type res struct {
		v        T
		panicked bool
	}
	done := make(chan res, 1) // buffered: a late result must not block forever
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("collector panicked", "collector", r.desc.ID, "during", what, "panic", fmt.Sprint(p))
				done <- res{panicked: true}
			}
		}()
		done <- res{v: fn(cctx)}
	}()
	select {
	case out := <-done:
		if out.panicked {
			return zero, "The collector failed unexpectedly."
		}
		return out.v, ""
	case <-cctx.Done():
		log.Warn("collector timed out", "collector", r.desc.ID, "during", what, "timeout", r.settings.Timeout)
		return zero, fmt.Sprintf("The collector did not respond within %s.", r.settings.Timeout)
	}
}

func (e *Engine) probe(ctx context.Context, r *runner) map[core.Capability]core.CapabilityStatus {
	got, failed := call(ctx, r, e.opts.Logger, "probe", r.c.Probe)
	if failed != "" {
		return allStatus(r.desc, core.TemporarilyUnavailable, failed)
	}
	return normalize(r.desc, got)
}

// normalize ensures every declared capability has a valid status.
func normalize(d collector.Descriptor, m map[core.Capability]core.CapabilityStatus) map[core.Capability]core.CapabilityStatus {
	out := make(map[core.Capability]core.CapabilityStatus, len(d.Capabilities))
	for _, c := range d.Capabilities {
		s, ok := m[c]
		if !ok || !s.Availability.Valid() {
			s = core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: "The collector did not report this capability."}
		}
		out[c] = s
	}
	return out
}

func (e *Engine) loop(ctx context.Context, r *runner) {
	// Small random delay so collectors do not all fire at once.
	jitter := time.Duration(rand.Int64N(int64(250 * time.Millisecond)))
	if r.settings.Interval > 0 && jitter > r.settings.Interval/4 {
		jitter = r.settings.Interval / 4
	}
	t := time.NewTimer(jitter)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e.runOnce(ctx, r)
		if r.settings.Interval <= 0 {
			return // run-once inventory
		}
		t.Reset(r.settings.Interval)
	}
}

func (e *Engine) runOnce(ctx context.Context, r *runner) {
	// At most one Collect per collector: if a previous call ignored its
	// deadline and is still running, skip this tick.
	if !r.inFlight.CompareAndSwap(false, true) {
		e.opts.Logger.Debug("skipping collection; previous run still in progress", "collector", r.desc.ID)
		return
	}
	res, failed := call(ctx, r, e.opts.Logger, "collect", func(c context.Context) collector.Result {
		defer r.inFlight.Store(false)
		return r.c.Collect(c)
	})
	if failed != "" && ctx.Err() != nil {
		return // shutting down
	}
	select {
	case e.results <- outcome{runner: r, result: res, failed: failed}:
	case <-ctx.Done():
	}
}

// Close stops scheduling, waits for loops and the aggregator, then closes
// every collector. Collect calls that ignore cancellation are abandoned.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	cancel, aggDone := e.cancel, e.aggDone
	e.cancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
		waited := make(chan struct{})
		go func() { e.loops.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-ctx.Done():
			return fmt.Errorf("engine shutdown: %w", ctx.Err())
		}
		<-aggDone
	}
	var errs []error
	for _, r := range e.runners {
		if err := r.c.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", r.desc.ID, err))
		}
	}
	return errors.Join(errs...)
}
