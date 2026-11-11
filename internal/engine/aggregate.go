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
	secServices
	secCPUInfo
	secPCI
	secUSB
	secBoard
	secMemoryModules
	secGPUs
	secRoutes
	secDNS
	secContainers
	secZFS
	secPower
	secUPS
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
	secServices:       {core.CapServicesSystemd},
	secCPUInfo:        {core.CapHardwareCPU},
	secPCI:            {core.CapHardwarePCI},
	secUSB:            {core.CapHardwareUSB},
	secBoard:          {core.CapHardwareBoard},
	secMemoryModules:  {core.CapHardwareMemory},
	secGPUs:           {core.CapGPUDevices},
	secRoutes:         {core.CapNetworkRoutes},
	secDNS:            {core.CapNetworkDNS},
	secContainers:     {core.CapContainers},
	secZFS:            {core.CapStorageZFS},
	secPower:          {core.CapPowerSupplies},
	secUPS:            {core.CapPowerUPS},
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
	case core.Services:
		return secServices, true
	case *core.CPUInfo:
		return secCPUInfo, true
	case core.PCIDevices:
		return secPCI, true
	case core.USBDevices:
		return secUSB, true
	case *core.BoardInfo:
		return secBoard, true
	case core.MemoryModules:
		return secMemoryModules, true
	case core.GPUs:
		return secGPUs, true
	case core.Routes:
		return secRoutes, true
	case *core.DNSConfig:
		return secDNS, true
	case core.ContainerSamples:
		return secContainers, true
	case *core.ZFSInfo:
		return secZFS, true
	case core.PowerSupplies:
		return secPower, true
	case core.UPSList:
		return secUPS, true
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
	prevCtr   map[string]core.ContainerSample
	prevCtrAt time.Time

	degraded map[string]bool  // "collector|capability" currently degraded
	pending  map[*runner]bool // scheduled collectors that have not reported yet

	custom map[*runner]*customState // collectors reporting generic metrics (D22)
}

func newAggregator(e *Engine, probes []map[core.Capability]core.CapabilityStatus) *aggregator {
	a := &aggregator{e: e, caps: map[string]map[core.Capability]core.CapabilityStatus{}}
	for s := range a.meta {
		a.meta[s] = core.SectionMeta{Status: core.Unsupported, Reason: "No collector provides this on this platform."}
	}
	for i, r := range e.runners {
		a.caps[r.desc.ID] = probes[i]
		if r.desc.GenericMetrics {
			if a.custom == nil {
				a.custom = map[*runner]*customState{}
			}
			a.custom[r] = newCustomState(r, probes[i])
		}
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
			if a.pending != nil {
				delete(a.pending, o.runner)
				if len(a.pending) == 0 {
					a.pending = nil
					a.e.markReady()
				}
			}
		}
	}
}

