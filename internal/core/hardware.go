package core

// Hardware inventory records (Phase 2). Fields marked sensitive are
// collected only to report that they exist; they never leave the agent
// (D9).

// CPUCache is one cache level.
type CPUCache struct {
	Level     int
	Type      string // Data, Instruction, Unified
	SizeBytes uint64
	SharedBy  int // logical CPUs sharing this cache instance
}

// CPUInfo describes the processor package(s).
type CPUInfo struct {
	Vendor          string // GenuineIntel, AuthenticAMD, ARM implementer name, ...
	ModelName       string
	Family          string
	Model           string
	Stepping        string
	Sockets         int
	Cores           int // physical cores across all sockets
	Threads         int // logical CPUs
	Caches          []CPUCache
	MaxFrequencyMHz *float64
	Virtualization  string   // "VT-x", "AMD-V" or ""
	Hypervisor      bool     // running under a hypervisor
	Flags           []string // notable feature flags only (avx2, aes, ...)
}

// PCIDevice is one PCI function.
type PCIDevice struct {
	Address    string // domain:bus:device.function
	ClassCode  string // e.g. "0x030000"
	ClassName  string // e.g. "VGA compatible controller"
	VendorID   string // e.g. "0x10de"
	DeviceID   string
	VendorName string // from pci.ids when installed
	DeviceName string
	Driver     string
}

// PCIDevices is the PCI record.
type PCIDevices []PCIDevice

// USBDevice is one USB device (not interface).
type USBDevice struct {
	Path         string // sysfs name, e.g. "1-2.4"
	VendorID     string // e.g. "046d"
	ProductID    string
	Manufacturer string
	Product      string
	SpeedMbps    *float64
	Serial       string // sensitive
}

// USBDevices is the USB record.
type USBDevices []USBDevice

// BoardInfo is system, board and firmware identity from DMI.
type BoardInfo struct {
	SystemVendor string
	ProductName  string
	BoardVendor  string
	BoardName    string
	BIOSVendor   string
	BIOSVersion  string
	BIOSDate     string
	ChassisType  string
	Serial       string // sensitive; usually root-only
}

// MemoryModule is one installed DIMM (SMBIOS type 17).
type MemoryModule struct {
	Locator      string // e.g. "DIMM_A1"
	SizeBytes    uint64
	Type         string // DDR4, DDR5, ...
	SpeedMTs     int    // configured speed, MT/s
	Manufacturer string
	PartNumber   string
	Serial       string // sensitive
}

// MemoryModules is the DIMM record.
type MemoryModules []MemoryModule

// GPU is one graphics device with optional vendor metrics.
type GPU struct {
	Vendor             string // NVIDIA, AMD, Intel, ...
	Name               string
	PCIAddress         string
	Driver             string
	DriverVersion      string
	MemoryTotalBytes   *uint64
	MemoryUsedBytes    *uint64
	UtilizationPercent *float64
	TemperatureCelsius *float64
	PowerWatts         *float64
	PowerLimitWatts    *float64
	FanPercent         *float64
	MetricsSource      string // "nvidia-smi", "amdgpu", or "" when no metrics
	UUID               string // sensitive
}

// GPUs is the GPU record.
type GPUs []GPU

func (*CPUInfo) isRecord()      {}
func (PCIDevices) isRecord()    {}
func (USBDevices) isRecord()    {}
func (*BoardInfo) isRecord()    {}
func (MemoryModules) isRecord() {}
func (GPUs) isRecord()          {}
