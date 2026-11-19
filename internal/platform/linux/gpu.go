//go:build linux

package linux

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/internal/platform/portable"
)

type gpuCollector struct {
	env collector.Env
	run runFunc

	mu     sync.Mutex
	energy map[string]energySample // Intel hwmon energy by PCI address
}

type energySample struct {
	microjoules uint64
	at          time.Time
}

func newGPU(env collector.Env) *gpuCollector {
	return &gpuCollector{env: env, run: realRun, energy: map[string]energySample{}}
}

func (c *gpuCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.gpu", Version: version, Source: "sysfs, nvidia-smi, amdgpu, i915/xe",
		Capabilities: []core.Capability{core.CapGPUDevices, core.CapGPUMetrics},
		Class:        collector.Sampler, DefaultInterval: 5 * time.Second, MinInterval: 5 * time.Second, DefaultTimeout: 4 * time.Second,
	}
}

func (c *gpuCollector) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return c.Collect(ctx).Status
}

func (c *gpuCollector) Close(context.Context) error { return nil }

func (c *gpuCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	pci, _ := listPCI(c.env.SysRoot, c.env.UsrRoot)
	var gpus core.GPUs
	for _, d := range pci {
		if strings.HasPrefix(strings.ToLower(d.ClassCode), "0x03") {
			g := core.GPU{Vendor: portable.GPUVendors[strings.ToLower(d.VendorID)], Name: d.DeviceName, PCIAddress: d.Address, Driver: d.Driver}
			if g.Vendor == "" {
				g.Vendor = d.VendorName
			}
			if g.Name == "" {
				g.Name = strings.TrimSpace(d.VendorName + " " + d.ClassName)
			}
			gpus = append(gpus, g)
		}
	}

	metrics := 0
	nvidiaTool, haveNvidia := "", false
	if c.env.Tools != nil {
		nvidiaTool, haveNvidia = c.env.Tools.Find("nvidia-smi")
	}
	if haveNvidia {
		if rows, ok := c.nvidia(ctx, nvidiaTool); ok {
			gpus = portable.MergeNvidia(gpus, rows)
			metrics += len(rows)
		}
	}
	seen := map[string]bool{}
	for i := range gpus {
		switch gpus[i].Driver {
		case "amdgpu":
			if c.amd(&gpus[i]) {
				metrics++
			}
		case "i915", "xe":
			seen[gpus[i].PCIAddress] = true
			if c.intel(&gpus[i]) {
				metrics++
			}
		}
	}
	c.mu.Lock()
	for addr := range c.energy { // bounded: forget GPUs that are gone
		if !seen[addr] {
			delete(c.energy, addr)
		}
	}
	c.mu.Unlock()

	if len(gpus) == 0 {
		res.Status[core.CapGPUDevices] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "No GPU was found."}
		res.Status[core.CapGPUMetrics] = res.Status[core.CapGPUDevices]
		return res
	}
	res.Status[core.CapGPUDevices] = available()
	switch {
	case metrics > 0:
		res.Status[core.CapGPUMetrics] = available()
	case hasVendor(gpus, "NVIDIA") && !haveNvidia:
		res.Status[core.CapGPUMetrics] = core.CapabilityStatus{Availability: core.NotConfigured,
			Reason: "nvidia-smi (NVIDIA driver utilities) is not installed."}
	default:
		res.Status[core.CapGPUMetrics] = core.CapabilityStatus{Availability: core.Unsupported,
			Reason: "No metrics source is available for this GPU."}
	}
	res.Records = []core.Record{gpus}
	return res
}

func hasVendor(gs core.GPUs, v string) bool {
	for _, g := range gs {
		if g.Vendor == v {
			return true
		}
	}
	return false
}

// nvidia runs the fixed nvidia-smi query and parses its CSV output.
func (c *gpuCollector) nvidia(ctx context.Context, tool string) ([]core.GPU, bool) {
	out, code, err := c.run(ctx, tool, "--query-gpu="+strings.Join(portable.NvidiaFields, ","), "--format=csv,noheader,nounits")
	if err != nil || code != 0 {
		return nil, false
	}
	return portable.ParseNvidiaCSV(out), true
}

