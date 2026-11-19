//go:build windows

package windows

import (
	"errors"
	"net"
	"sync"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

// ---- routes and DNS ----

func netConfig(r *collector.Result) {
	if routes, err := routeTable(); err != nil {
		r.Status[core.CapNetworkRoutes] = portable.Status(err, "the routing table")
	} else {
		r.Status[core.CapNetworkRoutes] = portable.Available()
		r.Records = append(r.Records, routes)
	}
	if dns, err := dnsConfig(); err != nil {
		r.Status[core.CapNetworkDNS] = portable.Status(err, "the DNS configuration")
	} else {
		r.Status[core.CapNetworkDNS] = portable.Available()
		r.Records = append(r.Records, dns)
	}
}

func routeTable() (core.Routes, error) {
	var table *win.MibIpForwardTable2
	if err := win.GetIpForwardTable2(win.AF_UNSPEC, &table); err != nil {
		return nil, err
	}
	defer win.FreeMibTable(unsafe.Pointer(table))
	type ifKey struct {
		family uint16
		luid   uint64
	}
	ifMetric := map[ifKey]int{}
	var rows []routeRow
	for _, row := range table.Rows() {
		fam := row.DestinationPrefix.Prefix.Family
		k := ifKey{fam, row.InterfaceLuid}
		m, ok := ifMetric[k]
		if !ok {
			e := win.MibIpInterfaceRow{Family: fam, InterfaceLuid: row.InterfaceLuid}
			if win.GetIpInterfaceEntry(&e) == nil {
				m = int(e.Metric)
			}
			ifMetric[k] = m
		}
		rows = append(rows, routeRow{
			Dst:      sockaddrIP(fam, row.DestinationPrefix.Prefix.Data),
			Prefix:   int(row.DestinationPrefix.PrefixLength),
			Gateway:  sockaddrIP(row.NextHop.Family, row.NextHop.Data),
			IfIndex:  row.InterfaceIndex,
			Metric:   int(row.Metric) + m,
			Loopback: row.Loopback != 0,
		})
	}
	names := map[uint32]string{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			names[uint32(i.Index)] = i.Name
		}
	}
	return windowsRoutes(rows, func(i uint32) string { return names[i] }), nil
}

const gaaFlags = win.GAA_FLAG_SKIP_ANYCAST | win.GAA_FLAG_SKIP_MULTICAST | win.GAA_FLAG_INCLUDE_GATEWAYS

