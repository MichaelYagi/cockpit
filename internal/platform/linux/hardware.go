//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func inventory(id, source string, caps ...core.Capability) collector.Descriptor {
	return collector.Descriptor{ID: id, Version: version, Source: source, Capabilities: caps,
		Class: collector.Inventory, DefaultInterval: 5 * time.Minute, DefaultTimeout: 3 * time.Second}
}

// ---- CPU details ----

// notableFlags are CPU features worth showing; the full list is long.
var notableFlags = []string{"sse4_2", "avx", "avx2", "avx512f", "fma", "bmi2", "aes", "sha_ni", "vmx", "svm", "hypervisor",
	"asimd", "sve", "sha2", "crc32"}

type cpuInfoCollector struct {
	base
	env collector.Env
}

func newCPUInfo(env collector.Env) *cpuInfoCollector {
	c := &cpuInfoCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *cpuInfoCollector) Descriptor() collector.Descriptor {
	d := inventory("linux.cpuinfo", "procfs, sysfs", core.CapHardwareCPU)
	d.DefaultInterval = time.Hour
	return d
}

func (c *cpuInfoCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	b, err := readFile(c.env.ProcRoot, "cpuinfo")
	if err != nil {
		res.Status[core.CapHardwareCPU] = status(err, "CPU information")
		return res
	}
	info := parseCPUInfo(b)
	if info.Threads == 0 {
		res.Status[core.CapHardwareCPU] = status(errParse, "CPU information")
		return res
	}
	info.Caches = c.caches()
	if khz, err := readUint(c.env.SysRoot, "devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); err == nil && khz > 0 {
		mhz := float64(khz) / 1000
		info.MaxFrequencyMHz = &mhz
	}
	res.Status[core.CapHardwareCPU] = available()
	res.Records = []core.Record{&info}
	return res
}

// parseCPUInfo summarizes /proc/cpuinfo.
func parseCPUInfo(b []byte) core.CPUInfo {
	var info core.CPUInfo
	sockets := map[string]bool{}
	cores := map[string]bool{}
	flags := map[string]bool{}
	var phys, core string
	flush := func() {
		if phys != "" || core != "" {
			sockets[phys] = true
			cores[phys+"/"+core] = true
		}
		phys, core = "", ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			flush()
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			info.Threads++
		case "vendor_id", "CPU implementer":
			if info.Vendor == "" {
				info.Vendor = cpuVendor(v)
			}
		case "model name":
			if info.ModelName == "" {
				info.ModelName = v
			}
		case "cpu family", "CPU architecture":
			if info.Family == "" {
				info.Family = v
			}
		case "model", "CPU part":
			if info.Model == "" {
				info.Model = v
			}
		case "stepping", "CPU revision":
			if info.Stepping == "" {
				info.Stepping = v
			}
		case "physical id":
			phys = v
		case "core id":
			core = v
		case "flags", "Features":
			if len(flags) == 0 {
				for _, f := range strings.Fields(v) {
					flags[f] = true
				}
			}
		}
	}
	flush()
	info.Sockets, info.Cores = len(sockets), len(cores)
	if info.Sockets == 0 {
		info.Sockets = 1
	}
	if info.Cores == 0 {
		info.Cores = info.Threads
	}
	for _, f := range notableFlags {
		if flags[f] {
			info.Flags = append(info.Flags, f)
		}
	}
	switch {
	case flags["vmx"]:
		info.Virtualization = "VT-x"
	case flags["svm"]:
		info.Virtualization = "AMD-V"
	}
	info.Hypervisor = flags["hypervisor"]
	return info
}

func cpuVendor(v string) string {
	switch v {
	case "0x41":
		return "ARM"
	case "0x42":
		return "Broadcom"
	case "0x51":
		return "Qualcomm"
	case "0x61":
		return "Apple"
	case "0xc0":
		return "Ampere"
	}
	return v
}

func (c *cpuInfoCollector) caches() []core.CPUCache {
	dir := "devices/system/cpu/cpu0/cache"
	names, err := listDir(c.env.SysRoot, dir)
	if err != nil {
		return nil
	}
	sort.Strings(names)
	var out []core.CPUCache
	for _, n := range names {
		if !strings.HasPrefix(n, "index") {
			continue
		}
		p := filepath.Join(dir, n)
		level, err := readInt(c.env.SysRoot, filepath.Join(p, "level"))
		if err != nil {
			continue
		}
		typ, _ := readTrim(c.env.SysRoot, filepath.Join(p, "type"))
		size, _ := readTrim(c.env.SysRoot, filepath.Join(p, "size"))
		shared, _ := readTrim(c.env.SysRoot, filepath.Join(p, "shared_cpu_list"))
		out = append(out, core.CPUCache{Level: int(level), Type: typ, SizeBytes: parseSize(size), SharedBy: countCPUList(shared)})
	}
	return out
}

