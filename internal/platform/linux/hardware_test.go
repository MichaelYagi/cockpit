//go:build linux

package linux

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const x86CPUInfo = `processor	: 0
vendor_id	: AuthenticAMD
cpu family	: 23
model		: 113
model name	: AMD Ryzen 5 3600X 6-Core Processor
stepping	: 0
physical id	: 0
core id		: 0
flags		: fpu sse4_2 avx avx2 aes svm hypervisor

processor	: 1
vendor_id	: AuthenticAMD
physical id	: 0
core id		: 0
flags		: fpu sse4_2 avx avx2 aes svm hypervisor

processor	: 2
physical id	: 0
core id		: 1

processor	: 3
physical id	: 0
core id		: 1
`

const armCPUInfo = `processor	: 0
BogoMIPS	: 108.00
Features	: fp asimd evtstrm aes crc32
CPU implementer	: 0x41
CPU architecture: 8
CPU part	: 0xd08
CPU revision	: 3

processor	: 1
Features	: fp asimd evtstrm aes crc32
`

func TestParseCPUInfo(t *testing.T) {
	x := parseCPUInfo([]byte(x86CPUInfo))
	if x.Vendor != "AuthenticAMD" || x.Family != "23" || x.Model != "113" || x.Threads != 4 || x.Cores != 2 || x.Sockets != 1 ||
		x.Virtualization != "AMD-V" || !x.Hypervisor || strings.Join(x.Flags, ",") != "sse4_2,avx,avx2,aes,svm,hypervisor" {
		t.Errorf("x86 %+v", x)
	}
	a := parseCPUInfo([]byte(armCPUInfo))
	if a.Vendor != "ARM" || a.Model != "0xd08" || a.Threads != 2 || a.Cores != 2 || a.Sockets != 1 || strings.Join(a.Flags, ",") != "aes,asimd,crc32" {
		t.Errorf("arm %+v", a)
	}
}

func TestSizesAndCPULists(t *testing.T) {
	for in, want := range map[string]uint64{"32K": 32 << 10, "16384K": 16 << 20, "1M": 1 << 20, "512": 512, "x": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d", in, got)
		}
	}
	for in, want := range map[string]int{"0-1": 2, "0-3,8,10-11": 7, "5": 1, "": 0, "3-1": 0} {
		if got := countCPUList(in); got != want {
			t.Errorf("countCPUList(%q) = %d", in, got)
		}
	}
}

func TestCPUInfoCollector(t *testing.T) {
	root := tree(t, map[string]string{
		"proc/cpuinfo": x86CPUInfo,
		"sys/devices/system/cpu/cpu0/cache/index0/level":           "1\n",
		"sys/devices/system/cpu/cpu0/cache/index0/type":            "Data\n",
		"sys/devices/system/cpu/cpu0/cache/index0/size":            "32K\n",
		"sys/devices/system/cpu/cpu0/cache/index0/shared_cpu_list": "0-1\n",
		"sys/devices/system/cpu/cpu0/cache/index3/level":           "3\n",
		"sys/devices/system/cpu/cpu0/cache/index3/type":            "Unified\n",
		"sys/devices/system/cpu/cpu0/cache/index3/size":            "16384K\n",
		"sys/devices/system/cpu/cpu0/cache/index3/shared_cpu_list": "0-3\n",
		"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq":     "4400000\n",
	})
	r := newCPUInfo(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapHardwareCPU, core.Available)
	info := record[*core.CPUInfo](t, r)
	if len(info.Caches) != 2 || info.Caches[1].SizeBytes != 16<<20 || info.Caches[1].SharedBy != 4 || info.MaxFrequencyMHz == nil || *info.MaxFrequencyMHz != 4400 {
		t.Errorf("%+v", info)
	}
}

const pciIDs = `# test pci.ids
10de  NVIDIA Corporation
	2504  GA106 [GeForce RTX 3060 Lite Hash Rate]
		1462 397d  sub-device line is ignored
8086  Intel Corporation
	15f3  Ethernet Controller I225-V
C 00  Unclassified device
`

