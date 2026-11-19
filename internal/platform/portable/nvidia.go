package portable

// nvidia-smi output (D16), shared by Linux and Windows.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// GPUVendors names GPU makers by PCI vendor ID.
var GPUVendors = map[string]string{"0x10de": "NVIDIA", "0x1002": "AMD", "0x8086": "Intel", "0x1414": "Microsoft", "0x1af4": "Virtio", "0x15ad": "VMware", "0x1234": "QEMU"}

// NvidiaFields is the fixed nvidia-smi query (D16). Units are removed with
// "nounits": memory in MiB, power in W, temperature in °C.
var NvidiaFields = []string{"name", "uuid", "pci.bus_id", "driver_version", "memory.total", "memory.used",
	"utilization.gpu", "temperature.gpu", "power.draw", "power.limit", "fan.speed"}

// ParseNvidiaCSV parses the NvidiaFields query in CSV without units.
func ParseNvidiaCSV(b []byte) []core.GPU {
	var out []core.GPU
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ",")
		if len(f) != len(NvidiaFields) {
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
		g := core.GPU{Vendor: "NVIDIA", Name: f[0], UUID: f[1], PCIAddress: NormalizePCIAddress(f[2]), DriverVersion: f[3],
			MemoryTotalBytes: mib(4), MemoryUsedBytes: mib(5), UtilizationPercent: num(6), TemperatureCelsius: num(7),
			PowerWatts: num(8), PowerLimitWatts: num(9), FanPercent: num(10), MetricsSource: "nvidia-smi", Driver: "nvidia"}
		out = append(out, g)
	}
	return out
}

// NormalizePCIAddress turns nvidia-smi's "00000000:26:00.0" into the
// sysfs form "0000:26:00.0".
func NormalizePCIAddress(s string) string {
	s = strings.ToLower(s)
	if dom, rest, ok := strings.Cut(s, ":"); ok && len(dom) == 8 {
		return dom[4:] + ":" + rest
	}
	return s
}

// MergeNvidia enriches PCI GPUs with nvidia-smi rows by bus address and
// adds rows without a visible PCI device (e.g. WSL, where the GPU is
// reached through a Microsoft paravirtual device, which is then dropped).
func MergeNvidia(pci core.GPUs, rows []core.GPU) core.GPUs {
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