// parseSize parses sysfs sizes like "32K", "16384K", "1M".
func parseSize(s string) uint64 {
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n * mult
}

// countCPUList counts CPUs in a list like "0-3,8,10-11".
func countCPUList(s string) int {
	n := 0
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(lo)
		if !isRange {
			if err1 == nil {
				n++
			}
			continue
		}
		b, err2 := strconv.Atoi(hi)
		if err1 == nil && err2 == nil && b >= a && b-a < 1<<16 {
			n += b - a + 1
		}
	}
	return n
}

// ---- hardware ID databases ----

// lookupIDs looks up names in pci.ids / usb.ids. Only the IDs actually
// needed are kept, so the large files are scanned, not stored.
func lookupIDs(path string, want map[[2]string]bool) (vendors map[string]string, devices map[[2]string]string) {
	vendors, devices = map[string]string{}, map[[2]string]string{}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	wantVendor := map[string]bool{}
	for k := range want {
		wantVendor[k[0]] = true
	}
	sc := bufio.NewScanner(f)
	vendor := ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == 'C' && len(line) > 1 && line[1] == ' ' {
			break // class section: no more vendors
		}
		if line[0] != '\t' {
			id, name, ok := strings.Cut(line, "  ")
			vendor = ""
			if ok && wantVendor[strings.ToLower(id)] {
				vendor = strings.ToLower(id)
				vendors[vendor] = strings.TrimSpace(name)
			}
			continue
		}
		if vendor == "" || strings.HasPrefix(line, "\t\t") {
			continue
		}
		id, name, ok := strings.Cut(strings.TrimPrefix(line, "\t"), "  ")
		if k := [2]string{vendor, strings.ToLower(id)}; ok && want[k] {
			devices[k] = strings.TrimSpace(name)
		}
	}
	return
}

