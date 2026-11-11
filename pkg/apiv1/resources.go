package apiv1

import "time"

// Section wraps one data area with its availability (docs/API.md §5).
// Data is null when nothing has been collected; it is kept with Stale=true
// after a transient failure.
type Section[T any] struct {
	Status      string     `json:"status"`
	CollectedAt *time.Time `json:"collected_at"`
	Stale       bool       `json:"stale"`
	Source      string     `json:"source"`
	Reason      string     `json:"reason,omitempty"`
	Data        *T         `json:"data"`
}

// ---- /system ----

// SystemResponse is GET /system.
type SystemResponse struct {
	Meta
	System Section[System] `json:"system"`
}

// System identifies the host and operating system.
type System struct {
	Hostname      string     `json:"hostname"`
	OS            OS         `json:"os"`
	Kernel        string     `json:"kernel"`
	Architecture  string     `json:"architecture"`
	BootTime      *time.Time `json:"boot_time"`
	UptimeSeconds *float64   `json:"uptime_seconds"`
}

// OS describes the operating system release.
type OS struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	PrettyName string `json:"pretty_name"`
}

// ---- /hardware ----

// HardwareResponse is GET /hardware.
type HardwareResponse struct {
	Meta
	CPU               Section[CPUInventory]                `json:"cpu"`
	Memory            Section[MemoryInventory]             `json:"memory"`
	MemoryModules     Section[[]MemoryModule]              `json:"memory_modules"`
	Board             Section[Board]                       `json:"board"`
	BlockDevices      Section[[]BlockDevice]               `json:"block_devices"`
	NetworkInterfaces Section[[]NetworkInterfaceInventory] `json:"network_interfaces"`
	PCIDevices        Section[[]PCIDevice]                 `json:"pci_devices"`
	USBDevices        Section[[]USBDevice]                 `json:"usb_devices"`
	GPUs              Section[[]GPU]                       `json:"gpus"`
}

// CPUInventory describes the processor.
type CPUInventory struct {
	ModelName       string     `json:"model_name"`
	LogicalCores    int        `json:"logical_cores"`
	Vendor          string     `json:"vendor"`
	Family          string     `json:"family"`
	Model           string     `json:"model"`
	Stepping        string     `json:"stepping"`
	Sockets         int        `json:"sockets"`
	Cores           int        `json:"cores"`
	Caches          []CPUCache `json:"caches"`
	MaxFrequencyMHz *float64   `json:"max_frequency_mhz"`
	Virtualization  string     `json:"virtualization"`
	Hypervisor      bool       `json:"hypervisor"`
	Flags           []string   `json:"flags"`
}