// amd reads amdgpu sysfs metrics. It reports whether any were found.
func (c *gpuCollector) amd(g *core.GPU) bool {
	dir := filepath.Join("bus", "pci", "devices", g.PCIAddress)
	found := false
	if v, err := readUint(c.env.SysRoot, filepath.Join(dir, "gpu_busy_percent")); err == nil {
		f := float64(v)
		g.UtilizationPercent, found = &f, true
	}
	if v, err := readUint(c.env.SysRoot, filepath.Join(dir, "mem_info_vram_total")); err == nil {
		g.MemoryTotalBytes, found = &v, true
	}
	if v, err := readUint(c.env.SysRoot, filepath.Join(dir, "mem_info_vram_used")); err == nil {
		g.MemoryUsedBytes, found = &v, true
	}
	if hw, err := listDir(c.env.SysRoot, filepath.Join(dir, "hwmon")); err == nil && len(hw) > 0 {
		h := filepath.Join(dir, "hwmon", hw[0])
		if v, err := readInt(c.env.SysRoot, filepath.Join(h, "temp1_input")); err == nil {
			t := float64(v) / 1000
			g.TemperatureCelsius, found = &t, true
		}
		for _, n := range []string{"power1_average", "power1_input"} {
			if v, err := readUint(c.env.SysRoot, filepath.Join(h, n)); err == nil {
				w := float64(v) / 1e6
				g.PowerWatts, found = &w, true
				break
			}
		}
	}
	if found {
		g.MetricsSource = "amdgpu"
	}
	return found
}

// intel reads i915 and xe sysfs metrics: the current and maximum GPU
// frequency, and power from the hwmon energy counter (discrete GPUs).
// Utilization needs perf events, which an unprivileged agent cannot use,
// so it stays unavailable.
func (c *gpuCollector) intel(g *core.GPU) bool {
	dev := filepath.Join("bus", "pci", "devices", g.PCIAddress)
	found := false
	mhz := func(paths ...string) *float64 {
		for _, p := range paths {
			if v, err := readUint(c.env.SysRoot, filepath.Join(dev, p)); err == nil {
				f := float64(v)
				return &f
			}
		}
		return nil
	}
	// i915: drm/cardN/gt_cur_freq_mhz; xe: tile0/gt0/freq0/cur_freq.
	var card string
	if cards, err := listDir(c.env.SysRoot, filepath.Join(dev, "drm")); err == nil {
		for _, n := range cards {
			if strings.HasPrefix(n, "card") {
				card = filepath.Join("drm", n)
				break
			}
		}
	}
	g.FrequencyMHz = mhz(filepath.Join(card, "gt_cur_freq_mhz"), "tile0/gt0/freq0/cur_freq")
	g.MaxFrequencyMHz = mhz(filepath.Join(card, "gt_RP0_freq_mhz"), "tile0/gt0/freq0/rp0_freq", filepath.Join(card, "gt_max_freq_mhz"), "tile0/gt0/freq0/max_freq")
	if card == "" {
		g.FrequencyMHz, g.MaxFrequencyMHz = mhz("tile0/gt0/freq0/cur_freq"), mhz("tile0/gt0/freq0/rp0_freq", "tile0/gt0/freq0/max_freq")
	}
	found = g.FrequencyMHz != nil

	if hw, err := listDir(c.env.SysRoot, filepath.Join(dev, "hwmon")); err == nil && len(hw) > 0 {
		h := filepath.Join(dev, "hwmon", hw[0])
		if v, err := readUint(c.env.SysRoot, filepath.Join(h, "power1_max")); err == nil && v > 0 {
			w := float64(v) / 1e6
			g.PowerLimitWatts = &w
		}
		if v, err := readUint(c.env.SysRoot, filepath.Join(h, "energy1_input")); err == nil {
			now := c.env.Now()
			c.mu.Lock()
			prev, ok := c.energy[g.PCIAddress]
			c.energy[g.PCIAddress] = energySample{microjoules: v, at: now}
			c.mu.Unlock()
			if dt := now.Sub(prev.at).Seconds(); ok && dt > 0 && v >= prev.microjoules {
				w := float64(v-prev.microjoules) / 1e6 / dt
				g.PowerWatts = &w
			}
			found = true
		}
		if v, err := readInt(c.env.SysRoot, filepath.Join(h, "temp1_input")); err == nil {
			t := float64(v) / 1000
			g.TemperatureCelsius, found = &t, true
		}
	}
	if found {
		g.MetricsSource = g.Driver
	}
	return found
}
