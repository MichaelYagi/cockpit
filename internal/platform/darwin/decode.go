package darwin

// Pure decoding of what the macOS calls return. This file has no build
// tag, so its tests run on every platform; the calls themselves are in
// the *_darwin.go files.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
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

// launchdJob is one job from ServiceManagement's job list (D48).
type launchdJob struct {
	Label   string
	PID     int64 // 0 when not running
	Status  int64 // last wait status; meaningful when HasStatus
	HasStat bool
	Program string
	User    bool // a user agent rather than a system daemon
}

// launchdJobFrom reads one job dictionary (keys Label, PID,
// LastExitStatus, Program).
func launchdJobFrom(d map[string]any, user bool) (launchdJob, bool) {
	label, _ := d["Label"].(string)
	if label == "" {
		return launchdJob{}, false
	}
	j := launchdJob{Label: label, User: user}
	if v, ok := d["PID"].(float64); ok && v > 0 {
		j.PID = int64(v)
	}
	if v, ok := d["LastExitStatus"].(float64); ok {
		j.Status, j.HasStat = int64(v), true
	}
	j.Program, _ = d["Program"].(string)
	return j, true
}

// launchdServices maps launchd jobs onto services. A running job is
// active; any other is inactive, with how it last ended. Jobs are never
// "failed": many macOS jobs end with a non-zero status as a matter of
// course, so that would raise findings for normal behavior.
func launchdServices(jobs []launchdJob) core.Services {
	seen := map[string]bool{}
	out := core.Services{}
	for _, j := range jobs {
		if seen[j.Label] {
			continue
		}
		seen[j.Label] = true
		kind := "System daemon"
		if j.User {
			kind = "User agent"
		}
		desc := kind
		if j.Program != "" {
			desc += " · " + j.Program
		}
		s := core.Service{Name: j.Label, Description: desc, LoadState: "loaded", ActiveState: "inactive", SubState: "waiting"}
		switch {
		case j.PID > 0:
			s.ActiveState, s.SubState = "active", "running (pid "+strconv.FormatInt(j.PID, 10)+")"
		case j.HasStat && j.Status&0x7f != 0:
			s.SubState = "killed (signal " + strconv.FormatInt(j.Status&0x7f, 10) + ")"
		case j.HasStat && j.Status>>8 != 0:
			s.SubState = "exited (status " + strconv.FormatInt((j.Status>>8)&0xff, 10) + ")"
		case j.HasStat:
			s.SubState = "exited"
		}
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// ---- IOKit registry: USB, PCI and disks ----

// propStr returns the first non-empty string property among keys. CFData
// holding text (as some PCI properties do) counts as a string.
func propStr(d map[string]any, keys ...string) string {
	for _, k := range keys {
		var s string
		switch v := d[k].(type) {
		case string:
			s = v
		case []byte:
			s = string(bytes.TrimRight(v, "\x00"))
		}
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func propNum(d map[string]any, key string) (float64, bool) {
	v, ok := d[key].(float64)
	return v, ok
}

// propLE32 reads a little-endian number from CFData, as IOPCIDevice
// stores its IDs.
func propLE32(d map[string]any, key string) (uint32, bool) {
	b, ok := d[key].([]byte)
	if !ok || len(b) == 0 || len(b) > 4 {
		return 0, false
	}
	var buf [4]byte
	copy(buf[:], b)
	return binary.LittleEndian.Uint32(buf[:]), true
}

// usbSpeeds maps IOUSBHostDevice "Device Speed" to Mbit/s.
var usbSpeeds = map[int]float64{0: 1.5, 1: 12, 2: 480, 3: 5000, 4: 10000, 5: 20000}

// usbDeviceFrom turns an IOUSBHostDevice's properties into a device; name
// is its registry name, used when the device has no product string.
func usbDeviceFrom(d map[string]any, name string) (core.USBDevice, bool) {
	vid, ok := propNum(d, "idVendor")
	if !ok {
		return core.USBDevice{}, false
	}
	pid, _ := propNum(d, "idProduct")
	u := core.USBDevice{
		VendorID:     fmt.Sprintf("%04x", uint16(vid)),
		ProductID:    fmt.Sprintf("%04x", uint16(pid)),
		Manufacturer: propStr(d, "USB Vendor Name", "kUSBVendorString"),
		Product:      propStr(d, "USB Product Name", "kUSBProductString"),
		Serial:       propStr(d, "USB Serial Number", "kUSBSerialNumberString"),
	}
	if u.Product == "" {
		u.Product = strings.TrimSpace(name)
	}
	if loc, ok := propNum(d, "locationID"); ok {
		u.Path = fmt.Sprintf("%08x", uint32(loc))
	}
	if sp, ok := propNum(d, "Device Speed"); ok {
		if v, ok := usbSpeeds[int(sp)]; ok {
			u.SpeedMbps = &v
		}
	}
	return u, true
}

// pciAddress turns "pcidebug" ("bus:device:function", sometimes followed
// by the bridged bus range) into domain:bus:device.function.
func pciAddress(debug string) (string, bool) {
	if i := strings.IndexByte(debug, '('); i >= 0 {
		debug = debug[:i]
	}
	parts := strings.Split(strings.TrimSpace(debug), ":")
	if len(parts) != 3 {
		return "", false
	}
	var n [3]uint64
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return "", false
		}
		n[i] = v
	}
	return fmt.Sprintf("0000:%02x:%02x.%x", n[0], n[1], n[2]), true
}

// pciDeviceFrom turns an IOPCIDevice's properties into a device. name is
// its registry name and driver the class of the driver attached to it.
func pciDeviceFrom(d map[string]any, name, driver string) (core.PCIDevice, bool) {
	vid, ok := propLE32(d, "vendor-id")
	if !ok {
		return core.PCIDevice{}, false
	}
	did, _ := propLE32(d, "device-id")
	class, _ := propLE32(d, "class-code")
	p := core.PCIDevice{
		VendorID:   fmt.Sprintf("0x%04x", vid&0xffff),
		DeviceID:   fmt.Sprintf("0x%04x", did&0xffff),
		ClassCode:  fmt.Sprintf("0x%06x", class&0xffffff),
		VendorName: portable.PCIVendorName(uint16(vid)),
		DeviceName: propStr(d, "model"),
		Driver:     driver,
	}
	p.ClassName = portable.PCIClassName(p.ClassCode)
	if a, ok := pciAddress(propStr(d, "pcidebug")); ok {
		p.Address = a
	} else {
		p.Address = strings.TrimSpace(name)
	}
	return p, true
}

// diskTransport maps IOKit's "Physical Interconnect" to the API's
// transports.
func diskTransport(interconnect string) string {
	switch strings.ToLower(interconnect) {
	case "sata", "serial ata", "ata":
		return "sata"
	case "usb":
		return "usb"
	case "pci-express", "pci", "apple fabric", "nvme":
		return "nvme"
	case "virtio":
		return "virtio"
	case "secure digital", "sd":
		return "mmc"
	case "sas", "scsi", "fibre channel":
		return "scsi"
	}
	return "unknown"
}

// blockDeviceFrom turns a whole-disk IOMedia and the "Device
// Characteristics" and "Protocol Characteristics" of the device below it
// into a disk. Disk images are skipped, as Linux skips loop devices.
func blockDeviceFrom(media, device, protocol map[string]any) (core.BlockDevice, bool) {
	name := propStr(media, "BSD Name")
	inter := propStr(protocol, "Physical Interconnect")
	if name == "" || inter == "Virtual Interface" {
		return core.BlockDevice{}, false
	}
	size, _ := propNum(media, "Size")
	b := core.BlockDevice{
		Name:      name,
		Model:     propStr(device, "Product Name"),
		Serial:    propStr(device, "Serial Number"),
		Transport: diskTransport(inter),
		SizeBytes: uint64(max(size, 0)),
	}
	b.Removable, _ = media["Removable"].(bool)
	switch propStr(device, "Medium Type") {
	case "Solid State":
		b.Rotational = new(false)
	case "Rotational":
		b.Rotational = new(true)
	}
	return b, true
}

// sortDisks orders disk2 before disk10.
func sortDisks(ds core.BlockDevices) {
	sort.Slice(ds, func(i, j int) bool {
		if len(ds[i].Name) != len(ds[j].Name) {
			return len(ds[i].Name) < len(ds[j].Name)
		}
		return ds[i].Name < ds[j].Name
	})
}

// ---- memory modules: system_profiler SPMemoryDataType -json ----

type spMemory struct {
	Name         string     `json:"_name"`
	Total        string     `json:"SPMemoryDataType"` // Apple silicon: the whole memory
	Size         string     `json:"dimm_size"`
	Type         string     `json:"dimm_type"`
	Speed        string     `json:"dimm_speed"`
	Manufacturer string     `json:"dimm_manufacturer"`
	PartNumber   string     `json:"dimm_part_number"`
	Serial       string     `json:"dimm_serial_number"`
	Items        []spMemory `json:"_items"` // Intel: one per slot
}

// memSize parses "16 GB".
func memSize(s string) (uint64, bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	mult := map[string]float64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}[strings.ToUpper(f[1])]
	if mult == 0 {
		return 0, false
	}
	return uint64(v * mult), true
}

// hexText decodes "0x4D3437..." when it is printable ASCII, as Intel
// Macs report part numbers; anything else is returned trimmed.
func hexText(s string) string {
	s = strings.TrimSpace(s)
	if h, ok := strings.CutPrefix(s, "0x"); ok && len(h) > 4 {
		if b, err := hex.DecodeString(h); err == nil {
			t := strings.TrimSpace(string(bytes.TrimRight(b, "\x00")))
			printable := t != ""
			for _, r := range t {
				printable = printable && r < unicode.MaxASCII && unicode.IsPrint(r)
			}
			if printable {
				return t
			}
		}
	}
	return s
}

func spModule(locator string, m spMemory, size uint64) core.MemoryModule {
	mod := core.MemoryModule{Locator: locator, SizeBytes: size, Type: strings.TrimSpace(m.Type),
		Manufacturer: hexText(m.Manufacturer), PartNumber: hexText(m.PartNumber), Serial: strings.TrimSpace(m.Serial)}
	if f := strings.Fields(m.Speed); len(f) > 0 {
		mod.SpeedMTs, _ = strconv.Atoi(f[0])
	}
	return mod
}

// memoryModules parses system_profiler's memory report. Apple silicon
// has one built-in memory; Intel Macs list each slot, empty ones with
// size "empty", which are skipped.
func memoryModules(js []byte) (core.MemoryModules, error) {
	var doc struct {
		Memory []spMemory `json:"SPMemoryDataType"`
	}
	if err := json.Unmarshal(js, &doc); err != nil {
		return nil, err
	}
	var out core.MemoryModules
	for _, m := range doc.Memory {
		for _, it := range m.Items {
			if size, ok := memSize(it.Size); ok {
				out = append(out, spModule(strings.TrimSpace(it.Name), it, size))
			}
		}
		if len(m.Items) == 0 {
			if size, ok := memSize(m.Total); ok {
				out = append(out, spModule("Built-in", m, size))
			}
		}
	}
	return out, nil
}

// ---- routes: sysctl NET_RT_DUMP ----

// struct rt_msghdr: the header before each route's socket addresses.
const (
	rtmType    = 3  // u_char rtm_type
	rtmIndex   = 4  // u_short rtm_index
	rtmFlags   = 8  // int rtm_flags
	rtmAddrs   = 12 // int rtm_addrs
	rtmHdrLen  = 92 // sizeof(struct rt_msghdr)
	rtmGet     = 4  // RTM_GET, the type of dumped routes
	maxRoutes  = 512
	afInet     = 2
	afInet6    = 30
	rtaDst     = 0x1
	rtaGateway = 0x2
	rtaNetmask = 0x4

	rtfUp        = 0x1
	rtfGateway   = 0x2
	rtfHost      = 0x4
	rtfReject    = 0x8
	rtfLLInfo    = 0x400
	rtfBlackhole = 0x1000
	rtfWasCloned = 0x20000
	rtfIfScope   = 0x1000000
)

// addrBytes reads the address of a socket address of the given family
// (4 bytes for IPv4, 16 for IPv6), zero-filling what the kernel left out,
// as it does for netmasks. Netmasks may also lack the family, so the
// caller passes the destination's.
func addrBytes(sa []byte, family int) []byte {
	off, n := 4, net.IPv4len // sockaddr_in: sin_addr at 4
	if family == afInet6 {
		off, n = 8, net.IPv6len // sockaddr_in6: sin6_addr at 8
	}
	ip := make([]byte, n)
	if len(sa) > off {
		copy(ip, sa[off:])
	}
	return ip
}

func saFamily(sa []byte) int {
	if len(sa) < 2 {
		return 0
	}
	return int(sa[1])
}

// routeAddrs splits the socket addresses after a header, by RTA_* bit;
// each takes its sa_len rounded up to 4 bytes, and an empty one takes 4.
func routeAddrs(b []byte, mask int) map[int][]byte {
	out := map[int][]byte{}
	for bit := 1; bit <= 0x80 && len(b) > 0; bit <<= 1 {
		if mask&bit == 0 {
			continue
		}
		l := int(b[0])
		step := 4
		if l > 0 {
			step = (l + 3) &^ 3
		}
		l, step = min(l, len(b)), min(step, len(b))
		out[bit] = b[:l]
		b = b[step:]
	}
	return out
}

// routeIP turns route address bytes into an IP, clearing the scope ID
// that macOS embeds in bytes 2–3 of IPv6 link-local addresses.
func routeIP(b []byte) net.IP {
	ip := net.IP(slices.Clone(b))
	if len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1]&0xc0 == 0x80 {
		ip[2], ip[3] = 0, 0
	}
	return ip
}

