package core

import "time"

// CPUMetrics is the derived CPU section.
type CPUMetrics struct {
	UsagePercent        *float64 // nil until two samples exist
	PerCoreUsagePercent []float64
	WindowSeconds       float64
	LogicalCores        int
	ModelName           string
	Load                *LoadAverage
}

// MemoryMetrics is the derived memory section.
type MemoryMetrics struct {
	MemoryStats
	UsedBytes       uint64
	UsedPercent     float64
	SwapUsedBytes   uint64
	SwapUsedPercent float64
}

// NetInterfaceMetrics adds rates to raw interface counters.
type NetInterfaceMetrics struct {
	NetInterface
	RxBytesPerSecond *float64 // nil until two samples exist or after a counter reset
	TxBytesPerSecond *float64
	WindowSeconds    float64
}

// Snapshot is an immutable view of everything the engine knows. A new
// snapshot is published after every collection; never modify one.
type Snapshot struct {
	Generation  uint64
	GeneratedAt time.Time

	System         Section[SystemInfo]
	CPU            Section[CPUMetrics]
	Memory         Section[MemoryMetrics]
	MemoryPressure Section[MemoryPressure]
	Filesystems    Section[Filesystems]
	Network        Section[[]NetInterfaceMetrics]
	BlockDevices   Section[BlockDevices]
	Sensors        Section[Sensors]
	DiskHealth     Section[DiskHealthList]
	Services       Section[Services]
	CPUInfo        Section[CPUInfo]
	PCI            Section[PCIDevices]
	USB            Section[USBDevices]
	Board          Section[BoardInfo]
	MemoryModules  Section[MemoryModules]
	GPUs           Section[GPUs]
	Routes         Section[Routes]
	DNS            Section[DNSConfig]
	Containers     Section[[]ContainerMetrics]

	// Custom holds generic metrics, one set per collector that reports
	// them (D22), in collector order.
	Custom []CustomSet

	Collectors []CollectorStatus
}

// IsStale reports whether the section's data is older than its deadline.
func (m SectionMeta) IsStale(now time.Time) bool {
	return !m.StaleAfter.IsZero() && now.After(m.StaleAfter)
}
