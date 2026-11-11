package engine

import (
	"context"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// section identifies one snapshot section and the capability whose status
// it reports.
type section int

const (
	secSystem section = iota
	secCPU
	secMemory
	secMemoryPressure
	secFilesystems
	secNetwork
	secBlockDevices
	secSensors
	secDiskHealth
	numSections
)

// primary capabilities per section. A section takes the best status of its
// capabilities (sensors: thermal or fan).
var sectionCaps = [numSections][]core.Capability{
	secSystem:         {core.CapSystemIdentity},
	secCPU:            {core.CapCPUUsage},
	secMemory:         {core.CapMemoryUsage},
	secMemoryPressure: {core.CapMemoryPressure},
	secFilesystems:    {core.CapStorageFilesystems},
	secNetwork:        {core.CapNetworkInterfaces},
	secBlockDevices:   {core.CapStorageDevices},
	secSensors:        {core.CapSensorsThermal, core.CapSensorsFan},
	secDiskHealth:     {core.CapStorageSMART},
}

func sectionOf(r core.Record) (section, bool) {
	switch r.(type) {
	case *core.SystemInfo:
		return secSystem, true
	case *core.CPUCounters:
		return secCPU, true
	case *core.MemoryStats:
		return secMemory, true
	case *core.MemoryPressure:
		return secMemoryPressure, true
	case core.Filesystems:
		return secFilesystems, true
	case core.NetInterfaces:
		return secNetwork, true
	case core.BlockDevices:
		return secBlockDevices, true
	case core.Sensors:
		return secSensors, true
	case core.DiskHealthList:
		return secDiskHealth, true
	}
	return 0, false
}

// availability rank: lower is better, used to merge statuses.
func rank(a core.Availability) int {
	switch a {
	case core.Available:
		return 0
	case core.TemporarilyUnavailable:
		return 1
	case core.PermissionDenied:
		return 2
	case core.NotConfigured:
		return 3
	}
	return 4 // unsupported
}

type aggregator struct {
	e          *Engine
	generation uint64

	meta  [numSections]core.SectionMeta
	owner [numSections]*runner // collector that provides each section
	data  [numSections]any     // *T of the section's data type

	caps map[string]map[core.Capability]core.CapabilityStatus // by collector ID

	prevCPU   *core.CPUCounters
	prevCPUAt time.Time
	prevNet   map[string]core.NetInterface
	prevNetAt time.Time
}

func newAggregator(e *Engine, probes []map[core.Capability]core.CapabilityStatus) *aggregator {
	a := &aggregator{e: e, caps: map[string]map[core.Capability]core.CapabilityStatus{}}
	for s := range a.meta {
		a.meta[s] = core.SectionMeta{Status: core.Unsupported, Reason: "No collector provides this on this platform."}
	}
	for i, r := range e.runners {
		a.caps[r.desc.ID] = probes[i]
		for s := section(0); s < numSections; s++ {
			if provides(r.desc, s) && a.owner[s] == nil {
				a.owner[s] = r
				a.meta[s] = a.statusMeta(s, probes[i], core.SectionMeta{Source: r.desc.Source})
			}
		}
	}
	return a
}

func provides(d collector.Descriptor, s section) bool {
	for _, want := range sectionCaps[s] {
		for _, c := range d.Capabilities {
			if c == want {
				return true
			}
		}
	}
	return false
}

// statusMeta merges the section's capability statuses into meta, keeping
// collected-at and staleness information.
func (a *aggregator) statusMeta(s section, caps map[core.Capability]core.CapabilityStatus, meta core.SectionMeta) core.SectionMeta {
	best := core.CapabilityStatus{Availability: core.Unsupported, Reason: "Not reported."}
	found := false
	for _, c := range sectionCaps[s] {
		if st, ok := caps[c]; ok && (!found || rank(st.Availability) < rank(best.Availability)) {
			best, found = st, true
		}
	}
	meta.Status, meta.Reason = best.Availability, best.Reason
	if best.Availability == core.Available {
		meta.Reason = ""
	}
	return meta
}

func (a *aggregator) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-a.e.results:
			a.apply(o)
			a.e.current.Store(a.publish())
		}
	}
}

func (a *aggregator) apply(o outcome) {
	r := o.runner
	if o.failed != "" {
		a.caps[r.desc.ID] = allStatus(r.desc, core.TemporarilyUnavailable, o.failed)
	} else {
		a.caps[r.desc.ID] = normalize(r.desc, o.result.Status)
	}
	caps := a.caps[r.desc.ID]
	at := o.result.CollectedAt.UTC()
	if at.IsZero() {
		at = a.e.opts.Now().UTC()
	}

	got := map[section]core.Record{}
	for _, rec := range o.result.Records {
		if s, ok := sectionOf(rec); ok && a.owner[s] == r {
			got[s] = rec
		}
	}

	for s := section(0); s < numSections; s++ {
		if a.owner[s] != r {
			continue
		}
		meta := a.statusMeta(s, caps, a.meta[s])
		rec, have := got[s]
		switch {
		case meta.Status == core.Available && have:
			a.data[s] = a.derive(s, rec, at)
			meta.CollectedAt = at
			meta.StaleAfter = time.Time{}
			if r.settings.Interval > 0 {
				meta.StaleAfter = at.Add(2 * r.settings.Interval)
			}
		case meta.Status == core.Available:
			meta.Status = core.TemporarilyUnavailable
			meta.Reason = "The collector returned no data."
		}
		// Otherwise the last good data (if any) is kept and becomes stale.
		a.meta[s] = meta
	}
}

