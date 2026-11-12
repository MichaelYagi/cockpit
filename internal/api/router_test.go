package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	openapi "github.com/MichaelYagi/cockpit/api"
	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/internal/api/middleware"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/events"
	"github.com/MichaelYagi/cockpit/internal/health"
	"github.com/MichaelYagi/cockpit/internal/storage/sqlite"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Test keys for the fake verifier are assembled at runtime so no
// key-shaped literal is committed.
var (
	systemKey  = "ckp" + "_systemreader_" + strings.Repeat("A", 43)
	metricsKey = "ckp" + "_metricsonly1_" + strings.Repeat("B", 43)
	brokenKey  = "ckp" + "_brokenstore1_" + strings.Repeat("C", 43)
)

// Sensitive test values that must never appear in responses (D9).
const (
	testMAC    = "02:00:5e:00:53:01"
	testSerial = "FAKE-SERIAL-0042"
	testWWN    = "eui.00000000000000fa"
)

type fakeVerifier map[string]auth.Principal

func (f fakeVerifier) Verify(_ context.Context, key string) (auth.Principal, error) {
	if key == brokenKey {
		return auth.Principal{}, io.ErrUnexpectedEOF // storage failure, not bad credentials
	}
	if p, ok := f[key]; ok {
		return p, nil
	}
	return auth.Principal{}, auth.ErrInvalidCredentials
}

var (
	t0       = time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)
	fixedNow = func() time.Time { return t0 }
)

type fakeStatus struct{ panics bool }

func (fakeStatus) MachineID() core.MachineID { return "3f0c6c1e-8f5b-4d4e-9a57-2b1d2a2f9b10" }
func (fakeStatus) StartedAt() time.Time      { return t0.Add(-30 * time.Minute) }
func (f fakeStatus) Collectors() []core.CollectorStatus {
	if f.panics {
		panic("status source exploded")
	}
	return []core.CollectorStatus{{
		ID: "test.cpu", Version: "1.0.0",
		Capabilities: map[core.Capability]core.CapabilityStatus{
			core.CapCPUUsage: {Availability: core.Available},
			core.CapCPULoad:  {Availability: core.PermissionDenied, Reason: "Not permitted."},
		},
	}}
}

type fixedSnapshot struct{ s *core.Snapshot }

func (f fixedSnapshot) Current() *core.Snapshot { return f.s }

func ptr[T any](v T) *T { return &v }

func meta() core.SectionMeta {
	return core.SectionMeta{Status: core.Available, CollectedAt: t0.Add(-time.Second), StaleAfter: t0.Add(time.Minute), Source: "procfs"}
}