// CPUCache is one cache level.
type CPUCache struct {
	Level     int    `json:"level"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	SharedBy  int    `json:"shared_by"`
}

// MemoryModule is one installed DIMM. Serial is sensitive (D9).
type MemoryModule struct {
	Locator      string   `json:"locator"`
	SizeBytes    uint64   `json:"size_bytes"`
	Type         string   `json:"type"`
	SpeedMTs     int      `json:"speed_mts"`
	Manufacturer string   `json:"manufacturer"`
	PartNumber   string   `json:"part_number"`
	Serial       string   `json:"serial,omitempty"`
	Redacted     []string `json:"redacted,omitempty"`
}

// Board is system, board and firmware identity. Serial is sensitive.
type Board struct {
	SystemVendor string   `json:"system_vendor"`
	ProductName  string   `json:"product_name"`
	BoardVendor  string   `json:"board_vendor"`
	BoardName    string   `json:"board_name"`
	BIOSVendor   string   `json:"bios_vendor"`
	BIOSVersion  string   `json:"bios_version"`
	BIOSDate     string   `json:"bios_date"`
	ChassisType  string   `json:"chassis_type"`
	Serial       string   `json:"serial,omitempty"`
	Redacted     []string `json:"redacted,omitempty"`
}

// PCIDevice is one PCI function.
type PCIDevice struct {
	Address    string `json:"address"`
	ClassCode  string `json:"class_code"`
	ClassName  string `json:"class_name"`
	VendorID   string `json:"vendor_id"`
	DeviceID   string `json:"device_id"`
	VendorName string `json:"vendor_name"`
	DeviceName string `json:"device_name"`
	Driver     string `json:"driver"`
}

// USBDevice is one USB device. Serial is sensitive.
type USBDevice struct {
	Path         string   `json:"path"`
	VendorID     string   `json:"vendor_id"`
	ProductID    string   `json:"product_id"`
	Manufacturer string   `json:"manufacturer"`
	Product      string   `json:"product"`
	SpeedMbps    *float64 `json:"speed_mbps"`
	Serial       string   `json:"serial,omitempty"`
	Redacted     []string `json:"redacted,omitempty"`
}

// GPU is one graphics device. Metric fields are null without a metrics
// source. UUID is sensitive.
type GPU struct {
	Vendor             string   `json:"vendor"`
	Name               string   `json:"name"`
	PCIAddress         string   `json:"pci_address"`
	Driver             string   `json:"driver"`
	DriverVersion      string   `json:"driver_version"`
	MemoryTotalBytes   *uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes    *uint64  `json:"memory_used_bytes"`
	UtilizationPercent *float64 `json:"utilization_percent"`
	TemperatureCelsius *float64 `json:"temperature_celsius"`
	PowerWatts         *float64 `json:"power_watts"`
	PowerLimitWatts    *float64 `json:"power_limit_watts"`
	FanPercent         *float64 `json:"fan_percent"`
	MetricsSource      string   `json:"metrics_source"`
	UUID               string   `json:"uuid,omitempty"`
	Redacted           []string `json:"redacted,omitempty"`
}

// MemoryInventory describes installed memory.
type MemoryInventory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
}

// BlockDevice is one disk. Serial and WWN are sensitive (D9).
type BlockDevice struct {
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	Transport  string   `json:"transport"`
	Rotational *bool    `json:"rotational"`
	Removable  bool     `json:"removable"`
	SizeBytes  uint64   `json:"size_bytes"`
	Serial     string   `json:"serial,omitempty"`
	WWN        string   `json:"wwn,omitempty"`
	Redacted   []string `json:"redacted,omitempty"`
}

// NetworkInterfaceInventory is a network interface's static properties.
// MACAddress is sensitive (D9).
type NetworkInterfaceInventory struct {
	Name       string   `json:"name"`
	MTU        int      `json:"mtu"`
	Loopback   bool     `json:"loopback"`
	SpeedMbps  *int     `json:"speed_mbps"`
	MACAddress string   `json:"mac_address,omitempty"`
	Redacted   []string `json:"redacted,omitempty"`
}

// ---- /metrics ----

// MetricsResponse is GET /metrics. Sections not requested are omitted.
type MetricsResponse struct {
	Meta
	CPU            *Section[CPUMetrics]         `json:"cpu,omitempty"`
	Memory         *Section[MemoryMetrics]      `json:"memory,omitempty"`
	MemoryPressure *Section[MemoryPressure]     `json:"memory_pressure,omitempty"`
	Filesystems    *Section[[]Filesystem]       `json:"filesystems,omitempty"`
	Network        *Section[[]NetworkInterface] `json:"network,omitempty"`
	Sensors        *Section[[]Sensor]           `json:"sensors,omitempty"`
	GPU            *Section[[]GPU]              `json:"gpu,omitempty"`
}

// MetricSections are the valid values of ?sections=.
var MetricSections = []string{"cpu", "memory", "memory_pressure", "filesystems", "network", "sensors", "gpu"}

// CPUMetrics is current CPU utilization.
type CPUMetrics struct {
	UsagePercent        *float64     `json:"usage_percent"`
	PerCoreUsagePercent []float64    `json:"per_core_usage_percent"`
	WindowSeconds       float64      `json:"window_seconds"`
	LogicalCores        int          `json:"logical_cores"`
	LoadAverage         *LoadAverage `json:"load_average"`
}

// LoadAverage is the run-queue average (Linux and macOS only).
type LoadAverage struct {
	One     float64 `json:"1m"`
	Five    float64 `json:"5m"`
	Fifteen float64 `json:"15m"`
}

// MemoryMetrics is current memory use.
type MemoryMetrics struct {
	TotalBytes      uint64  `json:"total_bytes"`
	AvailableBytes  uint64  `json:"available_bytes"`
	UsedBytes       uint64  `json:"used_bytes"`
	UsedPercent     float64 `json:"used_percent"`
	FreeBytes       uint64  `json:"free_bytes"`
	BuffersBytes    uint64  `json:"buffers_bytes"`
	CachedBytes     uint64  `json:"cached_bytes"`
	SwapTotalBytes  uint64  `json:"swap_total_bytes"`
	SwapUsedBytes   uint64  `json:"swap_used_bytes"`
	SwapUsedPercent float64 `json:"swap_used_percent"`
}

// MemoryPressure is Linux PSI: percent of time tasks stalled on memory.
type MemoryPressure struct {
	SomeAvg10Percent  float64 `json:"some_avg10_percent"`
	SomeAvg60Percent  float64 `json:"some_avg60_percent"`
	SomeAvg300Percent float64 `json:"some_avg300_percent"`
	FullAvg10Percent  float64 `json:"full_avg10_percent"`
	FullAvg60Percent  float64 `json:"full_avg60_percent"`
	FullAvg300Percent float64 `json:"full_avg300_percent"`
}

// Filesystem is one mounted filesystem's capacity.
type Filesystem struct {
	MountPoint        string   `json:"mount_point"`
	Device            string   `json:"device"`
	Type              string   `json:"type"`
	ReadOnly          bool     `json:"read_only"`
	TotalBytes        uint64   `json:"total_bytes"`
	UsedBytes         uint64   `json:"used_bytes"`
	AvailableBytes    uint64   `json:"available_bytes"`
	UsedPercent       float64  `json:"used_percent"`
	InodesTotal       uint64   `json:"inodes_total"`
	InodesFree        uint64   `json:"inodes_free"`
	InodesUsedPercent *float64 `json:"inodes_used_percent"`
}

// NetworkInterface is one interface's counters and rates. MACAddress is
// sensitive (D9).
type NetworkInterface struct {
	Name              string   `json:"name"`
	OperState         string   `json:"oper_state"`
	Loopback          bool     `json:"loopback"`
	MTU               int      `json:"mtu"`
	SpeedMbps         *int     `json:"speed_mbps"`
	RxBytes           uint64   `json:"rx_bytes"`
	TxBytes           uint64   `json:"tx_bytes"`
	RxPackets         uint64   `json:"rx_packets"`
	TxPackets         uint64   `json:"tx_packets"`
	RxErrors          uint64   `json:"rx_errors"`
	TxErrors          uint64   `json:"tx_errors"`
	RxDropped         uint64   `json:"rx_dropped"`
	TxDropped         uint64   `json:"tx_dropped"`
	RxBytesPerSecond  *float64 `json:"rx_bytes_per_second"`
	TxBytesPerSecond  *float64 `json:"tx_bytes_per_second"`
	RateWindowSeconds float64  `json:"rate_window_seconds"`
	MACAddress        string   `json:"mac_address,omitempty"`
	Redacted          []string `json:"redacted,omitempty"`
	Addresses         []string `json:"addresses"`
}

// Sensor is one hardware sensor reading.
type Sensor struct {
	Kind     string   `json:"kind"` // temperature | fan | voltage
	Chip     string   `json:"chip"`
	Label    string   `json:"label"`
	Value    float64  `json:"value"`
	Unit     string   `json:"unit"` // celsius | rpm | volts
	High     *float64 `json:"high"`
	Critical *float64 `json:"critical"`
	Source   string   `json:"source"` // hwmon | thermal_zone
	CPU      bool     `json:"cpu"`
}

// ---- /storage, /network, /sensors ----

// StorageResponse is GET /storage.
type StorageResponse struct {
	Meta
	BlockDevices Section[[]BlockDevice] `json:"block_devices"`
	Filesystems  Section[[]Filesystem]  `json:"filesystems"`
	DiskHealth   Section[[]DiskHealth]  `json:"disk_health"`
}

// DiskHealth is a SMART/NVMe summary for one disk.
type DiskHealth struct {
	Device             string   `json:"device"`
	Protocol           string   `json:"protocol"`
	SmartPassed        *bool    `json:"smart_passed"`
	TemperatureCelsius *float64 `json:"temperature_celsius"`
	PowerOnHours       *uint64  `json:"power_on_hours"`
	PercentageUsed     *int     `json:"percentage_used"`
	CriticalWarning    *int     `json:"critical_warning"`
	ReallocatedSectors *uint64  `json:"reallocated_sectors"`
	PendingSectors     *uint64  `json:"pending_sectors"`
	MediaErrors        *uint64  `json:"media_errors"`
}

// NetworkResponse is GET /network.
type NetworkResponse struct {
	Meta
	Interfaces Section[[]NetworkInterface] `json:"interfaces"`
	Routes     Section[[]Route]            `json:"routes"`
	DNS        Section[DNS]                `json:"dns"`
}

// Route is one routing table entry.
type Route struct {
	Family      string `json:"family"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
	Metric      int    `json:"metric"`
	Default     bool   `json:"default"`
}

