package windows

// Pure decoding of what the Windows calls return. This file has no build
// tag, so its tests run on every platform; the calls themselves are in
// the build-tagged files.

import (
	"encoding/binary"
	"math/bits"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

// LOGICAL_PROCESSOR_RELATIONSHIP values.
const (
	relationProcessorCore    = 0
	relationCache            = 2
	relationProcessorPackage = 3
	relationAll              = 0xffff
)

// processorTopology summarizes GetLogicalProcessorInformationEx: each
// entry is a relationship, its size, then PROCESSOR_RELATIONSHIP or
// CACHE_RELATIONSHIP. Affinity masks are 64-bit (GROUP_AFFINITY on
// 64-bit Windows, the only kind Cockpit builds for).
func processorTopology(b []byte) (cores, threads, sockets int, caches []core.CPUCache) {
	type key struct {
		level int
		typ   string
	}
	seen := map[key]core.CPUCache{}
	for len(b) >= 8 {
		rel := binary.LittleEndian.Uint32(b)
		size := int(binary.LittleEndian.Uint32(b[4:]))
		if size < 8 || size > len(b) {
			break
		}
		e := b[:size]
		b = b[size:]
		switch rel {
		case relationProcessorCore:
			// Flags@8, EfficiencyClass@9, Reserved[20], GroupCount@30, GroupMask[]@32.
			if len(e) < 32 {
				continue
			}
			cores++
			n := int(binary.LittleEndian.Uint16(e[30:]))
			for i := 0; i < n && 32+i*16+8 <= len(e); i++ {
				threads += bits.OnesCount64(binary.LittleEndian.Uint64(e[32+i*16:]))
			}
		case relationProcessorPackage:
			sockets++
		case relationCache:
			// Level@8, Associativity@9, LineSize@10, CacheSize@12, Type@16,
			// Reserved[18], GroupCount@38, GroupMask@40.
			if len(e) < 48 {
				continue
			}
			c := core.CPUCache{Level: int(e[8]), SizeBytes: uint64(binary.LittleEndian.Uint32(e[12:])),
				SharedBy: bits.OnesCount64(binary.LittleEndian.Uint64(e[40:]))}
			switch binary.LittleEndian.Uint32(e[16:]) {
			case 0:
				c.Type = "Unified"
			case 1:
				c.Type = "Instruction"
			case 2:
				c.Type = "Data"
			default:
				continue // trace caches are not listed
			}
			k := key{c.Level, c.Type}
			if _, ok := seen[k]; !ok {
				seen[k] = c
			}
		}
	}
	for _, c := range seen {
		caches = append(caches, c)
	}
	sort.Slice(caches, func(i, j int) bool {
		if caches[i].Level != caches[j].Level {
			return caches[i].Level < caches[j].Level
		}
		return caches[i].Type < caches[j].Type
	})
	return cores, threads, sockets, caches
}

// cpuIdentifier splits the registry's "Intel64 Family 6 Model 151
// Stepping 2" into family, model and stepping.
func cpuIdentifier(id string) (family, model, stepping string) {
	f := strings.Fields(id)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "Family":
			family = f[i+1]
		case "Model":
			model = f[i+1]
		case "Stepping":
			stepping = f[i+1]
		}
	}
	return family, model, stepping
}

// systemPowerStatus is SYSTEM_POWER_STATUS from GetSystemPowerStatus.
type systemPowerStatus struct {
	ACLineStatus        byte   // 0 offline, 1 online, 255 unknown
	BatteryFlag         byte   // 1 high, 2 low, 4 critical, 8 charging, 128 no battery, 255 unknown
	BatteryLifePercent  byte   // 0–100, 255 unknown
	SystemStatusFlag    byte   // battery saver
	BatteryLifeTime     uint32 // seconds remaining, 0xFFFFFFFF unknown
	BatteryFullLifeTime uint32
}

const (
	batteryCharging = 8
	batteryNone     = 128
	unknownByte     = 255
	unknownSeconds  = 0xFFFFFFFF
)