// firstExisting returns the first of root/rels that exists, or "".
func firstExisting(root string, rels ...string) string {
	for _, r := range rels {
		p := filepath.Join(root, r)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ---- PCI ----

var pciClasses = map[string]string{
	"0100": "SCSI storage controller", "0101": "IDE interface", "0104": "RAID bus controller", "0106": "SATA controller",
	"0107": "Serial Attached SCSI controller", "0108": "Non-Volatile memory controller", "0180": "Mass storage controller",
	"0200": "Ethernet controller", "0280": "Network controller", "0300": "VGA compatible controller", "0302": "3D controller",
	"0380": "Display controller", "0401": "Multimedia audio controller", "0403": "Audio device", "0480": "Multimedia controller",
	"0500": "RAM memory", "0580": "Memory controller", "0600": "Host bridge", "0601": "ISA bridge", "0604": "PCI bridge",
	"0680": "Bridge", "0780": "Communication controller", "0800": "PIC", "0880": "System peripheral", "0c03": "USB controller",
	"0c05": "SMBus", "0c80": "Serial bus controller", "0d80": "Wireless controller", "1080": "Encryption controller",
	"1180": "Signal processing controller", "1200": "Processing accelerators", "1300": "Non-Essential Instrumentation",
}

var pciBaseClasses = map[string]string{
	"01": "Mass storage controller", "02": "Network controller", "03": "Display controller", "04": "Multimedia controller",
	"05": "Memory controller", "06": "Bridge", "07": "Communication controller", "08": "System peripheral",
	"09": "Input device controller", "0c": "Serial bus controller", "0d": "Wireless controller", "10": "Encryption controller",
	"11": "Signal processing controller", "12": "Processing accelerators",
}

func pciClassName(code string) string {
	c := strings.TrimPrefix(strings.ToLower(code), "0x")
	if len(c) < 4 {
		return "Unknown"
	}
	if n, ok := pciClasses[c[:4]]; ok {
		return n
	}
	if n, ok := pciBaseClasses[c[:2]]; ok {
		return n
	}
	return "Unknown"
}

type pciCollector struct {
	base
	env collector.Env
}

func newPCI(env collector.Env) *pciCollector {
	c := &pciCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *pciCollector) Descriptor() collector.Descriptor {
	return inventory("linux.pci", "sysfs, pci.ids", core.CapHardwarePCI)
}

func listPCI(sysRoot, usrRoot string) ([]core.PCIDevice, error) {
	names, err := listDir(sysRoot, "bus/pci/devices")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []core.PCIDevice
	want := map[[2]string]bool{}
	for _, n := range names {
		dir := filepath.Join("bus", "pci", "devices", n)
		vendor, err := readTrim(sysRoot, filepath.Join(dir, "vendor"))
		if err != nil {
			continue
		}
		device, _ := readTrim(sysRoot, filepath.Join(dir, "device"))
		class, _ := readTrim(sysRoot, filepath.Join(dir, "class"))
		d := core.PCIDevice{Address: n, ClassCode: class, ClassName: pciClassName(class), VendorID: vendor, DeviceID: device}
		if target, err := os.Readlink(filepath.Join(sysRoot, dir, "driver")); err == nil {
			d.Driver = filepath.Base(target)
		}
		want[[2]string{strings.TrimPrefix(vendor, "0x"), strings.TrimPrefix(device, "0x")}] = true
		out = append(out, d)
	}
	if db := firstExisting(usrRoot, "share/hwdata/pci.ids", "share/misc/pci.ids", "share/pci.ids"); db != "" && len(out) > 0 {
		vendors, devices := lookupIDs(db, want)
		for i, d := range out {
			v, dev := strings.TrimPrefix(d.VendorID, "0x"), strings.TrimPrefix(d.DeviceID, "0x")
			out[i].VendorName, out[i].DeviceName = vendors[v], devices[[2]string{v, dev}]
		}
	}
	return out, nil
}

func (c *pciCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	devs, err := listPCI(c.env.SysRoot, c.env.UsrRoot)
	switch {
	case err != nil:
		res.Status[core.CapHardwarePCI] = status(err, "PCI device information")
	case len(devs) == 0:
		res.Status[core.CapHardwarePCI] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "No PCI devices are visible."}
	default:
		res.Status[core.CapHardwarePCI] = available()
		res.Records = []core.Record{core.PCIDevices(devs)}
	}
	return res
}

// ---- USB ----

type usbCollector struct {
	base
	env collector.Env
}

func newUSB(env collector.Env) *usbCollector {
	c := &usbCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *usbCollector) Descriptor() collector.Descriptor {
	return inventory("linux.usb", "sysfs, usb.ids", core.CapHardwareUSB)
}

func (c *usbCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	names, err := listDir(c.env.SysRoot, "bus/usb/devices")
	if err != nil {
		res.Status[core.CapHardwareUSB] = status(err, "USB device information")
		return res
	}
	sort.Strings(names)
	var out core.USBDevices
	want := map[[2]string]bool{}
	for _, n := range names {
		// Skip interfaces ("1-2:1.0") and root hubs ("usb1").
		if strings.Contains(n, ":") || strings.HasPrefix(n, "usb") {
			continue
		}
		dir := filepath.Join("bus", "usb", "devices", n)
		vendor, err := readTrim(c.env.SysRoot, filepath.Join(dir, "idVendor"))
		if err != nil {
			continue
		}
		d := core.USBDevice{Path: n, VendorID: vendor}
		d.ProductID, _ = readTrim(c.env.SysRoot, filepath.Join(dir, "idProduct"))
		d.Manufacturer, _ = readTrim(c.env.SysRoot, filepath.Join(dir, "manufacturer"))
		d.Product, _ = readTrim(c.env.SysRoot, filepath.Join(dir, "product"))
		d.Serial, _ = readTrim(c.env.SysRoot, filepath.Join(dir, "serial"))
		if s, err := readTrim(c.env.SysRoot, filepath.Join(dir, "speed")); err == nil {
			if v, err := strconv.ParseFloat(s, 64); err == nil {
				d.SpeedMbps = &v
			}
		}
		want[[2]string{strings.ToLower(vendor), strings.ToLower(d.ProductID)}] = true
		out = append(out, d)
	}
	if db := firstExisting(c.env.UsrRoot, "share/hwdata/usb.ids", "share/misc/usb.ids", "share/usb.ids"); db != "" && len(out) > 0 {
		vendors, products := lookupIDs(db, want)
		for i, d := range out {
			k := [2]string{strings.ToLower(d.VendorID), strings.ToLower(d.ProductID)}
			if d.Manufacturer == "" {
				out[i].Manufacturer = vendors[k[0]]
			}
			if d.Product == "" {
				out[i].Product = products[k]
			}
		}
	}
	if len(out) == 0 {
		res.Status[core.CapHardwareUSB] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "No USB devices are connected or visible."}
		return res
	}
	res.Status[core.CapHardwareUSB] = available()
	res.Records = []core.Record{out}
	return res
}

