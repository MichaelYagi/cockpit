// Package health evaluates the Phase 1 health checks (D11) against engine
// snapshots and tracks findings over time.
//
// A check whose data is unavailable reports "unknown" with the underlying
// availability and reason; missing hardware never produces an error.
// Findings are confirmed over several evaluations before they are raised
// or cleared, so values near a threshold do not flap.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// Status is the result of one check or the overall report.
type Status string

const (
	StatusOK       Status = "ok"
	StatusWarning  Status = "warning"
	StatusCritical Status = "critical"
	StatusUnknown  Status = "unknown"
)

func (s Status) rank() int {
	switch s {
	case StatusWarning:
		return 1
	case StatusCritical:
		return 2
	}
	return 0
}

// Check IDs.
const (
	CheckCPUTemperature  = "cpu_temperature"
	CheckMemory          = "memory"
	CheckStorageCapacity = "storage_capacity"
	CheckDiskHealth      = "disk_health"
	CheckServices        = "services"
	CheckNetworkErrors   = "network_errors"
)

// CheckResult is one check's outcome.
type CheckResult struct {
	ID           string
	Name         string
	Status       Status
	Availability core.Availability // of the data the check needs
	Reason       string            // why the check is unknown
	Summary      string
	Findings     []core.Finding
}

// Report is the full health evaluation.
type Report struct {
	Status      Status
	EvaluatedAt time.Time
	Checks      []CheckResult
}

// Thresholds configure the checks.
type Thresholds struct {
	CPUWarnCelsius, CPUCriticalCelsius           float64
	MemWarnAvailablePct, MemCriticalAvailablePct float64
	MemWarnPressurePct, MemCriticalPressurePct   float64
	StorageWarnPct, StorageCriticalPct           float64
	DiskWarnPercentageUsed                       int
	NetWarnErrorsPerMin, NetCriticalErrorsPerMin float64
}

// SnapshotSource provides the latest engine snapshot.
type SnapshotSource interface {
	Current() *core.Snapshot
}

// Options configure the evaluator.
type Options struct {
	Thresholds Thresholds
	Confirm    int // consecutive evaluations to raise or clear (>= 1)
	Now        func() time.Time
	Logger     *slog.Logger
	Publish    func(core.Event) // finding raised/resolved events; optional
	// Wait, if set, delays the first evaluation until it is closed (the
	// engine's first collection round) or 3 seconds pass.
	Wait <-chan struct{}
}

// Evaluator runs checks and holds the latest report.
type Evaluator struct {
	src  SnapshotSource
	opts Options

	mu    sync.Mutex
	state map[string]*tracked // key: checkID + "|" + subject

	// Previous interface error counters, for rates between evaluations.
	netPrev   map[string]uint64
	netPrevAt time.Time

	report atomic.Pointer[Report]
}

// tracked is the hysteresis state of one (check, subject).
type tracked struct {
	reported  core.Severity // "" = ok
	since     time.Time
	candidate core.Severity
	count     int
	last      core.Finding // most recent evidence for the reported severity
}

// New returns an evaluator. Call Evaluate or Run to produce reports.
func New(src SnapshotSource, opts Options) *Evaluator {
	if opts.Confirm < 1 {
		opts.Confirm = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	e := &Evaluator{src: src, opts: opts, state: map[string]*tracked{}}
	e.report.Store(&Report{Status: StatusUnknown, EvaluatedAt: opts.Now().UTC(), Checks: unknownChecks("Health has not been evaluated yet.")})
	return e
}

func unknownChecks(reason string) []CheckResult {
	var out []CheckResult
	for _, c := range checks {
		out = append(out, CheckResult{ID: c.id, Name: c.name, Status: StatusUnknown, Availability: core.TemporarilyUnavailable, Reason: reason})
	}
	return out
}

// SetThresholds replaces the thresholds and confirmation count; they apply
// from the next evaluation.
func (e *Evaluator) SetThresholds(t Thresholds, confirm int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opts.Thresholds = t
	if confirm >= 1 {
		e.opts.Confirm = confirm
	}
}

// Current returns the latest report. It never returns nil.
func (e *Evaluator) Current() *Report { return e.report.Load() }

// Run evaluates every interval until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	if e.opts.Wait != nil {
		select {
		case <-e.opts.Wait:
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
	e.Evaluate()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Evaluate()
		}
	}
}

