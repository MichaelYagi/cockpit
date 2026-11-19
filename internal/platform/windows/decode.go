package windows

// Pure decoding of what the Windows calls return. This file has no build
// tag, so its tests run on every platform; the calls themselves are in
// the build-tagged files.

import (
	"encoding/binary"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

// LOGICAL_PROCESSOR_RELATIONSHIP values.
const (
	relationProcessorCore    = 0
	relationCache            = 2
	relationProcessorPackage = 3
	relationAll              = 0xffff
)

// processorTopology summarizes GetLogicalProcessorInformationEx: each
// entry is a relationship, its size, then PROCESSOR_RELATIONSHIP or
// CACHE_RELATIONSHIP. Affinity masks are 64-bit (GROUP_AFFINITY on
// 64-bit Windows, the only kind Cockpit builds for).
func processorTopology(b []byte) (cores, threads, sockets int, caches []core.CPUCache) {
	type key struct {
		level int
		typ   string
	}
	seen := map[key]core.CPUCache{}
	for len(b) >= 8 {
		rel := binary.LittleEndian.Uint32(b)
		size := int(binary.LittleEndian.Uint32(b[4:]))
		if size < 8 || size > len(b) {
			break
		}
		e := b[:size]
		b = b[size:]
		switch rel {
		case relationProcessorCore:
			// Flags@8, EfficiencyClass@9, Reserved[20], GroupCount@30, GroupMask[]@32.
			if len(e) < 32 {
				continue
			}
			cores++
			n := int(binary.LittleEndian.Uint16(e[30:]))
			for i := 0; i < n && 32+i*16+8 <= len(e); i++ {
				threads += bits.OnesCount64(binary.LittleEndian.Uint64(e[32+i*16:]))
			}
		case relationProcessorPackage:
			sockets++
		case relationCache:
			// Level@8, Associativity@9, LineSize@10, CacheSize@12, Type@16,
			// Reserved[18], GroupCount@38, GroupMask@40.
			if len(e) < 48 {
				continue
			}
			c := core.CPUCache{Level: int(e[8]), SizeBytes: uint64(binary.LittleEndian.Uint32(e[12:])),
				SharedBy: bits.OnesCount64(binary.LittleEndian.Uint64(e[40:]))}
			switch binary.LittleEndian.Uint32(e[16:]) {
			case 0:
				c.Type = "Unified"
			case 1:
				c.Type = "Instruction"
			case 2:
				c.Type = "Data"
			default:
				continue // trace caches are not listed
			}
			k := key{c.Level, c.Type}
			if _, ok := seen[k]; !ok {
				seen[k] = c
			}
		}
	}
	for _, c := range seen {
		caches = append(caches, c)
	}
	sort.Slice(caches, func(i, j int) bool {
		if caches[i].Level != caches[j].Level {
			return caches[i].Level < caches[j].Level
		}
		return caches[i].Type < caches[j].Type
	})
	return cores, threads, sockets, caches
}

// cpuIdentifier splits the registry's "Intel64 Family 6 Model 151
// Stepping 2" into family, model and stepping.
func cpuIdentifier(id string) (family, model, stepping string) {
	f := strings.Fields(id)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "Family":
			family = f[i+1]
		case "Model":
			model = f[i+1]
		case "Stepping":
			stepping = f[i+1]
		}
	}
	return family, model, stepping
}

// systemPowerStatus is SYSTEM_POWER_STATUS from GetSystemPowerStatus.
type systemPowerStatus struct {
	ACLineStatus        byte   // 0 offline, 1 online, 255 unknown
	BatteryFlag         byte   // 1 high, 2 low, 4 critical, 8 charging, 128 no battery, 255 unknown
	BatteryLifePercent  byte   // 0–100, 255 unknown
	SystemStatusFlag    byte   // battery saver
	BatteryLifeTime     uint32 // seconds remaining, 0xFFFFFFFF unknown
	BatteryFullLifeTime uint32
}

const (
	batteryCharging = 8
	batteryNone     = 128
	unknownByte     = 255
	unknownSeconds  = 0xFFFFFFFF
)

// powerSupplies turns the power status into the battery (when there is
// one) and the mains adapter.
func powerSupplies(s systemPowerStatus) core.PowerSupplies {
	out := core.PowerSupplies{}
	if s.BatteryFlag != batteryNone && s.BatteryFlag != unknownByte {
		b := core.PowerSupply{Name: "Battery", Type: "battery", Status: "unknown"}
		if s.BatteryLifePercent <= 100 {
			v := float64(s.BatteryLifePercent)
			b.CapacityPercent = &v
		}
		switch {
		case s.BatteryFlag&batteryCharging != 0:
			b.Status = "charging"
		case s.ACLineStatus == 1 && b.CapacityPercent != nil && *b.CapacityPercent >= 100:
			b.Status = "full"
		case s.ACLineStatus == 1:
			b.Status = "not_charging"
		case s.ACLineStatus == 0:
			b.Status = "discharging"
		}
		if b.Status == "discharging" && s.BatteryLifeTime != unknownSeconds && s.BatteryLifeTime > 0 {
			h := float64(s.BatteryLifeTime) / 3600
			b.HoursRemaining = &h
		}
		out = append(out, b)
	}
	// A desktop reports AC online with no battery; that is still worth a
	// mains entry, as Linux shows one.
	if s.ACLineStatus <= 1 {
		online := s.ACLineStatus == 1
		out = append(out, core.PowerSupply{Name: "AC adapter", Type: "mains", Online: &online})
	}
	return out
}

// rawSMBIOS splits GetSystemFirmwareTable('RSMB') output: an 8-byte
// RawSMBIOSData header (calling method, major, minor, DMI revision,
// table length) and the table.
func rawSMBIOS(b []byte) [][]byte {
	if len(b) < 8 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b[4:]))
	t := b[8:]
	if n < len(t) {
		t = t[:n]
	}
	return portable.SMBIOSStructures(t)
}

// smbiosInventory picks memory modules (type 17) and the chassis type
// (type 3, byte 5) from the SMBIOS structures.
func smbiosInventory(structs [][]byte) (core.MemoryModules, string) {
	var mods core.MemoryModules
	chassis := ""
	for _, s := range structs {
		switch s[0] {
		case 17:
			if m, ok := portable.SMBIOSMemory(s); ok {
				mods = append(mods, m)
			}
		case 3:
			if chassis == "" && len(s) > 5 && int(s[1]) > 5 {
				chassis = portable.ChassisTypes[strconv.Itoa(int(s[5]&0x7f))]
			}
		}
	}
	return mods, chassis
}
