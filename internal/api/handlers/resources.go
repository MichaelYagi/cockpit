package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/api/redact"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// SnapshotSource provides the latest engine snapshot.
type SnapshotSource interface {
	Current() *core.Snapshot
}

// Resources serves the machine resources from engine snapshots.
type Resources struct {
	Snapshots SnapshotSource
	MachineID core.MachineID
	Policy    redact.Policy
	Now       func() time.Time
}

func (h *Resources) meta(now time.Time) apiv1.Meta {
	return apiv1.Meta{SchemaVersion: apiv1.SchemaVersion, MachineID: string(h.MachineID), GeneratedAt: now}
}

// section maps a core section to a wire section, converting data with conv.
func section[T, U any](s core.Section[T], now time.Time, conv func(*T) *U) apiv1.Section[U] {
	out := apiv1.Section[U]{
		Status: string(s.Meta.Status),
		Stale:  s.Meta.IsStale(now),
		Source: s.Meta.Source,
		Reason: s.Meta.Reason,
	}
	if out.Status == "" { // a section no collector has filled in
		out.Status, out.Reason = string(core.Unsupported), "Not collected on this platform."
	}
	if !s.Meta.CollectedAt.IsZero() {
		t := s.Meta.CollectedAt.UTC()
		out.CollectedAt = &t
	}
	if s.Data != nil {
		out.Data = conv(s.Data)
	}
	return out
}

// derived maps a section's metadata onto data derived from another
// section (e.g. CPU inventory from CPU metrics).
func derived[T, U any](s core.Section[T], now time.Time, conv func(*T) *U) apiv1.Section[U] {
	out := section(s, now, conv)
	out.Stale = false // inventory facts do not go stale with the sample
	return out
}

// ---- /system ----

// System serves GET /system.
func (h *Resources) System(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.system(h.Snapshots.Current(), h.Now().UTC()))
}

func (h *Resources) system(s *core.Snapshot, now time.Time) apiv1.SystemResponse {
	return apiv1.SystemResponse{
		Meta: h.meta(now),
		System: section(s.System, now, func(v *core.SystemInfo) *apiv1.System {
			out := &apiv1.System{
				Hostname:     v.Hostname,
				OS:           apiv1.OS{ID: v.OSID, Name: v.OSName, Version: v.OSVersion, PrettyName: v.OSPrettyName},
				Kernel:       v.Kernel,
				Architecture: v.Architecture,
			}
			if !v.BootTime.IsZero() {
				bt := v.BootTime.UTC()
				up := now.Sub(bt).Seconds()
				out.BootTime, out.UptimeSeconds = &bt, &up
			}
			return out
		}),
	}
}

// ---- /hardware ----

// Hardware serves GET /hardware.
func (h *Resources) Hardware(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.hardware(h.Snapshots.Current(), h.Now().UTC()))
}

