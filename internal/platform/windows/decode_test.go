package windows

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
)

func lpiEntry(rel uint32, body []byte) []byte {
	e := make([]byte, 8, 8+len(body))
	binary.LittleEndian.PutUint32(e, rel)
	binary.LittleEndian.PutUint32(e[4:], uint32(8+len(body)))
	return append(e, body...)
}

func coreEntry(mask uint64) []byte {
	b := make([]byte, 40) // Flags..GroupCount (24 bytes from offset 8) + one GROUP_AFFINITY
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint64(b[24:], mask)
	return lpiEntry(relationProcessorCore, b)
}

func cacheEntry(level byte, size, typ uint32, mask uint64) []byte {
	b := make([]byte, 48)
	b[0] = level
	binary.LittleEndian.PutUint32(b[4:], size)
	binary.LittleEndian.PutUint32(b[8:], typ)
	binary.LittleEndian.PutUint16(b[30:], 1)
	binary.LittleEndian.PutUint64(b[32:], mask)
	return lpiEntry(relationCache, b)
}

func TestProcessorTopology(t *testing.T) {
	var buf []byte
	buf = append(buf, lpiEntry(relationProcessorPackage, make([]byte, 40))...)
	buf = append(buf, coreEntry(0b11)...)   // core 0, two threads
	buf = append(buf, coreEntry(0b1100)...) // core 1
	buf = append(buf, cacheEntry(1, 48<<10, 2, 0b11)...)
	buf = append(buf, cacheEntry(1, 48<<10, 2, 0b1100)...) // a second L1d instance
	buf = append(buf, cacheEntry(1, 32<<10, 1, 0b11)...)
	buf = append(buf, cacheEntry(3, 30<<20, 0, 0b1111)...)
	buf = append(buf, cacheEntry(1, 1, 3, 0b11)...) // trace cache: skipped
	buf = append(buf, 1, 2, 3)                      // truncated tail

	cores, threads, sockets, caches := processorTopology(buf)
	if cores != 2 || threads != 4 || sockets != 1 {
		t.Errorf("cores %d threads %d sockets %d", cores, threads, sockets)
	}
	if len(caches) != 3 || caches[0].Level != 1 || caches[0].Type != "Data" || caches[0].SizeBytes != 48<<10 || caches[0].SharedBy != 2 ||
		caches[1].Type != "Instruction" || caches[2].Level != 3 || caches[2].Type != "Unified" || caches[2].SharedBy != 4 {
		t.Errorf("%+v", caches)
	}
	for i := range buf {
		processorTopology(buf[:i]) // no panic on any prefix
	}
}

func TestCPUIdentifier(t *testing.T) {
	if f, m, s := cpuIdentifier("Intel64 Family 6 Model 151 Stepping 2"); f != "6" || m != "151" || s != "2" {
		t.Errorf("%q %q %q", f, m, s)
	}
	if f, m, s := cpuIdentifier("ARMv8 (64-bit) Family 8 Model 1 Revision 201"); f != "8" || m != "1" || s != "" {
		t.Errorf("%q %q %q", f, m, s)
	}
}

func TestPowerSupplies(t *testing.T) {
	// A desktop: AC online, no battery.
	ps := powerSupplies(systemPowerStatus{ACLineStatus: 1, BatteryFlag: batteryNone, BatteryLifePercent: 255, BatteryLifeTime: unknownSeconds})
	if len(ps) != 1 || ps[0].Type != "mains" || !*ps[0].Online {
		t.Errorf("desktop %+v", ps)
	}
	// A laptop on battery with 2 hours left.
	ps = powerSupplies(systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 64, BatteryLifeTime: 7200})
	if len(ps) != 2 || ps[0].Status != "discharging" || *ps[0].CapacityPercent != 64 || *ps[0].HoursRemaining != 2 || *ps[1].Online {
		t.Errorf("on battery %+v", ps)
	}
	ps = powerSupplies(systemPowerStatus{ACLineStatus: 1, BatteryFlag: batteryCharging | 1, BatteryLifePercent: 80, BatteryLifeTime: unknownSeconds})
	if ps[0].Status != "charging" || ps[0].HoursRemaining != nil {
		t.Errorf("charging %+v", ps[0])
	}
	if ps = powerSupplies(systemPowerStatus{ACLineStatus: 1, BatteryFlag: 1, BatteryLifePercent: 100}); ps[0].Status != "full" {
		t.Errorf("full %+v", ps[0])
	}
	if ps = powerSupplies(systemPowerStatus{ACLineStatus: 255, BatteryFlag: 255}); len(ps) != 0 {
		t.Errorf("unknown %+v", ps)
	}
}

func TestRawSMBIOS(t *testing.T) {
	table := []byte{3, 6, 1, 0, 0, 3, 0, 0} // chassis: desktop
	mem := make([]byte, 0x15)
	mem[0], mem[1] = 17, 0x15
	mem[0x0C], mem[0x0D] = 0x00, 0x20 // 8192 MB
	mem = append(mem, 0, 0)
	table = append(table, mem...)
	table = append(table, 127, 4, 0, 0, 0, 0)
	raw := append([]byte{0, 3, 4, 0, 0, 0, 0, 0}, table...)
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(table)))
	mods, chassis := smbiosInventory(rawSMBIOS(raw))
	if len(mods) != 1 || mods[0].SizeBytes != 8<<30 || chassis != "Desktop" {
		t.Errorf("%+v %q", mods, chassis)
	}
	if rawSMBIOS([]byte{1, 2}) != nil {
		t.Error("short header accepted")
	}
}

