//go:build darwin

// Package darwin collects macOS metrics without cgo (D32): sysctl,
// getfsstat and the routing socket's interface statistics. CPU usage,
// sensors and disk health need Mach or IOKit calls that require cgo, so
// they are reported unsupported.
package darwin

import (
	"context"
	"encoding/binary"
	"errors"

	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
	"github.com/MichaelYagi/cockpit/internal/platform/unsupported"
)

const version = "1.0.0"

// implemented lists what the collectors below provide.
var implemented = []core.Capability{
	core.CapSystemIdentity, core.CapSystemUptime, core.CapCPUUsage, core.CapCPULoad, core.CapMemoryUsage,
	core.CapStorageFilesystems, core.CapNetworkInterfaces, core.CapNetworkThroughput,
}

// Collectors returns the macOS collector set.
func Collectors(env collector.Env) []collector.Collector {
	now := env.Now
	if now == nil {
		now = time.Now
	}
	return []collector.Collector{
		sampler("darwin.system", "sysctl", time.Minute, now, []core.Capability{core.CapSystemIdentity, core.CapSystemUptime}, system),
		sampler("darwin.cpu", "sysctl", 0, now, []core.Capability{core.CapCPUUsage, core.CapCPULoad}, cpu),
		sampler("darwin.memory", "sysctl", 0, now, []core.Capability{core.CapMemoryUsage}, memory),
		sampler("darwin.filesystems", "getfsstat", 10*time.Second, now, []core.Capability{core.CapStorageFilesystems},
			func(r *collector.Result) { filesystems(r, env.Filesystems) }),
		sampler("darwin.network", "sysctl net.route", 0, now, []core.Capability{core.CapNetworkInterfaces, core.CapNetworkThroughput}, network),
		unsupported.Rest("darwin.unsupported", implemented, "Not collected on macOS yet."),
	}
}

func sampler(id, source string, interval time.Duration, now func() time.Time, caps []core.Capability, fill func(*collector.Result)) collector.Collector {
	class, min := collector.Sampler, time.Duration(0)
	if interval == 0 {
		interval = 2 * time.Second
	}
	return portable.Func{
		Desc: collector.Descriptor{ID: id, Version: version, Source: source, Capabilities: caps, Class: class,
			DefaultInterval: interval, DefaultTimeout: time.Second, MinInterval: min},
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
	info := &core.SystemInfo{OSID: "macos", OSName: "macOS", Architecture: runtime.GOARCH}
	info.Hostname, _ = os.Hostname()
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil {
		info.OSVersion = v
	}
	info.OSPrettyName = strings.TrimSpace("macOS " + info.OSVersion)
	if t, err := unix.Sysctl("kern.ostype"); err == nil {
		if rel, err := unix.Sysctl("kern.osrelease"); err == nil {
			info.Kernel = t + " " + rel
		}
	}
	r.Status[core.CapSystemIdentity] = portable.Available()
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil && tv.Sec > 0 {
		info.BootTime = time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC()
		info.Uptime = r.CollectedAt.Sub(info.BootTime)
		r.Status[core.CapSystemUptime] = portable.Available()
	} else {
		r.Status[core.CapSystemUptime] = portable.Status(err, "the boot time")
	}
	r.Records = append(r.Records, info)
}

// cpu reports the load average; per-CPU tick counters need Mach calls.
func cpu(r *collector.Result) {
	r.Status[core.CapCPUUsage] = portable.Unavailable(core.Unsupported, "CPU usage needs Mach calls, which this build (without cgo) cannot make. The load average is shown instead.")
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(b) < 24 {
		r.Status[core.CapCPULoad] = portable.Status(errOr(err), "the load average")
		return
	}
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }
	scale := float64(binary.LittleEndian.Uint64(b[16:24]))
	if scale == 0 {
		r.Status[core.CapCPULoad] = portable.Unavailable(core.TemporarilyUnavailable, "The load average could not be read.")
		return
	}
	l := func(i int) float64 { return float64(binary.LittleEndian.Uint32(b[4*i:])) / scale }
	r.Status[core.CapCPULoad] = portable.Available()
	r.Records = append(r.Records, &core.CPUCounters{LogicalCores: runtime.NumCPU(), Load: &core.LoadAverage{One: l(0), Five: l(1), Fifteen: l(2)}})
}

