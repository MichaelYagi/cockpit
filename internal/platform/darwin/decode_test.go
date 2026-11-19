package darwin

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestCPUTimes(t *testing.T) {
	// Two CPUs: user, system, idle, nice.
	total, per := cpuTimes([]uint32{10, 20, 70, 1, 5, 5, 90, 0}, 2)
	if len(per) != 2 || per[0] != (core.CPUTimes{User: 10, System: 20, Idle: 70, Nice: 1}) || per[1].Idle != 90 {
		t.Errorf("per core %+v", per)
	}
	if total != (core.CPUTimes{User: 15, System: 25, Idle: 160, Nice: 1}) {
		t.Errorf("total %+v", total)
	}
	// A short array never reads past its end.
	if _, per := cpuTimes([]uint32{1, 2, 3, 4, 5}, 4); len(per) != 1 {
		t.Errorf("short array: %d cores", len(per))
	}
}

func TestDecodeTaskInfo(t *testing.T) {
	b := make([]byte, taskInfoSize)
	binary.LittleEndian.PutUint64(b[0:], 4<<30)
	binary.LittleEndian.PutUint64(b[8:], 64<<20)
	binary.LittleEndian.PutUint64(b[16:], 3000)
	binary.LittleEndian.PutUint64(b[24:], 1000)
	binary.LittleEndian.PutUint32(b[84:], 12)
	ti, ok := decodeTaskInfo(b)
	if !ok || ti.virtualBytes != 4<<30 || ti.residentBytes != 64<<20 || ti.userTime != 3000 || ti.systemTime != 1000 || ti.threads != 12 {
		t.Errorf("%+v %v", ti, ok)
	}
	if _, ok := decodeTaskInfo(b[:95]); ok {
		t.Error("short buffer accepted")
	}
}

func TestAbsToTicks(t *testing.T) {
	for _, c := range []struct {
		abs          uint64
		numer, denom uint32
		want         uint64
	}{
		{2_500_000_000, 1, 1, 250},          // Intel: nanoseconds
		{24_000_000, 125, 3, 100},           // Apple Silicon: 24 MHz timebase, 1 s
		{1 << 62, 125, 3, 0},                // no overflow (checked below)
		{5_000_000_000, 0, 0, 500},          // missing timebase: nanoseconds
		{9_999_999, 1, 1, 0},                // below one tick
		{24_000_000 * 3600, 125, 3, 360000}, // an hour
	} {
		got := absToTicks(c.abs, c.numer, c.denom)
		if c.abs == 1<<62 {
			if want := uint64(float64(c.abs) * 125 / 3 / 1e7); math.Abs(float64(got)-float64(want)) > float64(want)/1e6 {
				t.Errorf("large value: %d, want about %d", got, want)
			}
			continue
		}
		if got != c.want {
			t.Errorf("absToTicks(%d, %d/%d) = %d, want %d", c.abs, c.numer, c.denom, got, c.want)
		}
	}
}

func TestProcState(t *testing.T) {
	for stat, want := range map[int8]string{1: "I", 2: "R", 3: "S", 4: "T", 5: "Z", 9: "?"} {
		if got := procState(stat); got != want {
			t.Errorf("procState(%d) = %q, want %q", stat, got, want)
		}
	}
}