func (a *aggregator) apply(o outcome) {
	r := o.runner
	before := a.caps[r.desc.ID]
	if o.failed != "" {
		a.caps[r.desc.ID] = allStatus(r.desc, core.TemporarilyUnavailable, o.failed)
	} else {
		a.caps[r.desc.ID] = normalize(r.desc, o.result.Status)
	}
	caps := a.caps[r.desc.ID]
	a.transitions(r, before, caps)
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
	if cs := a.custom[r]; cs != nil {
		cs.apply(a.e.opts.Logger, r, caps, o.result.Records, at)
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
	case core.Services:
		c := append(core.Services(nil), v...)
		return &c
	case *core.CPUInfo:
		c := *v
		return &c
	case core.PCIDevices:
		c := append(core.PCIDevices(nil), v...)
		return &c
	case core.USBDevices:
		c := append(core.USBDevices(nil), v...)
		return &c
	case *core.BoardInfo:
		c := *v
		return &c
	case core.MemoryModules:
		c := append(core.MemoryModules(nil), v...)
		return &c
	case core.ContainerSamples:
		m := a.containers(v, at)
		return &m
	case *core.ZFSInfo:
		c := *v
		c.Pools = append([]core.ZFSPool(nil), v.Pools...)
		return &c
	case core.PowerSupplies:
		c := append(core.PowerSupplies(nil), v...)
		return &c
	case core.UPSList:
		c := append(core.UPSList(nil), v...)
		return &c
	case core.GPUs:
		c := append(core.GPUs(nil), v...)
		return &c
	case core.Routes:
		c := append(core.Routes(nil), v...)
		return &c
	case *core.DNSConfig:
		c := *v
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
	snap.Services = sec[core.Services](a, secServices)
	snap.CPUInfo = sec[core.CPUInfo](a, secCPUInfo)
	snap.PCI = sec[core.PCIDevices](a, secPCI)
	snap.USB = sec[core.USBDevices](a, secUSB)
	snap.Board = sec[core.BoardInfo](a, secBoard)
	snap.MemoryModules = sec[core.MemoryModules](a, secMemoryModules)
	snap.GPUs = sec[core.GPUs](a, secGPUs)
	snap.Routes = sec[core.Routes](a, secRoutes)
	snap.DNS = sec[core.DNSConfig](a, secDNS)
	snap.Containers = sec[[]core.ContainerMetrics](a, secContainers)
	snap.ZFS = sec[core.ZFSInfo](a, secZFS)
	snap.Power = sec[core.PowerSupplies](a, secPower)
	snap.UPS = sec[core.UPSList](a, secUPS)

	for _, r := range a.e.runners {
		if cs := a.custom[r]; cs != nil {
			snap.Custom = append(snap.Custom, cs.set)
		}
	}
	for _, r := range a.e.runners {
		caps := make(map[core.Capability]core.CapabilityStatus, len(a.caps[r.desc.ID]))
		for k, v := range a.caps[r.desc.ID] {
			caps[k] = v
		}
		snap.Collectors = append(snap.Collectors, core.CollectorStatus{ID: r.desc.ID, Version: r.desc.Version, Capabilities: caps,
			External: r.desc.External, SDKVersion: r.desc.SDKVersion})
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

// transitions publishes an event when a capability that was available
// stops being available, or comes back. Capabilities that were never
// available (missing hardware) produce no events.
func (a *aggregator) transitions(r *runner, before, after map[core.Capability]core.CapabilityStatus) {
	if a.e.opts.Publish == nil {
		return
	}
	for _, c := range r.desc.Capabilities {
		key := r.desc.ID + "|" + string(c)
		was, now := before[c].Availability, after[c].Availability
		switch {
		case was == core.Available && now != core.Available:
			if a.degraded == nil {
				a.degraded = map[string]bool{}
			}
			a.degraded[key] = true
			a.e.opts.Publish(core.Event{Time: a.e.opts.Now().UTC(), Type: core.EventCollectorDegraded, Severity: core.SeverityWarning,
				Subject: "collector:" + r.desc.ID, Message: string(c) + " is " + string(now) + ": " + after[c].Reason})
		case now == core.Available && a.degraded[key]:
			delete(a.degraded, key)
			a.e.opts.Publish(core.Event{Time: a.e.opts.Now().UTC(), Type: core.EventCollectorRecovered, Severity: core.SeverityInfo,
				Subject: "collector:" + r.desc.ID, Message: string(c) + " is available again"})
		}
	}
}

// containers derives CPU and I/O rates per container ID.
func (a *aggregator) containers(cur core.ContainerSamples, at time.Time) []core.ContainerMetrics {
	window := 0.0
	if a.prevCtr != nil && at.After(a.prevCtrAt) {
		window = at.Sub(a.prevCtrAt).Seconds()
	}
	rate := func(now, before *uint64) *float64 {
		if now == nil || before == nil || *now < *before || window <= 0 {
			return nil
		}
		v := float64(*now-*before) / window
		return &v
	}
	out := make([]core.ContainerMetrics, len(cur))
	next := make(map[string]core.ContainerSample, len(cur))
	for i, c := range cur {
		out[i] = core.ContainerMetrics{ContainerSample: c}
		if p, ok := a.prevCtr[c.ID]; ok && window > 0 {
			out[i].WindowSeconds = window
			if c.CPUUsageUsec >= p.CPUUsageUsec {
				v := float64(c.CPUUsageUsec-p.CPUUsageUsec) / 1e6 / window * 100
				out[i].CPUPercent = &v
			}
			out[i].IOReadBytesPerSecond = rate(c.IOReadBytes, p.IOReadBytes)
			out[i].IOWriteBytesPerSecond = rate(c.IOWriteBytes, p.IOWriteBytes)
		}
		next[c.ID] = c
	}
	a.prevCtr, a.prevCtrAt = next, at
	return out
}
