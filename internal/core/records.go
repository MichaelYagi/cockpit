package core

import "time"

// Records are what collectors return. They hold raw values (counters, not
// rates); the engine derives rates and builds snapshot sections.
//
// Fields marked "sensitive" are collected but never leave the agent unless
// a future policy allows it (D9).

// SystemInfo identifies the operating system and host.
type SystemInfo struct {
	Hostname     string
	OSID         string // e.g. "ubuntu"
	OSName       string // e.g. "Ubuntu"
	OSVersion    string // e.g. "24.04"
	OSPrettyName string
	Kernel       string
	Architecture string // GOARCH-style, e.g. "amd64"
	BootTime     time.Time
	Uptime       time.Duration
}

// CPUTimes are cumulative CPU time counters in clock ticks.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Busy and Total return non-idle and total ticks. IOWait counts as idle.
func (t CPUTimes) Busy() uint64 { return t.User + t.Nice + t.System + t.IRQ + t.SoftIRQ + t.Steal }

// Total returns all ticks.
func (t CPUTimes) Total() uint64 { return t.Busy() + t.Idle + t.IOWait }

// LoadAverage is the run-queue average (Linux and macOS only).
type LoadAverage struct {
	One, Five, Fifteen float64
}

// CPUCounters is a raw CPU sample.
type CPUCounters struct {
	Total        CPUTimes
	PerCore      []CPUTimes
	LogicalCores int
	ModelName    string
	Load         *LoadAverage // nil when unavailable
}

// MemoryStats is a raw memory sample.
type MemoryStats struct {
	TotalBytes     uint64
	AvailableBytes uint64
	FreeBytes      uint64
	BuffersBytes   uint64
	CachedBytes    uint64
	SwapTotalBytes uint64
	SwapFreeBytes  uint64
}

// MemoryPressure is Linux PSI for memory (percent of time stalled).
type MemoryPressure struct {
	SomeAvg10, SomeAvg60, SomeAvg300 float64
	FullAvg10, FullAvg60, FullAvg300 float64
}

// Filesystem is one mounted filesystem's capacity.
type Filesystem struct {
	MountPoint     string
	Device         string
	Type           string
	ReadOnly       bool
	TotalBytes     uint64
	FreeBytes      uint64 // includes blocks reserved for root
	AvailableBytes uint64 // available to unprivileged users
	TotalInodes    uint64
	FreeInodes     uint64
}

// Filesystems is the filesystem record.
type Filesystems []Filesystem

// NetInterface is one network interface's raw counters.
type NetInterface struct {
	Name                 string
	MACAddress           string // sensitive
	MTU                  int
	OperState            string // "up", "down", "unknown", ...
	Loopback             bool
	SpeedMbps            *int // nil when unknown (e.g. link down, virtual)
	RxBytes, TxBytes     uint64
	RxPackets, TxPackets uint64
	RxErrors, TxErrors   uint64
	RxDropped, TxDropped uint64
	Addresses            []string // CIDR, e.g. "192.168.1.10/24"
}

// Route is one kernel routing table entry.
type Route struct {
	Family      string // "ipv4" or "ipv6"
	Destination string // CIDR; "0.0.0.0/0" or "::/0" for the default route
	Gateway     string // empty for directly connected routes
	Interface   string
	Metric      int
	Default     bool
}

// Routes is the routing record.
type Routes []Route

// DNSConfig is the resolver configuration.
type DNSConfig struct {
	Nameservers []string
	Search      []string
	// Resolver is "resolv.conf" or "systemd-resolved" (when the local stub
	// is in use, Nameservers are its upstream servers).
	Resolver string
}

func (Routes) isRecord()     {}
func (*DNSConfig) isRecord() {}

// NetInterfaces is the network record.
type NetInterfaces []NetInterface

// BlockDevice is one physical or virtual disk.
type BlockDevice struct {
	Name       string // e.g. "nvme0n1", "sda"
	Model      string
	Serial     string // sensitive
	WWN        string // sensitive
	Transport  string // nvme, sata, usb, virtio, scsi, unknown
	Rotational *bool
	Removable  bool
	SizeBytes  uint64
}

// BlockDevices is the block device record.
type BlockDevices []BlockDevice

// SensorKind classifies a sensor.
type SensorKind string

const (
	SensorTemperature SensorKind = "temperature"
	SensorFan         SensorKind = "fan"
	SensorVoltage     SensorKind = "voltage"
)

// Sensor is one hardware sensor reading.
type Sensor struct {
	Kind     SensorKind
	Chip     string // e.g. "coretemp", "nvme", "acpitz"
	Label    string // e.g. "Package id 0"
	Value    float64
	Unit     string   // "celsius", "rpm", "volts"
	High     *float64 // hardware warning threshold, if exposed
	Critical *float64 // hardware critical threshold, if exposed
	Source   string   // "hwmon" or "thermal_zone"
	CPU      bool     // the sensor measures a CPU package/core
}

// Sensors is the sensor record.
type Sensors []Sensor

// DiskHealth is a SMART/NVMe health summary for one device.
type DiskHealth struct {
	Device             string
	Protocol           string // "ATA", "NVMe", "SCSI"
	Passed             *bool  // overall SMART assessment
	TemperatureCelsius *float64
	PowerOnHours       *uint64
	PercentageUsed     *int    // NVMe endurance used
	CriticalWarning    *int    // NVMe critical warning bitmap
	ReallocatedSectors *uint64 // ATA attribute 5
	PendingSectors     *uint64 // ATA attribute 197
	MediaErrors        *uint64 // NVMe media and data integrity errors
}

// DiskHealthList is the disk health record.
type DiskHealthList []DiskHealth

// Service is one systemd service unit.
type Service struct {
	Name        string // e.g. "ssh.service"
	Description string
	LoadState   string // loaded, not-found, masked, ...
	ActiveState string // active, inactive, failed, activating, deactivating, reloading
	SubState    string // running, exited, dead, ...
}

// Services is the service record.
type Services []Service

func (Services) isRecord() {}

func (*SystemInfo) isRecord()     {}
func (*CPUCounters) isRecord()    {}
func (*MemoryStats) isRecord()    {}
func (*MemoryPressure) isRecord() {}
func (Filesystems) isRecord()     {}
func (NetInterfaces) isRecord()   {}
func (BlockDevices) isRecord()    {}
func (Sensors) isRecord()         {}
func (DiskHealthList) isRecord()  {}