// DNS is the resolver configuration.
type DNS struct {
	Nameservers []string `json:"nameservers"`
	Search      []string `json:"search"`
	Resolver    string   `json:"resolver"`
}

// SensorsResponse is GET /sensors.
type SensorsResponse struct {
	Meta
	Sensors Section[[]Sensor] `json:"sensors"`
}

// ---- /health ----

// Health statuses.
const (
	HealthOK       = "ok"
	HealthWarning  = "warning"
	HealthCritical = "critical"
	HealthUnknown  = "unknown"
)

// HealthResponse is GET /health.
type HealthResponse struct {
	Meta
	Status      string        `json:"status"`
	EvaluatedAt time.Time     `json:"evaluated_at"`
	Checks      []HealthCheck `json:"checks"`
}

// HealthCheck is one check's result.
type HealthCheck struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Availability string    `json:"availability"`
	Reason       string    `json:"reason,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Findings     []Finding `json:"findings"`
}

// Finding is an active health problem with evidence.
type Finding struct {
	ID             string            `json:"id"`
	Check          string            `json:"check"`
	Severity       string            `json:"severity"` // warning | critical
	Subject        string            `json:"subject"`
	Summary        string            `json:"summary"`
	Evidence       map[string]string `json:"evidence"`
	Recommendation string            `json:"recommendation"`
	Since          time.Time         `json:"since"`
}

// ---- /events ----

// EventsResponse is GET /events.
type EventsResponse struct {
	Meta
	Items      []Event `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Event is a recorded occurrence.