func TestPowerSupply(t *testing.T) {
	battery := map[string]any{"Name": "InternalBattery-0", "Type": "InternalBattery", "Is Present": true,
		"Current Capacity": 80.0, "Max Capacity": 100.0, "Power Source State": "Battery Power",
		"Is Charging": false, "Time to Empty": 150.0, "BatteryHealth": "Good"}
	p, ok := powerSupply(battery)
	if !ok || p.Type != "battery" || p.Status != "discharging" || *p.CapacityPercent != 80 || *p.HoursRemaining != 2.5 || p.Health != "Good" {
		t.Errorf("discharging: %+v", p)
	}
	battery["Time to Empty"] = -1.0 // still estimating
	if p, _ := powerSupply(battery); p.HoursRemaining != nil {
		t.Errorf("estimating: %v hours", *p.HoursRemaining)
	}
	battery["Power Source State"], battery["Is Charging"] = "AC Power", true
	if p, _ := powerSupply(battery); p.Status != "charging" || p.HoursRemaining != nil {
		t.Errorf("charging: %+v", p)
	}
	battery["Is Charging"], battery["Current Capacity"] = false, 100.0
	if p, _ := powerSupply(battery); p.Status != "full" {
		t.Errorf("full: %+v", p)
	}
	battery["Current Capacity"] = 79.0 // macOS holds the charge (optimised charging)
	if p, _ := powerSupply(battery); p.Status != "not_charging" {
		t.Errorf("held: %+v", p)
	}
	if _, ok := powerSupply(map[string]any{"Type": "InternalBattery", "Is Present": false}); ok {
		t.Error("absent battery reported")
	}
	if _, ok := powerSupply(map[string]any{"Type": "Something"}); ok {
		t.Error("unknown type reported")
	}
	if p, _ := powerSupply(map[string]any{"Type": "UPS", "Current Capacity": 300.0, "Max Capacity": 200.0}); p.Type != "ups" || *p.CapacityPercent != 100 || p.Name != "ups" {
		t.Errorf("ups: %+v", p)
	}
}

func TestAdapter(t *testing.T) {
	p, ok := adapter(map[string]any{"Watts": 96.0, "Name": "USB-C"}, true)
	if !ok || p.Type != "mains" || !*p.Online || p.Model != "USB-C 96 W" {
		t.Errorf("%+v", p)
	}
	if _, ok := adapter(nil, false); ok {
		t.Error("adapter without details or AC power")
	}
	if p, ok := adapter(nil, true); !ok || p.Model != "" {
		t.Errorf("AC without details: %+v", p)
	}
}

func TestThermalSensors(t *testing.T) {
	got := thermalSensors([]thermalReading{
		{"PMU tdie2", 48.26}, {"PMU tdie1", 45.0}, {"PMU tdie1", 46.0},
		{"NAND CH0 temp", 0}, {"bogus", 400}, {"nan", math.NaN()}, {"", 30},
	})
	labels := []string{}
	for _, s := range got {
		labels = append(labels, s.Label)
		if s.Kind != core.SensorTemperature || s.Unit != "celsius" || s.CPU {
			t.Errorf("%+v", s)
		}
	}
	want := []string{"PMU tdie1", "PMU tdie1 #2", "PMU tdie2", "sensor"}
	if len(labels) != len(want) {
		t.Fatalf("labels %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("labels %v, want %v", labels, want)
		}
	}
	if got[2].Value != 48.3 {
		t.Errorf("rounding: %v", got[2].Value)
	}
	many := make([]thermalReading, 100)
	for i := range many {
		many[i] = thermalReading{"s", 40}
	}
	if n := len(thermalSensors(many)); n != maxSensors {
		t.Errorf("%d sensors kept", n)
	}
}

func TestCPUInfo(t *testing.T) {
	strs := map[string]string{"machdep.cpu.brand_string": "Apple M2 Pro"}
	nums := map[string]uint64{"hw.physicalcpu": 10, "hw.logicalcpu": 10, "hw.packages": 1,
		"hw.l1dcachesize": 64 << 10, "hw.l1icachesize": 128 << 10, "hw.l2cachesize": 4 << 20, "kern.hv_vmm_present": 1}
	str := func(n string) (string, bool) { v, ok := strs[n]; return v, ok }
	num := func(n string) (uint64, bool) { v, ok := nums[n]; return v, ok }
	c := cpuInfo("arm64", str, num)
	if c.ModelName != "Apple M2 Pro" || c.Vendor != "Apple" || c.Cores != 10 || c.Threads != 10 || c.Sockets != 1 ||
		len(c.Caches) != 3 || !c.Hypervisor || c.MaxFrequencyMHz != nil {
		t.Errorf("%+v", c)
	}
	strs["machdep.cpu.vendor"] = "GenuineIntel"
	nums["hw.cpufrequency_max"] = 2_600_000_000
	c = cpuInfo("amd64", str, num)
	if c.Vendor != "GenuineIntel" || c.MaxFrequencyMHz == nil || *c.MaxFrequencyMHz != 2600 {
		t.Errorf("intel %+v", c)
	}
}

