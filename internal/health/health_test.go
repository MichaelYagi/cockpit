package health

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type src struct {
	mu sync.Mutex
	s  *core.Snapshot
}

func (s *src) Current() *core.Snapshot { s.mu.Lock(); defer s.mu.Unlock(); return s.s }
func (s *src) set(snap *core.Snapshot) { s.mu.Lock(); s.s = snap; s.mu.Unlock() }

func ok(at time.Time) core.SectionMeta {
	return core.SectionMeta{Status: core.Available, CollectedAt: at, StaleAfter: at.Add(time.Hour)}
}

func ptr[T any](v T) *T { return &v }

// healthy returns a snapshot where every check passes.
func healthy() *core.Snapshot {
	return &core.Snapshot{
		Sensors: core.Section[core.Sensors]{Meta: ok(t0), Data: &core.Sensors{
			{Kind: core.SensorTemperature, Chip: "coretemp", Label: "Package id 0", Value: 50, CPU: true, High: ptr(80.0), Critical: ptr(100.0)},
			{Kind: core.SensorTemperature, Chip: "acpitz", Label: "temp1", Value: 99}, // not a CPU sensor
		}},
		Memory:         core.Section[core.MemoryMetrics]{Meta: ok(t0), Data: &core.MemoryMetrics{UsedPercent: 40}},
		MemoryPressure: core.Section[core.MemoryPressure]{Meta: ok(t0), Data: &core.MemoryPressure{FullAvg60: 0.5}},
		Filesystems: core.Section[core.Filesystems]{Meta: ok(t0), Data: &core.Filesystems{
			{MountPoint: "/", Type: "ext4", TotalBytes: 1000, FreeBytes: 500, AvailableBytes: 450, TotalInodes: 100, FreeInodes: 90},
		}},
		DiskHealth: core.Section[core.DiskHealthList]{Meta: ok(t0), Data: &core.DiskHealthList{
			{Device: "nvme0n1", Protocol: "NVMe", Passed: ptr(true), PercentageUsed: ptr(3), CriticalWarning: ptr(0), MediaErrors: ptr(uint64(0))},
		}},
		Services: core.Section[core.Services]{Meta: ok(t0), Data: &core.Services{
			{Name: "ssh.service", ActiveState: "active", SubState: "running"},
		}},
		Network: core.Section[[]core.NetInterfaceMetrics]{Meta: ok(t0), Data: &[]core.NetInterfaceMetrics{
			{NetInterface: core.NetInterface{Name: "eth0", RxErrors: 5}},
			{NetInterface: core.NetInterface{Name: "lo", Loopback: true, RxErrors: 999}},
		}},
		ZFS: core.Section[core.ZFSInfo]{Meta: ok(t0), Data: &core.ZFSInfo{Pools: []core.ZFSPool{{Name: "tank", State: "ONLINE"}}}},
		UPS: core.Section[core.UPSList]{Meta: ok(t0), Data: &core.UPSList{{Name: "rack", Status: []string{"OL"}}}},
	}
}

var defaultThresholds = Thresholds{
	CPUWarnCelsius: 85, CPUCriticalCelsius: 95,
	MemWarnAvailablePct: 10, MemCriticalAvailablePct: 5,
	MemWarnPressurePct: 5, MemCriticalPressurePct: 20,
	StorageWarnPct: 85, StorageCriticalPct: 95,
	DiskWarnPercentageUsed: 90,
	NetWarnErrorsPerMin:    10, NetCriticalErrorsPerMin: 100,
}

func newEval(t *testing.T, s *core.Snapshot, confirm int) (*Evaluator, *src, *[]core.Event) {
	t.Helper()
	source := &src{s: s}
	var evs []core.Event
	e := New(source, Options{Thresholds: defaultThresholds, Confirm: confirm, Now: func() time.Time { return t0 },
		Publish: func(ev core.Event) { evs = append(evs, ev) }})
	return e, source, &evs
}