// testSnapshot exercises every section, including sensitive values,
// unavailable sections and null rates.
func testSnapshot() *core.Snapshot {
	return &core.Snapshot{
		Generation: 7, GeneratedAt: t0,
		System: core.Section[core.SystemInfo]{Meta: meta(), Data: &core.SystemInfo{Hostname: "test-host", OSID: "ubuntu", OSName: "Ubuntu",
			OSVersion: "24.04", OSPrettyName: "Ubuntu 24.04 LTS", Kernel: "6.8.0", Architecture: "amd64", BootTime: t0.Add(-time.Hour)}},
		CPU: core.Section[core.CPUMetrics]{Meta: meta(), Data: &core.CPUMetrics{UsagePercent: ptr(12.5), PerCoreUsagePercent: []float64{10, 15},
			WindowSeconds: 2, LogicalCores: 2, ModelName: "Test CPU", Load: &core.LoadAverage{One: 0.5, Five: 0.4, Fifteen: 0.3}}},
		Memory: core.Section[core.MemoryMetrics]{Meta: meta(), Data: &core.MemoryMetrics{MemoryStats: core.MemoryStats{TotalBytes: 1000, AvailableBytes: 600},
			UsedBytes: 400, UsedPercent: 40}},
		MemoryPressure: core.Section[core.MemoryPressure]{Meta: core.SectionMeta{Status: core.Unsupported, Reason: "This system does not provide memory pressure (PSI)."}},
		Filesystems: core.Section[core.Filesystems]{Meta: meta(), Data: &core.Filesystems{
			{MountPoint: "/", Device: "/dev/sda1", Type: "ext4", TotalBytes: 1000, FreeBytes: 500, AvailableBytes: 450, TotalInodes: 100, FreeInodes: 40},
			{MountPoint: "/mnt/c", Device: "C:\\", Type: "9p", TotalBytes: 1000, FreeBytes: 900, AvailableBytes: 900},
		}},
		Network: core.Section[[]core.NetInterfaceMetrics]{Meta: meta(), Data: &[]core.NetInterfaceMetrics{
			{NetInterface: core.NetInterface{Name: "eth0", MACAddress: testMAC, MTU: 1500, OperState: "up", SpeedMbps: ptr(1000), RxBytes: 10, TxBytes: 5,
				Addresses: []string{"192.0.2.10/24"}},
				RxBytesPerSecond: ptr(100.0), TxBytesPerSecond: ptr(50.0), WindowSeconds: 2},
			{NetInterface: core.NetInterface{Name: "lo", Loopback: true, OperState: "unknown"}}, // first sample: rates null
		}},
		BlockDevices: core.Section[core.BlockDevices]{Meta: meta(), Data: &core.BlockDevices{
			{Name: "nvme0n1", Model: "Test SSD", Serial: testSerial, WWN: testWWN, Transport: "nvme", Rotational: ptr(false), SizeBytes: 512},
		}},
		Sensors:    core.Section[core.Sensors]{Meta: core.SectionMeta{Status: core.Unsupported, Source: "sysfs/hwmon", Reason: "No hardware temperature sensors are exposed."}},
		DiskHealth: core.Section[core.DiskHealthList]{Meta: core.SectionMeta{Status: core.NotConfigured, Source: "smartctl", Reason: "smartctl is not installed."}},
		CPUInfo: core.Section[core.CPUInfo]{Meta: meta(), Data: &core.CPUInfo{Vendor: "AuthenticAMD", ModelName: "Test CPU", Family: "23",
			Model: "113", Stepping: "0", Sockets: 1, Cores: 6, Threads: 12, Virtualization: "AMD-V", Flags: []string{"avx2"},
			Caches: []core.CPUCache{{Level: 3, Type: "Unified", SizeBytes: 32 << 20, SharedBy: 12}}}},
		Board: core.Section[core.BoardInfo]{Meta: meta(), Data: &core.BoardInfo{SystemVendor: "Vendor", ProductName: "Box", ChassisType: "Desktop", Serial: testSerial}},
		MemoryModules: core.Section[core.MemoryModules]{Meta: core.SectionMeta{Status: core.PermissionDenied, Source: "sysfs/dmi",
			Reason: "Reading memory module details requires root."}},
		PCI: core.Section[core.PCIDevices]{Meta: meta(), Data: &core.PCIDevices{{Address: "0000:01:00.0", ClassCode: "0x030000",
			ClassName: "VGA compatible controller", VendorID: "0x10de", DeviceID: "0x2504", Driver: "nvidia"}}},
		USB: core.Section[core.USBDevices]{Meta: meta(), Data: &core.USBDevices{{Path: "1-2", VendorID: "046d", ProductID: "c52b",
			Product: "Receiver", Serial: testSerial}}},
		GPUs: core.Section[core.GPUs]{Meta: meta(), Data: &core.GPUs{{Vendor: "NVIDIA", Name: "RTX 3060", PCIAddress: "0000:01:00.0",
			MemoryTotalBytes: ptr(uint64(12 << 30)), UtilizationPercent: ptr(7.0), MetricsSource: "nvidia-smi", UUID: testWWN}}},
		Services: core.Section[core.Services]{Meta: meta(), Data: &core.Services{
			{Name: "backup.service", Description: "Nightly backup", LoadState: "loaded", ActiveState: "failed", SubState: "failed"},
			{Name: "ssh.service", Description: "OpenSSH", LoadState: "loaded", ActiveState: "active", SubState: "running"},
		}},
		Routes: core.Section[core.Routes]{Meta: meta(), Data: &core.Routes{{Family: "ipv4", Destination: "0.0.0.0/0", Gateway: "192.0.2.1", Interface: "eth0", Default: true}}},
		DNS:    core.Section[core.DNSConfig]{Meta: meta(), Data: &core.DNSConfig{Nameservers: []string{"192.0.2.53"}, Resolver: "resolv.conf"}},
		Containers: core.Section[[]core.ContainerMetrics]{Meta: meta(), Data: &[]core.ContainerMetrics{
			{ContainerSample: core.ContainerSample{Runtime: "docker", ID: strings.Repeat("a", 64), UID: -1, CPUUsageUsec: 10, MemoryBytes: ptr(uint64(1 << 20))}},
			{ContainerSample: core.ContainerSample{Runtime: "podman", ID: strings.Repeat("b", 64), Rootless: true, UID: 1000}, CPUPercent: ptr(12.5)},
		}},
		ZFS: core.Section[core.ZFSInfo]{Meta: meta(), Data: &core.ZFSInfo{Pools: []core.ZFSPool{{Name: "tank", State: "ONLINE", SizeBytes: ptr(uint64(1000)), ChecksumErrors: ptr(uint64(2))}},
			ARC: &core.ZFSARC{SizeBytes: 10, Hits: 9, Misses: 1}}},
		Power: core.Section[core.PowerSupplies]{Meta: meta(), Data: &core.PowerSupplies{{Name: "BAT0", Type: "battery", Status: "discharging", CapacityPercent: ptr(80.0), HoursRemaining: ptr(4.0)},
			{Name: "AC", Type: "mains", Online: ptr(false)}}},
		UPS: core.Section[core.UPSList]{Meta: core.SectionMeta{Status: core.NotConfigured, Source: "Network UPS Tools", Reason: "UPS monitoring is off."}},
		Custom: []core.CustomSet{
			{Collector: "custom.demo", Meta: meta(), Dropped: 1, Problem: "metric \"x\" has an unknown unit", Metrics: []core.CustomMetric{
				{Name: "custom.demo.open", Kind: core.Gauge, Unit: "count", Value: 42},
				{Name: "custom.demo.read", Kind: core.Counter, Unit: "bytes", Labels: map[string]string{"dev": "a"}, Value: 900, RatePerSecond: ptr(30.0)},
			}},
			{Collector: "custom.idle", Meta: core.SectionMeta{Status: core.PermissionDenied, Source: "custom", Reason: "Not permitted."}},
		},
		Collectors: fakeStatus{}.Collectors(),
	}
}

type env struct {
	d      Deps
	keys   *auth.Service
	stats  *middleware.Stats
	events *events.Log
}

func newEnv(t *testing.T, logOut io.Writer) env {
	t.Helper()
	spec, err := openapi.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if logOut == nil {
		logOut = io.Discard
	}
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	evLog := events.NewLog(100, fixedNow)
	keys := auth.NewService(db, auth.ServiceOptions{Now: fixedNow, OnEvent: func(e core.Event) { evLog.Publish(e) }})
	snap := fixedSnapshot{testSnapshot()}
	h := health.New(snap, health.Options{Confirm: 1, Now: fixedNow, Publish: func(e core.Event) { evLog.Publish(e) },
		Thresholds: health.Thresholds{StorageWarnPct: 50, StorageCriticalPct: 95, MemWarnAvailablePct: 10, MemCriticalAvailablePct: 5,
			CPUWarnCelsius: 85, CPUCriticalCelsius: 95, MemWarnPressurePct: 5, MemCriticalPressurePct: 20, DiskWarnPercentageUsed: 90,
			NetWarnErrorsPerMin: 10, NetCriticalErrorsPerMin: 100}})
	h.Evaluate() // "/" is 52.6% full and backup.service failed: two warnings, two events
	for i := 0; i < 5; i++ {
		evLog.Publish(core.Event{Type: core.EventCollectorDegraded, Severity: core.SeverityInfo, Subject: "collector:test", Message: "test event"})
	}
	stats := middleware.NewStats(fixedNow)
	return env{
		keys:   keys,
		stats:  stats,
		events: evLog,
		d: Deps{
			Logger: slog.New(slog.NewTextHandler(logOut, nil)),
			Verifier: fakeVerifier{
				systemKey:  {Kind: auth.KindAPIKey, KeyID: "systemreader", Scopes: []auth.Scope{auth.ScopeSystemRead}},
				metricsKey: {Kind: auth.KindAPIKey, KeyID: "metricsonly1", Scopes: []auth.Scope{auth.ScopeMetricsRead}},
			},
			Status: fakeStatus{}, SpecJSON: spec, Now: fixedNow,
			Snapshots: snap, Health: h, Events: evLog, Keys: keys, Stats: stats,
			Subscriber: evLog, KeyChecker: keys, StreamHeartbeat: 50 * time.Millisecond,
			Listeners: func() []apiv1.Listener {
				return []apiv1.Listener{{Transport: "unix", Address: "/run/test.sock"}, {Transport: "tcp", Address: "127.0.0.1:7878"}}
			},
		},
	}
}