// derive converts a record into section data, computing rates.
func (a *aggregator) derive(s section, rec core.Record, at time.Time) any {
	switch v := rec.(type) {
	case *core.SystemInfo:
		c := *v
		return &c
	case *core.CPUCounters:
		m := a.cpu(v, at)
		return &m
	case *core.MemoryStats:
		m := memoryMetrics(*v)
		return &m
	case *core.MemoryPressure:
		c := *v
		return &c
	case core.Filesystems:
		c := append(core.Filesystems(nil), v...)
		return &c
	case core.NetInterfaces:
		m := a.network(v, at)
		return &m
	case core.BlockDevices:
		c := append(core.BlockDevices(nil), v...)
		return &c
	case core.Sensors:
		c := append(core.Sensors(nil), v...)
		return &c
	case core.DiskHealthList:
		c := append(core.DiskHealthList(nil), v...)
		return &c
	}
	return nil
}

func (a *aggregator) cpu(cur *core.CPUCounters, at time.Time) core.CPUMetrics {
	m := core.CPUMetrics{LogicalCores: cur.LogicalCores, ModelName: cur.ModelName}
	if cur.Load != nil {
		l := *cur.Load
		m.Load = &l
	}
	if prev := a.prevCPU; prev != nil && at.After(a.prevCPUAt) {
		m.WindowSeconds = at.Sub(a.prevCPUAt).Seconds()
		if u, ok := usage(prev.Total, cur.Total); ok {
			m.UsagePercent = &u
		}
		if len(prev.PerCore) == len(cur.PerCore) {
			per := make([]float64, len(cur.PerCore))
			ok := true
			for i := range cur.PerCore {
				if per[i], ok = usage(prev.PerCore[i], cur.PerCore[i]); !ok {
					break
				}
			}
			if ok {
				m.PerCoreUsagePercent = per
			}
		}
	}
	c := *cur
	c.PerCore = append([]core.CPUTimes(nil), cur.PerCore...)
	a.prevCPU, a.prevCPUAt = &c, at
	return m
}

// usage returns busy percent between two samples; ok=false if counters went
// backwards (reset) or did not advance.
func usage(prev, cur core.CPUTimes) (float64, bool) {
	if cur.Total() <= prev.Total() || cur.Busy() < prev.Busy() {
		return 0, false
	}
	pct := float64(cur.Busy()-prev.Busy()) / float64(cur.Total()-prev.Total()) * 100
	return clamp(pct, 0, 100), true
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func memoryMetrics(s core.MemoryStats) core.MemoryMetrics {
	m := core.MemoryMetrics{MemoryStats: s}
	if s.TotalBytes > 0 && s.AvailableBytes <= s.TotalBytes {
		m.UsedBytes = s.TotalBytes - s.AvailableBytes
		m.UsedPercent = float64(m.UsedBytes) / float64(s.TotalBytes) * 100
	}
	if s.SwapTotalBytes > 0 && s.SwapFreeBytes <= s.SwapTotalBytes {
		m.SwapUsedBytes = s.SwapTotalBytes - s.SwapFreeBytes
		m.SwapUsedPercent = float64(m.SwapUsedBytes) / float64(s.SwapTotalBytes) * 100
	}
	return m
}

func (a *aggregator) network(cur core.NetInterfaces, at time.Time) []core.NetInterfaceMetrics {
	out := make([]core.NetInterfaceMetrics, len(cur))
	window := 0.0
	if a.prevNet != nil && at.After(a.prevNetAt) {
		window = at.Sub(a.prevNetAt).Seconds()
	}
	next := make(map[string]core.NetInterface, len(cur))
	for i, n := range cur {
		out[i] = core.NetInterfaceMetrics{NetInterface: n}
		if p, ok := a.prevNet[n.Name]; ok && window > 0 {
			out[i].WindowSeconds = window
			if n.RxBytes >= p.RxBytes {
				v := float64(n.RxBytes-p.RxBytes) / window
				out[i].RxBytesPerSecond = &v
			}
			if n.TxBytes >= p.TxBytes {
				v := float64(n.TxBytes-p.TxBytes) / window
				out[i].TxBytesPerSecond = &v
			}
		}
		next[n.Name] = n
	}
	a.prevNet, a.prevNetAt = next, at
	return out
}

func (a *aggregator) publish() *core.Snapshot {
	a.generation++
	snap := &core.Snapshot{Generation: a.generation, GeneratedAt: a.e.opts.Now().UTC()}
	snap.System = sec[core.SystemInfo](a, secSystem)
	snap.CPU = sec[core.CPUMetrics](a, secCPU)
	snap.Memory = sec[core.MemoryMetrics](a, secMemory)
	snap.MemoryPressure = sec[core.MemoryPressure](a, secMemoryPressure)
	snap.Filesystems = sec[core.Filesystems](a, secFilesystems)
	snap.Network = sec[[]core.NetInterfaceMetrics](a, secNetwork)
	snap.BlockDevices = sec[core.BlockDevices](a, secBlockDevices)
	snap.Sensors = sec[core.Sensors](a, secSensors)
	snap.DiskHealth = sec[core.DiskHealthList](a, secDiskHealth)

	for _, r := range a.e.runners {
		caps := make(map[core.Capability]core.CapabilityStatus, len(a.caps[r.desc.ID]))
		for k, v := range a.caps[r.desc.ID] {
			caps[k] = v
		}
		snap.Collectors = append(snap.Collectors, core.CollectorStatus{ID: r.desc.ID, Version: r.desc.Version, Capabilities: caps})
	}
	return snap
}

// sec builds a section. Section data values are never mutated after
// derive, so sharing the pointer across snapshots is safe.
func sec[T any](a *aggregator, s section) core.Section[T] {
	out := core.Section[T]{Meta: a.meta[s]}
	if d, ok := a.data[s].(*T); ok {
		out.Data = d
	}
	return out
}