// observation is a check's raw view of one subject in one evaluation.
type observation struct {
	subject string
	finding core.Finding // Severity "" means ok
}

type checkDef struct {
	id, name string
	run      func(e *Evaluator, s *core.Snapshot, now time.Time) (obs []observation, avail core.Availability, reason string)
}

var checks = []checkDef{
	{CheckCPUTemperature, "CPU temperature", (*Evaluator).cpuTemperature},
	{CheckMemory, "Memory", (*Evaluator).memory},
	{CheckStorageCapacity, "Storage capacity", (*Evaluator).storage},
	{CheckDiskHealth, "Disk health", (*Evaluator).disk},
	{CheckServices, "Services", (*Evaluator).services},
	{CheckNetworkErrors, "Network errors", (*Evaluator).networkErrors},
}

// Evaluate runs every check once, updates findings and publishes events.
func (e *Evaluator) Evaluate() *Report {
	now := e.opts.Now().UTC()
	snap := e.src.Current()

	e.mu.Lock()
	defer e.mu.Unlock()

	rep := &Report{EvaluatedAt: now}
	seen := map[string]bool{}
	for _, c := range checks {
		obs, avail, reason := c.run(e, snap, now)
		res := CheckResult{ID: c.id, Name: c.name, Availability: avail}
		if avail != core.Available {
			res.Status, res.Reason = StatusUnknown, reason
			// Keep existing findings for this check: missing data is not
			// evidence that a problem went away.
			for k, tr := range e.state {
				if tr.reported != "" && tr.last.CheckID == c.id {
					seen[k] = true
					res.Findings = append(res.Findings, tr.last)
				}
			}
			sortFindings(res.Findings)
			rep.Checks = append(rep.Checks, res)
			continue
		}
		res.Status = StatusOK
		for _, o := range obs {
			key := c.id + "|" + o.subject
			seen[key] = true
			o.finding.CheckID, o.finding.Subject = c.id, o.subject
			if f, active := e.track(key, o.finding, now); active {
				res.Findings = append(res.Findings, f)
				if s := Status(f.Severity); s.rank() > res.Status.rank() {
					res.Status = s
				}
			}
		}
		sortFindings(res.Findings)
		res.Summary = summary(res)
		rep.Checks = append(rep.Checks, res)
	}
	// Subjects that disappeared (e.g. an unmounted filesystem) resolve.
	for k, tr := range e.state {
		if !seen[k] {
			if tr.reported != "" {
				e.emit(core.EventFindingResolved, core.SeverityInfo, tr.last, "Resolved: "+tr.last.Summary+" (no longer present)")
			}
			delete(e.state, k)
		}
	}

	rep.Status = StatusUnknown
	for _, c := range rep.Checks {
		if c.Status == StatusUnknown {
			continue
		}
		if rep.Status == StatusUnknown || c.Status.rank() > rep.Status.rank() {
			rep.Status = c.Status
		}
	}
	e.report.Store(rep)
	return rep
}

// track applies hysteresis and returns the reported finding, if any.
// A change of severity (including back to ok) takes effect only after it
// has been observed Confirm times in a row.
func (e *Evaluator) track(key string, f core.Finding, now time.Time) (core.Finding, bool) {
	tr := e.state[key]
	if tr == nil {
		tr = &tracked{}
		e.state[key] = tr
	}
	if f.Severity == tr.reported {
		tr.candidate, tr.count = "", 0
	} else {
		if f.Severity == tr.candidate {
			tr.count++
		} else {
			tr.candidate, tr.count = f.Severity, 1
		}
		if tr.count >= e.opts.Confirm {
			prev := tr.reported
			tr.reported, tr.candidate, tr.count = f.Severity, "", 0
			switch {
			case f.Severity == "":
				e.emit(core.EventFindingResolved, core.SeverityInfo, tr.last, "Resolved: "+tr.last.Summary)
			case prev == "":
				tr.since = now
				e.emit(core.EventFindingRaised, f.Severity, f, f.Summary)
			default:
				e.emit(core.EventFindingRaised, f.Severity, f, fmt.Sprintf("Now %s: %s", f.Severity, f.Summary))
			}
		}
	}
	if tr.reported == "" {
		return core.Finding{}, false
	}
	if f.Severity == tr.reported {
		f.Since = tr.since
		tr.last = f // refresh evidence while the condition holds
	}
	out := tr.last
	out.ID = findingID(key)
	return out, true
}

