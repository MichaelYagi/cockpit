package engine

import (
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// customState tracks one collector's generic metrics (D22).
type customState struct {
	set     core.CustomSet
	prev    map[string]float64 // counter values by series key
	prevAt  time.Time
	history map[string]bool
	logged  string // last problem logged, to avoid repeating it
}

func newCustomState(r *runner, probe map[core.Capability]core.CapabilityStatus) *customState {
	cs := &customState{history: map[string]bool{}}
	for _, n := range r.desc.HistoryMetrics {
		cs.history[n] = true
	}
	cs.set = core.CustomSet{Collector: r.desc.ID, Meta: bestMeta(r, probe, core.SectionMeta{Source: r.desc.Source})}
	return cs
}

// bestMeta sets meta's status to the best status of the collector's
// declared capabilities.
func bestMeta(r *runner, caps map[core.Capability]core.CapabilityStatus, meta core.SectionMeta) core.SectionMeta {
	best := core.CapabilityStatus{Availability: core.Unsupported, Reason: "Not reported."}
	for i, c := range r.desc.Capabilities {
		if st, ok := caps[c]; ok && (i == 0 || rank(st.Availability) < rank(best.Availability)) {
			best = st
		}
	}
	meta.Status, meta.Reason = best.Availability, best.Reason
	if best.Availability == core.Available {
		meta.Reason = ""
	}
	return meta
}

// apply validates and stores a collection result. Invalid metrics and
// series over the per-collector limit are dropped and counted; the rest
// are published. After a failure the last metrics are kept and go stale.
func (cs *customState) apply(log *slog.Logger, r *runner, caps map[core.Capability]core.CapabilityStatus, records []core.Record, at time.Time) {
	meta := bestMeta(r, caps, cs.set.Meta)
	var rec core.GenericMetrics
	have := false
	for _, x := range records {
		if g, ok := x.(core.GenericMetrics); ok {
			rec, have = g, true
			break
		}
	}
	switch {
	case meta.Status == core.Available && have:
		cs.set.Metrics, cs.set.Dropped, cs.set.Problem = cs.process(rec, r.desc.ID, at)
		meta.CollectedAt, meta.StaleAfter = at, time.Time{}
		if r.settings.Interval > 0 {
			meta.StaleAfter = at.Add(2 * r.settings.Interval)
		}
		if cs.set.Problem != cs.logged && log != nil {
			if cs.set.Problem != "" {
				log.Warn("collector metrics dropped", "collector", r.desc.ID, "dropped", cs.set.Dropped, "first_problem", cs.set.Problem)
			}
			cs.logged = cs.set.Problem
		}
	case meta.Status == core.Available:
		meta.Status, meta.Reason = core.TemporarilyUnavailable, "The collector returned no data."
	}
	cs.set.Meta = meta
}

func (cs *customState) process(rec core.GenericMetrics, namespace string, at time.Time) ([]core.CustomMetric, int, string) {
	window := 0.0
	if cs.prev != nil && at.After(cs.prevAt) {
		window = at.Sub(cs.prevAt).Seconds()
	}
	type keyed struct {
		key string
		m   core.CustomMetric
	}
	var kept []keyed
	seen := map[string]bool{}
	next := map[string]float64{}
	dropped, problem := 0, ""
	drop := func(msg string) {
		dropped++
		if problem == "" {
			problem = msg
		}
	}
	for _, m := range rec {
		if err := m.Validate(namespace); err != nil {
			drop(err.Error())
			continue
		}
		key := m.SeriesKey()
		switch {
		case seen[key]:
			drop(fmt.Sprintf("metric %s was reported twice", key))
			continue
		case len(kept) >= core.MaxSeriesPerCollector:
			drop(fmt.Sprintf("more than %d series; the rest were dropped", core.MaxSeriesPerCollector))
			continue
		}
		seen[key] = true
		cm := core.CustomMetric{Name: m.Name, Kind: m.Kind, Unit: m.Unit, Labels: maps.Clone(m.Labels), Value: m.Value, History: cs.history[m.Name]}
		if m.Kind == core.Counter {
			next[key] = m.Value
			// A counter that went backwards was reset; skip one rate.
			if p, ok := cs.prev[key]; ok && window > 0 && m.Value >= p {
				v := (m.Value - p) / window
				cm.RatePerSecond = &v
			}
		}
		kept = append(kept, keyed{key, cm})
	}
	cs.prev, cs.prevAt = next, at
	sort.Slice(kept, func(i, j int) bool { return kept[i].key < kept[j].key })
	out := make([]core.CustomMetric, len(kept))
	for i, k := range kept {
		out[i] = k.m
	}
	return out, dropped, problem
}
