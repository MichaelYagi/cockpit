//go:build linux

package linux

import (
	"context"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Taken from a real host (addresses are little-endian hex).
const routeV4 = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth1	00000000	0100A8C0	0003	0	0	25	00000000	0	0	0
eth1	0000A8C0	00000000	0001	0	0	281	00FFFFFF	0	0	0
eth9	0000000A	00000000	0201	0	0	0	000000FF	0	0	0
lo	0000007F	00000000	0001	0	0	0	000000FF	0	0	0
`

const routeV6 = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003     eth1
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     eth1
fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001     eth1
ff000000000000000000000000000000 08 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000003 00000000 00000001     eth1
00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000002 00000000 80200001       lo
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo
`

func TestParseRoutes(t *testing.T) {
	v4 := parseIPv4Routes([]byte(routeV4))
	if len(v4) != 2 {
		t.Fatalf("v4 %+v", v4)
	}
	if r := v4[0]; !r.Default || r.Destination != "0.0.0.0/0" || r.Gateway != "192.168.0.1" || r.Interface != "eth1" || r.Metric != 25 {
		t.Errorf("default %+v", r)
	}
	if r := v4[1]; r.Destination != "192.168.0.0/24" || r.Gateway != "" || r.Default {
		t.Errorf("connected %+v", r)
	}
	v6 := parseIPv6Routes([]byte(routeV6))
	if len(v6) != 2 {
		t.Fatalf("v6 %+v", v6)
	}
	if r := v6[0]; !r.Default || r.Destination != "::/0" || r.Gateway != "fe80::1" || r.Metric != 1024 {
		t.Errorf("v6 default %+v", r)
	}
	if v6[1].Destination != "2001:db8::/64" {
		t.Errorf("v6 prefix %+v", v6[1])
	}
}

func TestDNS(t *testing.T) {
	plain := tree(t, map[string]string{"etc/resolv.conf": "# generated\nnameserver 10.255.255.254\nnameserver not-an-ip\nsearch corp.example lab.example\n"})
	r := newNetConfig(env(plain)).Collect(context.Background())
	wantStatus(t, r, core.CapNetworkDNS, core.Available)
	d := record[*core.DNSConfig](t, r)
	if d.Resolver != "resolv.conf" || len(d.Nameservers) != 1 || d.Nameservers[0] != "10.255.255.254" || len(d.Search) != 2 {
		t.Errorf("%+v", d)
	}

	stub := tree(t, map[string]string{
		"etc/resolv.conf":                 "nameserver 127.0.0.53\noptions edns0\n",
		"run/systemd/resolve/resolv.conf": "nameserver 1.1.1.1\nnameserver 9.9.9.9\nsearch home.arpa\n",
	})
	d = record[*core.DNSConfig](t, newNetConfig(env(stub)).Collect(context.Background()))
	if d.Resolver != "systemd-resolved" || len(d.Nameservers) != 2 || d.Nameservers[0] != "1.1.1.1" || d.Search[0] != "home.arpa" {
		t.Errorf("stub %+v", d)
	}
}

func TestNetConfigMissing(t *testing.T) {
	r := newNetConfig(env(t.TempDir())).Collect(context.Background())
	wantStatus(t, r, core.CapNetworkRoutes, core.Unsupported)
	wantStatus(t, r, core.CapNetworkDNS, core.Unsupported)
}

func TestNetworkAddresses(t *testing.T) {
	root := tree(t, map[string]string{"proc/net/dev": netDev})
	c := newNetwork(env(root))
	c.addrs = func() map[string][]string { return map[string][]string{"eth0": {"192.168.1.10/24", "fe80::1/64"}} }
	nets := record[core.NetInterfaces](t, c.Collect(context.Background()))
	if len(nets[1].Addresses) != 2 || nets[1].Addresses[0] != "192.168.1.10/24" || nets[0].Addresses != nil {
		t.Errorf("%+v", nets)
	}
}