func findingID(key string) string {
	// Stable, non-sensitive ID derived from check and subject.
	var h uint64 = 1469598103934665603
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("fnd_%016x", h)
}

func (e *Evaluator) emit(t core.EventType, sev core.Severity, f core.Finding, msg string) {
	e.opts.Logger.Info("health finding changed", "type", t, "check", f.CheckID, "subject", f.Subject, "severity", sev)
	if e.opts.Publish != nil {
		e.opts.Publish(core.Event{Time: e.opts.Now().UTC(), Type: t, Severity: sev, Subject: f.Subject, Message: msg})
	}
}

func sortFindings(fs []core.Finding) {
	sort.Slice(fs, func(i, j int) bool {
		ri, rj := Status(fs[i].Severity).rank(), Status(fs[j].Severity).rank()
		if ri != rj {
			return ri > rj
		}
		return fs[i].Subject < fs[j].Subject
	})
}

func summary(r CheckResult) string {
	switch len(r.Findings) {
	case 0:
		return "No problems found."
	case 1:
		return r.Findings[0].Summary
	}
	return fmt.Sprintf("%d problems found; worst: %s", len(r.Findings), r.Findings[0].Summary)
}

// usable reports whether a section can be evaluated now.
func usable(m core.SectionMeta, hasData bool, now time.Time) (core.Availability, string, bool) {
	switch {
	case !hasData && m.Status == core.Available:
		return core.TemporarilyUnavailable, "No data has been collected yet.", false
	case !hasData:
		return m.Status, m.Reason, false
	case m.Status != core.Available:
		return m.Status, m.Reason, false
	case m.IsStale(now):
		return core.TemporarilyUnavailable, "The latest data is stale.", false
	}
	return core.Available, "", true
}

func severityFor(value, warn, crit float64, rising bool) core.Severity {
	if rising {
		switch {
		case value >= crit:
			return core.SeverityCritical
		case value >= warn:
			return core.SeverityWarning
		}
		return ""
	}
	switch {
	case value <= crit:
		return core.SeverityCritical
	case value <= warn:
		return core.SeverityWarning
	}
	return ""
}

func worse(a, b core.Severity) core.Severity {
	if Status(b).rank() > Status(a).rank() {
		return b
	}
	return a
}

func f1(v float64) string { return fmt.Sprintf("%.1f", math.Round(v*10)/10) }

// ---- checks ----

func (e *Evaluator) cpuTemperature(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.Sensors.Meta, s.Sensors.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	var hottest *core.Sensor
	for i, sn := range *s.Sensors.Data {
		if sn.Kind == core.SensorTemperature && sn.CPU && (hottest == nil || sn.Value > hottest.Value) {
			hottest = &(*s.Sensors.Data)[i]
		}
	}
	if hottest == nil {
		return nil, core.Unsupported, "No CPU temperature sensor was found."
	}
	warn, crit, src := e.opts.Thresholds.CPUWarnCelsius, e.opts.Thresholds.CPUCriticalCelsius, "configured"
	if hottest.Critical != nil {
		crit, src = *hottest.Critical, "hardware"
		warn = crit - 10
		if hottest.High != nil && *hottest.High < crit {
			warn = *hottest.High
		}
	}
	sev := severityFor(hottest.Value, warn, crit, true)
	f := core.Finding{
		Severity: sev,
		Summary:  fmt.Sprintf("CPU temperature is %s °C", f1(hottest.Value)),
		Evidence: map[string]string{
			"temperature_celsius": f1(hottest.Value), "sensor": hottest.Chip + " " + hottest.Label,
			"warn_celsius": f1(warn), "critical_celsius": f1(crit), "threshold_source": src,
		},
		Recommendation: "Check cooling: fans, airflow, dust and thermal paste. Sustained high temperatures cause throttling and shorten hardware life.",
	}
	return []observation{{subject: "cpu", finding: f}}, core.Available, ""
}

