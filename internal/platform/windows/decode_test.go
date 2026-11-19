package windows

import (
	"encoding/binary"
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