type Event struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Severity string    `json:"severity"` // info | warning | critical
	Subject  string    `json:"subject"`
	Message  string    `json:"message"`
}

// ---- /keys (socket only) ----

// APIKey is key metadata. It never contains the key or its hash.
type APIKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Display     string     `json:"display"`
	Scopes      []string   `json:"scopes"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	RotatedFrom *string    `json:"rotated_from"`
}

// APIKeyList is GET /keys.
type APIKeyList struct {
	Items []APIKey `json:"items"`
}

// CreateAPIKeyRequest is the body of POST /keys.
type CreateAPIKeyRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreatedAPIKey is returned by POST /keys and POST /keys/{id}/rotate.
// Key is shown exactly once and can never be retrieved again.
type CreatedAPIKey struct {
	Key    string `json:"key"`
	APIKey APIKey `json:"api_key"`
}

// APIKeyResponse is returned by DELETE /keys/{id}.
type APIKeyResponse struct {
	APIKey APIKey `json:"api_key"`
}

// ---- /status (additions) ----

// APIServerStatus is included in /status for admin principals only.
type APIServerStatus struct {
	Listeners     []Listener   `json:"listeners"`
	Requests      RequestStats `json:"requests"`
	ActiveClients int          `json:"active_clients"`
	OpenStreams   int          `json:"open_streams"`
}

// StreamLagged is the data of a "lagged" SSE event: events were dropped
// because the client read too slowly.
type StreamLagged struct {
	Dropped int `json:"dropped"`
}

// Listener is one open API listener.
type Listener struct {
	Transport string `json:"transport"` // unix | tcp
	Address   string `json:"address"`
	TLS       bool   `json:"tls"`
}

// RequestStats are counters since the agent started.
type RequestStats struct {
	Total        uint64 `json:"total"`
	Status2xx    uint64 `json:"status_2xx"`
	Status4xx    uint64 `json:"status_4xx"`
	Status5xx    uint64 `json:"status_5xx"`
	AuthFailures uint64 `json:"auth_failures"`
	RateLimited  uint64 `json:"rate_limited"`
}

// ---- /processes ----

// ProcessesResponse is GET /processes. Command lines are never included
// (D9); "redacted" says so explicitly.
type ProcessesResponse struct {
	Meta
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	CollectedAt   *time.Time `json:"collected_at"`
	WindowSeconds float64    `json:"window_seconds"`
	Partial       bool       `json:"partial"`
	Total         int        `json:"total"`
	Items         []Process  `json:"items"`
	NextCursor    *string    `json:"next_cursor"`
	Redacted      []string   `json:"redacted"`
}

// Process is one process.
type Process struct {
	PID           int        `json:"pid"`
	PPID          int        `json:"ppid"`
	Name          string     `json:"name"`
	User          string     `json:"user"`
	UID           int        `json:"uid"`
	State         string     `json:"state"`
	Threads       int        `json:"threads"`
	CPUPercent    *float64   `json:"cpu_percent"`
	MemoryPercent float64    `json:"memory_percent"`
	RSSBytes      uint64     `json:"rss_bytes"`
	VirtualBytes  uint64     `json:"virtual_bytes"`
	StartedAt     *time.Time `json:"started_at"`
	KernelThread  bool       `json:"kernel_thread"`
}

// ProcessSorts are the valid values of ?sort= for /processes.
var ProcessSorts = []string{"cpu", "memory", "pid", "name"}

// ---- /services ----

// ServicesResponse is GET /services.
type ServicesResponse struct {
	Meta
	Services Section[ServiceList] `json:"services"`
}

// ServiceList is the systemd service list with counts by state. Counts
// cover all services, even when ?state= filters the items.
type ServiceList struct {
	Total    int       `json:"total"`
	Active   int       `json:"active"`
	Failed   int       `json:"failed"`
	Inactive int       `json:"inactive"`
	Items    []Service `json:"items"`
}

// Service is one systemd service unit.
type Service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}
