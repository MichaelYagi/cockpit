package netaddr

import "testing"

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