func TestPCI(t *testing.T) {
	gpu := "sys/devices/pci0000:00/0000:00:01.0/0000:01:00.0"
	nic := "sys/devices/pci0000:00/0000:00:1c.0/0000:05:00.0"
	root := tree(t, map[string]string{
		gpu + "/vendor": "0x10de\n", gpu + "/device": "0x2504\n", gpu + "/class": "0x030000\n",
		nic + "/vendor": "0x8086\n", nic + "/device": "0x15f3\n", nic + "/class": "0x020000\n",
		"sys/bus/pci/drivers/nvidia/.keep": "",
		gpu + "/driver":                    "->../../../../bus/pci/drivers/nvidia",
		"sys/bus/pci/devices/0000:01:00.0": "->../../../devices/pci0000:00/0000:00:01.0/0000:01:00.0",
		"sys/bus/pci/devices/0000:05:00.0": "->../../../devices/pci0000:00/0000:00:1c.0/0000:05:00.0",
		"usr/share/hwdata/pci.ids":         pciIDs,
	})
	r := newPCI(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapHardwarePCI, core.Available)
	devs := record[core.PCIDevices](t, r)
	if len(devs) != 2 {
		t.Fatalf("%+v", devs)
	}
	g, n := devs[0], devs[1]
	if g.Address != "0000:01:00.0" || g.ClassName != "VGA compatible controller" || g.VendorName != "NVIDIA Corporation" ||
		!strings.Contains(g.DeviceName, "RTX 3060") || g.Driver != "nvidia" {
		t.Errorf("gpu %+v", g)
	}
	if n.ClassName != "Ethernet controller" || n.DeviceName != "Ethernet Controller I225-V" || n.Driver != "" {
		t.Errorf("nic %+v", n)
	}
}

func TestPCIWithoutIDDatabase(t *testing.T) {
	d := "sys/bus/pci/devices/0000:00:00.0"
	root := tree(t, map[string]string{d + "/vendor": "0x1234\n", d + "/device": "0x1111\n", d + "/class": "0x0c0330\n"})
	devs := record[core.PCIDevices](t, newPCI(env(root)).Collect(context.Background()))
	if devs[0].VendorName != "" || devs[0].ClassName != "USB controller" {
		t.Errorf("%+v", devs[0])
	}
}

func TestUSB(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/bus/usb/devices/usb1/idVendor":           "1d6b\n", // root hub: skipped
		"sys/bus/usb/devices/1-0:1.0/bInterfaceClass": "09\n",
		"sys/bus/usb/devices/1-2/idVendor":            "046d\n",
		"sys/bus/usb/devices/1-2/idProduct":           "c52b\n",
		"sys/bus/usb/devices/1-2/product":             "USB Receiver\n",
		"sys/bus/usb/devices/1-2/serial":              "FAKE-USB-SERIAL\n",
		"sys/bus/usb/devices/1-2/speed":               "12\n",
		"sys/bus/usb/devices/2-1/idVendor":            "0bda\n",
		"sys/bus/usb/devices/2-1/idProduct":           "8153\n",
		"usr/share/misc/usb.ids":                      "046d  Logitech, Inc.\n\tc52b  Unifying Receiver\n0bda  Realtek Semiconductor Corp.\n\t8153  RTL8153 Gigabit Ethernet Adapter\n",
	})
	r := newUSB(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapHardwareUSB, core.Available)
	devs := record[core.USBDevices](t, r)
	if len(devs) != 2 {
		t.Fatalf("%+v", devs)
	}
	if d := devs[0]; d.Product != "USB Receiver" || d.Manufacturer != "Logitech, Inc." || d.Serial != "FAKE-USB-SERIAL" || *d.SpeedMbps != 12 {
		t.Errorf("1-2 %+v", d)
	}
	if d := devs[1]; d.Product != "RTL8153 Gigabit Ethernet Adapter" || d.SpeedMbps != nil {
		t.Errorf("2-1 %+v", d)
	}
}

// smbios17 builds a type 17 structure: size word, locator, type, speed,
// manufacturer, serial, part number, optional extended size and
// configured speed.
func smbios17(size uint16, ext uint32, memType byte, speed, configured uint16, strs ...string) []byte {
	b := make([]byte, 0x22)
	b[0], b[1] = 17, 0x22
	b[0x0C], b[0x0D] = byte(size), byte(size>>8)
	b[0x10] = 1 // locator = string 1
	b[0x12] = memType
	b[0x15], b[0x16] = byte(speed), byte(speed>>8)
	b[0x17], b[0x18], b[0x1A] = 2, 3, 4
	b[0x1C], b[0x1D], b[0x1E], b[0x1F] = byte(ext), byte(ext>>8), byte(ext>>16), byte(ext>>24)
	b[0x20], b[0x21] = byte(configured), byte(configured>>8)
	for _, s := range strs {
		b = append(append(b, s...), 0)
	}
	return append(b, 0)
}

