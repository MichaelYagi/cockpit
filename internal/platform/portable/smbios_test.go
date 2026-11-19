package portable

import "testing"

func TestSMBIOSStructures(t *testing.T) {
	var table []byte
	table = append(table, 0, 4, 0, 0, 'B', 'I', 'O', 'S', 0, 0) // type 0 with one string
	table = append(table, 3, 6, 1, 0, 0, 9, 0, 0)               // type 3 (laptop), no strings
	mem := []byte{17, 0x15, 2, 0}
	mem = append(mem, make([]byte, 0x15-4)...)
	mem[0x0C], mem[0x0D] = 0x00, 0x40            // 16384 MB
	mem[0x10], mem[0x11], mem[0x12] = 1, 2, 0x1A // locator, bank locator, DDR4
	mem = append(mem, "DIMM 1\x00P0 CHANNEL B\x00\x00"...)
	table = append(table, mem...)
	table = append(table, 127, 4, 3, 0, 0, 0)
	table = append(table, 1, 2, 3) // trailing junk after the end is ignored

	got := SMBIOSStructures(table)
	if len(got) != 4 || got[0][0] != 0 || got[1][0] != 3 || got[2][0] != 17 || got[3][0] != 127 {
		t.Fatalf("%d structures: %v", len(got), got)
	}
	if ChassisTypes["9"] != "Laptop" || got[1][5] != 9 {
		t.Errorf("chassis %v", got[1])
	}
	m, ok := SMBIOSMemory(got[2])
	if !ok || m.SizeBytes != 16<<30 || m.Locator != "P0 CHANNEL B DIMM 1" || m.Type != "DDR4" {
		t.Errorf("%+v %v", m, ok)
	}
	for i := range table {
		_ = SMBIOSStructures(table[:i]) // no panic on any prefix
	}
	if got := SMBIOSStructures([]byte{17, 0x40, 0, 0}); len(got) != 0 {
		t.Errorf("oversized length accepted: %v", got)
	}
}
