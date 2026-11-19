//go:build linux

package linux

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

// runFunc executes a command without a shell and returns stdout and the
// exit code. Injectable for tests.
type runFunc func(ctx context.Context, path string, args ...string) (stdout []byte, exitCode int, err error)

// realRun runs a tool with a fixed environment, bounded output and
// stderr discarded (it may contain device details).
func realRun(ctx context.Context, path string, args ...string) ([]byte, int, error) {
	return portable.RunTool(ctx, []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}, path, args...)
}

type smartCollector struct {
	env  collector.Env
	run  runFunc
	open func(path string) error // checks device access in Probe
}

func newSMART(env collector.Env) *smartCollector {
	return &smartCollector{env: env, run: realRun, open: func(p string) error {
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err == nil {
			f.Close()
		}
		return err
	}}
}

func (c *smartCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.smart", Version: version, Source: "smartctl",
		Capabilities: []core.Capability{core.CapStorageSMART},
		Class:        collector.Inventory, DefaultInterval: time.Hour, DefaultTimeout: 30 * time.Second,
	}
}

func (c *smartCollector) Close(context.Context) error { return nil }

func (c *smartCollector) tool() (string, bool) {
	if c.env.Tools == nil {
		return "", false
	}
	return c.env.Tools.Find("smartctl")
}

var (
	smartNotInstalled = core.CapabilityStatus{Availability: core.NotConfigured,
		Reason: "smartctl (smartmontools 7.0 or newer) is not installed."}
	smartNoPermission = core.CapabilityStatus{Availability: core.PermissionDenied,
		Reason: "Reading SMART data requires elevated privileges. Disk health is unavailable while the agent runs unprivileged."}
	smartNoDisks = core.CapabilityStatus{Availability: core.Unsupported, Reason: "No disks were found."}
	smartStale   = core.CapabilityStatus{Availability: core.TemporarilyUnavailable,
		Reason: "SMART results from the helper are more than 3 hours old. Check: systemctl status cockpit-smart.timer"}
	smartVirtual = core.CapabilityStatus{Availability: core.Unsupported,
		Reason: "None of the disks report SMART health (common for virtual disks)."}
)

// Probe is cheap: it checks for the tool and whether disks can be opened,
// without running smartctl.
func (c *smartCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	st := func(s core.CapabilityStatus) map[core.Capability]core.CapabilityStatus {
		return map[core.Capability]core.CapabilityStatus{core.CapStorageSMART: s}
	}
	disks, err := listDisks(c.env.SysRoot)
	if err != nil {
		return st(status(err, "block device information"))
	}
	if len(disks) == 0 {
		return st(smartNoDisks)
	}
	if _, found, _ := readHelperResults(c.env.SMARTResultsDir, disks, c.env.Now()); found {
		return st(available())
	}
	if _, ok := c.tool(); !ok {
		return st(smartNotInstalled)
	}
	for _, d := range disks {
		if err := c.open(filepath.Join(c.env.DevRoot, d)); err == nil {
			return st(available())
		}
	}
	return st(smartNoPermission)
}

func (c *smartCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	disks, err := listDisks(c.env.SysRoot)
	if err != nil {
		res.Status[core.CapStorageSMART] = status(err, "block device information")
		return res
	}
	// Results from the privileged helper take precedence (D20).
	if recs, found, stale := readHelperResults(c.env.SMARTResultsDir, disks, c.env.Now()); found {
		var out core.DiskHealthList
		for _, r := range recs {
			if h, _, ok := portable.ParseSmartctl(r.Smartctl, r.ExitCode); ok {
				h.Device = r.Device
				out = append(out, h)
			}
		}
		switch {
		case len(out) > 0:
			res.Status[core.CapStorageSMART] = available()
			res.Records = []core.Record{out}
		case stale > 0:
			res.Status[core.CapStorageSMART] = smartStale
		default:
			res.Status[core.CapStorageSMART] = smartVirtual
		}
		return res
	}
	path, ok := c.tool()
	if !ok {
		res.Status[core.CapStorageSMART] = smartNotInstalled
		return res
	}
	var out core.DiskHealthList
	denied := 0
	for _, d := range disks {
		if ctx.Err() != nil {
			break
		}
		dev := filepath.Join(c.env.DevRoot, d)
		// -n standby: never spin up a sleeping disk just to read health.
		stdout, code, err := c.run(ctx, path, "--json=c", "--info", "--health", "--attributes", "--nocheck=standby", dev)
		if err != nil {
			continue
		}
		h, perm, ok := portable.ParseSmartctl(stdout, code)
		if perm {
			denied++
		}
		if ok {
			h.Device = d
			out = append(out, h)
		}
	}
	switch {
	case len(out) > 0:
		res.Status[core.CapStorageSMART] = available()
		res.Records = []core.Record{out}
	case denied > 0:
		res.Status[core.CapStorageSMART] = smartNoPermission
	case len(disks) == 0:
		res.Status[core.CapStorageSMART] = smartNoDisks
	default:
		res.Status[core.CapStorageSMART] = smartVirtual
	}
	return res
}
