package core

// ZFS, power supplies and UPS (D25).

// ZFSPool is one pool. Capacity and error counts come from zpool's JSON
// output and are nil without it (OpenZFS before 2.3, or no zpool).
type ZFSPool struct {
	Name           string
	State          string // ONLINE | DEGRADED | FAULTED | OFFLINE | UNAVAIL | REMOVED | SUSPENDED
	SizeBytes      *uint64
	AllocatedBytes *uint64
	FreeBytes      *uint64
	ReadErrors     *uint64
	WriteErrors    *uint64
	ChecksumErrors *uint64
}

// ZFSARC is the adaptive replacement cache.
type ZFSARC struct {
	SizeBytes, TargetBytes, MaxBytes uint64
	Hits, Misses                     uint64 // since module load
}

// ZFSInfo is the ZFS collector's record.
type ZFSInfo struct {
	Pools []ZFSPool
	ARC   *ZFSARC
	// DetailReason explains missing capacity or error counts.
	DetailReason string
}

func (*ZFSInfo) isRecord() {}

// PowerSupply is a battery, mains adapter or USB supply from
// /sys/class/power_supply.
type PowerSupply struct {
	Name            string
	Type            string // battery | mains | usb | ups | wireless
	Online          *bool  // mains and USB
	Status          string // battery: charging | discharging | full | not_charging | unknown
	CapacityPercent *float64
	Health          string
	PowerWatts      *float64
	EnergyNowWh     *float64
	EnergyFullWh    *float64
	// HoursRemaining is set while discharging with a known draw.
	HoursRemaining *float64
	Model          string
}

// PowerSupplies is the power collector's record.
type PowerSupplies []PowerSupply

func (PowerSupplies) isRecord() {}

// UPS is one UPS read from a Network UPS Tools server.
type UPS struct {
	Name           string
	Description    string
	Status         []string // NUT status flags, e.g. OL, OB, LB, CHRG
	OnBattery      bool
	LowBattery     bool
	ChargePercent  *float64
	RuntimeSeconds *float64
	LoadPercent    *float64
	InputVolts     *float64
	OutputVolts    *float64
	Model          string
}

// UPSList is the UPS collector's record.
type UPSList []UPS

func (UPSList) isRecord() {}
