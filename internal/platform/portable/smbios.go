package portable

// SMBIOS decoding shared by Linux (one structure per sysfs file) and
// Windows (the whole table from GetSystemFirmwareTable).

import (
	"bytes"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// ChassisTypes names SMBIOS chassis types (type 3, byte 5, low 7 bits).
var ChassisTypes = map[string]string{
	"1": "Other", "2": "Unknown", "3": "Desktop", "4": "Low profile desktop", "5": "Pizza box", "6": "Mini tower",
	"7": "Tower", "8": "Portable", "9": "Laptop", "10": "Notebook", "11": "Hand held", "12": "Docking station",
	"13": "All in one", "14": "Sub notebook", "15": "Space-saving", "16": "Lunch box", "17": "Main server chassis",
	"23": "Rack mount chassis", "24": "Sealed-case PC", "30": "Tablet", "31": "Convertible", "32": "Detachable",
	"35": "Mini PC", "36": "Stick PC",
}

var smbiosMemoryTypes = map[byte]string{
	0x12: "DDR", 0x13: "DDR2", 0x18: "DDR3", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3",
	0x1E: "LPDDR4", 0x22: "DDR5", 0x23: "LPDDR5",
}

// SMBIOSMemory decodes an SMBIOS type 17 (Memory Device) structure.
// Empty slots (size 0) return ok=false.
func SMBIOSMemory(b []byte) (core.MemoryModule, bool) {
	var m core.MemoryModule
	if len(b) < 0x15 || b[0] != 17 {
		return m, false
	}
	n := int(b[1]) // formatted area length
	if n > len(b) || n < 0x15 {
		return m, false
	}
	word := func(off int) uint32 {
		if off+2 > n {
			return 0
		}
		return uint32(b[off]) | uint32(b[off+1])<<8
	}
	dword := func(off int) uint64 {
		if off+4 > n {
			return 0
		}
		return uint64(b[off]) | uint64(b[off+1])<<8 | uint64(b[off+2])<<16 | uint64(b[off+3])<<24
	}
	size := word(0x0C)
	switch {
	case size == 0 || size == 0xFFFF:
		return m, false
	case size == 0x7FFF:
		m.SizeBytes = (dword(0x1C) & 0x7FFFFFFF) << 20
	case size&0x8000 != 0:
		m.SizeBytes = uint64(size&0x7FFF) << 10
	default:
		m.SizeBytes = uint64(size) << 20
	}
	// Strings follow the formatted area, NUL-terminated, 1-indexed.
	var strs []string
	for _, s := range bytes.Split(b[n:], []byte{0}) {
		if len(s) == 0 {
			break
		}
		strs = append(strs, strings.TrimSpace(string(s)))
	}
	str := func(off int) string {
		if off >= n {
			return ""
		}
		i := int(b[off])
		if i == 0 || i > len(strs) {
			return ""
		}
		return strs[i-1]
	}
	m.Locator = str(0x10)
	// Boards often repeat "DIMM 0" in each channel; the bank locator
	// ("P0 CHANNEL A") tells them apart.
	if bank := str(0x11); bank != "" && !strings.Contains(m.Locator, bank) {
		m.Locator = strings.TrimSpace(bank + " " + m.Locator)
	}
	m.Type = smbiosMemoryTypes[b[0x12]]
	m.SpeedMTs = int(word(0x15))
	if cs := int(word(0x20)); cs > 0 {
		m.SpeedMTs = cs // configured speed, when present
	}
	m.Manufacturer = str(0x17)
	m.Serial = str(0x18)
	m.PartNumber = str(0x1A)
	return m, true
}

// SMBIOSStructures splits a raw SMBIOS table into structures, each its
// formatted area followed by its strings. It stops at the end-of-table
// structure (type 127) or at the first malformed one.
func SMBIOSStructures(table []byte) [][]byte {
	var out [][]byte
	for len(table) >= 4 && len(out) < 4096 {
		n := int(table[1])
		if n < 4 || n > len(table) {
			break
		}
		end := bytes.Index(table[n:], []byte{0, 0})
		if end < 0 {
			break
		}
		end += n + 2
		out = append(out, table[:end])
		if table[0] == 127 {
			break
		}
		table = table[end:]
	}
	return out
}