func (e *Evaluator) memory(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.Memory.Meta, s.Memory.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	m := s.Memory.Data
	th := e.opts.Thresholds
	availPct := 100 - m.UsedPercent
	sev := severityFor(availPct, th.MemWarnAvailablePct, th.MemCriticalAvailablePct, false)
	ev := map[string]string{
		"available_percent": f1(availPct), "used_percent": f1(m.UsedPercent),
		"warn_available_percent": f1(th.MemWarnAvailablePct), "critical_available_percent": f1(th.MemCriticalAvailablePct),
	}
	summaryText := fmt.Sprintf("%s%% of memory is available", f1(availPct))
	if _, _, pok := usable(s.MemoryPressure.Meta, s.MemoryPressure.Data != nil, now); pok {
		p := s.MemoryPressure.Data.FullAvg60
		ev["pressure_full_avg60_percent"] = f1(p)
		if ps := severityFor(p, th.MemWarnPressurePct, th.MemCriticalPressurePct, true); ps != "" {
			sev = worse(sev, ps)
			summaryText += fmt.Sprintf("; all tasks were stalled on memory %s%% of the last minute", f1(p))
		}
	} else {
		ev["pressure"] = "unavailable"
	}
	f := core.Finding{Severity: sev, Summary: summaryText, Evidence: ev,
		Recommendation: "Find the largest memory consumers and stop or limit them, or add memory or swap."}
	return []observation{{subject: "memory", finding: f}}, core.Available, ""
}

func (e *Evaluator) storage(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.Filesystems.Meta, s.Filesystems.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	th := e.opts.Thresholds
	var out []observation
	for _, fs := range *s.Filesystems.Data {
		if fs.ReadOnly {
			continue // read-only images are full by design
		}
		usedPct := usedPercent(fs.TotalBytes, fs.FreeBytes, fs.AvailableBytes)
		sev := severityFor(usedPct, th.StorageWarnPct, th.StorageCriticalPct, true)
		ev := map[string]string{"used_percent": f1(usedPct), "available_bytes": fmt.Sprint(fs.AvailableBytes),
			"filesystem_type": fs.Type, "warn_percent": f1(th.StorageWarnPct), "critical_percent": f1(th.StorageCriticalPct)}
		summaryText := fmt.Sprintf("%s is %s%% full", fs.MountPoint, f1(usedPct))
		if fs.TotalInodes > 0 {
			inodePct := float64(fs.TotalInodes-min(fs.FreeInodes, fs.TotalInodes)) / float64(fs.TotalInodes) * 100
			ev["inodes_used_percent"] = f1(inodePct)
			if is := severityFor(inodePct, th.StorageWarnPct, th.StorageCriticalPct, true); Status(is).rank() > Status(sev).rank() {
				sev = is
				summaryText = fmt.Sprintf("%s has used %s%% of its inodes", fs.MountPoint, f1(inodePct))
			}
		}
		out = append(out, observation{subject: "filesystem:" + fs.MountPoint, finding: core.Finding{
			Severity: sev, Summary: summaryText, Evidence: ev,
			Recommendation: "Remove or archive unneeded files (logs, caches, old packages), or grow the filesystem.",
		}})
	}
	return out, core.Available, ""
}

// usedPercent matches df: used / (used + available to unprivileged users).
func usedPercent(total, free, avail uint64) float64 {
	if total == 0 || free > total {
		return 0
	}
	used := total - free
	if used+avail == 0 {
		return 0
	}
	return float64(used) / float64(used+avail) * 100
}

