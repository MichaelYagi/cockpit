//go:build windows

// Package windows collects Windows metrics without cgo (D32): kernel32
// and the IP helper API through golang.org/x/sys/windows. There is no load
// average on Windows; sensors and disk health are not collected yet.
package windows

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
	"github.com/MichaelYagi/cockpit/internal/platform/unsupported"
)

const version = "1.0.0"

var (
	kernel32                 = win.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
)

var implemented = []core.Capability{
	core.CapSystemIdentity, core.CapSystemUptime, core.CapCPUUsage, core.CapCPULoad, core.CapMemoryUsage,
	core.CapStorageFilesystems, core.CapNetworkInterfaces, core.CapNetworkThroughput,
}

// Collectors returns the Windows collector set.
func Collectors(env collector.Env) []collector.Collector {
	now := env.Now
	if now == nil {
		now = time.Now
	}
	return []collector.Collector{
		sampler("windows.system", "kernel32, registry", time.Minute, now, []core.Capability{core.CapSystemIdentity, core.CapSystemUptime}, system),
		sampler("windows.cpu", "kernel32", 0, now, []core.Capability{core.CapCPUUsage, core.CapCPULoad}, cpu),
		sampler("windows.memory", "kernel32", 0, now, []core.Capability{core.CapMemoryUsage}, memory),
		sampler("windows.filesystems", "kernel32", 10*time.Second, now, []core.Capability{core.CapStorageFilesystems}, drives),
		sampler("windows.network", "iphlpapi", 0, now, []core.Capability{core.CapNetworkInterfaces, core.CapNetworkThroughput}, network),
		unsupported.Rest("windows.unsupported", implemented, "Not collected on Windows yet."),
	}
}

func sampler(id, source string, interval time.Duration, now func() time.Time, caps []core.Capability, fill func(*collector.Result)) collector.Collector {
	if interval == 0 {
		interval = 2 * time.Second
	}
	return portable.Func{
		Desc: collector.Descriptor{ID: id, Version: version, Source: source, Capabilities: caps, Class: collector.Sampler,
			DefaultInterval: interval, DefaultTimeout: time.Second},
		Fn: func(ctx context.Context) collector.Result {
			r := collector.Result{CollectedAt: now(), Status: map[core.Capability]core.CapabilityStatus{}}
			if ctx.Err() != nil {
				for _, c := range caps {
					r.Status[c] = portable.Unavailable(core.TemporarilyUnavailable, "The collection was cancelled.")
				}
				return r
			}
			fill(&r)
			return r
		},
	}
}

func system(r *collector.Result) {
	info := &core.SystemInfo{OSID: "windows", OSName: "Windows", Architecture: runtime.GOARCH}
	info.Hostname, _ = os.Hostname()
	v := win.RtlGetVersion()
	info.Kernel = fmt.Sprintf("NT %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	info.OSVersion = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	info.OSPrettyName = "Windows " + info.OSVersion
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		product, _, _ := k.GetStringValue("ProductName")
		display, _, _ := k.GetStringValue("DisplayVersion")
		k.Close()
		// ProductName still says "Windows 10" on Windows 11; build 22000
		// and later are Windows 11.
		if v.BuildNumber >= 22000 {
			product = strings.Replace(product, "Windows 10", "Windows 11", 1)
		}
		if product != "" {
			info.OSPrettyName = strings.TrimSpace(product + " " + display)
		}
	}
	r.Status[core.CapSystemIdentity] = portable.Available()
	if ms, _, _ := procGetTickCount64.Call(); ms > 0 {
		info.Uptime = time.Duration(ms) * time.Millisecond
		info.BootTime = r.CollectedAt.Add(-info.Uptime).UTC().Truncate(time.Second)
		r.Status[core.CapSystemUptime] = portable.Available()
	} else {
		r.Status[core.CapSystemUptime] = portable.Unavailable(core.TemporarilyUnavailable, "Could not read the uptime.")
	}
	r.Records = append(r.Records, info)
}

