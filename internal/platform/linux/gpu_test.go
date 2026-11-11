//go:build linux

package linux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

const nvidiaCSV = "NVIDIA GeForce RTX 3060, GPU-00000000-1111-2222-3333-444444444444, 00000000:26:00.0, 610.88, 12288, 1073, 7, 41, 10.88, 170.00, 30\n" +
	"Tesla T4, GPU-aaaa, 00000000:3B:00.0, 610.88, 15360, 0, [N/A], 35, [Not Supported], 70.00, [N/A]\n" +
	"garbage line\n"

func TestParseNvidiaCSV(t *testing.T) {
	gs := parseNvidiaCSV([]byte(nvidiaCSV))
	if len(gs) != 2 {
		t.Fatalf("%+v", gs)
	}
	g := gs[0]
	if g.Name != "NVIDIA GeForce RTX 3060" || g.PCIAddress != "0000:26:00.0" || *g.MemoryTotalBytes != 12288<<20 || *g.MemoryUsedBytes != 1073<<20 ||
		*g.UtilizationPercent != 7 || *g.TemperatureCelsius != 41 || *g.PowerWatts != 10.88 || *g.FanPercent != 30 || g.DriverVersion != "610.88" {
		t.Errorf("rtx %+v", g)
	}
	if t4 := gs[1]; t4.UtilizationPercent != nil || t4.PowerWatts != nil || t4.FanPercent != nil || t4.PCIAddress != "0000:3b:00.0" || *t4.PowerLimitWatts != 70 {
		t.Errorf("unavailable fields must be null: %+v", t4)
	}
}

func gpuEnv(t *testing.T, files map[string]string, nvidia bool) (*gpuCollector, *[]string) {
	t.Helper()
	e := env(tree(t, files))
	if nvidia {
		e.Tools = tools{"nvidia-smi": "/usr/bin/nvidia-smi"}
	}
	c := newGPU(e)
	var calls []string
	c.run = func(_ context.Context, path string, args ...string) ([]byte, int, error) {
		calls = append(calls, path)
		return []byte(nvidiaCSV[:len(nvidiaCSV)-len("Tesla T4, GPU-aaaa, 00000000:3B:00.0, 610.88, 15360, 0, [N/A], 35, [Not Supported], 70.00, [N/A]\ngarbage line\n")]), 0, nil
	}
	return c, &calls
}

func pciDev(addr, vendor, class, driver string) map[string]string {
	d := "sys/bus/pci/devices/" + addr
	m := map[string]string{d + "/vendor": vendor + "\n", d + "/device": "0x0001\n", d + "/class": class + "\n"}
	if driver != "" {
		m["sys/bus/pci/drivers/"+driver+"/.keep"] = ""
		m[d+"/driver"] = "->../../drivers/" + driver
	}
	return m
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// WSL: the GPU is reached through Microsoft's paravirtual device; the
// nvidia-smi row replaces it instead of appearing twice.
func TestGPUWSLNvidia(t *testing.T) {
	c, calls := gpuEnv(t, pciDev("7c10:00:00.0", "0x1414", "0x030200", "dxgkrnl"), true)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUDevices, core.Available)
	wantStatus(t, r, core.CapGPUMetrics, core.Available)
	gs := record[core.GPUs](t, r)
	if len(gs) != 1 || gs[0].Name != "NVIDIA GeForce RTX 3060" || gs[0].MetricsSource != "nvidia-smi" || len(*calls) != 1 {
		t.Errorf("%+v", gs)
	}
}

func TestGPUNvidiaMatchedToPCI(t *testing.T) {
	c, _ := gpuEnv(t, pciDev("0000:26:00.0", "0x10de", "0x030000", "nvidia"), true)
	gs := record[core.GPUs](t, c.Collect(context.Background()))
	if len(gs) != 1 || gs[0].PCIAddress != "0000:26:00.0" || gs[0].Driver != "nvidia" || gs[0].UtilizationPercent == nil {
		t.Errorf("%+v", gs)
	}
}

func TestGPUNvidiaWithoutTool(t *testing.T) {
	c, calls := gpuEnv(t, pciDev("0000:26:00.0", "0x10de", "0x030000", "nvidia"), false)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUDevices, core.Available)
	wantStatus(t, r, core.CapGPUMetrics, core.NotConfigured)
	if len(*calls) != 0 {
		t.Error("tool run without being installed")
	}
}