func TestWindowsService(t *testing.T) {
	cases := []struct {
		in                winService
		load, active, sub string
	}{
		{winService{Name: "Spooler", State: svcRunning, PID: 3012, StartType: startAuto}, "auto", "active", "running (pid 3012)"},
		{winService{State: svcStopped, StartType: startDemand, ExitCode: 0}, "manual", "inactive", "stopped"},
		{winService{State: svcStopped, StartType: startDemand, ExitCode: 1}, "manual", "inactive", "stopped (exit 1)"},
		{winService{State: svcStopped, StartType: startAuto, Delayed: true, ExitCode: errServiceSpecific, SpecificExit: 7}, "auto (delayed)", "inactive", "stopped (exit 7)"},
		{winService{State: svcStopped, StartType: startAuto, ExitCode: errServiceSpecific, SpecificExit: 7}, "auto", "failed", "stopped (exit 7)"},
		{winService{State: svcStopped, StartType: startAuto, ExitCode: errServiceNeverStarted}, "auto", "inactive", "stopped"},
		{winService{State: svcStopped, StartType: startDisabled, ExitCode: 0}, "disabled", "inactive", "stopped"},
		{winService{State: svcPaused, StartType: startUnknown}, "unknown", "active", "paused"},
		{winService{State: svcStartPending, StartType: startAuto}, "auto", "activating", "start pending"},
		{winService{State: svcStopPending, StartType: startAuto}, "auto", "deactivating", "stop pending"},
	}
	for _, c := range cases {
		s := windowsService(c.in)
		if s.LoadState != c.load || s.ActiveState != c.active || s.SubState != c.sub {
			t.Errorf("%+v: %+v", c.in, s)
		}
	}
}

func TestSockaddrIP(t *testing.T) {
	v4 := sockaddrIP(2, [6]uint32{0x0101a8c0}) // 192.168.1.1 in memory order
	if v4.String() != "192.168.1.1" {
		t.Errorf("%v", v4)
	}
	// flow info, then 2001:db8::1, then the scope ID.
	var d [6]uint32
	d[1] = 0xb80d0120
	d[4] = 0x01000000
	if ip := sockaddrIP(23, d); ip.String() != "2001:db8::1" {
		t.Errorf("%v", ip)
	}
	if sockaddrIP(0, d) != nil {
		t.Error("unknown family accepted")
	}
}

func TestWindowsRoutes(t *testing.T) {
	ip := net.ParseIP
	rows := []routeRow{
		{Dst: ip("192.168.0.0").To4(), Prefix: 24, IfIndex: 7, Metric: 281},
		{Dst: ip("0.0.0.0").To4(), Prefix: 0, Gateway: ip("192.168.0.1").To4(), IfIndex: 7, Metric: 25},
		{Dst: ip("192.168.0.185").To4(), Prefix: 32, Gateway: ip("0.0.0.0").To4(), IfIndex: 7},  // own address
		{Dst: ip("192.168.0.255").To4(), Prefix: 32, IfIndex: 7},                                // broadcast
		{Dst: ip("255.255.255.255").To4(), Prefix: 32, IfIndex: 7},                              // limited broadcast
		{Dst: ip("224.0.0.0").To4(), Prefix: 4, IfIndex: 7},                                     // multicast
		{Dst: ip("127.0.0.0").To4(), Prefix: 8, IfIndex: 1, Loopback: true},                     // loopback
		{Dst: ip("10.8.0.5").To4(), Prefix: 32, Gateway: ip("192.168.0.254").To4(), IfIndex: 7}, // host via gateway: kept
		{Dst: ip("::"), Prefix: 0, Gateway: ip("fe80::1"), IfIndex: 7, Metric: 281},
		{Dst: ip("fe80::"), Prefix: 64, IfIndex: 7},
		{Dst: ip("2001:db8::"), Prefix: 64, Gateway: ip("::"), IfIndex: 7, Metric: 281},
	}
	got := windowsRoutes(rows, func(i uint32) string { return map[uint32]string{7: "Ethernet"}[i] })
	var lines []string
	for _, r := range got {
		lines = append(lines, fmt.Sprintf("%s %s %s %s %d %v", r.Family, r.Destination, r.Gateway, r.Interface, r.Metric, r.Default))
	}
	want := []string{
		"ipv4 0.0.0.0/0 192.168.0.1 Ethernet 25 true",
		"ipv6 ::/0 fe80::1 Ethernet 281 true",
		"ipv4 10.8.0.5/32 192.168.0.254 Ethernet 0 false",
		"ipv4 192.168.0.0/24  Ethernet 281 false",
		"ipv6 2001:db8::/64  Ethernet 281 false",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestWindowsDNS(t *testing.T) {
	cfg := windowsDNS([]adapterDNS{
		{Up: true, Servers: []string{"10.0.0.1"}, Suffix: "vpn.example"},                                             // no gateway: after
		{Up: true, HasGateway: true, Servers: []string{"192.168.0.1", "fec0:0:0:ffff::1", "1.1.1.1"}, Suffix: "lan"}, // first
		{Up: false, HasGateway: true, Servers: []string{"8.8.8.8"}, Suffix: "down"},                                  // ignored
		{Up: true, HasGateway: true, Servers: []string{"1.1.1.1"}},                                                   // duplicate
	}, "corp.example, lan")
	if cfg.Resolver != "windows" || strings.Join(cfg.Nameservers, ",") != "192.168.0.1,1.1.1.1,10.0.0.1" ||
		strings.Join(cfg.Search, ",") != "corp.example,lan,vpn.example" {
		t.Errorf("%+v", cfg)
	}
	if cfg := windowsDNS(nil, ""); cfg.Nameservers == nil || len(cfg.Search) != 0 {
		t.Errorf("empty %+v", cfg)
	}
}