func check(t *testing.T, r *Report, id string) CheckResult {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return CheckResult{}
}

func TestAllHealthy(t *testing.T) {
	e, _, evs := newEval(t, healthy(), 1)
	r := e.Evaluate()
	if r.Status != StatusOK || len(r.Checks) != 8 {
		t.Fatalf("report %+v", r)
	}
	for _, c := range r.Checks {
		if c.Status != StatusOK || len(c.Findings) != 0 || c.Summary == "" {
			t.Errorf("%s: %+v", c.ID, c)
		}
	}
	if len(*evs) != 0 {
		t.Errorf("events on a healthy system: %v", *evs)
	}
}

// WSL/VM-like host: no sensors, no smartctl. Unknown checks carry the
// collector's availability and reason; the others still evaluate.
func TestUnavailableDataIsUnknownNotError(t *testing.T) {
	s := healthy()
	s.Sensors = core.Section[core.Sensors]{Meta: core.SectionMeta{Status: core.Unsupported, Reason: "No hardware temperature sensors are exposed."}}
	s.DiskHealth = core.Section[core.DiskHealthList]{Meta: core.SectionMeta{Status: core.NotConfigured, Reason: "smartctl is not installed."}}
	e, _, _ := newEval(t, s, 1)
	r := e.Evaluate()
	cpu := check(t, r, CheckCPUTemperature)
	if cpu.Status != StatusUnknown || cpu.Availability != core.Unsupported || !strings.Contains(cpu.Reason, "sensors") {
		t.Errorf("cpu %+v", cpu)
	}
	disk := check(t, r, CheckDiskHealth)
	if disk.Status != StatusUnknown || disk.Availability != core.NotConfigured {
		t.Errorf("disk %+v", disk)
	}
	if r.Status != StatusOK {
		t.Errorf("overall should reflect the checks that ran: %s", r.Status)
	}
}

func TestNoCPUSensorAmongTemperatures(t *testing.T) {
	s := healthy()
	(*s.Sensors.Data)[0].CPU = false
	e, _, _ := newEval(t, s, 1)
	if c := check(t, e.Evaluate(), CheckCPUTemperature); c.Status != StatusUnknown || c.Availability != core.Unsupported {
		t.Errorf("%+v", c)
	}
}

func TestOverallUnknownWhenNothingEvaluates(t *testing.T) {
	e, _, _ := newEval(t, &core.Snapshot{}, 1)
	if r := e.Evaluate(); r.Status != StatusUnknown {
		t.Errorf("status %s", r.Status)
	}
	if New(&src{s: &core.Snapshot{}}, Options{}).Current().Status != StatusUnknown {
		t.Error("initial report must be unknown")
	}
}

func TestCPUTemperatureHardwareThresholdsAndHysteresis(t *testing.T) {
	s := healthy()
	e, source, evs := newEval(t, s, 2)
	set := func(v float64) {
		n := healthy()
		(*n.Sensors.Data)[0].Value = v
		source.set(n)
	}

	set(85) // above hardware high (80), below critical (100)
	if c := check(t, e.Evaluate(), CheckCPUTemperature); c.Status != StatusOK {
		t.Fatalf("raised after one evaluation: %s", c.Status)
	}
	c := check(t, e.Evaluate(), CheckCPUTemperature)
	if c.Status != StatusWarning || len(c.Findings) != 1 {
		t.Fatalf("not raised after confirm: %+v", c)
	}
	f := c.Findings[0]
	if f.Evidence["threshold_source"] != "hardware" || f.Evidence["warn_celsius"] != "80.0" || !f.Since.Equal(t0) || f.ID == "" || f.Recommendation == "" {
		t.Errorf("finding %+v", f)
	}
	if len(*evs) != 1 || (*evs)[0].Type != core.EventFindingRaised || (*evs)[0].Severity != core.SeverityWarning {
		t.Errorf("events %+v", *evs)
	}

	set(50)
	if check(t, e.Evaluate(), CheckCPUTemperature).Status != StatusWarning {
		t.Error("cleared after one ok evaluation")
	}
	if check(t, e.Evaluate(), CheckCPUTemperature).Status != StatusOK {
		t.Error("not cleared after confirm")
	}
	if len(*evs) != 2 || (*evs)[1].Type != core.EventFindingResolved {
		t.Errorf("events %+v", *evs)
	}
}

