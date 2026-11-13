// Package netaddr has small helpers for classifying listen and Host addresses.
package netaddr

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// IsLoopbackHost reports whether host is a loopback name or address.
// ok is false when host is neither an IP address, "localhost", nor empty.
// An empty host means "all interfaces" and is not loopback.
func IsLoopbackHost(host string) (loopback, ok bool) {
	if host == "" {
		return false, true
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true, true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false, false
	}
	return addr.IsLoopback(), true
}

// IsLoopbackHostHeader reports whether an HTTP Host header value (with or
// without port) names a loopback address. Used to block DNS rebinding.
func IsLoopbackHostHeader(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	if host == "" {
		return false
	}
	lb, ok := IsLoopbackHost(host)
	return ok && lb
}

// LANAddresses lists this machine's IPv4 addresses that other machines can
// reach: up, not loopback, not link-local. Private (home and office)
// addresses come first. Errors give an empty list.
func LANAddresses() []netip.Addr {
	var addrs []net.Addr
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if a, err := ifc.Addrs(); err == nil {
			addrs = append(addrs, a...)
		}
	}
	return lanAddresses(addrs)
}

func lanAddresses(in []net.Addr) []netip.Addr {
	seen := map[netip.Addr]bool{}
	var private, other []netip.Addr
	for _, a := range in {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if !addr.Is4() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || seen[addr] {
			continue
		}
		seen[addr] = true
		if addr.IsPrivate() {
			private = append(private, addr)
		} else {
			other = append(other, addr)
		}
	}
	slices.SortFunc(private, netip.Addr.Compare)
	slices.SortFunc(other, netip.Addr.Compare)
	return append(private, other...)
}

// IsPrivateClient reports whether a connection's remote address is on this
// machine or a private network: loopback, RFC 1918 and fc00::/7, or
// link-local.
func IsPrivateClient(a net.Addr) bool {
	var ip net.IP
	switch v := a.(type) {
	case *net.TCPAddr:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