// reclaimable page counters; not every macOS version has every one.
var reclaimable = []string{"vm.page_speculative_count", "vm.page_purgeable_count", "vm.page_pageable_external_count"}

// memory estimates available memory as free, speculative, purgeable and
// file-backed pages, which the kernel can hand out without swapping.
func memory(r *collector.Result) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		r.Status[core.CapMemoryUsage] = portable.Status(errOr(err), "the memory size")
		return
	}
	page, err := unix.SysctlUint32("vm.pagesize")
	if err != nil || page == 0 {
		r.Status[core.CapMemoryUsage] = portable.Status(errOr(err), "the page size")
		return
	}
	free, err := unix.SysctlUint32("vm.page_free_count")
	if err != nil {
		r.Status[core.CapMemoryUsage] = portable.Unavailable(core.Unsupported, "This macOS version does not report free memory pages.")
		return
	}
	// Reclaimable pages, where this macOS version reports them.
	pages := uint64(free)
	for _, name := range reclaimable {
		pages += sysctlCount(name)
	}
	s := &core.MemoryStats{TotalBytes: total, AvailableBytes: min(total, pages*uint64(page)), FreeBytes: uint64(free) * uint64(page)}
	// struct xsw_usage { u_int64_t xsu_total, xsu_avail, xsu_used; ... }
	if b, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(b) >= 16 {
		s.SwapTotalBytes, s.SwapFreeBytes = binary.LittleEndian.Uint64(b[0:]), binary.LittleEndian.Uint64(b[8:])
	}
	r.Status[core.CapMemoryUsage] = portable.Available()
	r.Records = append(r.Records, s)
}

// skippedFSTypes are pseudo filesystems; network ones are skipped too (D25).
var skippedFSTypes = map[string]bool{"devfs": true, "autofs": true, "nullfs": true, "lifs": true}

func filesystems(r *collector.Result, filter collector.FilesystemFilter) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		r.Status[core.CapStorageFilesystems] = portable.Status(err, "the mount table")
		return
	}
	buf := make([]unix.Statfs_t, n+8)
	if n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT); err != nil {
		r.Status[core.CapStorageFilesystems] = portable.Status(err, "the mount table")
		return
	}
	out := core.Filesystems{}
	for _, s := range buf[:n] {
		typ, mount, dev := cstr(s.Fstypename[:]), cstr(s.Mntonname[:]), cstr(s.Mntfromname[:])
		// MNT_DONTBROWSE marks APFS system volumes (VM, Preboot, …) that
		// share the root container and are not user storage.
		if s.Flags&unix.MNT_DONTBROWSE != 0 || skippedFSTypes[typ] || core.IsNetworkFilesystem(typ) || typ == "smbfs" || typ == "afpfs" || typ == "webdav" {
			continue
		}
		if slices.Contains(filter.ExcludeTypes, typ) || excluded(filter.ExcludeMountPoints, mount) || s.Blocks == 0 {
			continue
		}
		bs := uint64(s.Bsize)
		out = append(out, core.Filesystem{MountPoint: mount, Device: dev, Type: typ, ReadOnly: s.Flags&unix.MNT_RDONLY != 0,
			TotalBytes: s.Blocks * bs, FreeBytes: s.Bfree * bs, AvailableBytes: s.Bavail * bs, TotalInodes: s.Files, FreeInodes: s.Ffree})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	r.Status[core.CapStorageFilesystems] = portable.Available()
	r.Records = append(r.Records, out)
}

func excluded(patterns []string, mount string) bool {
	for _, p := range patterns {
		if dir, ok := strings.CutSuffix(p, "/**"); ok && (mount == dir || strings.HasPrefix(mount, dir+"/")) {
			return true
		}
		if p == mount {
			return true
		}
	}
	return false
}

// ifStats are counters from an RTM_IFINFO2 message (struct if_msghdr2).
// Offsets were checked against both natural and 4-byte packing of
// struct if_data64: every 64-bit field is 8-aligned either way.
type ifStats struct {
	index                                int
	mtu                                  int
	baud                                 uint64
	ipackets, ierrors, opackets, oerrors uint64
	ibytes, obytes, iqdrops              uint64
	flags                                uint32
}

