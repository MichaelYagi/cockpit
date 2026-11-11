//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/bits"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// maxRoutes bounds the routing table we report.
const maxRoutes = 256

// netConfigCollector reports routes and DNS configuration. It is passive:
// nothing is probed or sent.
type netConfigCollector struct {
	base
	env collector.Env
}

func newNetConfig(env collector.Env) *netConfigCollector {
	c := &netConfigCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *netConfigCollector) Descriptor() collector.Descriptor {
	d := inventory("linux.netconfig", "procfs, resolv.conf", core.CapNetworkRoutes, core.CapNetworkDNS)
	d.DefaultInterval = 30 * time.Second
	return d
}

func (c *netConfigCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}

	var routes core.Routes
	b4, err4 := readFile(c.env.ProcRoot, "net/route")
	if err4 == nil {
		routes = append(routes, parseIPv4Routes(b4)...)
	}
	if b6, err := readFile(c.env.ProcRoot, "net/ipv6_route"); err == nil {
		routes = append(routes, parseIPv6Routes(b6)...)
	}
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Default != routes[j].Default {
			return routes[i].Default
		}
		return routes[i].Metric < routes[j].Metric
	})
	if len(routes) > maxRoutes {
		routes = routes[:maxRoutes]
	}
	switch {
	case err4 != nil && len(routes) == 0:
		res.Status[core.CapNetworkRoutes] = status(err4, "the routing table")
	default:
		res.Status[core.CapNetworkRoutes] = available()
		res.Records = append(res.Records, routes)
	}

	dns, err := c.dns()
	if err != nil {
		res.Status[core.CapNetworkDNS] = status(err, "the resolver configuration")
	} else {
		res.Status[core.CapNetworkDNS] = available()
		res.Records = append(res.Records, dns)
	}
	return res
}

func (c *netConfigCollector) dns() (*core.DNSConfig, error) {
	b, err := readFile(c.env.EtcRoot, "resolv.conf")
	if err != nil {
		return nil, err
	}
	cfg := parseResolvConf(b)
	cfg.Resolver = "resolv.conf"
	// systemd-resolved's stub listens on 127.0.0.53; its real upstream
	// servers are in /run/systemd/resolve/resolv.conf.
	if len(cfg.Nameservers) == 1 && cfg.Nameservers[0] == "127.0.0.53" {
		if up, err := readFile(c.env.RunRoot, "systemd/resolve/resolv.conf"); err == nil {
			u := parseResolvConf(up)
			cfg.Nameservers, cfg.Resolver = u.Nameservers, "systemd-resolved"
			if len(cfg.Search) == 0 {
				cfg.Search = u.Search
			}
		}
	}
	return &cfg, nil
}

func parseResolvConf(b []byte) core.DNSConfig {
	var cfg core.DNSConfig
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
			continue
		}
		switch f[0] {
		case "nameserver":
			if net.ParseIP(f[1]) != nil && len(cfg.Nameservers) < 16 {
				cfg.Nameservers = append(cfg.Nameservers, f[1])
			}
		case "search", "domain":
			cfg.Search = append(cfg.Search[:0], f[1:]...)
		}
	}
	return cfg
}

// Route flags from <linux/route.h>.
const (
	rtfUp      = 0x0001
	rtfGateway = 0x0002
	rtfReject  = 0x0200
)

// parseIPv4Routes parses /proc/net/route. Addresses are little-endian hex.
func parseIPv4Routes(b []byte) []core.Route {
	var out []core.Route
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		flags, err1 := strconv.ParseUint(f[3], 16, 32)
		dst, ok1 := hexIPv4(f[1])
		gw, ok2 := hexIPv4(f[2])
		mask, ok3 := hexIPv4(f[7])
		metric, err2 := strconv.Atoi(f[6])
		if err1 != nil || err2 != nil || !ok1 || !ok2 || !ok3 || flags&rtfUp == 0 || flags&rtfReject != 0 || f[0] == "lo" {
			continue
		}
		ones := bits.OnesCount32(uint32(mask[0])<<24 | uint32(mask[1])<<16 | uint32(mask[2])<<8 | uint32(mask[3]))
		r := core.Route{Family: "ipv4", Destination: fmt.Sprintf("%s/%d", dst, ones), Interface: f[0], Metric: metric, Default: ones == 0}
		if flags&rtfGateway != 0 {
			r.Gateway = gw.String()
		}
		out = append(out, r)
	}
	return out
}

func hexIPv4(s string) (net.IP, bool) {
	if len(s) != 8 {
		return nil, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return net.IPv4(b[3], b[2], b[1], b[0]).To4(), true
}

// parseIPv6Routes parses /proc/net/ipv6_route, keeping usable unicast
// routes (not loopback, multicast or reject routes).
func parseIPv6Routes(b []byte) []core.Route {
	var out []core.Route
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		dst, err1 := hex.DecodeString(f[0])
		plen, err2 := strconv.ParseUint(f[1], 16, 8)
		gw, err3 := hex.DecodeString(f[4])
		metric, err4 := strconv.ParseUint(f[5], 16, 32)
		flags, err5 := strconv.ParseUint(f[8], 16, 32)
		iface := f[9]
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || len(dst) != 16 || len(gw) != 16 || plen > 128 {
			continue
		}
		d := net.IP(dst)
		if iface == "lo" || flags&rtfUp == 0 || flags&rtfReject != 0 || d.IsMulticast() || d.IsLinkLocalUnicast() && plen >= 64 {
			continue
		}
		r := core.Route{Family: "ipv6", Destination: fmt.Sprintf("%s/%d", d, plen), Interface: iface, Metric: int(metric), Default: plen == 0}
		if g := net.IP(gw); !g.IsUnspecified() {
			r.Gateway = g.String()
		}
		out = append(out, r)
	}
	return out
}
