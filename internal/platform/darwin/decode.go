package darwin

// Pure decoding of what the macOS calls return. This file has no build
// tag, so its tests run on every platform; the calls themselves are in
// the *_darwin.go files.

import (
	"encoding/binary"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// cpuStates is CPU_STATE_MAX: host_processor_info reports user, system,
// idle and nice ticks for each CPU, in that order.
const cpuStates = 4

// cpuTimes turns host_processor_info's PROCESSOR_CPU_LOAD_INFO counters
// into a total and per-core times.
func cpuTimes(ticks []uint32, cpus int) (core.CPUTimes, []core.CPUTimes) {
	if cpus*cpuStates > len(ticks) {
		cpus = len(ticks) / cpuStates
	}
	var total core.CPUTimes
	per := make([]core.CPUTimes, cpus)
	for i := range per {
		t := ticks[i*cpuStates:]
		per[i] = core.CPUTimes{User: uint64(t[0]), System: uint64(t[1]), Idle: uint64(t[2]), Nice: uint64(t[3])}
		total.User += per[i].User
		total.System += per[i].System
		total.Idle += per[i].Idle
		total.Nice += per[i].Nice
	}
	return total, per
}

// taskInfoSize is sizeof(struct proc_taskinfo).
const taskInfoSize = 96

// taskInfo is the part of struct proc_taskinfo (PROC_PIDTASKINFO) used here.
type taskInfo struct {
	virtualBytes, residentBytes uint64
	userTime, systemTime        uint64 // Mach absolute time units
	threads                     int
}

func decodeTaskInfo(b []byte) (taskInfo, bool) {
	if len(b) < taskInfoSize {
		return taskInfo{}, false
	}
	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
	return taskInfo{
		virtualBytes:  u64(0),                                         // pti_virtual_size
		residentBytes: u64(8),                                         // pti_resident_size
		userTime:      u64(16),                                        // pti_total_user
		systemTime:    u64(24),                                        // pti_total_system
		threads:       int(int32(binary.LittleEndian.Uint32(b[84:]))), // pti_threadnum
	}, true
}

// ticksPerSecond is the engine's process clock (USER_HZ); process CPU
// times are reported in these ticks on every platform.
const ticksPerSecond = 100

// absToTicks converts Mach absolute time (nanoseconds on Intel, a
// timebase of 125/3 ns on Apple Silicon) to 1/100 s ticks.
func absToTicks(abs uint64, numer, denom uint32) uint64 {
	if numer == 0 || denom == 0 {
		numer, denom = 1, 1
	}
	// abs·numer / (denom·10⁷) in 128 bits, so huge values cannot wrap.
	d := uint64(denom) * (1e9 / ticksPerSecond)
	hi, lo := bits.Mul64(abs, uint64(numer))
	if hi >= d {
		return math.MaxUint64
	}
	q, _ := bits.Div64(hi, lo, d)
	return q
}

// procState maps kinfo_proc's p_stat to the Linux-style letters the API
// uses.
func procState(stat int8) string {
	switch stat {
	case 1: // SIDL: being created
		return "I"
	case 2: // SRUN
		return "R"
	case 3: // SSLEEP
		return "S"
	case 4: // SSTOP
		return "T"
	case 5: // SZOMB
		return "Z"
	}
	return "?"
}

// powerSupply maps one IOKit power source description (keys from
// IOPSKeys.h) to a power supply.
func powerSupply(d map[string]any) (core.PowerSupply, bool) {
	str := func(k string) string { s, _ := d[k].(string); return s }
	num := func(k string) (float64, bool) {
		f, ok := d[k].(float64)
		return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	flag := func(k string) bool { b, _ := d[k].(bool); return b }
	if present, ok := d["Is Present"].(bool); ok && !present {
		return core.PowerSupply{}, false
	}
	p := core.PowerSupply{Name: str("Name"), Health: str("BatteryHealth")}
	switch str("Type") {
	case "InternalBattery":
		p.Type = "battery"
	case "UPS":
		p.Type = "ups"
	default:
		return core.PowerSupply{}, false
	}
	if p.Name == "" {
		p.Name = p.Type
	}
	if cur, ok := num("Current Capacity"); ok {
		max, ok := num("Max Capacity")
		if !ok || max <= 0 {
			max = 100
		}
		v := math.Max(0, math.Min(100, cur/max*100))
		p.CapacityPercent = &v
	}
	onAC := str("Power Source State") == "AC Power"
	switch {
	case flag("Is Charging"):
		p.Status = "charging"
	case flag("Is Charged") || onAC && p.CapacityPercent != nil && *p.CapacityPercent >= 100:
		p.Status = "full"
	case onAC:
		p.Status = "not_charging"
	case str("Power Source State") == "Battery Power":
		p.Status = "discharging"
	default:
		p.Status = "unknown"
	}
	// Time to Empty is in minutes; -1 while macOS is still estimating.
	if m, ok := num("Time to Empty"); ok && m > 0 && p.Status == "discharging" {
		h := m / 60
		p.HoursRemaining = &h
	}
	return p, true
}

// adapter describes the external power adapter, if one is connected.
func adapter(d map[string]any, onAC bool) (core.PowerSupply, bool) {
	if d == nil && !onAC {
		return core.PowerSupply{}, false
	}
	online := onAC
	p := core.PowerSupply{Name: "AC adapter", Type: "mains", Online: &online}
	if w, ok := d["Watts"].(float64); ok && w > 0 {
		p.Model = strconv.FormatFloat(w, 'f', 0, 64) + " W"
	}
	if name, ok := d["Name"].(string); ok && name != "" {
		p.Model = strings.TrimSpace(name + " " + p.Model)
	}
	return p, true
}

// thermalReading is one IOKit HID temperature sensor.
type thermalReading struct {
	name    string
	celsius float64
}

// maxSensors bounds the sensor list; Apple Silicon reports a few dozen.
const maxSensors = 64

// thermalSensors keeps plausible readings, names duplicates apart and
// sorts them. They are not marked as CPU sensors: Apple publishes no
// thresholds for them, and their normal range under load is above the
// default CPU limits, so the CPU temperature check would raise false
// alarms.
func thermalSensors(rs []thermalReading) core.Sensors {
	seen := map[string]int{}
	out := core.Sensors{}
	for _, r := range rs {
		if math.IsNaN(r.celsius) || r.celsius <= 0 || r.celsius > 150 {
			continue // unpopulated or nonsense
		}
		name := strings.TrimSpace(r.name)
		if name == "" {
			name = "sensor"
		}
		seen[name]++
		if n := seen[name]; n > 1 {
			name += " #" + strconv.Itoa(n)
		}
		out = append(out, core.Sensor{Kind: core.SensorTemperature, Chip: "iohid", Label: name,
			Value: math.Round(r.celsius*10) / 10, Unit: "celsius", Source: "iohid"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	if len(out) > maxSensors {
		out = out[:maxSensors]
	}
	return out
}

// cpuInfo builds the processor inventory from sysctl values. str and num
// look up a sysctl by name.
func cpuInfo(arch string, str func(string) (string, bool), num func(string) (uint64, bool)) *core.CPUInfo {
	n := func(name string) int {
		v, _ := num(name)
		return int(v) //nolint:gosec // core and cache counts are small
	}
	info := &core.CPUInfo{Cores: n("hw.physicalcpu"), Threads: n("hw.logicalcpu"), Sockets: n("hw.packages")}
	info.ModelName, _ = str("machdep.cpu.brand_string")
	if v, ok := str("machdep.cpu.vendor"); ok {
		info.Vendor = v
	} else if arch == "arm64" {
		info.Vendor = "Apple"
	}
	if f, ok := num("hw.cpufrequency_max"); ok && f > 0 { // Intel only
		mhz := float64(f) / 1e6
		info.MaxFrequencyMHz = &mhz
	}
	if v, ok := num("kern.hv_vmm_present"); ok {
		info.Hypervisor = v == 1
	}
	for _, c := range []struct {
		name  string
		level int
		typ   string
	}{
		{"hw.l1dcachesize", 1, "Data"},
		{"hw.l1icachesize", 1, "Instruction"},
		{"hw.l2cachesize", 2, "Unified"},
		{"hw.l3cachesize", 3, "Unified"},
	} {
		if size, ok := num(c.name); ok && size > 0 {
			info.Caches = append(info.Caches, core.CPUCache{Level: c.level, Type: c.typ, SizeBytes: size})
		}
	}
	return info
}
