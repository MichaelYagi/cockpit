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
	BlockDevices      Section[[]BlockDevice]               `json:"block_devices"`
	NetworkInterfaces Section[[]NetworkInterfaceInventory] `json:"network_interfaces"`
}

// CPUInventory describes the processor.
type CPUInventory struct {
	ModelName    string `json:"model_name"`
	LogicalCores int    `json:"logical_cores"`
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
}

// MetricSections are the valid values of ?sections=.
var MetricSections = []string{"cpu", "memory", "memory_pressure", "filesystems", "network", "sensors"}

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
