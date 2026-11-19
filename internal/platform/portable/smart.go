package portable

import (
	"context"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// SMARTDisk is one disk to query: Name is how the API names it
// ("disk0", "PhysicalDrive0"), Path is what smartctl opens ("/dev/disk0",
// "/dev/pd0").
type SMARTDisk struct{ Name, Path string }

// SMART configures NewSMART for macOS and Windows, which have no
// privileged helper (D20 is Linux's systemd timer).
type SMART struct {
	ID, Version string
	Now         func() time.Time
	// Tool finds smartctl; ok is false when it is not installed.
	Tool func() (path string, ok bool)
	// Disks lists the physical disks.
	Disks func() ([]SMARTDisk, error)
	// Run executes smartctl; RunTool when nil.
	Run func(ctx context.Context, path string, args ...string) ([]byte, int, error)
	// NoPermission explains how to grant access on this platform.
	NoPermission string
}

type smartCollector struct{ cfg SMART }

// NewSMART reads disk health with `smartctl --json` (D54), hourly. It is
// optional: without smartctl the capability is not_configured.
func NewSMART(cfg SMART) collector.Collector {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Run == nil {
		cfg.Run = func(ctx context.Context, path string, args ...string) ([]byte, int, error) {
			return RunTool(ctx, nil, path, args...)
		}
	}
	return &smartCollector{cfg: cfg}
}

func (c *smartCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: c.cfg.ID, Version: c.cfg.Version, Source: "smartctl",
		Capabilities: []core.Capability{core.CapStorageSMART},
		Class:        collector.Inventory, DefaultInterval: time.Hour, DefaultTimeout: 30 * time.Second,
	}
}

func (c *smartCollector) Close(context.Context) error { return nil }

// Probe checks only for the tool, so startup never waits for disks.
func (c *smartCollector) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	st := Available()
	if _, ok := c.cfg.Tool(); !ok {
		st = smartNotInstalled
	}
	return map[core.Capability]core.CapabilityStatus{core.CapStorageSMART: st}
}

var smartNotInstalled = Unavailable(core.NotConfigured, "smartctl (smartmontools 7.0 or newer) is not installed.")

func (c *smartCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.cfg.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	set := func(s core.CapabilityStatus) collector.Result {
		res.Status[core.CapStorageSMART] = s
		return res
	}
	path, ok := c.cfg.Tool()
	if !ok {
		return set(smartNotInstalled)
	}
	disks, err := c.cfg.Disks()
	if err != nil {
		return set(Status(err, "the disk list"))
	}
	if len(disks) == 0 {
		return set(Unavailable(core.Unsupported, "No disks were found."))
	}
	var out core.DiskHealthList
	denied := 0
	for _, d := range disks {
		if ctx.Err() != nil {
			break
		}
		// --nocheck=standby: never spin up a sleeping disk to read health.
		stdout, code, err := c.cfg.Run(ctx, path, "--json=c", "--info", "--health", "--attributes", "--nocheck=standby", d.Path)
		if err != nil {
			continue
		}
		h, perm, ok := ParseSmartctl(stdout, code)
		if perm {
			denied++
		}
		if ok {
			h.Device = d.Name
			out = append(out, h)
		}
	}
	switch {
	case len(out) > 0:
		res.Status[core.CapStorageSMART] = Available()
		res.Records = []core.Record{out}
	case denied > 0:
		res.Status[core.CapStorageSMART] = Unavailable(core.PermissionDenied, c.cfg.NoPermission)
	case ctx.Err() != nil:
		res.Status[core.CapStorageSMART] = Unavailable(core.TemporarilyUnavailable, "Reading disk health took too long.")
	default:
		res.Status[core.CapStorageSMART] = Unavailable(core.Unsupported, "None of the disks report SMART health (common for virtual disks).")
	}
	return res
}

// FirstTool returns the first existing executable among paths, after
// trying the agent's tool finder.
func FirstTool(tools collector.ToolFinder, name string, paths ...string) (string, bool) {
	if tools != nil {
		if p, ok := tools.Find(name); ok {
			return p, true
		}
	}
	for _, p := range paths {
		if isExecutable(p) {
			return p, true
		}
	}
	return "", false
}