func (h *Resources) hardware(s *core.Snapshot, now time.Time) apiv1.HardwareResponse {
	return apiv1.HardwareResponse{
		Meta: h.meta(now),
		CPU:  h.cpuInventory(s, now),
		MemoryModules: section(s.MemoryModules, now, func(v *core.MemoryModules) *[]apiv1.MemoryModule {
			out := make([]apiv1.MemoryModule, 0, len(*v))
			for _, m := range *v {
				mm := apiv1.MemoryModule{Locator: m.Locator, SizeBytes: m.SizeBytes, Type: m.Type, SpeedMTs: m.SpeedMTs,
					Manufacturer: m.Manufacturer, PartNumber: m.PartNumber}
				h.Policy.Field(redact.Serial, m.Serial, &mm.Serial, &mm.Redacted)
				out = append(out, mm)
			}
			return &out
		}),
		Board: section(s.Board, now, func(v *core.BoardInfo) *apiv1.Board {
			b := &apiv1.Board{SystemVendor: v.SystemVendor, ProductName: v.ProductName, BoardVendor: v.BoardVendor, BoardName: v.BoardName,
				BIOSVendor: v.BIOSVendor, BIOSVersion: v.BIOSVersion, BIOSDate: v.BIOSDate, ChassisType: v.ChassisType}
			h.Policy.Field(redact.Serial, v.Serial, &b.Serial, &b.Redacted)
			return b
		}),
		PCIDevices: section(s.PCI, now, func(v *core.PCIDevices) *[]apiv1.PCIDevice {
			out := make([]apiv1.PCIDevice, 0, len(*v))
			for _, d := range *v {
				out = append(out, apiv1.PCIDevice{Address: d.Address, ClassCode: d.ClassCode, ClassName: d.ClassName, VendorID: d.VendorID,
					DeviceID: d.DeviceID, VendorName: d.VendorName, DeviceName: d.DeviceName, Driver: d.Driver})
			}
			return &out
		}),
		USBDevices: section(s.USB, now, func(v *core.USBDevices) *[]apiv1.USBDevice {
			out := make([]apiv1.USBDevice, 0, len(*v))
			for _, d := range *v {
				u := apiv1.USBDevice{Path: d.Path, VendorID: d.VendorID, ProductID: d.ProductID, Manufacturer: d.Manufacturer,
					Product: d.Product, SpeedMbps: d.SpeedMbps}
				h.Policy.Field(redact.Serial, d.Serial, &u.Serial, &u.Redacted)
				out = append(out, u)
			}
			return &out
		}),
		GPUs: section(s.GPUs, now, h.gpus),
		Memory: derived(s.Memory, now, func(v *core.MemoryMetrics) *apiv1.MemoryInventory {
			return &apiv1.MemoryInventory{TotalBytes: v.TotalBytes, SwapTotalBytes: v.SwapTotalBytes}
		}),
		BlockDevices: section(s.BlockDevices, now, h.blockDevices),
		NetworkInterfaces: derived(s.Network, now, func(v *[]core.NetInterfaceMetrics) *[]apiv1.NetworkInterfaceInventory {
			out := make([]apiv1.NetworkInterfaceInventory, 0, len(*v))
			for _, n := range *v {
				inv := apiv1.NetworkInterfaceInventory{Name: n.Name, MTU: n.MTU, Loopback: n.Loopback, SpeedMbps: n.SpeedMbps}
				h.Policy.Field(redact.MACAddress, n.MACAddress, &inv.MACAddress, &inv.Redacted)
				out = append(out, inv)
			}
			return &out
		}),
	}
}

// cpuInventory prefers the detailed CPU collector and falls back to what
// the CPU metrics collector knows.
func (h *Resources) cpuInventory(s *core.Snapshot, now time.Time) apiv1.Section[apiv1.CPUInventory] {
	if s.CPUInfo.Data != nil || s.CPU.Data == nil {
		return derived(s.CPUInfo, now, func(v *core.CPUInfo) *apiv1.CPUInventory {
			inv := &apiv1.CPUInventory{ModelName: v.ModelName, LogicalCores: v.Threads, Vendor: v.Vendor, Family: v.Family,
				Model: v.Model, Stepping: v.Stepping, Sockets: v.Sockets, Cores: v.Cores, MaxFrequencyMHz: v.MaxFrequencyMHz,
				Virtualization: v.Virtualization, Hypervisor: v.Hypervisor, Flags: v.Flags, Caches: []apiv1.CPUCache{}}
			if inv.Flags == nil {
				inv.Flags = []string{}
			}
			for _, c := range v.Caches {
				inv.Caches = append(inv.Caches, apiv1.CPUCache{Level: c.Level, Type: c.Type, SizeBytes: c.SizeBytes, SharedBy: c.SharedBy})
			}
			return inv
		})
	}
	return derived(s.CPU, now, func(v *core.CPUMetrics) *apiv1.CPUInventory {
		return &apiv1.CPUInventory{ModelName: v.ModelName, LogicalCores: v.LogicalCores, Cores: v.LogicalCores, Sockets: 1,
			Caches: []apiv1.CPUCache{}, Flags: []string{}}
	})
}

