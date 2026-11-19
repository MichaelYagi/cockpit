//go:build windows

package windows

import (
	"runtime"
	"strings"
	"unsafe"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	xcpu "golang.org/x/sys/cpu"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

var (
	procGetLogicalProcessorInformationEx = kernel32.NewProc("GetLogicalProcessorInformationEx")
	procGetSystemFirmwareTable           = kernel32.NewProc("GetSystemFirmwareTable")
	procGetSystemPowerStatus             = kernel32.NewProc("GetSystemPowerStatus")
	procIsProcessorFeaturePresent        = kernel32.NewProc("IsProcessorFeaturePresent")
)

const (
	rsmb                  = 0x52534D42 // 'RSMB': the raw SMBIOS table provider
	pfVirtFirmwareEnabled = 21         // PF_VIRT_FIRMWARE_ENABLED
)

// hardware reads the processor (registry and topology), the board and
// firmware (registry, chassis from SMBIOS) and memory modules (SMBIOS).
func hardware(r *collector.Result) {
	if info, ok := cpuInfo(); ok {
		r.Status[core.CapHardwareCPU] = portable.Available()
		r.Records = append(r.Records, info)
	} else {
		r.Status[core.CapHardwareCPU] = portable.Unavailable(core.TemporarilyUnavailable, "The processor could not be identified.")
	}

	mods, chassis := core.MemoryModules(nil), ""
	raw, smErr := firmwareTable(rsmb)
	if smErr == nil {
		mods, chassis = smbiosInventory(rawSMBIOS(raw))
	}
	if b, ok := board(chassis); ok {
		r.Status[core.CapHardwareBoard] = portable.Available()
		r.Records = append(r.Records, b)
	} else {
		r.Status[core.CapHardwareBoard] = portable.Unavailable(core.Unsupported, "The firmware did not report the system or board.")
	}
	switch {
	case smErr != nil:
		r.Status[core.CapHardwareMemory] = portable.Status(smErr, "the SMBIOS firmware table")
	case len(mods) == 0:
		r.Status[core.CapHardwareMemory] = portable.Unavailable(core.Unsupported, "No memory module entries are present in the firmware tables.")
	default:
		r.Status[core.CapHardwareMemory] = portable.Available()
		r.Records = append(r.Records, mods)
	}
}

func cpuInfo() (*core.CPUInfo, bool) {
	info := &core.CPUInfo{}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProcessorNameString")
		vendor, _, _ := k.GetStringValue("VendorIdentifier")
		id, _, _ := k.GetStringValue("Identifier")
		if mhz, _, err := k.GetIntegerValue("~MHz"); err == nil && mhz > 0 {
			f := float64(mhz) // the rated speed, not the boost clock
			info.MaxFrequencyMHz = &f
		}
		k.Close()
		info.ModelName, info.Vendor = strings.TrimSpace(name), strings.TrimSpace(vendor)
		info.Family, info.Model, info.Stepping = cpuIdentifier(id)
	}
	if buf, err := topology(); err == nil {
		info.Cores, info.Threads, info.Sockets, info.Caches = processorTopology(buf)
	}
	if info.Threads == 0 {
		info.Threads = runtime.NumCPU()
	}
	if ok, _, _ := procIsProcessorFeaturePresent.Call(pfVirtFirmwareEnabled); ok != 0 {
		switch info.Vendor {
		case "GenuineIntel":
			info.Virtualization = "VT-x"
		case "AuthenticAMD":
			info.Virtualization = "AMD-V"
		}
	}
	info.Flags = cpuFlags()
	return info, info.ModelName != "" || info.Cores > 0
}

// cpuFlags lists the notable features Linux shows, from CPUID.
func cpuFlags() []string {
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	switch runtime.GOARCH {
	case "amd64", "386":
		add(xcpu.X86.HasSSE42, "sse4_2")
		add(xcpu.X86.HasAVX, "avx")
		add(xcpu.X86.HasAVX2, "avx2")
		add(xcpu.X86.HasAVX512F, "avx512f")
		add(xcpu.X86.HasFMA, "fma")
		add(xcpu.X86.HasBMI2, "bmi2")
		add(xcpu.X86.HasAES, "aes")
	case "arm64":
		add(xcpu.ARM64.HasASIMD, "asimd")
		add(xcpu.ARM64.HasSVE, "sve")
		add(xcpu.ARM64.HasSHA2, "sha2")
		add(xcpu.ARM64.HasCRC32, "crc32")
	}
	return out
}

func topology() ([]byte, error) {
	var n uint32
	procGetLogicalProcessorInformationEx.Call(relationAll, 0, uintptr(unsafe.Pointer(&n)))
	if n == 0 || n > 4<<20 {
		return nil, win.ERROR_INSUFFICIENT_BUFFER
	}
	buf := make([]byte, n)
	if ok, _, err := procGetLogicalProcessorInformationEx.Call(relationAll, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); ok == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// firmwareTable reads a whole firmware table; it needs no privileges.
func firmwareTable(provider uint32) ([]byte, error) {
	n, _, err := procGetSystemFirmwareTable.Call(uintptr(provider), 0, 0, 0)
	if n == 0 || n > 4<<20 {
		return nil, err
	}
	buf := make([]byte, n)
	got, _, err := procGetSystemFirmwareTable.Call(uintptr(provider), 0, uintptr(unsafe.Pointer(&buf[0])), n)
	if got == 0 || got > n {
		return nil, err
	}
	return buf[:got], nil
}

// board reads what Windows copies from SMBIOS into the registry at boot.
func board(chassis string) (*core.BoardInfo, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return nil, false
	}
	defer k.Close()
	get := func(name string) string { s, _, _ := k.GetStringValue(name); return strings.TrimSpace(s) }
	b := &core.BoardInfo{
		SystemVendor: get("SystemManufacturer"), ProductName: get("SystemProductName"),
		BoardVendor: get("BaseBoardManufacturer"), BoardName: get("BaseBoardProduct"),
		BIOSVendor: get("BIOSVendor"), BIOSVersion: get("BIOSVersion"), BIOSDate: get("BIOSReleaseDate"),
		ChassisType: chassis,
	}
	return b, b.SystemVendor != "" || b.BoardName != "" || b.BIOSVendor != ""
}

func power(r *collector.Result) {
	var s systemPowerStatus
	if ok, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); ok == 0 {
		r.Status[core.CapPowerSupplies] = portable.Status(err, "the power status")
		return
	}
	ps := powerSupplies(s)
	if len(ps) == 0 {
		r.Status[core.CapPowerSupplies] = portable.Unavailable(core.Unsupported, "This system does not report power supplies.")
		return
	}
	r.Status[core.CapPowerSupplies] = portable.Available()
	r.Records = append(r.Records, ps)
}