// powerSupplies turns the power status into the battery (when there is
// one) and the mains adapter.
func powerSupplies(s systemPowerStatus) core.PowerSupplies {
	out := core.PowerSupplies{}
	if s.BatteryFlag != batteryNone && s.BatteryFlag != unknownByte {
		b := core.PowerSupply{Name: "Battery", Type: "battery", Status: "unknown"}
		if s.BatteryLifePercent <= 100 {
			v := float64(s.BatteryLifePercent)
			b.CapacityPercent = &v
		}
		switch {
		case s.BatteryFlag&batteryCharging != 0:
			b.Status = "charging"
		case s.ACLineStatus == 1 && b.CapacityPercent != nil && *b.CapacityPercent >= 100:
			b.Status = "full"
		case s.ACLineStatus == 1:
			b.Status = "not_charging"
		case s.ACLineStatus == 0:
			b.Status = "discharging"
		}
		if b.Status == "discharging" && s.BatteryLifeTime != unknownSeconds && s.BatteryLifeTime > 0 {
			h := float64(s.BatteryLifeTime) / 3600
			b.HoursRemaining = &h
		}
		out = append(out, b)
	}
	// A desktop reports AC online with no battery; that is still worth a
	// mains entry, as Linux shows one.
	if s.ACLineStatus <= 1 {
		online := s.ACLineStatus == 1
		out = append(out, core.PowerSupply{Name: "AC adapter", Type: "mains", Online: &online})
	}
	return out
}

// rawSMBIOS splits GetSystemFirmwareTable('RSMB') output: an 8-byte
// RawSMBIOSData header (calling method, major, minor, DMI revision,
// table length) and the table.
func rawSMBIOS(b []byte) [][]byte {
	if len(b) < 8 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b[4:]))
	t := b[8:]
	if n < len(t) {
		t = t[:n]
	}
	return portable.SMBIOSStructures(t)
}

// smbiosInventory picks memory modules (type 17) and the chassis type
// (type 3, byte 5) from the SMBIOS structures.
func smbiosInventory(structs [][]byte) (core.MemoryModules, string) {
	var mods core.MemoryModules
	chassis := ""
	for _, s := range structs {
		switch s[0] {
		case 17:
			if m, ok := portable.SMBIOSMemory(s); ok {
				mods = append(mods, m)
			}
		case 3:
			if chassis == "" && len(s) > 5 && int(s[1]) > 5 {
				chassis = portable.ChassisTypes[strconv.Itoa(int(s[5]&0x7f))]
			}
		}
	}
	return mods, chassis
}

// ---- services ----

// Service states (SERVICE_STATUS.dwCurrentState) and start types.
const (
	svcStopped         = 1
	svcStartPending    = 2
	svcStopPending     = 3
	svcRunning         = 4
	svcContinuePending = 5
	svcPausePending    = 6
	svcPaused          = 7

	startBoot     = 0
	startSystem   = 1
	startAuto     = 2
	startDemand   = 3
	startDisabled = 4
	startUnknown  = 0xFFFFFFFF

	errServiceSpecific     = 1066 // ERROR_SERVICE_SPECIFIC_ERROR
	errServiceNeverStarted = 1077 // ERROR_SERVICE_NEVER_STARTED
)

// winService is what EnumServicesStatusEx and QueryServiceConfig report
// about one service.
type winService struct {
	Name, Display          string
	State, PID             uint32
	ExitCode, SpecificExit uint32
	StartType              uint32 // startUnknown when the configuration was refused
	Delayed                bool
}

var startTypes = map[uint32]string{startBoot: "boot", startSystem: "system", startAuto: "auto", startDemand: "manual", startDisabled: "disabled"}

// windowsService maps a service to the API's systemd-shaped fields (D51).
// A stopped automatic service that exited with an error is failed: it
// should be running and is not. Manual and delayed-start services may
// stop by design (Downloaded Maps Manager exits with code 50 when it has
// nothing to do), so their exit codes are shown but never failed.
func windowsService(w winService) core.Service {
	s := core.Service{Name: w.Name, Description: w.Display, LoadState: startTypes[w.StartType]}
	if s.LoadState == "" {
		s.LoadState = "unknown"
	} else if w.StartType == startAuto && w.Delayed {
		s.LoadState = "auto (delayed)"
	}
	exit := w.ExitCode
	if exit == errServiceSpecific {
		exit = w.SpecificExit
	}
	switch w.State {
	case svcRunning:
		s.ActiveState, s.SubState = "active", "running (pid "+strconv.FormatUint(uint64(w.PID), 10)+")"
	case svcPaused, svcPausePending, svcContinuePending:
		s.ActiveState, s.SubState = "active", "paused"
	case svcStartPending:
		s.ActiveState, s.SubState = "activating", "start pending"
	case svcStopPending:
		s.ActiveState, s.SubState = "deactivating", "stop pending"
	default:
		s.ActiveState, s.SubState = "inactive", "stopped"
		if w.ExitCode != 0 && w.ExitCode != errServiceNeverStarted {
			s.SubState = "stopped (exit " + strconv.FormatUint(uint64(exit), 10) + ")"
			if w.StartType == startAuto && !w.Delayed || w.StartType == startBoot || w.StartType == startSystem {
				s.ActiveState = "failed"
			}
		}
	}
	return s
}