func router(t *testing.T, d Deps, o Options) http.Handler {
	t.Helper()
	h, err := NewRouter(d, o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type result struct {
	req    *http.Request
	status int
	header http.Header
	body   []byte
}

type reqOpt func(*http.Request)

func bearer(key string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }
}

func jsonBody(s string) reqOpt {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(s))
		r.ContentLength = int64(len(s))
		r.Header.Set("Content-Type", "application/json")
	}
}

func do(t *testing.T, h http.Handler, method, target string, opts ...reqOpt) result {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "127.0.0.1:7878"
	req.RemoteAddr = "127.0.0.1:50000"
	for _, o := range opts {
		o(req)
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// A copy with a fresh body for spec validation.
	check := req.Clone(context.Background())
	check.Body = io.NopCloser(bytes.NewReader(body))
	return result{req: check, status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

var requestIDPattern = regexp.MustCompile(`^req_[0-9A-HJKMNP-TV-Z]{26}$`)

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env apiv1.ErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("not an error envelope: %s", body)
	}
	return env.Error.Code
}

func TestContract(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	tcp := router(t, e.d, Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	panicky := e.d
	panicky.Status = fakeStatus{panics: true}
	broken := router(t, panicky, Options{Listener: ListenerSocket})

	created, err := e.keys.Create(context.Background(), auth.CreateRequest{Name: "to-rotate", Scopes: []auth.Scope{auth.ScopeMetricsRead}})
	if err != nil {
		t.Fatal(err)
	}
	revokable, _ := e.keys.Create(context.Background(), auth.CreateRequest{Name: "to-revoke", Scopes: []auth.Scope{auth.ScopeSystemRead}})

	tests := []struct {
		name     string
		h        http.Handler
		method   string
		path     string
		opts     []reqOpt
		status   int
		wantCode string
	}{
		{"livez", tcp, "GET", "/api/v1/livez", nil, 200, ""},
		{"openapi", tcp, "GET", "/api/v1/openapi.json", nil, 200, ""},
		{"status without key", tcp, "GET", "/api/v1/status", nil, 401, apiv1.CodeAuthenticationRequired},
		{"status unknown key", tcp, "GET", "/api/v1/status", []reqOpt{bearer("ckp_nope_nope")}, 401, apiv1.CodeInvalidCredentials},
		{"status wrong scheme", tcp, "GET", "/api/v1/status", []reqOpt{func(r *http.Request) { r.Header.Set("Authorization", "Basic "+systemKey) }}, 401, apiv1.CodeInvalidCredentials},
		{"status wrong scope", tcp, "GET", "/api/v1/status", []reqOpt{bearer(metricsKey)}, 403, apiv1.CodeInsufficientScope},
		{"status verifier failure", tcp, "GET", "/api/v1/status", []reqOpt{bearer(brokenKey)}, 500, apiv1.CodeInternal},
		{"status tcp", tcp, "GET", "/api/v1/status", []reqOpt{bearer(systemKey)}, 200, ""},
		{"status socket", sock, "GET", "/api/v1/status", nil, 200, ""},
		{"status unknown param", sock, "GET", "/api/v1/status?verbose=1", nil, 400, apiv1.CodeInvalidParameter},
		{"system", tcp, "GET", "/api/v1/system", []reqOpt{bearer(systemKey)}, 200, ""},
		{"hardware", tcp, "GET", "/api/v1/hardware", []reqOpt{bearer(systemKey)}, 200, ""},
		{"hardware needs system scope", tcp, "GET", "/api/v1/hardware", []reqOpt{bearer(metricsKey)}, 403, apiv1.CodeInsufficientScope},
		{"metrics", tcp, "GET", "/api/v1/metrics", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"metrics subset", tcp, "GET", "/api/v1/metrics?sections=cpu,memory", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"metrics gpu", tcp, "GET", "/api/v1/metrics?sections=gpu", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"metrics custom", tcp, "GET", "/api/v1/metrics?sections=custom", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"containers", sock, "GET", "/api/v1/containers", nil, 200, ""},
		{"metrics power and ups", tcp, "GET", "/api/v1/metrics?sections=power,ups", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"containers need processes scope", tcp, "GET", "/api/v1/containers", []reqOpt{bearer(metricsKey)}, 403, apiv1.CodeInsufficientScope},
		{"metrics bad section", tcp, "GET", "/api/v1/metrics?sections=cpu,tpu", []reqOpt{bearer(metricsKey)}, 400, apiv1.CodeInvalidParameter},
		{"metrics needs metrics scope", tcp, "GET", "/api/v1/metrics", []reqOpt{bearer(systemKey)}, 403, apiv1.CodeInsufficientScope},
		{"storage", tcp, "GET", "/api/v1/storage", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"services", tcp, "GET", "/api/v1/services", []reqOpt{bearer(systemKey)}, 200, ""},
		{"services failed", tcp, "GET", "/api/v1/services?state=failed", []reqOpt{bearer(systemKey)}, 200, ""},
		{"services bad state", tcp, "GET", "/api/v1/services?state=sleepy", []reqOpt{bearer(systemKey)}, 400, apiv1.CodeInvalidParameter},
		{"services needs system scope", tcp, "GET", "/api/v1/services", []reqOpt{bearer(metricsKey)}, 403, apiv1.CodeInsufficientScope},
		{"network", tcp, "GET", "/api/v1/network", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"sensors", tcp, "GET", "/api/v1/sensors", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"health", tcp, "GET", "/api/v1/health", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"events", tcp, "GET", "/api/v1/events?limit=2", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"events filtered", tcp, "GET", "/api/v1/events?severity=warning&since=2026-01-01T00:00:00Z", []reqOpt{bearer(metricsKey)}, 200, ""},
		{"events bad limit", tcp, "GET", "/api/v1/events?limit=0", []reqOpt{bearer(metricsKey)}, 400, apiv1.CodeInvalidParameter},
		{"events bad since", tcp, "GET", "/api/v1/events?since=yesterday", []reqOpt{bearer(metricsKey)}, 400, apiv1.CodeInvalidParameter},
		{"events bad cursor", tcp, "GET", "/api/v1/events?cursor=zzz", []reqOpt{bearer(metricsKey)}, 400, apiv1.CodeInvalidCursor},
		{"events repeated param", tcp, "GET", "/api/v1/events?limit=1&limit=2", []reqOpt{bearer(metricsKey)}, 400, apiv1.CodeInvalidParameter},
		{"rebinding host", tcp, "GET", "/api/v1/livez", []reqOpt{func(r *http.Request) { r.Host = "attacker.example:7878" }}, 400, apiv1.CodeInvalidHost},
		{"internal error", broken, "GET", "/api/v1/status", nil, 500, apiv1.CodeInternal},
		{"list keys", sock, "GET", "/api/v1/keys", nil, 200, ""},
		{"create key", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":"grafana","scopes":["metrics:read"]}`)}, 201, ""},
		{"create key with expiry", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":"temp","scopes":["system:read"],"expires_at":"2027-01-01T00:00:00Z"}`)}, 201, ""},
		{"create admin key refused", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":"x","scopes":["admin"]}`)}, 400, apiv1.CodeInvalidRequest},
		{"create key bad json", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":`)}, 400, apiv1.CodeInvalidRequest},
		{"create key unknown field", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":"x","scopes":["system:read"],"admin":true}`)}, 400, apiv1.CodeInvalidRequest},
		{"create key too large", sock, "POST", "/api/v1/keys", []reqOpt{jsonBody(`{"name":"` + strings.Repeat("x", 70<<10) + `","scopes":["system:read"]}`)}, 413, apiv1.CodeRequestTooLarge},
		{"rotate key", sock, "POST", "/api/v1/keys/" + created.Meta.ID + "/rotate", nil, 201, ""},
		{"rotate unknown key", sock, "POST", "/api/v1/keys/aaaaaaaaaaaa/rotate", nil, 404, apiv1.CodeNotFound},
		{"rotate revoked key", sock, "POST", "/api/v1/keys/" + created.Meta.ID + "/rotate", nil, 400, apiv1.CodeInvalidRequest},
		{"remove active key refused", sock, "POST", "/api/v1/keys/" + revokable.Meta.ID + "/remove", nil, 400, apiv1.CodeInvalidRequest},
		{"revoke key", sock, "DELETE", "/api/v1/keys/" + revokable.Meta.ID, nil, 200, ""},
		{"revoke key again", sock, "DELETE", "/api/v1/keys/" + revokable.Meta.ID, nil, 200, ""},
		{"revoke unknown key", sock, "DELETE", "/api/v1/keys/aaaaaaaaaaaa", nil, 404, apiv1.CodeNotFound},
		{"remove revoked key", sock, "POST", "/api/v1/keys/" + revokable.Meta.ID + "/remove", nil, 204, ""},
		{"remove removed key", sock, "POST", "/api/v1/keys/" + revokable.Meta.ID + "/remove", nil, 404, apiv1.CodeNotFound},
		{"remove over TCP", tcp, "POST", "/api/v1/keys/" + created.Meta.ID + "/remove", []reqOpt{bearer(systemKey)}, 404, apiv1.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, tt.h, tt.method, tt.path, tt.opts...)
			if r.status != tt.status {
				t.Fatalf("status %d, want %d: %s", r.status, tt.status, r.body)
			}
			spec.CheckResponse(t, r.req, r.status, r.header, r.body)
			if r.status < 400 && len(tt.opts) > 0 && tt.method == "POST" {
				spec.CheckRequest(t, r.req)
			}
			if id := r.header.Get(apiv1.RequestIDHeader); !requestIDPattern.MatchString(id) {
				t.Errorf("bad request id %q", id)
			}
			if tt.wantCode != "" && errorCode(t, r.body) != tt.wantCode {
				t.Errorf("code %s, want %s", errorCode(t, r.body), tt.wantCode)
			}
			if r.status == 500 && (bytes.Contains(r.body, []byte("exploded")) || bytes.Contains(r.body, []byte("EOF"))) {
				t.Error("internal detail leaked to client")
			}
		})
	}
}

func TestSensitiveFieldsRedacted(t *testing.T) {
	e := newEnv(t, nil)
	tcp := router(t, e.d, Options{Listener: ListenerTCP})
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	for _, h := range []http.Handler{tcp, sock} {
		for _, path := range []string{"/api/v1/hardware", "/api/v1/network", "/api/v1/metrics", "/api/v1/storage"} {
			key := metricsKey
			if path == "/api/v1/hardware" {
				key = systemKey
			}
			r := do(t, h, "GET", path, bearer(key))
			if r.status != 200 {
				t.Fatalf("%s: %d", path, r.status)
			}
			for _, secret := range []string{testMAC, testSerial, testWWN} {
				if bytes.Contains(r.body, []byte(secret)) {
					t.Errorf("%s leaked %s", path, secret)
				}
			}
		}
	}
	var hw apiv1.HardwareResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/hardware").body, &hw)
	dev := (*hw.BlockDevices.Data)[0]
	if strings.Join(dev.Redacted, ",") != "serial,wwn" {
		t.Errorf("block device redacted %v", dev.Redacted)
	}
	nics := *hw.NetworkInterfaces.Data
	if strings.Join(nics[0].Redacted, ",") != "mac_address" || nics[1].Redacted != nil {
		t.Errorf("nic redacted %v / %v", nics[0].Redacted, nics[1].Redacted)
	}
}

func TestResourceContent(t *testing.T) {
	e := newEnv(t, nil)
	sock := router(t, e.d, Options{Listener: ListenerSocket})

	var sys apiv1.SystemResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/system").body, &sys)
	if sys.System.Data.UptimeSeconds == nil || *sys.System.Data.UptimeSeconds != 3600 || sys.System.Status != "available" || sys.System.Stale {
		t.Errorf("system %+v", sys.System)
	}

	var m apiv1.MetricsResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/metrics?sections=cpu,memory_pressure,filesystems").body, &m)
	if m.Memory != nil || m.CPU == nil || *m.CPU.Data.UsagePercent != 12.5 || m.CPU.Data.LoadAverage.Five != 0.4 {
		t.Errorf("metrics selection %+v", m)
	}
	if m.MemoryPressure.Status != "unsupported" || m.MemoryPressure.Data != nil || m.MemoryPressure.Reason == "" || m.MemoryPressure.CollectedAt != nil {
		t.Errorf("unsupported section %+v", m.MemoryPressure)
	}
	root := (*m.Filesystems.Data)[0]
	if root.UsedBytes != 500 || int(root.UsedPercent*10) != 526 || root.InodesUsedPercent == nil || *root.InodesUsedPercent != 60 {
		t.Errorf("filesystem %+v", root)
	}
	if (*m.Filesystems.Data)[1].InodesUsedPercent != nil {
		t.Error("filesystem without inodes must have null inode usage")
	}

	var hl apiv1.HealthResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/health").body, &hl)
	statuses := map[string]string{}
	for _, c := range hl.Checks {
		statuses[c.ID] = c.Status + "/" + c.Availability
	}
	if hl.Status != "warning" || statuses["storage_capacity"] != "warning/available" || statuses["cpu_temperature"] != "unknown/unsupported" ||
		statuses["disk_health"] != "unknown/not_configured" {
		t.Errorf("health %s %v", hl.Status, statuses)
	}
}

func TestEventsPagination(t *testing.T) {
	e := newEnv(t, nil)
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	var all []string
	cursor := ""
	for i := 0; i < 10; i++ {
		target := "/api/v1/events?limit=2"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		var page apiv1.EventsResponse
		_ = json.Unmarshal(do(t, sock, "GET", target).body, &page)
		for _, ev := range page.Items {
			all = append(all, ev.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(all) != 7 { // 2 health findings + 5 test events
		t.Errorf("paged %d events", len(all))
	}
}

// Keys created over the socket work over TCP until revoked; the key is
// shown once and never again.
func TestKeyLifecycleEndToEnd(t *testing.T) {
	e := newEnv(t, nil)
	e.d.Verifier = e.keys
	tcp := router(t, e.d, Options{Listener: ListenerTCP})
	sock := router(t, e.d, Options{Listener: ListenerSocket})

	r := do(t, sock, "POST", "/api/v1/keys", jsonBody(`{"name":"script","scopes":["metrics:read"]}`))
	var created apiv1.CreatedAPIKey
	if err := json.Unmarshal(r.body, &created); err != nil || r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if !strings.HasPrefix(created.APIKey.Display, "ckp_"+created.APIKey.ID+"_") || strings.Contains(created.APIKey.Display, created.Key[20:]) {
		t.Errorf("display %q", created.APIKey.Display)
	}

	if r := do(t, tcp, "GET", "/api/v1/metrics", bearer(created.Key)); r.status != 200 {
		t.Fatalf("new key rejected: %d", r.status)
	}
	if r := do(t, tcp, "GET", "/api/v1/system", bearer(created.Key)); r.status != 403 {
		t.Errorf("scope not enforced: %d", r.status)
	}
	list := do(t, sock, "GET", "/api/v1/keys")
	if bytes.Contains(list.body, []byte(created.Key)) || bytes.Contains(list.body, []byte(created.Key[len("ckp_")+13:])) {
		t.Fatal("key listing contains the key")
	}

	// Rotation: old key stops working immediately, new key works.
	r = do(t, sock, "POST", "/api/v1/keys/"+created.APIKey.ID+"/rotate")
	var rotated apiv1.CreatedAPIKey
	_ = json.Unmarshal(r.body, &rotated)
	if do(t, tcp, "GET", "/api/v1/metrics", bearer(created.Key)).status != 401 {
		t.Error("rotated-out key still works")
	}
	if do(t, tcp, "GET", "/api/v1/metrics", bearer(rotated.Key)).status != 200 {
		t.Error("rotated key does not work")
	}
	if rotated.APIKey.RotatedFrom == nil || *rotated.APIKey.RotatedFrom != created.APIKey.ID {
		t.Errorf("rotated_from %v", rotated.APIKey.RotatedFrom)
	}

	if r := do(t, sock, "DELETE", "/api/v1/keys/"+rotated.APIKey.ID); r.status != 200 {
		t.Fatalf("revoke %d", r.status)
	}
	if do(t, tcp, "GET", "/api/v1/metrics", bearer(rotated.Key)).status != 401 {
		t.Error("revoked key still works")
	}

	// Key management is not reachable over TCP, even with a valid key.
	for _, m := range []string{"GET", "POST"} {
		if r := do(t, tcp, m, "/api/v1/keys", bearer(systemKey)); r.status != 404 && r.status != 405 {
			t.Errorf("%s /keys over TCP: %d", m, r.status)
		}
	}
}

func TestAdminStatusOnlyOnSocket(t *testing.T) {
	e := newEnv(t, nil)
	tcp := router(t, e.d, Options{Listener: ListenerTCP})
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	do(t, tcp, "GET", "/api/v1/livez")

	var viaTCP, viaSock apiv1.Status
	_ = json.Unmarshal(do(t, tcp, "GET", "/api/v1/status", bearer(systemKey)).body, &viaTCP)
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/status").body, &viaSock)
	if viaTCP.API != nil {
		t.Error("admin fields exposed to an API key")
	}
	if viaSock.API == nil || len(viaSock.API.Listeners) != 2 || viaSock.API.Requests.Total < 2 || viaSock.API.ActiveClients != 1 {
		t.Errorf("admin status %+v", viaSock.API)
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t, nil)
	limits := middleware.NewLimits(middleware.LimitConfig{PerKeyRPS: 0.001, PerKeyBurst: 2, GlobalRPS: 1000, GlobalBurst: 1000})
	tcp := router(t, e.d, Options{Listener: ListenerTCP, Limits: limits})
	spec := contracttest.Load(t)

	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, do(t, tcp, "GET", "/api/v1/status", bearer(systemKey)).status)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Errorf("per-key limit: %v", codes)
	}
	// Another key is unaffected.
	if r := do(t, tcp, "GET", "/api/v1/metrics", bearer(metricsKey)); r.status != 200 {
		t.Errorf("other key %d", r.status)
	}

	// Failed authentication is limited per client IP.
	var last result
	for i := 0; i < 15; i++ {
		last = do(t, tcp, "GET", "/api/v1/status", bearer("ckp_wrongwrongwr_"+strings.Repeat("D", 43)))
	}
	if last.status != 429 || last.header.Get("Retry-After") == "" {
		t.Fatalf("auth failures not limited: %d", last.status)
	}
	spec.CheckResponse(t, last.req, last.status, last.header, last.body)
	// A valid key from the same IP is never locked out by others' failures.
	if r := do(t, tcp, "GET", "/api/v1/metrics", bearer(metricsKey)); r.status != 200 {
		t.Errorf("valid key locked out by failed attempts: %d", r.status)
	}
	// A different client IP is unaffected.
	r := do(t, tcp, "GET", "/api/v1/status", func(r *http.Request) { r.RemoteAddr = "127.0.0.2:1" })
	if r.status != 401 {
		t.Errorf("other IP %d", r.status)
	}

	global := middleware.NewLimits(middleware.LimitConfig{PerKeyRPS: 1000, PerKeyBurst: 1000, GlobalRPS: 0.001, GlobalBurst: 1})
	g := router(t, e.d, Options{Listener: ListenerTCP, Limits: global})
	do(t, g, "GET", "/api/v1/livez")
	if r := do(t, g, "GET", "/api/v1/livez"); r.status != 429 {
		t.Errorf("global limit: %d", r.status)
	}

	// The socket is never rate limited.
	sock := router(t, e.d, Options{Listener: ListenerSocket, Limits: global})
	for i := 0; i < 5; i++ {
		if r := do(t, sock, "GET", "/api/v1/status"); r.status != 200 {
			t.Fatalf("socket limited: %d", r.status)
		}
	}
}

// Every route must be in the spec with the same scope and socket-only flag,
// and vice versa.
func TestRoutesMatchSpec(t *testing.T) {
	spec := contracttest.Load(t)
	var fromTable []contracttest.Operation
	for _, rt := range Routes(newEnv(t, nil).d) {
		scope := string(rt.Scope)
		if rt.Public {
			scope = "public"
		}
		fromTable = append(fromTable, contracttest.Operation{Method: rt.Method, Path: BasePath + rt.Path, Scope: scope, SocketOnly: rt.SocketOnly})
	}
	sort.Slice(fromTable, func(i, j int) bool {
		if fromTable[i].Path != fromTable[j].Path {
			return fromTable[i].Path < fromTable[j].Path
		}
		return fromTable[i].Method < fromTable[j].Method
	})
	fromSpec := spec.Operations()
	if len(fromSpec) != len(fromTable) {
		t.Fatalf("spec has %d operations, route table has %d:\nspec:  %v\ntable: %v", len(fromSpec), len(fromTable), fromSpec, fromTable)
	}
	for i := range fromSpec {
		if fromSpec[i] != fromTable[i] {
			t.Errorf("mismatch: spec %+v, table %+v", fromSpec[i], fromTable[i])
		}
	}
}

// Walk the routers actually built: nothing is registered outside the
// table, every non-public route rejects anonymous TCP requests, and
// socket-only routes are absent from TCP.
func TestEveryRegisteredRouteIsDeclared(t *testing.T) {
	d := newEnv(t, nil).d
	declared := map[string]Route{}
	for _, rt := range Routes(d) {
		declared[rt.Method+" "+BasePath+rt.Path] = rt
	}
	for _, l := range []Listener{ListenerTCP, ListenerSocket} {
		h := router(t, d, Options{Listener: l})
		walked := 0
		err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			walked++
			rt, ok := declared[method+" "+route]
			if !ok {
				t.Errorf("%s: route %s %s is registered but not declared", l, method, route)
				return nil
			}
			if rt.SocketOnly && l == ListenerTCP {
				t.Errorf("socket-only route %s %s mounted on TCP", method, route)
			}
			if l == ListenerTCP && !rt.Public {
				target := strings.ReplaceAll(route, "{id}", "aaaaaaaaaaaa")
				if r := do(t, h, method, target); r.status != http.StatusUnauthorized {
					t.Errorf("%s %s answered %d without a key", method, route, r.status)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, rt := range declared {
			if !rt.SocketOnly || l == ListenerSocket {
				want++
			}
		}
		if walked != want {
			t.Errorf("%s: walked %d routes, want %d", l, walked, want)
		}
	}
}

func TestInvalidRouteDeclarations(t *testing.T) {
	ok := func(http.ResponseWriter, *http.Request) {}
	for name, rt := range map[string]Route{
		"no scope":      {Method: "GET", Path: "/x", Handler: ok},
		"public+scope":  {Method: "GET", Path: "/x", Public: true, Scope: auth.ScopeAdmin, Handler: ok},
		"unknown scope": {Method: "GET", Path: "/x", Scope: "x:write", Handler: ok},
		"no handler":    {Method: "GET", Path: "/x", Public: true},
	} {
		if _, err := newRouter(newEnv(t, nil).d, Options{Listener: ListenerTCP}, []Route{rt}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNoCORS(t *testing.T) {
	h := router(t, newEnv(t, nil).d, Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		r := do(t, h, method, "/api/v1/livez", func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set("Access-Control-Request-Method", "GET")
		})
		for k := range r.header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s present", method, k)
			}
		}
	}
}

func TestNotFoundAndMethodEnvelope(t *testing.T) {
	h := router(t, newEnv(t, nil).d, Options{Listener: ListenerTCP})
	for target, want := range map[string]int{"/api/v1/nope": 404, "/": 404} {
		r := do(t, h, http.MethodGet, target)
		if r.status != want || !bytes.Contains(r.body, []byte(`"request_id"`)) {
			t.Errorf("%s: %d %s", target, r.status, r.body)
		}
	}
	if r := do(t, h, http.MethodPost, "/api/v1/livez"); r.status != 405 {
		t.Errorf("POST livez: %d", r.status)
	}
}

func TestIncomingRequestIDIgnored(t *testing.T) {
	h := router(t, newEnv(t, nil).d, Options{Listener: ListenerTCP})
	r := do(t, h, http.MethodGet, "/api/v1/livez", func(r *http.Request) { r.Header.Set("X-Request-ID", "evil\nline") })
	if got := r.header.Get(apiv1.RequestIDHeader); got == "evil\nline" || !requestIDPattern.MatchString(got) {
		t.Errorf("request id %q", got)
	}
}

// The access log must never contain credentials or key material, even
// with a plain handler that has no redaction.
func TestAccessLogHasNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	e := newEnv(t, &buf)
	e.d.Verifier = e.keys
	tcp := router(t, e.d, Options{Listener: ListenerTCP})
	sock := router(t, e.d, Options{Listener: ListenerSocket})

	r := do(t, sock, "POST", "/api/v1/keys", jsonBody(`{"name":"logcheck","scopes":["system:read"]}`))
	var created apiv1.CreatedAPIKey
	_ = json.Unmarshal(r.body, &created)
	do(t, tcp, "GET", "/api/v1/status?token=secretquery", bearer(created.Key))
	do(t, tcp, "GET", "/api/v1/status", bearer("ckp_unknownkeyx_"+strings.Repeat("C", 43)))

	out := buf.String()
	for _, leak := range []string{created.Key, created.Key[len("ckp_")+13:], "CCCCCCCC", "secretquery", "Bearer", "Authorization", "logcheck\",\"scopes"} {
		if strings.Contains(out, leak) {
			t.Errorf("access log leaked %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"principal=key:" + created.APIKey.ID, "route=/api/v1/status", "status=400", "status=401", "status=201", "principal=local-admin", "listener=socket"} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q:\n%s", want, out)
		}
	}
}

func TestServicesFilterAndCounts(t *testing.T) {
	sock := router(t, newEnv(t, nil).d, Options{Listener: ListenerSocket})
	var r apiv1.ServicesResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/services?state=failed").body, &r)
	d := r.Services.Data
	if d.Total != 2 || d.Failed != 1 || d.Active != 1 || len(d.Items) != 1 || d.Items[0].Name != "backup.service" {
		t.Errorf("%+v", d)
	}
}

func TestHardwareDetails(t *testing.T) {
	sock := router(t, newEnv(t, nil).d, Options{Listener: ListenerSocket})
	var hw apiv1.HardwareResponse
	_ = json.Unmarshal(do(t, sock, "GET", "/api/v1/hardware").body, &hw)
	if c := hw.CPU.Data; c.Cores != 6 || c.LogicalCores != 12 || c.Virtualization != "AMD-V" || len(c.Caches) != 1 {
		t.Errorf("cpu %+v", c)
	}
	if hw.MemoryModules.Status != "permission_denied" || hw.MemoryModules.Reason == "" || hw.MemoryModules.Data != nil {
		t.Errorf("memory modules %+v", hw.MemoryModules)
	}
	if b := hw.Board.Data; b.ProductName != "Box" || strings.Join(b.Redacted, ",") != "serial" {
		t.Errorf("board %+v", b)
	}
	if g := (*hw.GPUs.Data)[0]; g.Name != "RTX 3060" || strings.Join(g.Redacted, ",") != "uuid" || g.UUID != "" {
		t.Errorf("gpu %+v", g)
	}
	if u := (*hw.USBDevices.Data)[0]; strings.Join(u.Redacted, ",") != "serial" {
		t.Errorf("usb %+v", u)
	}
}

func TestCustomMetricsContent(t *testing.T) {
	e := newEnv(t, nil)
	r := do(t, router(t, e.d, Options{Listener: ListenerSocket}), "GET", "/api/v1/metrics?sections=custom")
	var out apiv1.MetricsResponse
	if err := json.Unmarshal(r.body, &out); err != nil || out.Custom == nil || out.CPU != nil {
		t.Fatalf("%v %s", err, r.body)
	}
	sets := *out.Custom
	if len(sets) != 2 || sets[0].Collector != "custom.demo" || sets[0].Status != "available" || sets[0].DroppedSeries != 1 {
		t.Fatalf("sets %+v", sets)
	}
	ms := *sets[0].Data
	if ms[0].Labels == nil || ms[0].RatePerSecond != nil || ms[0].RateUnit != "" {
		t.Errorf("gauge %+v", ms[0])
	}
	if ms[1].Kind != "counter" || *ms[1].RatePerSecond != 30 || ms[1].RateUnit != "bytes_per_second" || ms[1].Labels["dev"] != "a" {
		t.Errorf("counter %+v", ms[1])
	}
	if sets[1].Data != nil || sets[1].Status != "permission_denied" || sets[1].Reason == "" {
		t.Errorf("unavailable set %+v", sets[1])
	}
}

func TestContainersContent(t *testing.T) {
	e := newEnv(t, nil)
	r := do(t, router(t, e.d, Options{Listener: ListenerSocket}), "GET", "/api/v1/containers")
	var out apiv1.ContainersResponse
	if err := json.Unmarshal(r.body, &out); err != nil || out.Containers.Data == nil {
		t.Fatalf("%v %s", err, r.body)
	}
	l := out.Containers.Data
	if l.Total != 2 || l.Items[0].Runtime != "podman" || *l.Items[0].CPUPercent != 12.5 || *l.Items[0].UID != 1000 || !l.Items[0].Rootless {
		t.Errorf("busiest first, rootless owner: %+v", l.Items[0])
	}
	if l.Items[1].UID != nil || *l.Items[1].MemoryBytes != 1<<20 || l.Items[1].CPUPercent != nil || l.NamesReason == "" {
		t.Errorf("second %+v", l.Items[1])
	}
}

var networkKey = "ckp" + "_networkread1_" + strings.Repeat("E", 43)

// D27: addresses, routes and DNS need network:read.
func TestNetworkScope(t *testing.T) {
	spec := contracttest.Load(t)
	e := newEnv(t, nil)
	e.d.Verifier.(fakeVerifier)[networkKey] = auth.Principal{Kind: auth.KindAPIKey, KeyID: "networkread1", Scopes: []auth.Scope{auth.ScopeMetricsRead, auth.ScopeNetworkRead}}
	tcp := router(t, e.d, Options{Listener: ListenerTCP, CheckLoopbackHost: true})
	get := func(key, target string) apiv1.NetworkResponse {
		t.Helper()
		r := do(t, tcp, "GET", target, bearer(key))
		if r.status != 200 {
			t.Fatalf("%s: %d %s", target, r.status, r.body)
		}
		spec.CheckResponse(t, r.req, r.status, r.header, r.body)
		var out apiv1.NetworkResponse
		_ = json.Unmarshal(r.body, &out)
		return out
	}
	without := get(metricsKey, "/api/v1/network")
	eth0 := (*without.Interfaces.Data)[0]
	if len(eth0.Addresses) != 0 || !slices.Contains(eth0.Redacted, "addresses") || eth0.RxBytes != 10 {
		t.Errorf("metrics-only interface %+v", eth0)
	}
	if without.Routes.Status != "permission_denied" || without.Routes.Data != nil || !strings.Contains(without.Routes.Reason, "network:read") ||
		without.DNS.Status != "permission_denied" || without.DNS.Data != nil {
		t.Errorf("routes %+v dns %+v", without.Routes, without.DNS)
	}
	with := get(networkKey, "/api/v1/network")
	if a := (*with.Interfaces.Data)[0].Addresses; len(a) != 1 || a[0] != "192.0.2.10/24" || with.Routes.Data == nil || with.DNS.Data == nil {
		t.Errorf("network key: %+v", with)
	}
	r := do(t, tcp, "GET", "/api/v1/metrics?sections=network", bearer(metricsKey))
	if strings.Contains(string(r.body), "192.0.2.10") {
		t.Error("/metrics leaks addresses without network:read")
	}
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	if r := do(t, sock, "GET", "/api/v1/network"); !strings.Contains(string(r.body), "192.0.2.53") {
		t.Error("socket must see DNS")
	}
}

// Browser access is opt-in per exact origin (D34).
func TestCORS(t *testing.T) {
	e := newEnv(t, io.Discard)
	tcp := router(t, e.d, Options{Listener: ListenerTCP, CheckLoopbackHost: true, CORSOrigins: []string{"https://app.example"}})
	sock := router(t, e.d, Options{Listener: ListenerSocket})
	origin := func(o string) reqOpt { return func(r *http.Request) { r.Header.Set("Origin", o) } }
	preflight := func(r *http.Request) {
		r.Header.Set("Access-Control-Request-Method", "GET")
		r.Header.Set("Access-Control-Request-Headers", "authorization")
	}

	r := do(t, tcp, "OPTIONS", "/api/v1/health", origin("https://app.example"), preflight)
	if r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		r.header.Get("Access-Control-Allow-Methods") != "GET" || !strings.Contains(r.header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("allowed preflight: %d %v", r.status, r.header)
	}
	if r.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials must never be allowed")
	}

	r = do(t, tcp, "OPTIONS", "/api/v1/health", origin("https://evil.example"), preflight)
	if r.status != 403 || errorCode(t, r.body) != apiv1.CodeOriginNotAllowed || r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("other origin preflight: %d %s", r.status, r.body)
	}

	// Real requests still need a key; the CORS headers let the page read
	// the answer, including errors.
	r = do(t, tcp, "GET", "/api/v1/health", origin("https://app.example"))
	if r.status != 401 || r.header.Get("Access-Control-Allow-Origin") != "https://app.example" || !strings.Contains(r.header.Get("Vary"), "Origin") {
		t.Errorf("allowed origin without key: %d %v", r.status, r.header)
	}
	r = do(t, tcp, "GET", "/api/v1/status", origin("https://app.example"), bearer(systemKey))
	if r.status != 200 || r.header.Get("Access-Control-Allow-Origin") != "https://app.example" || !strings.Contains(r.header.Get("Access-Control-Expose-Headers"), "X-Request-ID") {
		t.Errorf("allowed origin with key: %d %v", r.status, r.header)
	}
	r = do(t, tcp, "GET", "/api/v1/status", origin("https://evil.example"), bearer(systemKey))
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("headers sent to an unlisted origin: %v", r.header)
	}

	// The admin socket never answers browsers, even for a listed origin.
	r = do(t, sock, "OPTIONS", "/api/v1/health", origin("https://app.example"), preflight)
	if r.header.Get("Access-Control-Allow-Origin") != "" || r.status == 204 {
		t.Errorf("socket answered a preflight: %d %v", r.status, r.header)
	}
}