// parseRouteDump parses NET_RT_DUMP. It keeps the routes netstat -rn
// shows without -a: not ARP/neighbor entries, cloned host routes,
// interface-scoped copies, reject routes, loopback or multicast routes.
// Host routes without a gateway (this machine's addresses, the router's
// link entry, broadcasts) are left out too, as Linux keeps them in its
// separate local table and Windows hides them.
// ifName maps an interface index to its name.
func parseRouteDump(b []byte, ifName func(int) string) core.Routes {
	out := core.Routes{}
	for len(b) >= 4 && len(out) < maxRoutes {
		l := int(binary.LittleEndian.Uint16(b))
		if l < 4 || l > len(b) {
			break
		}
		msg := b[:l]
		b = b[l:]
		if len(msg) < rtmHdrLen || msg[rtmType] != rtmGet {
			continue
		}
		flags := binary.LittleEndian.Uint32(msg[rtmFlags:])
		if flags&rtfUp == 0 || flags&(rtfReject|rtfBlackhole|rtfLLInfo|rtfWasCloned|rtfIfScope) != 0 {
			continue
		}
		addrs := routeAddrs(msg[rtmHdrLen:], int(binary.LittleEndian.Uint32(msg[rtmAddrs:])))
		family := saFamily(addrs[rtaDst])
		if family != afInet && family != afInet6 {
			continue
		}
		dst := addrBytes(addrs[rtaDst], family)
		bitsLen := len(dst) * 8
		plen := bitsLen
		if m, ok := addrs[rtaNetmask]; ok && flags&rtfHost == 0 {
			var total int
			if plen, total = net.IPMask(addrBytes(m, family)).Size(); total == 0 {
				continue // not a contiguous mask
			}
		} else if !ok && flags&rtfHost == 0 {
			plen = 0 // no netmask: the default route
		}
		ip := routeIP(dst).Mask(net.CIDRMask(plen, bitsLen))
		iface := ifName(int(binary.LittleEndian.Uint16(msg[rtmIndex:])))
		gateway := flags&rtfGateway != 0
		if ip == nil || ip.IsLoopback() || ip.IsMulticast() || strings.HasPrefix(iface, "lo") ||
			ip.IsLinkLocalUnicast() && len(ip) == net.IPv6len && plen >= 64 || plen == bitsLen && !gateway {
			continue
		}
		name := "ipv4"
		if family == afInet6 {
			name = "ipv6"
		}
		r := core.Route{Family: name, Destination: fmt.Sprintf("%s/%d", ip, plen), Interface: iface, Default: plen == 0}
		if gw, ok := addrs[rtaGateway]; ok && gateway && saFamily(gw) == family {
			r.Gateway = routeIP(addrBytes(gw, family)).String()
		}
		out = append(out, r)
	}
	return out
}