func TestFlappingValueNeverRaises(t *testing.T) {
	e, source, evs := newEval(t, healthy(), 2)
	for i := 0; i < 10; i++ {
		n := healthy()
		if i%2 == 0 {
			(*n.Sensors.Data)[0].Value = 90
		}
		source.set(n)
		e.Evaluate()
	}
	if len(*evs) != 0 {
		t.Errorf("flapping produced events: %v", *evs)
	}
}

func TestCPUConfiguredThresholds(t *testing.T) {
	s := healthy()
	(*s.Sensors.Data)[0] = core.Sensor{Kind: core.SensorTemperature, Chip: "k10temp", Label: "Tctl", Value: 96, CPU: true}
	e, _, _ := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckCPUTemperature)
	if c.Status != StatusCritical || c.Findings[0].Evidence["threshold_source"] != "configured" {
		t.Errorf("%+v", c)
	}
}

func TestMemory(t *testing.T) {
	low := healthy()
	low.Memory.Data.UsedPercent = 97
	e, _, _ := newEval(t, low, 1)
	if c := check(t, e.Evaluate(), CheckMemory); c.Status != StatusCritical {
		t.Errorf("low memory %+v", c)
	}

	stalled := healthy()
	stalled.MemoryPressure.Data.FullAvg60 = 25
	e, _, _ = newEval(t, stalled, 1)
	c := check(t, e.Evaluate(), CheckMemory)
	if c.Status != StatusCritical || !strings.Contains(c.Findings[0].Summary, "stalled") {
		t.Errorf("pressure %+v", c)
	}

	noPSI := healthy()
	noPSI.Memory.Data.UsedPercent = 92
	noPSI.MemoryPressure = core.Section[core.MemoryPressure]{Meta: core.SectionMeta{Status: core.Unsupported}}
	e, _, _ = newEval(t, noPSI, 1)
	c = check(t, e.Evaluate(), CheckMemory)
	if c.Status != StatusWarning || c.Findings[0].Evidence["pressure"] != "unavailable" {
		t.Errorf("without PSI %+v", c)
	}
}

func TestStorage(t *testing.T) {
	s := healthy()
	*s.Filesystems.Data = core.Filesystems{
		{MountPoint: "/", TotalBytes: 1000, FreeBytes: 100, AvailableBytes: 50},                                       // 900/(900+50) = 94.7% warning
		{MountPoint: "/data", TotalBytes: 1000, FreeBytes: 900, AvailableBytes: 900, TotalInodes: 100, FreeInodes: 2}, // inodes 98%
		{MountPoint: "/snap/core", ReadOnly: true, TotalBytes: 1000},                                                  // full, read-only: ignored
	}
	e, source, evs := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckStorageCapacity)
	if c.Status != StatusCritical || len(c.Findings) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.Findings[0].Subject != "filesystem:/data" || !strings.Contains(c.Findings[0].Summary, "inodes") {
		t.Errorf("worst first: %+v", c.Findings[0])
	}
	if c.Findings[1].Severity != core.SeverityWarning {
		t.Errorf("root %+v", c.Findings[1])
	}

	// /data is unmounted: its finding resolves.
	n := healthy()
	*n.Filesystems.Data = core.Filesystems{(*s.Filesystems.Data)[0]}
	source.set(n)
	e.Evaluate()
	resolved := 0
	for _, ev := range *evs {
		if ev.Type == core.EventFindingResolved && ev.Subject == "filesystem:/data" {
			resolved++
		}
	}
	if resolved != 1 {
		t.Errorf("events %+v", *evs)
	}
}