func (h *Resources) gpus(v *core.GPUs) *[]apiv1.GPU {
	out := make([]apiv1.GPU, 0, len(*v))
	for _, g := range *v {
		a := apiv1.GPU{Vendor: g.Vendor, Name: g.Name, PCIAddress: g.PCIAddress, Driver: g.Driver, DriverVersion: g.DriverVersion,
			MemoryTotalBytes: g.MemoryTotalBytes, MemoryUsedBytes: g.MemoryUsedBytes, UtilizationPercent: g.UtilizationPercent,
			TemperatureCelsius: g.TemperatureCelsius, PowerWatts: g.PowerWatts, PowerLimitWatts: g.PowerLimitWatts,
			FanPercent: g.FanPercent, MetricsSource: g.MetricsSource}
		h.Policy.Field(redact.UUID, g.UUID, &a.UUID, &a.Redacted)
		out = append(out, a)
	}
	return &out
}

func (h *Resources) blockDevices(v *core.BlockDevices) *[]apiv1.BlockDevice {
	out := make([]apiv1.BlockDevice, 0, len(*v))
	for _, d := range *v {
		b := apiv1.BlockDevice{Name: d.Name, Model: d.Model, Transport: d.Transport, Rotational: d.Rotational,
			Removable: d.Removable, SizeBytes: d.SizeBytes}
		h.Policy.Field(redact.Serial, d.Serial, &b.Serial, &b.Redacted)
		h.Policy.Field(redact.WWN, d.WWN, &b.WWN, &b.Redacted)
		out = append(out, b)
	}
	return &out
}

// ---- /metrics ----

func cpuMetrics(v *core.CPUMetrics) *apiv1.CPUMetrics {
	out := &apiv1.CPUMetrics{UsagePercent: v.UsagePercent, PerCoreUsagePercent: v.PerCoreUsagePercent,
		WindowSeconds: v.WindowSeconds, LogicalCores: v.LogicalCores}
	if v.Load != nil {
		out.LoadAverage = &apiv1.LoadAverage{One: v.Load.One, Five: v.Load.Five, Fifteen: v.Load.Fifteen}
	}
	return out
}

func memoryMetrics(v *core.MemoryMetrics) *apiv1.MemoryMetrics {
	return &apiv1.MemoryMetrics{
		TotalBytes: v.TotalBytes, AvailableBytes: v.AvailableBytes, UsedBytes: v.UsedBytes, UsedPercent: v.UsedPercent,
		FreeBytes: v.FreeBytes, BuffersBytes: v.BuffersBytes, CachedBytes: v.CachedBytes,
		SwapTotalBytes: v.SwapTotalBytes, SwapUsedBytes: v.SwapUsedBytes, SwapUsedPercent: v.SwapUsedPercent,
	}
}

func memoryPressure(v *core.MemoryPressure) *apiv1.MemoryPressure {
	return &apiv1.MemoryPressure{
		SomeAvg10Percent: v.SomeAvg10, SomeAvg60Percent: v.SomeAvg60, SomeAvg300Percent: v.SomeAvg300,
		FullAvg10Percent: v.FullAvg10, FullAvg60Percent: v.FullAvg60, FullAvg300Percent: v.FullAvg300,
	}
}