func TestGPUAMD(t *testing.T) {
	d := "sys/bus/pci/devices/0000:03:00.0"
	files := merge(pciDev("0000:03:00.0", "0x1002", "0x030000", "amdgpu"), map[string]string{
		d + "/gpu_busy_percent":            "42\n",
		d + "/mem_info_vram_total":         "8589934592\n",
		d + "/mem_info_vram_used":          "1073741824\n",
		d + "/hwmon/hwmon3/temp1_input":    "55000\n",
		d + "/hwmon/hwmon3/power1_average": "35000000\n",
	})
	c, _ := gpuEnv(t, files, false)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUMetrics, core.Available)
	g := record[core.GPUs](t, r)[0]
	if g.Vendor != "AMD" || *g.UtilizationPercent != 42 || *g.MemoryTotalBytes != 8<<30 || *g.TemperatureCelsius != 55 || *g.PowerWatts != 35 || g.MetricsSource != "amdgpu" {
		t.Errorf("%+v", g)
	}
}

func TestGPUNone(t *testing.T) {
	c, _ := gpuEnv(t, pciDev("0000:00:1f.3", "0x8086", "0x040300", ""), false)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUDevices, core.Unsupported)
	if len(r.Records) != 0 {
		t.Error("records without a GPU")
	}
}

func TestIntelGPUWithoutMetrics(t *testing.T) {
	c, _ := gpuEnv(t, pciDev("0000:00:02.0", "0x8086", "0x030000", "i915"), false)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUDevices, core.Available)
	wantStatus(t, r, core.CapGPUMetrics, core.Unsupported)
}

func TestGPUIntelI915(t *testing.T) {
	d := "sys/bus/pci/devices/0000:00:02.0"
	files := merge(pciDev("0000:00:02.0", "0x8086", "0x030000", "i915"), map[string]string{
		d + "/drm/card1/gt_cur_freq_mhz":  "350\n",
		d + "/drm/card1/gt_RP0_freq_mhz":  "1300\n",
		d + "/drm/renderD128/dev":         "226:128\n",
		d + "/hwmon/hwmon5/energy1_input": "1000000\n",
		d + "/hwmon/hwmon5/power1_max":    "25000000\n",
	})
	c, _ := gpuEnv(t, files, false)
	root := c.env.SysRoot[:len(c.env.SysRoot)-len("/sys")]
	now := c.env.Now()
	c.env.Now = func() time.Time { return now }
	g := record[core.GPUs](t, c.Collect(context.Background()))[0]
	if g.Vendor != "Intel" || *g.FrequencyMHz != 350 || *g.MaxFrequencyMHz != 1300 || *g.PowerLimitWatts != 25 || g.PowerWatts != nil ||
		g.UtilizationPercent != nil || g.MetricsSource != "i915" {
		t.Errorf("first sample %+v", g)
	}
	// 5 J over 2 s: 2.5 W.
	if err := os.WriteFile(filepath.Join(root, d, "hwmon/hwmon5/energy1_input"), []byte("6000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.env.Now = func() time.Time { return now.Add(2 * time.Second) }
	g = record[core.GPUs](t, c.Collect(context.Background()))[0]
	if g.PowerWatts == nil || *g.PowerWatts != 2.5 {
		t.Errorf("power %v", g.PowerWatts)
	}
}

func TestGPUIntelXe(t *testing.T) {
	d := "sys/bus/pci/devices/0000:03:00.0"
	files := merge(pciDev("0000:03:00.0", "0x8086", "0x030000", "xe"), map[string]string{
		d + "/tile0/gt0/freq0/cur_freq": "2000\n",
		d + "/tile0/gt0/freq0/rp0_freq": "2400\n",
	})
	c, _ := gpuEnv(t, files, false)
	r := c.Collect(context.Background())
	wantStatus(t, r, core.CapGPUMetrics, core.Available)
	if g := record[core.GPUs](t, r)[0]; *g.FrequencyMHz != 2000 || *g.MaxFrequencyMHz != 2400 || g.MetricsSource != "xe" {
		t.Errorf("%+v", g)
	}
}
