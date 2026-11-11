package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

var t0 = time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)

func sampleReport() *apiv1.Report {
	meta := apiv1.Meta{SchemaVersion: "1", MachineID: "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10", GeneratedAt: t0}
	avail := func() (string, *time.Time) { return apiv1.Available, &t0 }
	st, at := avail()
	return &apiv1.Report{
		Meta: meta,
		Status: apiv1.Status{Meta: meta, Agent: apiv1.Agent{Version: "0.3.0", APIVersion: "v1", StartedAt: t0, UptimeSeconds: 3600},
			API: &apiv1.APIServerStatus{Listeners: []apiv1.Listener{{Transport: "tcp", Address: "192.168.1.20:7878"},
				{Transport: "unix", Address: "/home/alice/.local/state/cockpit-monitor/agent.sock"}}}},
		System: &apiv1.SystemResponse{Meta: meta, System: apiv1.Section[apiv1.System]{Status: st, CollectedAt: at,
			Data: &apiv1.System{Hostname: "Web01.corp.example", Kernel: "6.18.33.2-microsoft-standard", OS: apiv1.OS{PrettyName: "Debian 13"}}}},
		Metrics: &apiv1.MetricsResponse{Meta: meta},
		Storage: &apiv1.StorageResponse{Meta: meta, Filesystems: apiv1.Section[[]apiv1.Filesystem]{Status: st, CollectedAt: at,
			Data: &[]apiv1.Filesystem{{MountPoint: "/home/alice", Device: "/dev/sda2"}, {MountPoint: "/", Device: "/dev/sda1"}}}},
		Network: &apiv1.NetworkResponse{Meta: meta,
			Interfaces: apiv1.Section[[]apiv1.NetworkInterface]{Status: st, CollectedAt: at, Data: &[]apiv1.NetworkInterface{
				{Name: "eth0", Addresses: []string{"192.168.1.20/24", "fe80::1234:5678:9abc:def0/64"}},
				{Name: "lo", Addresses: []string{"127.0.0.1/8", "::1/128"}},
			}},
			Routes: apiv1.Section[[]apiv1.Route]{Status: st, CollectedAt: at, Data: &[]apiv1.Route{
				{Family: "ipv4", Destination: "0.0.0.0/0", Gateway: "192.168.1.1", Interface: "eth0", Default: true},
				{Family: "ipv6", Destination: "ff00::/8", Interface: "eth0"},
			}},
			DNS: apiv1.Section[apiv1.DNS]{Status: st, CollectedAt: at, Data: &apiv1.DNS{Nameservers: []string{"192.168.1.1"}, Search: []string{"corp.example"}}},
		},
		Events: &apiv1.EventsResponse{Meta: meta, Items: []apiv1.Event{{ID: "01J", Time: t0, Type: "finding.raised", Severity: "warning",
			Subject: "host:web01",
			Message: "Web01 at 192.168.1.20 (not 192.168.1.200) saw 00:11:22:33:44:55 and 00-11-22-33-44-55 under /home/alice/data; alice was told. Host web01.corp.example."}}},
		Omitted: []apiv1.ReportOmission{},
	}
}

func TestAnonymize(t *testing.T) {
	in := sampleReport()
	before, _ := json.Marshal(in)
	out, err := Anonymize(in)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(in); string(after) != string(before) {
		t.Fatal("Anonymize modified its input")
	}
	raw, _ := json.Marshal(out)
	js := string(raw)
	for _, secret := range []string{"web01", "Web01", "corp.example", "192.168.1.20\"", "192.168.1.20/", "192.168.1.20 ", "192.168.1.1\"",
		"1234:5678", "00:11:22", "00-11-22", "alice", "3f0c6c1e"} {
		if strings.Contains(js, secret) {
			t.Errorf("anonymized report still contains %q", secret)
		}
	}
	if !out.Anonymized || out.MachineID != AnonymousMachineID || out.Status.MachineID != AnonymousMachineID {
		t.Errorf("anonymized=%v machine id %q / %q", out.Anonymized, out.MachineID, out.Status.MachineID)
	}
	sys := out.System.System.Data
	if sys.Hostname != "host-1" {
		t.Errorf("hostname %q", sys.Hostname)
	}
	if sys.Kernel != "6.18.33.2-microsoft-standard" {
		t.Errorf("kernel version changed: %q", sys.Kernel)
	}
	ifs := *out.Network.Interfaces.Data
	if got := ifs[0].Addresses; got[0] != "192.0.2.2/24" || got[1] != "2001:db8::1/64" {
		t.Errorf("eth0 addresses %v", got)
	}
	if got := ifs[1].Addresses; got[0] != "127.0.0.1/8" || got[1] != "::1/128" {
		t.Errorf("loopback addresses must be kept: %v", got)
	}
	routes := *out.Network.Routes.Data
	if routes[0].Gateway != "192.0.2.1" || routes[0].Destination != "0.0.0.0/0" || routes[1].Destination != "ff00::/8" {
		t.Errorf("routes %+v", routes)
	}
	if ns := out.Network.DNS.Data.Nameservers[0]; ns != routes[0].Gateway {
		t.Errorf("same address must map to the same placeholder: %s vs %s", ns, routes[0].Gateway)
	}
	if s := out.Network.DNS.Data.Search[0]; s != "domain-1.example" {
		t.Errorf("search domain %q", s)
	}
	if mp := (*out.Storage.Filesystems.Data)[0].MountPoint; mp != "/home/user-1" {
		t.Errorf("mount point %q", mp)
	}
	if a := out.Status.API.Listeners; a[0].Address != "192.0.2.2:7878" || a[1].Address != "/home/user-1/.local/state/cockpit-monitor/agent.sock" {
		t.Errorf("listeners %+v", a)
	}
	msg := out.Events.Items[0].Message
	want := "host-1 at 192.0.2.2 (not 192.168.1.200) saw 02:00:00:00:00:01 and 02:00:00:00:00:01 under /home/user-1/data; user-1 was told. Host host-1."
	if msg != want {
		t.Errorf("message\n got %s\nwant %s", msg, want)
	}
	if s := out.Events.Items[0].Subject; s != "host:host-1" {
		t.Errorf("subject %q", s)
	}
}

