package core

// Containers (D24) are found from the cgroup v2 hierarchy; nothing talks to
// a container engine's API.

// ContainerSample is one container's raw counters. Pointers are nil when
// the cgroup controller is not enabled for the container (common for
// rootless containers without delegation).
type ContainerSample struct {
	Runtime  string // docker | podman | containerd | cri-o
	ID       string // 64 hex characters
	Rootless bool
	UID      int // owner of a rootless container; -1 otherwise
	// CPUUsageUsec is total CPU time in microseconds (cpu.stat usage_usec).
	CPUUsageUsec     uint64
	MemoryBytes      *uint64
	MemoryLimitBytes *uint64 // nil when unlimited or unknown
	IOReadBytes      *uint64
	IOWriteBytes     *uint64
	PIDs             *uint64
}

// ContainerSamples is the containers collector's record.
type ContainerSamples []ContainerSample

func (ContainerSamples) isRecord() {}

// ContainerMetrics adds rates derived by the engine. CPUPercent is a
// percentage of one CPU, as for processes.
type ContainerMetrics struct {
	ContainerSample
	CPUPercent            *float64
	IOReadBytesPerSecond  *float64
	IOWriteBytesPerSecond *float64
	WindowSeconds         float64
}