func TestDiskHealth(t *testing.T) {
	s := healthy()
	*s.DiskHealth.Data = core.DiskHealthList{
		{Device: "sda", Protocol: "ATA", Passed: ptr(false)},
		{Device: "sdb", Protocol: "ATA", Passed: ptr(true), ReallocatedSectors: ptr(uint64(12)), PendingSectors: ptr(uint64(0))},
		{Device: "nvme0n1", Protocol: "NVMe", Passed: ptr(true), PercentageUsed: ptr(95), CriticalWarning: ptr(0)},
		{Device: "nvme1n1", Protocol: "NVMe", Passed: ptr(true), PercentageUsed: ptr(4), CriticalWarning: ptr(0)},
	}
	e, _, _ := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckDiskHealth)
	if c.Status != StatusCritical || len(c.Findings) != 3 {
		t.Fatalf("%+v", c)
	}
	bySubject := map[string]core.Finding{}
	for _, f := range c.Findings {
		bySubject[f.Subject] = f
	}
	if bySubject["disk:sda"].Severity != core.SeverityCritical || !strings.Contains(bySubject["disk:sda"].Summary, "FAILED") {
		t.Errorf("sda %+v", bySubject["disk:sda"])
	}
	if bySubject["disk:sdb"].Severity != core.SeverityWarning || bySubject["disk:nvme0n1"].Severity != core.SeverityWarning {
		t.Errorf("warnings %+v", bySubject)
	}
}

func TestStaleDataKeepsFindings(t *testing.T) {
	s := healthy()
	s.Memory.Data.UsedPercent = 97
	e, source, evs := newEval(t, s, 1)
	e.Evaluate()
	stale := healthy()
	stale.Memory = core.Section[core.MemoryMetrics]{Meta: core.SectionMeta{Status: core.Available, StaleAfter: t0.Add(-time.Second)}, Data: s.Memory.Data}
	source.set(stale)
	c := check(t, e.Evaluate(), CheckMemory)
	if c.Status != StatusUnknown || c.Availability != core.TemporarilyUnavailable || len(c.Findings) != 1 {
		t.Errorf("%+v", c)
	}
	for _, ev := range *evs {
		if ev.Type == core.EventFindingResolved {
			t.Error("missing data resolved a finding")
		}
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	e, _, _ := newEval(t, healthy(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx, time.Millisecond); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done
	if e.Current().Status != StatusOK {
		t.Error("Run did not evaluate")
	}
}

func TestFailedService(t *testing.T) {
	s := healthy()
	*s.Services.Data = append(*s.Services.Data, core.Service{Name: "backup.service", Description: "Nightly backup", ActiveState: "failed", SubState: "failed"})
	e, _, evs := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckServices)
	if c.Status != StatusWarning || len(c.Findings) != 1 || c.Findings[0].Subject != "service:backup.service" ||
		!strings.Contains(c.Findings[0].Recommendation, "journalctl -u backup.service") {
		t.Errorf("%+v", c)
	}
	if len(*evs) != 1 {
		t.Errorf("events %v", *evs)
	}
}

func TestNetworkErrorRate(t *testing.T) {
	now := t0
	source := &src{s: healthy()}
	e := New(source, Options{Thresholds: defaultThresholds, Confirm: 1, Now: func() time.Time { return now }})
	if c := check(t, e.Evaluate(), CheckNetworkErrors); c.Status != StatusOK {
		t.Fatalf("errors accumulated before the first evaluation must not count: %+v", c)
	}
	step := func(errs uint64, after time.Duration) CheckResult {
		now = now.Add(after)
		s := healthy()
		(*s.Network.Data)[0].RxErrors = errs
		(*s.Network.Data)[1].RxErrors = errs * 100 // loopback never counts
		source.set(s)
		return check(t, e.Evaluate(), CheckNetworkErrors)
	}
	if c := step(25, time.Minute); c.Status != StatusWarning || c.Findings[0].Evidence["errors_per_minute"] != "20.0" {
		t.Errorf("20/min: %+v", c)
	}
	if c := step(25+500, time.Minute); c.Status != StatusCritical {
		t.Errorf("500/min: %+v", c)
	}
	if c := step(525, time.Minute); c.Status != StatusOK {
		t.Errorf("errors stopped rising: %+v", c)
	}
	if c := step(3, time.Minute); c.Status != StatusOK {
		t.Errorf("counter reset must not count: %+v", c)
	}
}

func TestRunWaitsForFirstData(t *testing.T) {
	wait := make(chan struct{})
	source := &src{s: healthy()}
	var evaluated sync.WaitGroup
	evaluated.Add(1)
	e := New(source, Options{Thresholds: defaultThresholds, Confirm: 1, Now: func() time.Time { return t0 }, Wait: wait})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx, time.Hour); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if e.Current().Status != StatusUnknown {
		t.Error("evaluated before the engine's first round")
	}
	close(wait)
	deadline := time.Now().Add(2 * time.Second)
	for e.Current().Status == StatusUnknown && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.Current().Status != StatusOK {
		t.Error("not evaluated after the first round")
	}
	cancel()
	<-done
}

