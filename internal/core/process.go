package core

import "time"

// ProcessSample is one process's raw counters at a point in time.
// Command lines are deliberately not collected (D9).
type ProcessSample struct {
	PID, PPID    int
	Name         string // kernel "comm", at most 15 characters
	UID          int
	State        string // R, S, D, Z, T, I, ...
	Threads      int
	CPUTicks     uint64 // utime + stime, in USER_HZ ticks
	StartTicks   uint64 // since boot, in USER_HZ ticks
	RSSBytes     uint64
	VirtualBytes uint64
	KernelThread bool
}

// ProcessList is one sample of all visible processes.
type ProcessList struct {
	TakenAt   time.Time
	Processes []ProcessSample
	// Partial is set when the kernel hides other users' processes
	// (/proc mounted with hidepid) and the agent is not privileged.
	Partial bool
	// Users maps UIDs to names (from /etc/passwd); missing UIDs are
	// shown numerically.
	Users map[int]string
}

// ProcessInfo is a process with derived metrics, as served by the API.
type ProcessInfo struct {
	ProcessSample
	User          string
	CPUPercent    *float64 // percent of one core over WindowSeconds; nil for new processes
	MemoryPercent float64
	StartedAt     time.Time
}

// ProcessView is an immutable, derived process listing.
type ProcessView struct {
	ID            uint64 // identifies the view for snapshot-bound cursors
	TakenAt       time.Time
	WindowSeconds float64
	Partial       bool
	Status        CapabilityStatus
	Processes     []ProcessInfo
}