const (
	ifmFlags   = 8  // int32 ifm_flags
	ifmIndex   = 12 // u_short ifm_index
	ifmData    = 32 // struct if_data64 ifm_data
	ifdMTU     = ifmData + 8
	ifdBaud    = ifmData + 16
	ifdIpack   = ifmData + 24
	ifdIerr    = ifmData + 32
	ifdOpack   = ifmData + 40
	ifdOerr    = ifmData + 48
	ifdIbytes  = ifmData + 64
	ifdObytes  = ifmData + 72
	ifdIqdrops = ifmData + 96
	ifmMinLen  = ifdIqdrops + 8
)

func parseIfList(b []byte) []ifStats {
	var out []ifStats
	for len(b) >= 4 {
		l := int(binary.LittleEndian.Uint16(b))
		if l < 4 || l > len(b) {
			break
		}
		msg := b[:l]
		b = b[l:]
		if msg[3] != unix.RTM_IFINFO2 || len(msg) < ifmMinLen {
			continue
		}
		u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(msg[off:]) }
		out = append(out, ifStats{
			index: int(binary.LittleEndian.Uint16(msg[ifmIndex:])), flags: binary.LittleEndian.Uint32(msg[ifmFlags:]),
			mtu: int(binary.LittleEndian.Uint32(msg[ifdMTU:])), baud: u64(ifdBaud),
			ipackets: u64(ifdIpack), ierrors: u64(ifdIerr), opackets: u64(ifdOpack), oerrors: u64(ifdOerr),
			ibytes: u64(ifdIbytes), obytes: u64(ifdObytes), iqdrops: u64(ifdIqdrops),
		})
	}
	return out
}

func network(r *collector.Result) {
	ifs, err := portable.Interfaces()
	if err != nil {
		r.Status[core.CapNetworkInterfaces] = portable.Status(err, "network interfaces")
		r.Status[core.CapNetworkThroughput] = r.Status[core.CapNetworkInterfaces]
		return
	}
	stats := map[int]ifStats{}
	raw, serr := ifList()
	for _, s := range parseIfList(raw) {
		stats[s.index] = s
	}
	out := core.NetInterfaces{}
	for _, i := range ifs {
		n := core.NetInterface{Name: i.Name, MACAddress: i.MAC, MTU: i.MTU, OperState: portable.OperState(i.Up), Loopback: i.Loopback, Addresses: i.Addresses}
		if s, ok := stats[i.Index]; ok {
			n.RxBytes, n.TxBytes, n.RxPackets, n.TxPackets = s.ibytes, s.obytes, s.ipackets, s.opackets
			n.RxErrors, n.TxErrors, n.RxDropped = s.ierrors, s.oerrors, s.iqdrops
			if s.baud > 0 && i.Up {
				mbps := int(s.baud / 1_000_000)
				n.SpeedMbps = &mbps
			}
		}
		out = append(out, n)
	}
	r.Status[core.CapNetworkInterfaces] = portable.Available()
	switch {
	case serr != nil:
		r.Status[core.CapNetworkThroughput] = portable.Status(serr, "interface statistics")
	case len(stats) == 0:
		r.Status[core.CapNetworkThroughput] = portable.Unavailable(core.TemporarilyUnavailable, "The kernel returned no interface statistics.")
	default:
		r.Status[core.CapNetworkThroughput] = portable.Available()
	}
	r.Records = append(r.Records, out)
}

// sysctlCount reads a page counter that is 32-bit on some macOS
// versions and 64-bit on others; missing counters are 0.
func sysctlCount(name string) uint64 {
	if n, err := unix.SysctlUint32(name); err == nil {
		return uint64(n)
	}
	if n, err := unix.SysctlUint64(name); err == nil {
		return n
	}
	return 0
}

// ifList fetches the RTM_IFINFO2 messages for every interface, by MIB
// {CTL_NET, PF_ROUTE, 0, 0, NET_RT_IFLIST2, 0}: x/sys only takes sysctl
// names, and "net.route" does not resolve by name.
func ifList() ([]byte, error) {
	return syscall.RouteRIB(syscall.NET_RT_IFLIST2, 0) //lint:ignore SA1019 the replacement (x/net/route) would be a new dependency for one call
}

func cstr(b []byte) string {
	if i := slices.Index(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func errOr(err error) error {
	if err != nil {
		return err
	}
	return errors.New("empty value")
}