func filesystems(v *core.Filesystems) *[]apiv1.Filesystem {
	out := make([]apiv1.Filesystem, 0, len(*v))
	for _, f := range *v {
		fs := apiv1.Filesystem{MountPoint: f.MountPoint, Device: f.Device, Type: f.Type, ReadOnly: f.ReadOnly,
			TotalBytes: f.TotalBytes, AvailableBytes: f.AvailableBytes, InodesTotal: f.TotalInodes, InodesFree: f.FreeInodes}
		if f.FreeBytes <= f.TotalBytes {
			fs.UsedBytes = f.TotalBytes - f.FreeBytes
			if d := fs.UsedBytes + f.AvailableBytes; d > 0 {
				fs.UsedPercent = float64(fs.UsedBytes) / float64(d) * 100
			}
		}
		if f.TotalInodes > 0 && f.FreeInodes <= f.TotalInodes {
			p := float64(f.TotalInodes-f.FreeInodes) / float64(f.TotalInodes) * 100
			fs.InodesUsedPercent = &p
		}
		out = append(out, fs)
	}
	return &out
}

func (h *Resources) network(v *[]core.NetInterfaceMetrics) *[]apiv1.NetworkInterface {
	out := make([]apiv1.NetworkInterface, 0, len(*v))
	for _, n := range *v {
		ni := apiv1.NetworkInterface{
			Name: n.Name, OperState: n.OperState, Loopback: n.Loopback, MTU: n.MTU, SpeedMbps: n.SpeedMbps,
			RxBytes: n.RxBytes, TxBytes: n.TxBytes, RxPackets: n.RxPackets, TxPackets: n.TxPackets,
			RxErrors: n.RxErrors, TxErrors: n.TxErrors, RxDropped: n.RxDropped, TxDropped: n.TxDropped,
			RxBytesPerSecond: n.RxBytesPerSecond, TxBytesPerSecond: n.TxBytesPerSecond, RateWindowSeconds: n.WindowSeconds,
		}
		h.Policy.Field(redact.MACAddress, n.MACAddress, &ni.MACAddress, &ni.Redacted)
		ni.Addresses = append([]string{}, n.Addresses...)
		out = append(out, ni)
	}
	return &out
}

func sensors(v *core.Sensors) *[]apiv1.Sensor {
	out := make([]apiv1.Sensor, 0, len(*v))
	for _, s := range *v {
		out = append(out, apiv1.Sensor{Kind: string(s.Kind), Chip: s.Chip, Label: s.Label, Value: s.Value, Unit: s.Unit,
			High: s.High, Critical: s.Critical, Source: s.Source, CPU: s.CPU})
	}
	return &out
}

func diskHealth(v *core.DiskHealthList) *[]apiv1.DiskHealth {
	out := make([]apiv1.DiskHealth, 0, len(*v))
	for _, d := range *v {
		out = append(out, apiv1.DiskHealth{Device: d.Device, Protocol: d.Protocol, SmartPassed: d.Passed,
			TemperatureCelsius: d.TemperatureCelsius, PowerOnHours: d.PowerOnHours, PercentageUsed: d.PercentageUsed,
			CriticalWarning: d.CriticalWarning, ReallocatedSectors: d.ReallocatedSectors, PendingSectors: d.PendingSectors,
			MediaErrors: d.MediaErrors})
	}
	return &out
}

func ptrTo[T any](v T) *T { return &v }

// Metrics serves GET /metrics.
func (h *Resources) Metrics(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r, "sections") {
		return
	}
	want := map[string]bool{}
	if raw := r.URL.Query().Get("sections"); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			valid := false
			for _, s := range apiv1.MetricSections {
				if s == name {
					valid = true
				}
			}
			if !valid {
				badParam(w, r, "sections", "unknown section "+quote(name)+"; valid sections are "+strings.Join(apiv1.MetricSections, ", "))
				return
			}
			want[name] = true
		}
	} else {
		for _, s := range apiv1.MetricSections {
			want[s] = true
		}
	}
	httpx.WriteJSON(w, http.StatusOK, h.metrics(h.Snapshots.Current(), h.Now().UTC(), want))
}