func TestAnonymizeNoIdentifiers(t *testing.T) {
	r := &apiv1.Report{Meta: apiv1.Meta{SchemaVersion: "1", MachineID: "x", GeneratedAt: t0}, Omitted: []apiv1.ReportOmission{}}
	out, err := Anonymize(r)
	if err != nil || !out.Anonymized || out.MachineID != AnonymousMachineID {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestMarkdownEscapesUntrustedText(t *testing.T) {
	r := sampleReport()
	hostile := "evil|cell\n# Heading <script>alert(1)</script> **bold** [link](http://x) \x1b[31m\u202e"
	r.Services = &apiv1.ServicesResponse{Services: apiv1.Section[apiv1.ServiceList]{Status: apiv1.Available,
		Data: &apiv1.ServiceList{Total: 1, Failed: 1, Items: []apiv1.Service{{Name: "x.service", Description: hostile, ActiveState: "failed", SubState: "failed"}}}}}
	md := string(Markdown(r))
	for _, bad := range []string{"<script>", "\x1b", "\u202e", "\n# Heading", "**bold**", "[link](", "evil|cell"} {
		if strings.Contains(md, bad) {
			t.Errorf("markdown contains unescaped %q", bad)
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| x.service") {
			cells := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|")
			if cells != 4 {
				t.Errorf("row has %d separators, want 4: %s", cells, line)
			}
		}
	}
}

func TestMarkdownSections(t *testing.T) {
	r := sampleReport()
	r.Health = &apiv1.HealthResponse{Status: "warning", Checks: []apiv1.HealthCheck{
		{ID: "storage", Name: "Storage capacity", Status: "warning", Availability: apiv1.Available, Summary: "1 filesystem above 85%",
			Findings: []apiv1.Finding{{Severity: "warning", Subject: "fs:/", Summary: "/ is 90% full", Recommendation: "Free space.",
				Evidence: map[string]string{"used_percent": "90"}, Since: t0}}},
		{ID: "cpu_temperature", Name: "CPU temperature", Status: "unknown", Availability: apiv1.Unsupported, Reason: "No sensors."},
	}}
	r.Omitted = []apiv1.ReportOmission{{Section: "settings", Reason: "Socket only."}}
	sz, al := uint64(1<<40), uint64(1<<39)
	r.Storage.ZFS = apiv1.Section[apiv1.ZFS]{Status: apiv1.Available, Data: &apiv1.ZFS{Pools: []apiv1.ZFSPool{{Name: "tank", State: "DEGRADED", SizeBytes: &sz, AllocatedBytes: &al}}}}
	charge := 42.0
	r.Metrics.UPS = &apiv1.Section[[]apiv1.UPS]{Status: apiv1.Available, Data: &[]apiv1.UPS{{Name: "rack", Status: []string{"OB"}, ChargePercent: &charge}}}
	rate := 30.0
	r.Metrics.Custom = &[]apiv1.CustomMetricsSet{{Collector: "custom.demo", DroppedSeries: 1, Problem: "bad unit",
		Section: apiv1.Section[[]apiv1.CustomMetric]{Status: apiv1.Available, Data: &[]apiv1.CustomMetric{
			{Name: "custom.demo.read", Kind: "counter", Unit: "bytes", Labels: map[string]string{"dev": "a"}, Value: 900, RatePerSecond: &rate, RateUnit: "bytes_per_second"},
		}}}}
	md := string(Markdown(r))
	for _, want := range []string{"# Cockpit diagnostic report", "## System", "Web01.corp.example", "## Health", "WARNING", "No sensors.",
		"used\\_percent: 90", "## ZFS pools", "| tank | DEGRADED | 512.0 GiB | 1.0 TiB |", "## Power", "| rack | OB | 42.0% |", "## Extension metrics", "custom.demo.read", "dev=a", "30 bytes\\_per\\_second", "1 metrics dropped", "## Storage", "## Network", "## Recent events", "## Not included", "settings: Socket only."} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}