func dnsConfig() (*core.DNSConfig, error) {
	size := uint32(16 << 10)
	var buf []byte
	for range 4 {
		buf = make([]byte, size)
		err := win.GetAdaptersAddresses(win.AF_UNSPEC, gaaFlags, 0, (*win.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, win.ERROR_BUFFER_OVERFLOW) || size > 4<<20 {
			return nil, err
		}
		buf = nil
	}
	if buf == nil {
		return nil, win.ERROR_BUFFER_OVERFLOW
	}
	var adapters []adapterDNS
	for a := (*win.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil && len(adapters) < 64; a = a.Next {
		d := adapterDNS{Up: a.OperStatus == win.IfOperStatusUp, HasGateway: a.FirstGatewayAddress != nil}
		if a.IfType == win.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		if a.DnsSuffix != nil {
			d.Suffix = win.UTF16PtrToString(a.DnsSuffix)
		}
		for s := a.FirstDnsServerAddress; s != nil && len(d.Servers) < 16; s = s.Next {
			if ip := s.Address.IP(); ip != nil {
				d.Servers = append(d.Servers, ip.String())
			}
		}
		adapters = append(adapters, d)
	}
	search := ""
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, registry.QUERY_VALUE); err == nil {
		search, _, _ = k.GetStringValue("SearchList")
		k.Close()
	}
	return windowsDNS(adapters, search), nil
}

// ---- services (D51) ----

type servicesCollector struct {
	mu       sync.Mutex
	configs  map[string]svcConfig
	loadedAt time.Time
}

type svcConfig struct {
	startType uint32
	delayed   bool
}

// configTTL is how long start types are cached: they change only when
// an administrator reconfigures a service.
const configTTL = 5 * time.Minute

func (c *servicesCollector) collect(r *collector.Result) {
	m, err := win.OpenSCManager(nil, nil, win.SC_MANAGER_CONNECT|win.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		r.Status[core.CapServicesWindows] = portable.Status(err, "the service list")
		return
	}
	defer win.CloseServiceHandle(m)
	list, err := enumServices(m)
	if err != nil {
		r.Status[core.CapServicesWindows] = portable.Status(err, "the service list")
		return
	}
	c.mu.Lock()
	if c.configs == nil || r.CollectedAt.Sub(c.loadedAt) > configTTL {
		c.configs, c.loadedAt = map[string]svcConfig{}, r.CollectedAt
	}
	out := make(core.Services, 0, len(list))
	for _, w := range list {
		cfg, ok := c.configs[w.Name]
		if !ok {
			cfg = serviceConfig(m, w.Name)
			c.configs[w.Name] = cfg
		}
		w.StartType, w.Delayed = cfg.startType, cfg.delayed
		out = append(out, windowsService(w))
	}
	c.mu.Unlock()
	r.Status[core.CapServicesWindows] = portable.Available()
	r.Records = append(r.Records, out)
}

func enumServices(m win.Handle) ([]winService, error) {
	var out []winService
	var resume uint32
	buf := make([]byte, 64<<10)
	for range 64 {
		var needed, n uint32
		err := win.EnumServicesStatusEx(m, win.SC_ENUM_PROCESS_INFO, win.SERVICE_WIN32, win.SERVICE_STATE_ALL,
			&buf[0], uint32(len(buf)), &needed, &n, &resume, nil)
		if err != nil && !errors.Is(err, win.ERROR_MORE_DATA) {
			return nil, err
		}
		if n > 0 {
			entries := unsafe.Slice((*win.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), n)
			for _, e := range entries {
				st := e.ServiceStatusProcess
				out = append(out, winService{
					Name: win.UTF16PtrToString(e.ServiceName), Display: win.UTF16PtrToString(e.DisplayName),
					State: st.CurrentState, PID: st.ProcessId, ExitCode: st.Win32ExitCode, SpecificExit: st.ServiceSpecificExitCode,
					StartType: startUnknown,
				})
			}
		}
		if err == nil {
			return out, nil
		}
		if n == 0 && needed > uint32(len(buf)) {
			if needed > 16<<20 {
				return nil, err
			}
			buf = make([]byte, needed)
		}
	}
	return out, nil
}

// serviceConfig reads a service's start type; services whose
// configuration Windows refuses keep startUnknown.
func serviceConfig(m win.Handle, name string) svcConfig {
	cfg := svcConfig{startType: startUnknown}
	p, err := win.UTF16PtrFromString(name)
	if err != nil {
		return cfg
	}
	s, err := win.OpenService(m, p, win.SERVICE_QUERY_CONFIG)
	if err != nil {
		return cfg
	}
	defer win.CloseServiceHandle(s)
	var needed uint32
	_ = win.QueryServiceConfig(s, nil, 0, &needed)
	if needed == 0 || needed > 64<<10 {
		return cfg
	}
	buf := make([]byte, needed)
	qc := (*win.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if win.QueryServiceConfig(s, qc, needed, &needed) != nil {
		return cfg
	}
	cfg.startType = qc.StartType
	if cfg.startType == startAuto {
		var info win.SERVICE_DELAYED_AUTO_START_INFO
		if win.QueryServiceConfig2(s, win.SERVICE_CONFIG_DELAYED_AUTO_START_INFO, (*byte)(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)), &needed) == nil {
			cfg.delayed = info.IsDelayedAutoStartUp != 0
		}
	}
	return cfg
}