// cpu reads system-wide idle, kernel and user time (100 ns units; kernel
// time includes idle time).
func cpu(r *collector.Result) {
	r.Status[core.CapCPULoad] = portable.Unavailable(core.Unsupported, "Windows has no load average.")
	var idle, kernel, user win.Filetime
	if ok, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		r.Status[core.CapCPUUsage] = portable.Status(err, "CPU times")
		return
	}
	ft := func(f win.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	i, k, u := ft(idle), ft(kernel), ft(user)
	sys := uint64(0)
	if k > i {
		sys = k - i
	}
	r.Status[core.CapCPUUsage] = portable.Available()
	r.Records = append(r.Records, &core.CPUCounters{Total: core.CPUTimes{User: u, System: sys, Idle: i}, LogicalCores: runtime.NumCPU()})
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memory(r *collector.Result) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); ok == 0 || m.TotalPhys == 0 {
		r.Status[core.CapMemoryUsage] = portable.Status(err, "memory status")
		return
	}
	s := &core.MemoryStats{TotalBytes: m.TotalPhys, AvailableBytes: m.AvailPhys, FreeBytes: m.AvailPhys}
	// The page file total includes physical memory (the commit limit).
	if m.TotalPageFile > m.TotalPhys {
		s.SwapTotalBytes = m.TotalPageFile - m.TotalPhys
		if m.AvailPageFile > m.AvailPhys {
			s.SwapFreeBytes = min(s.SwapTotalBytes, m.AvailPageFile-m.AvailPhys)
		}
	}
	r.Status[core.CapMemoryUsage] = portable.Available()
	r.Records = append(r.Records, s)
}

// drives reports fixed drives (C:\, D:\, …). Removable, network and
// optical drives are skipped: they can block or come and go.
func drives(r *collector.Result) {
	mask, err := win.GetLogicalDrives()
	if err != nil {
		r.Status[core.CapStorageFilesystems] = portable.Status(err, "the drive list")
		return
	}
	out := core.Filesystems{}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := win.UTF16PtrFromString(root)
		if win.GetDriveType(p) != win.DRIVE_FIXED {
			continue
		}
		var avail, total, free uint64
		if err := win.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil || total == 0 {
			continue
		}
		fsName := make([]uint16, 64)
		var flags uint32
		typ := ""
		if win.GetVolumeInformation(p, nil, 0, nil, nil, &flags, &fsName[0], uint32(len(fsName))) == nil {
			typ = strings.ToLower(win.UTF16ToString(fsName))
		}
		out = append(out, core.Filesystem{MountPoint: root, Device: root, Type: typ, ReadOnly: flags&win.FILE_READ_ONLY_VOLUME != 0,
			TotalBytes: total, FreeBytes: free, AvailableBytes: avail})
	}
	r.Status[core.CapStorageFilesystems] = portable.Available()
	r.Records = append(r.Records, out)
}

func network(r *collector.Result) {
	ifs, err := portable.Interfaces()
	if err != nil {
		r.Status[core.CapNetworkInterfaces] = portable.Status(err, "network interfaces")
		r.Status[core.CapNetworkThroughput] = r.Status[core.CapNetworkInterfaces]
		return
	}
	out := core.NetInterfaces{}
	counted := 0
	for _, i := range ifs {
		n := core.NetInterface{Name: i.Name, MACAddress: i.MAC, MTU: i.MTU, OperState: portable.OperState(i.Up), Loopback: i.Loopback, Addresses: i.Addresses}
		row := win.MibIfRow2{InterfaceIndex: uint32(i.Index)}
		if win.GetIfEntry2Ex(win.MibIfEntryNormal, &row) == nil {
			counted++
			n.RxBytes, n.TxBytes = row.InOctets, row.OutOctets
			n.RxPackets, n.TxPackets = row.InUcastPkts+row.InNUcastPkts, row.OutUcastPkts+row.OutNUcastPkts
			n.RxErrors, n.TxErrors, n.RxDropped, n.TxDropped = row.InErrors, row.OutErrors, row.InDiscards, row.OutDiscards
			n.OperState = portable.OperState(row.OperStatus == win.IfOperStatusUp)
			if row.TransmitLinkSpeed > 0 && row.OperStatus == win.IfOperStatusUp {
				mbps := int(row.TransmitLinkSpeed / 1_000_000)
				n.SpeedMbps = &mbps
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	r.Status[core.CapNetworkInterfaces] = portable.Available()
	if counted == 0 {
		r.Status[core.CapNetworkThroughput] = portable.Unavailable(core.TemporarilyUnavailable, "Interface statistics could not be read.")
	} else {
		r.Status[core.CapNetworkThroughput] = portable.Available()
	}
	r.Records = append(r.Records, out)
}
