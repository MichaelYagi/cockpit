//go:build linux

package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// runFunc executes a command without a shell and returns stdout and the
// exit code. Injectable for tests.
type runFunc func(ctx context.Context, path string, args ...string) (stdout []byte, exitCode int, err error)

const maxToolOutput = 1 << 20

func realRun(ctx context.Context, path string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	var out limitedBuffer
	out.max = maxToolOutput
	cmd.Stdout = &out
	cmd.Stderr = nil // discarded; may contain device details
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out.Bytes(), 0, nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil // drop the rest; output is bounded
	}
	return b.Buffer.Write(p)
}

// smartctl exit status bits (see smartctl(8)).
const (
	smartBitCmdline    = 1 << 0
	smartBitOpenFailed = 1 << 1
)

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
)

// Probe is cheap: it checks for the tool and whether disks can be opened,
// without running smartctl.
func (c *smartCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	st := func(s core.CapabilityStatus) map[core.Capability]core.CapabilityStatus {
		return map[core.Capability]core.CapabilityStatus{core.CapStorageSMART: s}
	}
	if _, ok := c.tool(); !ok {
		return st(smartNotInstalled)
	}
	disks, err := listDisks(c.env.SysRoot)
	if err != nil {
		return st(status(err, "block device information"))
	}
	if len(disks) == 0 {
		return st(smartNoDisks)
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
	path, ok := c.tool()
	if !ok {
		res.Status[core.CapStorageSMART] = smartNotInstalled
		return res
	}
	disks, err := listDisks(c.env.SysRoot)
	if err != nil {
		res.Status[core.CapStorageSMART] = status(err, "block device information")
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
		h, perm, ok := parseSmartctl(stdout, code)
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
		res.Status[core.CapStorageSMART] = core.CapabilityStatus{Availability: core.Unsupported,
			Reason: "None of the disks report SMART health (common for virtual disks)."}
	}
	return res
}

// smartctlJSON is the subset of `smartctl --json` output Cockpit uses.
type smartctlJSON struct {
	Smartctl struct {
		Messages []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *uint64 `json:"hours"`
	} `json:"power_on_time"`
	NVMe *struct {
		CriticalWarning *int    `json:"critical_warning"`
		PercentageUsed  *int    `json:"percentage_used"`
		MediaErrors     *uint64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	ATA *struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value uint64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// parseSmartctl interprets smartctl JSON output and exit code. perm reports
// a permission failure; ok reports usable health data.
func parseSmartctl(stdout []byte, code int) (h core.DiskHealth, perm, ok bool) {
	var j smartctlJSON
	if err := json.Unmarshal(stdout, &j); err != nil {
		return h, false, false
	}
	if code&(smartBitCmdline|smartBitOpenFailed) != 0 {
		for _, m := range j.Smartctl.Messages {
			if strings.Contains(strings.ToLower(m.String), "permission denied") || strings.Contains(m.String, "requires root") {
				return h, true, false
			}
		}
		return h, false, false
	}
	if j.SmartStatus == nil {
		return h, false, false // device does not support SMART (e.g. virtual disk)
	}
	passed := j.SmartStatus.Passed
	h.Passed = &passed
	h.Protocol = j.Device.Protocol
	if j.Temperature != nil {
		h.TemperatureCelsius = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		h.PowerOnHours = j.PowerOnTime.Hours
	}
	if n := j.NVMe; n != nil {
		h.CriticalWarning, h.PercentageUsed, h.MediaErrors = n.CriticalWarning, n.PercentageUsed, n.MediaErrors
	}
	if a := j.ATA; a != nil {
		for _, attr := range a.Table {
			v := attr.Raw.Value
			switch attr.ID {
			case 5:
				h.ReallocatedSectors = &v
			case 197:
				h.PendingSectors = &v
			}
		}
	}
	return h, false, true
}