func (h *Resources) metrics(s *core.Snapshot, now time.Time, want map[string]bool) apiv1.MetricsResponse {
	out := apiv1.MetricsResponse{Meta: h.meta(now)}
	if want["cpu"] {
		out.CPU = ptrTo(section(s.CPU, now, cpuMetrics))
	}
	if want["memory"] {
		out.Memory = ptrTo(section(s.Memory, now, memoryMetrics))
	}
	if want["memory_pressure"] {
		out.MemoryPressure = ptrTo(section(s.MemoryPressure, now, memoryPressure))
	}
	if want["filesystems"] {
		out.Filesystems = ptrTo(section(s.Filesystems, now, filesystems))
	}
	if want["network"] {
		out.Network = ptrTo(section(s.Network, now, h.network))
	}
	if want["sensors"] {
		out.Sensors = ptrTo(section(s.Sensors, now, sensors))
	}
	if want["gpu"] {
		out.GPU = ptrTo(section(s.GPUs, now, h.gpus))
	}
	if want["custom"] {
		out.Custom = ptrTo(customSets(s.Custom, now))
	}
	if want["power"] {
		out.Power = ptrTo(section(s.Power, now, power))
	}
	if want["ups"] {
		out.UPS = ptrTo(section(s.UPS, now, upsList))
	}
	return out
}

// Storage serves GET /storage.
func (h *Resources) Storage(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.storage(h.Snapshots.Current(), h.Now().UTC()))
}

func (h *Resources) storage(s *core.Snapshot, now time.Time) apiv1.StorageResponse {
	return apiv1.StorageResponse{
		Meta:         h.meta(now),
		BlockDevices: section(s.BlockDevices, now, h.blockDevices),
		Filesystems:  section(s.Filesystems, now, filesystems),
		DiskHealth:   section(s.DiskHealth, now, diskHealth),
		ZFS:          section(s.ZFS, now, zfs),
	}
}

// Network serves GET /network.
func (h *Resources) Network(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.networkResponse(h.Snapshots.Current(), h.Now().UTC()))
}

func (h *Resources) networkResponse(s *core.Snapshot, now time.Time) apiv1.NetworkResponse {
	return apiv1.NetworkResponse{
		Meta:       h.meta(now),
		Interfaces: section(s.Network, now, h.network),
		Routes: section(s.Routes, now, func(v *core.Routes) *[]apiv1.Route {
			out := make([]apiv1.Route, 0, len(*v))
			for _, r := range *v {
				out = append(out, apiv1.Route{Family: r.Family, Destination: r.Destination, Gateway: r.Gateway, Interface: r.Interface,
					Metric: r.Metric, Default: r.Default})
			}
			return &out
		}),
		DNS: section(s.DNS, now, func(v *core.DNSConfig) *apiv1.DNS {
			d := &apiv1.DNS{Nameservers: append([]string{}, v.Nameservers...), Search: append([]string{}, v.Search...), Resolver: v.Resolver}
			return d
		}),
	}
}

// Sensors serves GET /sensors.
func (h *Resources) Sensors(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	now := h.Now().UTC()
	httpx.WriteJSON(w, http.StatusOK, apiv1.SensorsResponse{Meta: h.meta(now), Sensors: section(h.Snapshots.Current().Sensors, now, sensors)})
}

// Services serves GET /services.
func (h *Resources) Services(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r, "state") {
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "active", "failed", "inactive":
	default:
		badParam(w, r, "state", "must be active, failed or inactive")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.services(h.Snapshots.Current(), h.Now().UTC(), state))
}

func (h *Resources) services(s *core.Snapshot, now time.Time, state string) apiv1.ServicesResponse {
	return apiv1.ServicesResponse{
		Meta: h.meta(now),
		Services: section(s.Services, now, func(v *core.Services) *apiv1.ServiceList {
			out := &apiv1.ServiceList{Items: []apiv1.Service{}}
			for _, s := range *v {
				out.Total++
				switch s.ActiveState {
				case "active":
					out.Active++
				case "failed":
					out.Failed++
				case "inactive":
					out.Inactive++
				}
				if state != "" && s.ActiveState != state {
					continue
				}
				out.Items = append(out.Items, apiv1.Service{Name: s.Name, Description: s.Description, LoadState: s.LoadState,
					ActiveState: s.ActiveState, SubState: s.SubState})
			}
			return out
		}),
	}
}

