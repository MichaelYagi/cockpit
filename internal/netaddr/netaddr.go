// Package netaddr has small helpers for classifying listen and Host addresses.
package netaddr

import (
	"net"
	"net/netip"
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