func TestParseSMBIOSMemory(t *testing.T) {
	m, ok := parseSMBIOSMemory(smbios17(16384, 0, 0x1A, 3200, 3000, "DIMM_A1", "Kingston", "FAKE-DIMM-SN", "KF432C16BB/16"))
	if !ok || m.SizeBytes != 16<<30 || m.Type != "DDR4" || m.SpeedMTs != 3000 || m.Locator != "DIMM_A1" || m.Manufacturer != "Kingston" ||
		m.PartNumber != "KF432C16BB/16" || m.Serial != "FAKE-DIMM-SN" {
		t.Errorf("%+v", m)
	}
	if m, ok := parseSMBIOSMemory(smbios17(0x7FFF, 65536, 0x22, 4800, 0, "A", "B", "C", "D")); !ok || m.SizeBytes != 64<<30 || m.Type != "DDR5" || m.SpeedMTs != 4800 {
		t.Errorf("extended %+v", m)
	}
	if m, ok := parseSMBIOSMemory(smbios17(0x8000|512, 0, 0x18, 1600, 0, "A", "B", "C", "D")); !ok || m.SizeBytes != 512<<10 {
		t.Errorf("KB unit %+v", m)
	}
	if _, ok := parseSMBIOSMemory(smbios17(0, 0, 0x1A, 0, 0, "EMPTY", "", "", "")); ok {
		t.Error("empty slot reported")
	}
	for _, bad := range [][]byte{nil, {17}, {16, 0x22}, append([]byte{17, 0xFF}, make([]byte, 30)...)} {
		if _, ok := parseSMBIOSMemory(bad); ok {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestDMI(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/dmi/id/sys_vendor":       "Micro-Star International Co., Ltd.\n",
		"sys/class/dmi/id/product_name":     "MS-7C37\n",
		"sys/class/dmi/id/board_name":       "MPG X570 GAMING PLUS\n",
		"sys/class/dmi/id/bios_version":     "H.40\n",
		"sys/class/dmi/id/chassis_type":     "3\n",
		"sys/class/dmi/id/product_serial":   "FAKE-BOARD-SN\n",
		"sys/firmware/dmi/entries/17-0/raw": string(smbios17(8192, 0, 0x1A, 3200, 0, "DIMM_A2", "Corsair", "SN", "CMK16GX4")),
		"sys/firmware/dmi/entries/17-1/raw": string(smbios17(0, 0, 0, 0, 0, "DIMM_B1", "", "", "")), // empty slot
	})
	r := newDMI(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapHardwareBoard, core.Available)
	wantStatus(t, r, core.CapHardwareMemory, core.Available)
	b := record[*core.BoardInfo](t, r)
	if b.ProductName != "MS-7C37" || b.ChassisType != "Desktop" || b.Serial != "FAKE-BOARD-SN" || b.BIOSVersion != "H.40" {
		t.Errorf("board %+v", b)
	}
	if mods := record[core.MemoryModules](t, r); len(mods) != 1 || mods[0].SizeBytes != 8<<30 || mods[0].Locator != "DIMM_A2" {
		t.Errorf("modules %+v", mods)
	}
}

func TestDMIMemoryNeedsRoot(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/class/dmi/id/sys_vendor":       "Vendor\n",
		"sys/firmware/dmi/entries/17-0/raw": string(smbios17(8192, 0, 0x1A, 3200, 0, "A", "B", "C", "D")),
	})
	unreadable(t, filepath.Join(root, "sys/firmware/dmi/entries/17-0/raw"))
	r := newDMI(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapHardwareBoard, core.Available)
	if s := r.Status[core.CapHardwareMemory]; s.Availability != core.PermissionDenied || !strings.Contains(s.Reason, "root") {
		t.Errorf("%+v", s)
	}
}
