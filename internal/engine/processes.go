package engine

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Process listing is on demand (never scheduled): the first request after
// a quiet period samples twice to compute CPU usage, later requests within
// processCacheTTL share one view.
const (
	processCacheTTL   = 2 * time.Second
	processPrevMaxAge = 30 * time.Second
	processSampleGap  = 500 * time.Millisecond
	processViewsKept  = 8
	processViewTTL    = 2 * time.Minute
	userHZ            = 100 // USER_HZ is fixed at 100 in the Linux ABI
)

type processState struct {
	mu     sync.Mutex
	nextID uint64
	prev   *core.ProcessList
	views  []*core.ProcessView // newest last, bounded
}

// bootTimer is optionally implemented by process listers.
type bootTimer interface {
	BootTime() (time.Time, bool)
}

func (e *Engine) processRunner() (*runner, collector.ProcessLister) {
	for _, r := range e.runners {
		if pl, ok := r.c.(collector.ProcessLister); ok && r.settings.Enabled {
			return r, pl
		}
	}
	return nil, nil
}

// Processes returns a current process view, sampling if the cached one is
// older than processCacheTTL.
func (e *Engine) Processes(ctx context.Context) *core.ProcessView {
	now := e.opts.Now()
	r, pl := e.processRunner()
	if pl == nil {
		return &core.ProcessView{TakenAt: now.UTC(), Status: core.CapabilityStatus{
			Availability: core.Unsupported, Reason: "No collector can list processes on this platform."}}
	}

	ps := &e.procs
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if n := len(ps.views); n > 0 && now.Sub(ps.views[n-1].TakenAt) < processCacheTTL {
		return ps.views[n-1]
	}

	sample := func() (core.ProcessList, core.CapabilityStatus) {
		type res struct {
			l core.ProcessList
			s core.CapabilityStatus
		}
		out, failed := call(ctx, r, e.opts.Logger, "list processes", func(c context.Context) res {
			l, s := pl.ListProcesses(c)
			return res{l, s}
		})
		if failed != "" {
			return core.ProcessList{}, core.CapabilityStatus{Availability: core.TemporarilyUnavailable, Reason: failed}
		}
		return out.l, out.s
	}

	if ps.prev == nil || now.Sub(ps.prev.TakenAt) > processPrevMaxAge {
		first, st := sample()
		if st.Availability != core.Available {
			return &core.ProcessView{TakenAt: now.UTC(), Status: st}
		}
		ps.prev = &first
		select {
		case <-time.After(processSampleGap):
		case <-ctx.Done():
			return &core.ProcessView{TakenAt: now.UTC(), Status: core.CapabilityStatus{
				Availability: core.TemporarilyUnavailable, Reason: "The request was cancelled."}}
		}
	}
	cur, st := sample()
	if st.Availability != core.Available {
		return &core.ProcessView{TakenAt: now.UTC(), Status: st}
	}
	view := e.deriveProcesses(ps.prev, &cur, pl)
	ps.prev = &cur
	ps.nextID++
	view.ID = ps.nextID
	ps.views = append(ps.views, view)
	// Keep a few recent views for cursors; drop old or excess ones.
	keep := ps.views[:0]
	for i, v := range ps.views {
		if len(ps.views)-i <= processViewsKept && now.Sub(v.TakenAt) <= processViewTTL {
			keep = append(keep, v)
		}
	}
	ps.views = keep
	return view
}

// ProcessView returns a recent view by ID (for cursors), or nil if it has
// expired.
func (e *Engine) ProcessView(id uint64) *core.ProcessView {
	ps := &e.procs
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, v := range ps.views {
		if v.ID == id && e.opts.Now().Sub(v.TakenAt) <= processViewTTL {
			return v
		}
	}
	return nil
}

func (e *Engine) deriveProcesses(prev, cur *core.ProcessList, pl collector.ProcessLister) *core.ProcessView {
	window := cur.TakenAt.Sub(prev.TakenAt).Seconds()
	type key struct {
		pid   int
		start uint64
	}
	before := make(map[key]uint64, len(prev.Processes))
	for _, p := range prev.Processes {
		before[key{p.PID, p.StartTicks}] = p.CPUTicks
	}
	var total uint64
	if m := e.Current().Memory.Data; m != nil {
		total = m.TotalBytes
	}
	var boot time.Time
	if bt, ok := pl.(bootTimer); ok {
		boot, _ = bt.BootTime()
	}

	view := &core.ProcessView{TakenAt: cur.TakenAt.UTC(), WindowSeconds: window, Partial: cur.Partial,
		Status: core.CapabilityStatus{Availability: core.Available}, Processes: make([]core.ProcessInfo, 0, len(cur.Processes))}
	for _, p := range cur.Processes {
		info := core.ProcessInfo{ProcessSample: p, User: cur.Users[p.UID]}
		if info.User == "" && p.UID >= 0 {
			info.User = strconv.Itoa(p.UID)
		}
		// Match on (pid, start time) so a reused PID is not mistaken for
		// the old process.
		if t, ok := before[key{p.PID, p.StartTicks}]; ok && window > 0 && p.CPUTicks >= t {
			pct := float64(p.CPUTicks-t) / userHZ / window * 100
			info.CPUPercent = &pct
		}
		if total > 0 {
			info.MemoryPercent = float64(p.RSSBytes) / float64(total) * 100
		}
		if !boot.IsZero() {
			info.StartedAt = boot.Add(time.Duration(p.StartTicks) * time.Second / userHZ)
		}
		view.Processes = append(view.Processes, info)
	}
	return view
}