// customSets maps generic metrics (D22).
func customSets(in []core.CustomSet, now time.Time) []apiv1.CustomMetricsSet {
	out := make([]apiv1.CustomMetricsSet, 0, len(in))
	for _, cs := range in {
		set := apiv1.CustomMetricsSet{Collector: cs.Collector, DroppedSeries: cs.Dropped, Problem: cs.Problem}
		var data *[]core.CustomMetric
		if cs.Metrics != nil {
			data = &cs.Metrics
		}
		set.Section = section(core.Section[[]core.CustomMetric]{Meta: cs.Meta, Data: data}, now, func(v *[]core.CustomMetric) *[]apiv1.CustomMetric {
			ms := make([]apiv1.CustomMetric, 0, len(*v))
			for _, m := range *v {
				labels := m.Labels
				if labels == nil {
					labels = map[string]string{}
				}
				cm := apiv1.CustomMetric{Name: m.Name, Kind: string(m.Kind), Unit: m.Unit, Labels: labels, Value: m.Value, RatePerSecond: m.RatePerSecond}
				if m.Kind == core.Counter {
					cm.RateUnit = core.RateUnit(m.Unit)
				}
				ms = append(ms, cm)
			}
			return &ms
		})
		out = append(out, set)
	}
	return out
}

func zfs(v *core.ZFSInfo) *apiv1.ZFS {
	out := &apiv1.ZFS{Pools: make([]apiv1.ZFSPool, 0, len(v.Pools)), DetailReason: v.DetailReason}
	for _, p := range v.Pools {
		out.Pools = append(out.Pools, apiv1.ZFSPool{Name: p.Name, State: p.State, SizeBytes: p.SizeBytes, AllocatedBytes: p.AllocatedBytes,
			FreeBytes: p.FreeBytes, ReadErrors: p.ReadErrors, WriteErrors: p.WriteErrors, ChecksumErrors: p.ChecksumErrors})
	}
	if a := v.ARC; a != nil {
		arc := &apiv1.ZFSARC{SizeBytes: a.SizeBytes, TargetBytes: a.TargetBytes, MaxBytes: a.MaxBytes, Hits: a.Hits, Misses: a.Misses}
		if t := a.Hits + a.Misses; t > 0 {
			arc.HitPercent = ptrTo(float64(a.Hits) / float64(t) * 100)
		}
		out.ARC = arc
	}
	return out
}

func power(v *core.PowerSupplies) *[]apiv1.PowerSupply {
	out := make([]apiv1.PowerSupply, 0, len(*v))
	for _, p := range *v {
		out = append(out, apiv1.PowerSupply{Name: p.Name, Type: p.Type, Online: p.Online, Status: p.Status, CapacityPercent: p.CapacityPercent,
			Health: p.Health, PowerWatts: p.PowerWatts, EnergyNowWh: p.EnergyNowWh, EnergyFullWh: p.EnergyFullWh, HoursRemaining: p.HoursRemaining, Model: p.Model})
	}
	return &out
}

func upsList(v *core.UPSList) *[]apiv1.UPS {
	out := make([]apiv1.UPS, 0, len(*v))
	for _, u := range *v {
		st := u.Status
		if st == nil {
			st = []string{}
		}
		out = append(out, apiv1.UPS{Name: u.Name, Description: u.Description, Status: st, OnBattery: u.OnBattery, LowBattery: u.LowBattery,
			ChargePercent: u.ChargePercent, RuntimeSeconds: u.RuntimeSeconds, LoadPercent: u.LoadPercent, InputVolts: u.InputVolts,
			OutputVolts: u.OutputVolts, Model: u.Model})
	}
	return &out
}
