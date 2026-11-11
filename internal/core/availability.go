// Package core defines Cockpit's platform-neutral domain model.
//
// It must not import any other internal package.
package core

// Availability says whether a capability or data section could be collected.
type Availability string

const (
	// Available means the data was collected successfully.
	Available Availability = "available"
	// Unsupported means the platform or hardware does not provide it.
	Unsupported Availability = "unsupported"
	// PermissionDenied means it exists but the agent lacks privileges.
	PermissionDenied Availability = "permission_denied"
	// TemporarilyUnavailable means a transient failure (timeout, I/O error).
	TemporarilyUnavailable Availability = "temporarily_unavailable"
	// NotConfigured means an optional integration is disabled or missing.
	NotConfigured Availability = "not_configured"
)

// Valid reports whether a is one of the defined availability states.
func (a Availability) Valid() bool {
	switch a {
	case Available, Unsupported, PermissionDenied, TemporarilyUnavailable, NotConfigured:
		return true
	}
	return false
}

// Capability identifies something a collector can provide, e.g. "cpu.usage".
type Capability string

// Known capabilities. Collectors may only declare capabilities from this list,
// so clients can rely on a stable vocabulary.
const (
	CapSystemIdentity     Capability = "system.identity"
	CapSystemUptime       Capability = "system.uptime"
	CapCPUUsage           Capability = "cpu.usage"
	CapCPULoad            Capability = "cpu.load"
	CapCPUFrequency       Capability = "cpu.frequency"
	CapMemoryUsage        Capability = "memory.usage"
	CapMemoryPressure     Capability = "memory.pressure"
	CapStorageDevices     Capability = "storage.devices"
	CapStorageFilesystems Capability = "storage.filesystems"
	CapStorageSMART       Capability = "storage.smart"
	CapNetworkInterfaces  Capability = "network.interfaces"
	CapNetworkThroughput  Capability = "network.throughput"
	CapSensorsThermal     Capability = "sensors.thermal"
	CapSensorsFan         Capability = "sensors.fan"
)

// KnownCapabilities returns every defined capability in a stable order.
func KnownCapabilities() []Capability {
	return []Capability{
		CapSystemIdentity, CapSystemUptime,
		CapCPUUsage, CapCPULoad, CapCPUFrequency,
		CapMemoryUsage, CapMemoryPressure,
		CapStorageDevices, CapStorageFilesystems, CapStorageSMART,
		CapNetworkInterfaces, CapNetworkThroughput,
		CapSensorsThermal, CapSensorsFan,
	}
}

// Known reports whether c is a defined capability.
func (c Capability) Known() bool {
	for _, k := range KnownCapabilities() {
		if k == c {
			return true
		}
	}
	return false
}

// CapabilityStatus is the current availability of one capability.
// Reason must be safe to show to API clients: no secrets, paths to secret
// files, or raw command output.
type CapabilityStatus struct {
	Availability Availability
	Reason       string
}