// ---- routes and DNS ----

// routeRow is one MIB_IPFORWARD_ROW2, decoded.
type routeRow struct {
	Dst      net.IP
	Prefix   int
	Gateway  net.IP // nil or unspecified when on-link
	IfIndex  uint32
	Metric   int // route metric plus the interface's, as route print shows
	Loopback bool
}

// sockaddrIP reads the address of a SOCKADDR_INET: for AF_INET (2) the
// four bytes after family and port, for AF_INET6 (23) the sixteen after
// family, port and flow info.
func sockaddrIP(family uint16, data [6]uint32) net.IP {
	var b [24]byte
	for i, v := range data {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	switch family {
	case 2:
		return net.IP(append([]byte(nil), b[0:4]...))
	case 23:
		return net.IP(append([]byte(nil), b[4:20]...))
	}
	return nil
}

// windowsRoutes keeps the routes `route print` lists as active routes,
// without the local entries Linux keeps in its separate local table:
// on-link host routes (this machine's addresses and broadcasts),
// loopback, multicast and IPv6 link-local subnets.
func windowsRoutes(rows []routeRow, ifName func(uint32) string) core.Routes {
	out := core.Routes{}
	for _, r := range rows {
		if len(out) >= 512 {
			break
		}
		if r.Dst == nil || r.Loopback {
			continue
		}
		bitsLen := 32
		family := "ipv4"
		if r.Dst.To4() == nil {
			bitsLen, family = 128, "ipv6"
		}
		gw := r.Gateway
		if gw != nil && gw.IsUnspecified() {
			gw = nil
		}
		if r.Prefix < 0 || r.Prefix > bitsLen || r.Dst.IsLoopback() || r.Dst.IsMulticast() ||
			r.Prefix == bitsLen && gw == nil || r.Dst.Equal(net.IPv4bcast) ||
			family == "ipv6" && r.Dst.IsLinkLocalUnicast() && r.Prefix >= 64 {
			continue
		}
		dst := r.Dst.Mask(net.CIDRMask(r.Prefix, bitsLen))
		rt := core.Route{Family: family, Destination: dst.String() + "/" + strconv.Itoa(r.Prefix), Interface: ifName(r.IfIndex),
			Metric: r.Metric, Default: r.Prefix == 0}
		if gw != nil {
			rt.Gateway = gw.String()
		}
		out = append(out, rt)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// adapterDNS is one network adapter's DNS settings.
type adapterDNS struct {
	Up, HasGateway bool
	Servers        []string
	Suffix         string
}

// windowsDNS merges the adapters' DNS servers the way Windows uses them:
// adapters with a default gateway first, in binding order, then other
// connected adapters. searchList is the global suffix search list.
func windowsDNS(adapters []adapterDNS, searchList string) *core.DNSConfig {
	cfg := &core.DNSConfig{Resolver: "windows", Nameservers: []string{}, Search: []string{}}
	seen := map[string]bool{}
	add := func(list *[]string, v string, limit int) {
		if v = strings.TrimSpace(v); v != "" && !seen[v] && len(*list) < limit {
			seen[v] = true
			*list = append(*list, v)
		}
	}
	for _, pass := range []bool{true, false} {
		for _, a := range adapters {
			if !a.Up || a.HasGateway != pass {
				continue
			}
			for _, s := range a.Servers {
				// fec0:0:0:ffff::1–3 are the deprecated site-local defaults
				// Windows lists when no IPv6 DNS server is configured.
				if !strings.HasPrefix(s, "fec0:0:0:ffff::") {
					add(&cfg.Nameservers, s, 16)
				}
			}
		}
	}
	for _, s := range strings.Split(searchList, ",") {
		add(&cfg.Search, s, 16)
	}
	for _, a := range adapters {
		if a.Up {
			add(&cfg.Search, a.Suffix, 16)
		}
	}
	return cfg
}