// sortRoutes puts default routes first and IPv4 before IPv6, as Linux
// lists them.
func sortRoutes(rs core.Routes) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Default != rs[j].Default {
			return rs[i].Default
		}
		return rs[i].Family < rs[j].Family
	})
}

// ---- GPUs: IOAccelerator ----

// gpuFrom turns an IOAccelerator entry into a GPU: its class (the
// driver, e.g. AGXAcceleratorG13X), "model", the PCI vendor ID of the
// device below it (Intel Macs; Apple silicon has none), and the
// "PerformanceStatistics" dictionary the driver keeps up to date.
func gpuFrom(class string, props map[string]any, vendorID uint32, haveVendor bool, stats map[string]any) core.GPU {
	g := core.GPU{Driver: class, Name: propStr(props, "model")}
	switch {
	case strings.HasPrefix(class, "AGX") || haveVendor && vendorID == 0x106b:
		g.Vendor = "Apple"
	case haveVendor:
		g.Vendor = portable.PCIVendorName(uint16(vendorID))
	}
	if g.Name == "" {
		g.Name = strings.TrimSpace(g.Vendor + " GPU")
	}
	if cores, ok := propNum(props, "gpu-core-count"); ok && cores > 0 {
		g.Name += " (" + strconv.Itoa(int(cores)) + "-core)"
	}
	pct := func(keys ...string) *float64 {
		for _, k := range keys {
			if v, ok := propNum(stats, k); ok && v >= 0 && v <= 100 {
				return &v
			}
		}
		return nil
	}
	bytesOf := func(keys ...string) *uint64 {
		for _, k := range keys {
			if v, ok := propNum(stats, k); ok && v >= 0 {
				b := uint64(v)
				return &b
			}
		}
		return nil
	}
	g.UtilizationPercent = pct("Device Utilization %", "GPU Activity(%)")
	g.MemoryUsedBytes = bytesOf("In use system memory", "vramUsedBytes")
	if g.UtilizationPercent != nil || g.MemoryUsedBytes != nil {
		g.MetricsSource = "iokit"
	}
	return g
}
