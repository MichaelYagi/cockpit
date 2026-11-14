package fleet

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func livez(cockpit bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			panic("scan sent credentials")
		}
		if cockpit {
			w.Header().Set("X-Request-ID", "req_01JD0000000000000000000000")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func scanOne(t *testing.T, srv *httptest.Server) []Found {
	t.Helper()
	ap := netip.MustParseAddrPort(strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://"))
	found, err := Scan(context.Background(), ScanOptions{
		Subnets:    []netip.Prefix{netip.PrefixFrom(ap.Addr(), 32)},
		Port:       int(ap.Port()),
		LookupAddr: func(context.Context, string) ([]string, error) { return []string{"My_Mac.lan."}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestScanFindsPlainHTTPAgent(t *testing.T) {
	srv := httptest.NewServer(livez(true))
	defer srv.Close()
	found := scanOne(t, srv)
	if len(found) != 1 || found[0].URL != srv.URL || found[0].Transport != TransportLoopback || found[0].Fingerprint != "" || found[0].Name != "my_mac" {
		t.Fatalf("found %+v", found)
	}
}

func TestScanFindsHTTPSAgentWithFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(livez(true))
	defer srv.Close()
	found := scanOne(t, srv)
	if len(found) != 1 || found[0].URL != srv.URL || found[0].Transport != TransportHTTPS || len(found[0].Fingerprint) != 64 {
		t.Fatalf("found %+v", found)
	}
}

// Another service on the port (Radarr defaults to 7878) is not Cockpit.
func TestScanIgnoresOtherServices(t *testing.T) {
	srv := httptest.NewServer(livez(false))
	defer srv.Close()
	if found := scanOne(t, srv); len(found) != 0 {
		t.Fatalf("found %+v", found)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "req_01JD0000000000000000000000")
		_, _ = w.Write([]byte(`{"status":"ok","version":"5"}`))
	}))
	defer other.Close()
	if found := scanOne(t, other); len(found) != 0 {
		t.Fatalf("found %+v", found)
	}
}

func TestCheckSubnets(t *testing.T) {
	for _, c := range []struct {
		s  []string
		ok bool
	}{
		{[]string{"192.168.0.0/24"}, true},
		{[]string{"10.0.0.0/20"}, true},
		{[]string{"127.0.0.1/32"}, true},
		{[]string{"10.0.0.0/16"}, false},                 // too large
		{[]string{"8.8.8.0/24"}, false},                  // public
		{[]string{"fd00::/120"}, false},                  // IPv6
		{[]string{"10.0.0.0/20", "10.0.16.0/24"}, false}, // over the total
		{[]string{"192.168.0.0/24", "192.168.1.0/24"}, true},
	} {
		var ps []netip.Prefix
		for _, s := range c.s {
			ps = append(ps, netip.MustParsePrefix(s))
		}
		if err := CheckSubnets(ps); (err == nil) != c.ok {
			t.Errorf("%v: %v", c.s, err)
		}
	}
}

func TestLocalSubnets(t *testing.T) {
	in := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.0.185"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("10.5.6.7"), Mask: net.CIDRMask(8, 32)},    // widened to a /24
		&net.IPNet{IP: net.ParseIP("172.16.1.9"), Mask: net.CIDRMask(28, 32)}, // kept narrow
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("203.0.113.5"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.0.186"), Mask: net.CIDRMask(24, 32)}, // duplicate
	}
	got := localSubnets(in)
	want := []string{"192.168.0.0/24", "10.5.6.0/24", "172.16.1.0/28"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestSuggestNameFallback(t *testing.T) {
	o := ScanOptions{LookupAddr: func(context.Context, string) ([]string, error) { return nil, &net.DNSError{IsNotFound: true} }}
	if got := suggestName(context.Background(), o, netip.MustParseAddr("192.168.0.20")); got != "host-192-168-0-20" {
		t.Errorf("got %q", got)
	}
}