// ---- DMI: board, firmware and memory modules ----

var chassisTypes = map[string]string{
	"1": "Other", "2": "Unknown", "3": "Desktop", "4": "Low profile desktop", "5": "Pizza box", "6": "Mini tower",
	"7": "Tower", "8": "Portable", "9": "Laptop", "10": "Notebook", "11": "Hand held", "12": "Docking station",
	"13": "All in one", "14": "Sub notebook", "15": "Space-saving", "16": "Lunch box", "17": "Main server chassis",
	"23": "Rack mount chassis", "24": "Sealed-case PC", "30": "Tablet", "31": "Convertible", "32": "Detachable",
	"35": "Mini PC", "36": "Stick PC",
}

type dmiCollector struct {
	base
	env collector.Env
}

func newDMI(env collector.Env) *dmiCollector {
	c := &dmiCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *dmiCollector) Descriptor() collector.Descriptor {
	d := inventory("linux.dmi", "sysfs/dmi", core.CapHardwareBoard, core.CapHardwareMemory)
	d.DefaultInterval = time.Hour
	return d
}

func (c *dmiCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	dir := "class/dmi/id"
	if _, err := listDir(c.env.SysRoot, dir); err != nil {
		st := status(err, "firmware (DMI) information")
		if st.Availability == core.Unsupported {
			st.Reason = "This system does not expose DMI firmware tables (common on ARM boards, containers and WSL)."
		}
		res.Status[core.CapHardwareBoard], res.Status[core.CapHardwareMemory] = st, st
		return res
	}
	attr := func(name string) string { s, _ := readTrim(c.env.SysRoot, filepath.Join(dir, name)); return s }
	b := &core.BoardInfo{
		SystemVendor: attr("sys_vendor"), ProductName: attr("product_name"),
		BoardVendor: attr("board_vendor"), BoardName: attr("board_name"),
		BIOSVendor: attr("bios_vendor"), BIOSVersion: attr("bios_version"), BIOSDate: attr("bios_date"),
		Serial: attr("product_serial"), // usually root-only; never exposed (D9)
	}
	if ct := attr("chassis_type"); ct != "" {
		b.ChassisType = chassisTypes[ct]
	}
	res.Status[core.CapHardwareBoard] = available()
	res.Records = append(res.Records, b)

	mods, st := c.memoryModules()
	res.Status[core.CapHardwareMemory] = st
	if st.Availability == core.Available {
		res.Records = append(res.Records, mods)
	}
	return res
}

func (c *dmiCollector) memoryModules() (core.MemoryModules, core.CapabilityStatus) {
	dir := "firmware/dmi/entries"
	names, err := listDir(c.env.SysRoot, dir)
	if err != nil {
		return nil, status(err, "memory module (SMBIOS) tables")
	}
	sort.Strings(names)
	var out core.MemoryModules
	denied := false
	for _, n := range names {
		if !strings.HasPrefix(n, "17-") {
			continue
		}
		raw, err := readFile(c.env.SysRoot, filepath.Join(dir, n, "raw"))
		if err != nil {
			denied = denied || os.IsPermission(err)
			continue
		}
		if m, ok := parseSMBIOSMemory(raw); ok {
			out = append(out, m)
		}
	}
	switch {
	case len(out) > 0:
		return out, available()
	case denied:
		return nil, core.CapabilityStatus{Availability: core.PermissionDenied,
			Reason: "Reading memory module details requires root (SMBIOS tables are root-only)."}
	}
	return nil, core.CapabilityStatus{Availability: core.Unsupported, Reason: "No memory module entries are present in the firmware tables."}
}

var smbiosMemoryTypes = map[byte]string{
	0x12: "DDR", 0x13: "DDR2", 0x18: "DDR3", 0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3",
	0x1E: "LPDDR4", 0x22: "DDR5", 0x23: "LPDDR5",
}

// parseSMBIOSMemory decodes an SMBIOS type 17 (Memory Device) structure.
// Empty slots (size 0) return ok=false.
func parseSMBIOSMemory(b []byte) (core.MemoryModule, bool) {
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