func TestLaunchdServices(t *testing.T) {
	var jobs []launchdJob
	for _, d := range []struct {
		m    map[string]any
		user bool
	}{
		{map[string]any{"Label": "com.apple.running", "PID": 412.0, "LastExitStatus": 0.0, "Program": "/usr/libexec/running"}, false},
		{map[string]any{"Label": "com.example.exited", "LastExitStatus": float64(78 << 8)}, true},
		{map[string]any{"Label": "com.example.killed", "LastExitStatus": 9.0}, true},
		{map[string]any{"Label": "com.example.clean", "LastExitStatus": 0.0}, false},
		{map[string]any{"Label": "com.example.never"}, false},
		{map[string]any{"Label": "com.apple.running", "PID": 9.0}, true}, // duplicate label: first wins
		{map[string]any{"PID": 1.0}, false},                              // no label: skipped
	} {
		if j, ok := launchdJobFrom(d.m, d.user); ok {
			jobs = append(jobs, j)
		}
	}
	got := launchdServices(jobs)
	want := map[string][3]string{
		"com.apple.running":  {"active", "running (pid 412)", "System daemon · /usr/libexec/running"},
		"com.example.exited": {"inactive", "exited (status 78)", "User agent"},
		"com.example.killed": {"inactive", "killed (signal 9)", "User agent"},
		"com.example.clean":  {"inactive", "exited", "System daemon"},
		"com.example.never":  {"inactive", "waiting", "System daemon"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d services: %+v", len(got), got)
	}
	for i, s := range got {
		w, ok := want[s.Name]
		if !ok || s.ActiveState != w[0] || s.SubState != w[1] || s.Description != w[2] || s.LoadState != "loaded" {
			t.Errorf("%s: %+v, want %v", s.Name, s, w)
		}
		if i > 0 && got[i-1].Name > s.Name {
			t.Error("not sorted")
		}
		if s.ActiveState == "failed" {
			t.Errorf("%s reported failed", s.Name)
		}
	}
}

func TestUSBDeviceFrom(t *testing.T) {
	d, ok := usbDeviceFrom(map[string]any{"idVendor": float64(0x46d), "idProduct": float64(0xc52b),
		"USB Vendor Name": "Logitech", "USB Product Name": "USB Receiver ", "USB Serial Number": "S1",
		"locationID": float64(0x01100000), "Device Speed": float64(1)}, "USB Receiver")
	if !ok || d.VendorID != "046d" || d.ProductID != "c52b" || d.Manufacturer != "Logitech" || d.Product != "USB Receiver" ||
		d.Serial != "S1" || d.Path != "01100000" || d.SpeedMbps == nil || *d.SpeedMbps != 12 {
		t.Errorf("%+v", d)
	}
	d, ok = usbDeviceFrom(map[string]any{"idVendor": float64(0x5ac), "kUSBProductString": "Keyboard", "Device Speed": float64(9)}, "kbd")
	if !ok || d.Product != "Keyboard" || d.SpeedMbps != nil {
		t.Errorf("%+v", d)
	}
	if d, _ = usbDeviceFrom(map[string]any{"idVendor": float64(1)}, " Hub "); d.Product != "Hub" {
		t.Errorf("registry name not used: %+v", d)
	}
	if _, ok := usbDeviceFrom(map[string]any{"USB Product Name": "x"}, "x"); ok {
		t.Error("a device without a vendor ID was accepted")
	}
}

func TestPCIDeviceFrom(t *testing.T) {
	d, ok := pciDeviceFrom(map[string]any{"vendor-id": []byte{0x86, 0x80, 0, 0}, "device-id": []byte{0x12, 0x3e, 0, 0},
		"class-code": []byte{0, 0, 3, 0}, "pcidebug": "0:2:0", "model": []byte("Intel UHD Graphics 630\x00")}, "IGPU", "AppleIntelCFLGraphics")
	if !ok || d.Address != "0000:00:02.0" || d.VendorID != "0x8086" || d.DeviceID != "0x3e12" || d.ClassCode != "0x030000" ||
		d.ClassName != "VGA compatible controller" || d.VendorName != "Intel" || d.DeviceName != "Intel UHD Graphics 630" ||
		d.Driver != "AppleIntelCFLGraphics" {
		t.Errorf("%+v", d)
	}
	d, _ = pciDeviceFrom(map[string]any{"vendor-id": []byte{0xe4, 0x14, 0, 0}, "class-code": []byte{0x80, 2, 2, 0},
		"pcidebug": "1:0:0(1:1)"}, "wlan", "")
	if d.Address != "0000:01:00.0" || d.ClassName != "Network controller" || d.VendorName != "Broadcom" {
		t.Errorf("%+v", d)
	}
	if d, _ = pciDeviceFrom(map[string]any{"vendor-id": []byte{1, 2}, "pcidebug": "bad"}, "pci-bridge", ""); d.Address != "pci-bridge" {
		t.Errorf("registry name not used: %+v", d)
	}
	if _, ok := pciDeviceFrom(map[string]any{"vendor-id": []byte{1, 2, 3, 4, 5}}, "x", ""); ok {
		t.Error("an oversized ID was accepted")
	}
}

func TestBlockDeviceFrom(t *testing.T) {
	d, ok := blockDeviceFrom(map[string]any{"BSD Name": "disk0", "Size": float64(500107862016), "Removable": false},
		map[string]any{"Product Name": "APPLE SSD AP0512Q ", "Serial Number": "X1", "Medium Type": "Solid State"},
		map[string]any{"Physical Interconnect": "Apple Fabric"})
	if !ok || d.Name != "disk0" || d.Model != "APPLE SSD AP0512Q" || d.Serial != "X1" || d.Transport != "nvme" ||
		d.SizeBytes != 500107862016 || d.Removable || d.Rotational == nil || *d.Rotational {
		t.Errorf("%+v", d)
	}
	d, _ = blockDeviceFrom(map[string]any{"BSD Name": "disk4", "Removable": true}, map[string]any{"Medium Type": "Rotational"},
		map[string]any{"Physical Interconnect": "USB"})
	if d.Transport != "usb" || !d.Removable || d.Rotational == nil || !*d.Rotational {
		t.Errorf("%+v", d)
	}
	if d, _ = blockDeviceFrom(map[string]any{"BSD Name": "disk1"}, nil, nil); d.Transport != "unknown" || d.Rotational != nil {
		t.Errorf("%+v", d)
	}
	if _, ok := blockDeviceFrom(map[string]any{"BSD Name": "disk5"}, nil, map[string]any{"Physical Interconnect": "Virtual Interface"}); ok {
		t.Error("a disk image was listed")
	}
	ds := core.BlockDevices{{Name: "disk10"}, {Name: "disk2"}, {Name: "disk0"}}
	sortDisks(ds)
	if ds[0].Name != "disk0" || ds[1].Name != "disk2" || ds[2].Name != "disk10" {
		t.Errorf("%+v", ds)
	}
}

func TestMemoryModules(t *testing.T) {
	apple := `{"SPMemoryDataType":[{"SPMemoryDataType":"16 GB","dimm_manufacturer":"Hynix","dimm_type":"LPDDR5"}]}`
	got, err := memoryModules([]byte(apple))
	if err != nil || len(got) != 1 || got[0].Locator != "Built-in" || got[0].SizeBytes != 16<<30 || got[0].Type != "LPDDR5" ||
		got[0].Manufacturer != "Hynix" {
		t.Fatalf("%+v %v", got, err)
	}
	intel := `{"SPMemoryDataType":[{"_name":"Memory Slots","_items":[
		{"_name":"BANK 0/ChannelA-DIMM0","dimm_size":"8 GB","dimm_speed":"2667 MHz","dimm_type":"DDR4",
		 "dimm_manufacturer":"0x80AD","dimm_part_number":"0x484D4134314753364146523847422D53572020","dimm_serial_number":"0x1234"},
		{"_name":"BANK 1/ChannelB-DIMM0","dimm_size":"empty","dimm_type":"empty"}]}]}`
	got, err = memoryModules([]byte(intel))
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	m := got[0]
	if m.Locator != "BANK 0/ChannelA-DIMM0" || m.SizeBytes != 8<<30 || m.SpeedMTs != 2667 || m.Type != "DDR4" ||
		m.Manufacturer != "0x80AD" || m.PartNumber != "HMA41GS6AFR8GB-SW" || m.Serial != "0x1234" {
		t.Errorf("%+v", m)
	}
	if got, err := memoryModules([]byte(`{"SPMemoryDataType":[]}`)); err != nil || len(got) != 0 {
		t.Errorf("%+v %v", got, err)
	}
	if _, err := memoryModules([]byte(`not json`)); err == nil {
		t.Error("bad JSON accepted")
	}
}

// rtMsg builds one NET_RT_DUMP message: a 92-byte rt_msghdr and the given
// socket addresses, each padded to 4 bytes.
func rtMsg(index uint16, flags, addrs uint32, sas ...[]byte) []byte {
	var body []byte
	for _, sa := range sas {
		body = append(body, sa...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
		if len(sa) == 0 {
			body = append(body, 0, 0, 0, 0)
		}
	}
	h := make([]byte, rtmHdrLen)
	binary.LittleEndian.PutUint16(h, uint16(rtmHdrLen+len(body)))
	h[2], h[rtmType] = 5, rtmGet
	binary.LittleEndian.PutUint16(h[rtmIndex:], index)
	binary.LittleEndian.PutUint32(h[rtmFlags:], flags)
	binary.LittleEndian.PutUint32(h[rtmAddrs:], addrs)
	return append(h, body...)
}

func sin(a, b, c, d byte) []byte { return []byte{16, afInet, 0, 0, a, b, c, d, 0, 0, 0, 0, 0, 0, 0, 0} }

func sin6(ip ...byte) []byte {
	sa := make([]byte, 28)
	sa[0], sa[1] = 28, afInet6
	copy(sa[8:], ip)
	return sa
}

func TestParseRouteDump(t *testing.T) {
	names := map[int]string{1: "lo0", 4: "en0"}
	ifName := func(i int) string { return names[i] }
	linkGW := []byte{20, 18, 4, 0, 6, 3, 6, 0, 'e', 'n', '0', 0, 0, 0, 0, 0, 0, 0, 0, 0} // sockaddr_dl
	var dump []byte
	// default via 192.168.1.1: an empty netmask.
	dump = append(dump, rtMsg(4, rtfUp|rtfGateway, rtaDst|rtaGateway|rtaNetmask, sin(0, 0, 0, 0), sin(192, 168, 1, 1), nil)...)
	// 192.168.1.0/24 on en0, netmask shortened to 7 bytes with no family.
	dump = append(dump, rtMsg(4, rtfUp, rtaDst|rtaGateway|rtaNetmask, sin(192, 168, 1, 0), linkGW, []byte{7, 0, 0, 0, 255, 255, 255})...)
	// A scoped default, an ARP entry, a cloned host and a reject route are skipped.
	dump = append(dump, rtMsg(4, rtfUp|rtfGateway|rtfIfScope, rtaDst|rtaGateway|rtaNetmask, sin(0, 0, 0, 0), sin(192, 168, 1, 1), nil)...)
	dump = append(dump, rtMsg(4, rtfUp|rtfHost|rtfLLInfo, rtaDst|rtaGateway, sin(192, 168, 1, 7), linkGW)...)
	dump = append(dump, rtMsg(4, rtfUp|rtfHost|rtfWasCloned, rtaDst|rtaGateway, sin(192, 168, 1, 8), linkGW)...)
	dump = append(dump, rtMsg(4, rtfUp|rtfReject, rtaDst|rtaNetmask, sin(10, 0, 0, 0), []byte{5, 0, 0, 0, 255})...)
	// Loopback is skipped.
	dump = append(dump, rtMsg(1, rtfUp|rtfHost, rtaDst|rtaGateway, sin(127, 0, 0, 1), sin(127, 0, 0, 1))...)
	// Host routes without a gateway (own address, router link entry,
	// broadcast) are skipped.
	dump = append(dump, rtMsg(4, rtfUp|rtfHost, rtaDst|rtaGateway, sin(192, 168, 1, 5), linkGW)...)
	dump = append(dump, rtMsg(4, rtfUp|rtfHost, rtaDst|rtaGateway, sin(255, 255, 255, 255), linkGW)...)
	// A host route to 10.0.0.5 via a gateway.
	dump = append(dump, rtMsg(4, rtfUp|rtfHost|rtfGateway, rtaDst|rtaGateway, sin(10, 0, 0, 5), sin(192, 168, 1, 254))...)
	// IPv6 default via a link-local gateway with the embedded scope ID.
	dump = append(dump, rtMsg(4, rtfUp|rtfGateway, rtaDst|rtaGateway|rtaNetmask, sin6(), sin6(0xfe, 0x80, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1), nil)...)
	// IPv6 2001:db8::/32 with a shortened mask; fe80::/64 is skipped.
	dump = append(dump, rtMsg(4, rtfUp, rtaDst|rtaNetmask, sin6(0x20, 0x01, 0x0d, 0xb8), []byte{12, afInet6, 0, 0, 0, 0, 0, 0, 255, 255, 255, 255})...)
	dump = append(dump, rtMsg(4, rtfUp, rtaDst|rtaNetmask, sin6(0xfe, 0x80, 0, 4), []byte{16, afInet6, 0, 0, 0, 0, 0, 0, 255, 255, 255, 255, 255, 255, 255, 255})...)
	// A truncated message ends parsing without a panic.
	dump = append(dump, 200, 0, 5, rtmGet)

	got := parseRouteDump(dump, ifName)
	sortRoutes(got)
	want := []string{
		"ipv4 0.0.0.0/0 192.168.1.1 en0 true",
		"ipv6 ::/0 fe80::1 en0 true",
		"ipv4 192.168.1.0/24  en0 false",
		"ipv4 10.0.0.5/32 192.168.1.254 en0 false",
		"ipv6 2001:db8::/32  en0 false",
	}
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%s %s %s %s %v", r.Family, r.Destination, r.Gateway, r.Interface, r.Default))
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for i := range len(dump) {
		_ = parseRouteDump(dump[:i], ifName) // any prefix parses without a panic
	}
}

func TestGPUFrom(t *testing.T) {
	g := gpuFrom("AGXAcceleratorG13X", map[string]any{"model": "Apple M1 Pro", "gpu-core-count": float64(16)}, 0, false,
		map[string]any{"Device Utilization %": float64(12), "In use system memory": float64(734003200), "Renderer Utilization %": float64(9)})
	if g.Vendor != "Apple" || g.Name != "Apple M1 Pro (16-core)" || g.Driver != "AGXAcceleratorG13X" || g.MetricsSource != "iokit" ||
		*g.UtilizationPercent != 12 || *g.MemoryUsedBytes != 734003200 || g.MemoryTotalBytes != nil {
		t.Errorf("%+v", g)
	}
	g = gpuFrom("AMDRadeonX6000_AMDNavi10GraphicsAccelerator", map[string]any{"model": []byte("AMD Radeon Pro 5500M\x00")}, 0x1002, true,
		map[string]any{"Device Utilization %": float64(150)})
	if g.Vendor != "AMD" || g.Name != "AMD Radeon Pro 5500M" || g.UtilizationPercent != nil || g.MetricsSource != "" {
		t.Errorf("%+v", g)
	}
	if g = gpuFrom("AppleParavirtGPU", nil, 0, false, nil); g.Name != "GPU" || g.Vendor != "" {
		t.Errorf("%+v", g)
	}
}