func TestSetThresholdsAppliesToNextEvaluation(t *testing.T) {
	s := healthy() // "/" is 52.6% full
	e, _, _ := newEval(t, s, 1)
	if c := check(t, e.Evaluate(), CheckStorageCapacity); c.Status != StatusOK {
		t.Fatalf("before %+v", c)
	}
	th := defaultThresholds
	th.StorageWarnPct, th.StorageCriticalPct = 40, 95
	e.SetThresholds(th, 1)
	if c := check(t, e.Evaluate(), CheckStorageCapacity); c.Status != StatusWarning {
		t.Errorf("new threshold not applied: %+v", c)
	}
}

func TestZFSPools(t *testing.T) {
	s := healthy()
	s.ZFS.Data.Pools = []core.ZFSPool{{Name: "tank", State: "DEGRADED", ChecksumErrors: ptr(uint64(5))}, {Name: "old", State: "OFFLINE"}, {Name: "gone", State: "FAULTED"}}
	e, _, _ := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckZFSPools)
	if c.Status != StatusCritical || len(c.Findings) != 2 {
		t.Fatalf("%+v", c)
	}
	sev := map[string]core.Severity{}
	for _, f := range c.Findings {
		sev[f.Subject] = f.Severity
	}
	if sev["zpool:tank"] != core.SeverityWarning || sev["zpool:gone"] != core.SeverityCritical {
		t.Errorf("severities %v", sev)
	}
	for _, f := range c.Findings {
		if f.Subject == "zpool:tank" && f.Evidence["checksum_errors"] != "5" {
			t.Errorf("evidence %v", f.Evidence)
		}
	}
}

func TestUPSCheck(t *testing.T) {
	s := healthy()
	*s.UPS.Data = core.UPSList{{Name: "a", Status: []string{"OB"}, OnBattery: true, ChargePercent: ptr(60.0)},
		{Name: "b", Status: []string{"OB", "LB"}, OnBattery: true, LowBattery: true}}
	e, _, _ := newEval(t, s, 1)
	c := check(t, e.Evaluate(), CheckUPS)
	if c.Status != StatusCritical || len(c.Findings) != 2 || c.Findings[0].Summary != "UPS b battery is low" {
		t.Errorf("%+v", c)
	}
	s.UPS = core.Section[core.UPSList]{Meta: core.SectionMeta{Status: core.NotConfigured, Reason: "UPS monitoring is off."}}
	if c := check(t, e.Evaluate(), CheckUPS); c.Status != StatusUnknown || c.Availability != core.NotConfigured {
		t.Errorf("off: %+v", c)
	}
}
