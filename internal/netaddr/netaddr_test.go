package netaddr

import (
	"net"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host         string
		loopback, ok bool
	}{
		{"127.0.0.1", true, true},
		{"127.1.2.3", true, true},
		{"::1", true, true},
		{"[::1]", true, true},
		{"localhost", true, true},
		{"LOCALHOST", true, true},
		{"", false, true},
		{"0.0.0.0", false, true},
		{"::", false, true},
		{"192.168.1.10", false, true},
		{"example.com", false, false},
		{"localhost.evil.com", false, false},
	}
	for _, tt := range tests {
		lb, ok := IsLoopbackHost(tt.host)
		if lb != tt.loopback || ok != tt.ok {
			t.Errorf("IsLoopbackHost(%q) = %v,%v want %v,%v", tt.host, lb, ok, tt.loopback, tt.ok)
		}
	}
}

func TestIsLoopbackHostHeader(t *testing.T) {
	for _, h := range []string{"127.0.0.1:7878", "localhost:7878", "[::1]:7878", "localhost", "127.0.0.1"} {
		if !IsLoopbackHostHeader(h) {
			t.Errorf("%q rejected", h)
		}
	}
	for _, h := range []string{"", "evil.example:7878", "attacker.com", "10.0.0.1:7878", ":7878", "localhost.attacker.com"} {
		if IsLoopbackHostHeader(h) {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestLANAddresses(t *testing.T) {
	ipnet := func(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)} }
	in := []net.Addr{ipnet("127.0.0.1"), ipnet("203.0.113.9"), ipnet("192.168.0.185"), ipnet("169.254.1.1"),
		ipnet("fe80::1"), ipnet("2001:db8::1"), ipnet("10.0.0.2"), ipnet("192.168.0.185"), &net.IPAddr{IP: net.ParseIP("172.16.0.1")}}
	var got []string
	for _, a := range lanAddresses(in) {
		got = append(got, a.String())
	}
	if strings.Join(got, " ") != "10.0.0.2 172.16.0.1 192.168.0.185 203.0.113.9" {
		t.Errorf("got %v", got)
	}
}

func TestIsPrivateClient(t *testing.T) {
	for ip, want := range map[string]bool{"127.0.0.1": true, "::1": true, "192.168.0.9": true, "10.1.2.3": true, "172.20.0.1": true,
		"fd12::1": true, "fe80::1": true, "169.254.0.5": true, "::ffff:192.168.0.9": true,
		"203.0.113.9": false, "8.8.8.8": false, "2001:db8::1": false, "172.32.0.1": false} {
		if got := IsPrivateClient(&net.TCPAddr{IP: net.ParseIP(ip)}); got != want {
			t.Errorf("%s: %v", ip, got)
		}
	}
	if IsPrivateClient(&net.UnixAddr{Name: "x"}) {
		t.Error("unix address counted as private")
	}
}