func (e *Evaluator) disk(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.DiskHealth.Meta, s.DiskHealth.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	var out []observation
	for _, d := range *s.DiskHealth.Data {
		var sev core.Severity
		var problems []string
		ev := map[string]string{"protocol": d.Protocol}
		add := func(s core.Severity, msg string) {
			sev = worse(sev, s)
			problems = append(problems, msg)
		}
		if d.Passed != nil {
			ev["smart_passed"] = fmt.Sprint(*d.Passed)
			if !*d.Passed {
				add(core.SeverityCritical, "SMART overall health check FAILED")
			}
		}
		if d.CriticalWarning != nil && *d.CriticalWarning != 0 {
			ev["nvme_critical_warning"] = fmt.Sprint(*d.CriticalWarning)
			add(core.SeverityCritical, "NVMe critical warning is set")
		}
		if d.PercentageUsed != nil {
			ev["percentage_used"] = fmt.Sprint(*d.PercentageUsed)
			switch {
			case *d.PercentageUsed >= 100:
				add(core.SeverityCritical, fmt.Sprintf("rated endurance is exhausted (%d%% used)", *d.PercentageUsed))
			case *d.PercentageUsed >= e.opts.Thresholds.DiskWarnPercentageUsed:
				add(core.SeverityWarning, fmt.Sprintf("%d%% of rated endurance is used", *d.PercentageUsed))
			}
		}
		for _, c := range []struct {
			v    *uint64
			key  string
			text string
		}{
			{d.MediaErrors, "media_errors", "media errors"},
			{d.ReallocatedSectors, "reallocated_sectors", "reallocated sectors"},
			{d.PendingSectors, "pending_sectors", "sectors pending reallocation"},
		} {
			if c.v != nil {
				ev[c.key] = fmt.Sprint(*c.v)
				if *c.v > 0 {
					add(core.SeverityWarning, fmt.Sprintf("%d %s", *c.v, c.text))
				}
			}
		}
		summaryText := d.Device + " reports no problems"
		if len(problems) > 0 {
			summaryText = d.Device + ": " + joinProblems(problems)
		}
		out = append(out, observation{subject: "disk:" + d.Device, finding: core.Finding{
			Severity: sev, Summary: summaryText, Evidence: ev,
			Recommendation: "Back up important data now and plan to replace the disk.",
		}})
	}
	return out, core.Available, ""
}

func joinProblems(p []string) string {
	out := p[0]
	for _, s := range p[1:] {
		out += "; " + s
	}
	return out
}

func (e *Evaluator) services(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.Services.Meta, s.Services.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	var out []observation
	for _, svc := range *s.Services.Data {
		var sev core.Severity
		if svc.ActiveState == "failed" {
			sev = core.SeverityWarning
		}
		out = append(out, observation{subject: "service:" + svc.Name, finding: core.Finding{
			Severity: sev,
			Summary:  svc.Name + " has failed",
			Evidence: map[string]string{"active_state": svc.ActiveState, "sub_state": svc.SubState, "description": svc.Description},
			Recommendation: "Inspect the unit with: systemctl status " + svc.Name + " and journalctl -u " + svc.Name +
				". Restart it after fixing the cause, or disable it if it is not needed.",
		}})
	}
	return out, core.Available, ""
}

// networkErrors reports interfaces whose error counters are rising. The
// rate is measured between evaluations, so errors accumulated before the
// agent started never count.
func (e *Evaluator) networkErrors(s *core.Snapshot, now time.Time) ([]observation, core.Availability, string) {
	avail, reason, ok := usable(s.Network.Meta, s.Network.Data != nil, now)
	if !ok {
		return nil, avail, reason
	}
	cur := map[string]uint64{}
	for _, n := range *s.Network.Data {
		if !n.Loopback {
			cur[n.Name] = n.RxErrors + n.TxErrors
		}
	}
	prev, prevAt := e.netPrev, e.netPrevAt
	e.netPrev, e.netPrevAt = cur, now
	minutes := now.Sub(prevAt).Minutes()
	th := e.opts.Thresholds
	var out []observation
	for _, n := range *s.Network.Data {
		if n.Loopback {
			continue
		}
		rate := 0.0
		if before, seen := prev[n.Name]; seen && minutes > 0 && cur[n.Name] >= before {
			rate = float64(cur[n.Name]-before) / minutes
		}
		sev := severityFor(rate, th.NetWarnErrorsPerMin, th.NetCriticalErrorsPerMin, true)
		out = append(out, observation{subject: "interface:" + n.Name, finding: core.Finding{
			Severity: sev,
			Summary:  fmt.Sprintf("%s is reporting %s errors per minute", n.Name, f1(rate)),
			Evidence: map[string]string{"errors_per_minute": f1(rate), "rx_errors": fmt.Sprint(n.RxErrors), "tx_errors": fmt.Sprint(n.TxErrors),
				"warn_per_minute": f1(th.NetWarnErrorsPerMin), "critical_per_minute": f1(th.NetCriticalErrorsPerMin)},
			Recommendation: "Check the cable, port and link negotiation (duplex/speed); for wireless, check signal quality and interference.",
		}})
	}
	return out, core.Available, ""
}
