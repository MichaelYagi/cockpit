//go:build linux

package linux

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

var gpuVendors = map[string]string{"0x10de": "NVIDIA", "0x1002": "AMD", "0x8086": "Intel", "0x1414": "Microsoft", "0x1af4": "Virtio", "0x15ad": "VMware", "0x1234": "QEMU"}

// nvidiaFields is the fixed nvidia-smi query (D16). Units are removed with
// "nounits": memory in MiB, power in W, temperature in °C.
var nvidiaFields = []string{"name", "uuid", "pci.bus_id", "driver_version", "memory.total", "memory.used",
	"utilization.gpu", "temperature.gpu", "power.draw", "power.limit", "fan.speed"}

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
			g := core.GPU{Vendor: gpuVendors[strings.ToLower(d.VendorID)], Name: d.DeviceName, PCIAddress: d.Address, Driver: d.Driver}
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
			gpus = mergeNvidia(gpus, rows)
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
	out, code, err := c.run(ctx, tool, "--query-gpu="+strings.Join(nvidiaFields, ","), "--format=csv,noheader,nounits")
	if err != nil || code != 0 {
		return nil, false
	}
	return parseNvidiaCSV(out), true
}

func parseNvidiaCSV(b []byte) []core.GPU {
	var out []core.GPU
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ",")
		if len(f) != len(nvidiaFields) {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		num := func(i int) *float64 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil { // "[N/A]", "[Not Supported]", ...
				return nil
			}
			return &v
		}
		mib := func(i int) *uint64 {
			if v := num(i); v != nil && *v >= 0 {
				b := uint64(*v) << 20
				return &b
			}
			return nil
		}
		g := core.GPU{Vendor: "NVIDIA", Name: f[0], UUID: f[1], PCIAddress: normalizePCIAddress(f[2]), DriverVersion: f[3],
			MemoryTotalBytes: mib(4), MemoryUsedBytes: mib(5), UtilizationPercent: num(6), TemperatureCelsius: num(7),
			PowerWatts: num(8), PowerLimitWatts: num(9), FanPercent: num(10), MetricsSource: "nvidia-smi", Driver: "nvidia"}
		out = append(out, g)
	}
	return out
}

// normalizePCIAddress turns nvidia-smi's "00000000:26:00.0" into the
// sysfs form "0000:26:00.0".
func normalizePCIAddress(s string) string {
	s = strings.ToLower(s)
	if dom, rest, ok := strings.Cut(s, ":"); ok && len(dom) == 8 {
		return dom[4:] + ":" + rest
	}
	return s
}

// mergeNvidia enriches PCI GPUs with nvidia-smi rows by bus address and
// adds rows without a visible PCI device (e.g. WSL, where the GPU is
// reached through a Microsoft paravirtual device, which is then dropped).
func mergeNvidia(pci core.GPUs, rows []core.GPU) core.GPUs {
	out := make(core.GPUs, 0, len(pci)+len(rows))
	used := map[int]bool{}
	for _, g := range pci {
		matched := false
		for i, r := range rows {
			if r.PCIAddress == g.PCIAddress {
				r.Driver = g.Driver
				out, used[i], matched = append(out, r), true, true
				break
			}
		}
		if !matched && !(g.Vendor == "Microsoft" && g.Driver == "dxgkrnl") {
			out = append(out, g)
		}
	}
	for i, r := range rows {
		if !used[i] {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PCIAddress < out[j].PCIAddress })
	return out
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
