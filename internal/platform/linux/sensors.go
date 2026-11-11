//go:build linux

package linux

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// cpuChips are hwmon drivers that report CPU package or core temperature.
var cpuChips = map[string]bool{
	"coretemp": true, "k10temp": true, "k8temp": true, "zenpower": true,
	"cpu_thermal": true, "cpu-thermal": true, "via_cputemp": true,
}

// cpuZones are thermal zone types that measure the CPU.
var cpuZones = map[string]bool{
	"x86_pkg_temp": true, "cpu-thermal": true, "cpu_thermal": true, "cpu0-thermal": true, "soc_thermal": true,
}

type sensorsCollector struct {
	base
	env collector.Env
}

func newSensors(env collector.Env) *sensorsCollector {
	c := &sensorsCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *sensorsCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.sensors", Version: version, Source: "sysfs/hwmon, sysfs/thermal",
		Capabilities: []core.Capability{core.CapSensorsThermal, core.CapSensorsFan},
		Class:        collector.Sampler, DefaultInterval: 2 * time.Second, DefaultTimeout: time.Second,
	}
}

func (c *sensorsCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	sensors := c.hwmon()
	hasTemp := false
	for _, s := range sensors {
		if s.Kind == core.SensorTemperature {
			hasTemp = true
			break
		}
	}
	// Thermal zones usually duplicate hwmon; use them only as a fallback.
	if !hasTemp {
		zones := c.thermalZones()
		sensors = append(sensors, zones...)
		hasTemp = len(zones) > 0
	}
	hasFan := false
	for _, s := range sensors {
		if s.Kind == core.SensorFan {
			hasFan = true
		}
	}
	none := "No hardware %s sensors are exposed. This is normal in virtual machines, containers and WSL."
	res.Status[core.CapSensorsThermal] = available()
	if !hasTemp {
		res.Status[core.CapSensorsThermal] = core.CapabilityStatus{Availability: core.Unsupported, Reason: strings.Replace(none, "%s", "temperature", 1)}
	}
	res.Status[core.CapSensorsFan] = available()
	if !hasFan {
		res.Status[core.CapSensorsFan] = core.CapabilityStatus{Availability: core.Unsupported, Reason: strings.Replace(none, "%s", "fan", 1)}
	}
	if len(sensors) > 0 {
		res.Records = []core.Record{core.Sensors(sensors)}
	}
	return res
}

func (c *sensorsCollector) hwmon() []core.Sensor {
	dirs, err := listDir(c.env.SysRoot, "class/hwmon")
	if err != nil {
		return nil
	}
	sort.Strings(dirs)
	var out []core.Sensor
	for _, d := range dirs {
		base := filepath.Join("class", "hwmon", d)
		// Older drivers put attributes under device/.
		for _, dir := range []string{base, filepath.Join(base, "device")} {
			chip, err := readTrim(c.env.SysRoot, filepath.Join(dir, "name"))
			if err != nil {
				continue
			}
			found := c.hwmonChip(dir, chip)
			out = append(out, found...)
			if len(found) > 0 {
				break
			}
		}
	}
	return out
}

func (c *sensorsCollector) hwmonChip(dir, chip string) []core.Sensor {
	names, err := listDir(c.env.SysRoot, dir)
	if err != nil {
		return nil
	}
	sort.Strings(names)
	var out []core.Sensor
	for _, n := range names {
		var kind core.SensorKind
		var unit string
		var scale float64
		switch {
		case strings.HasPrefix(n, "temp") && strings.HasSuffix(n, "_input"):
			kind, unit, scale = core.SensorTemperature, "celsius", 1000
		case strings.HasPrefix(n, "fan") && strings.HasSuffix(n, "_input"):
			kind, unit, scale = core.SensorFan, "rpm", 1
		case strings.HasPrefix(n, "in") && strings.HasSuffix(n, "_input"):
			kind, unit, scale = core.SensorVoltage, "volts", 1000
		default:
			continue
		}
		prefix := strings.TrimSuffix(n, "_input")
		raw, err := readInt(c.env.SysRoot, filepath.Join(dir, n))
		if err != nil {
			continue // unplugged probes return EIO or ENODATA
		}
		s := core.Sensor{
			Kind: kind, Chip: chip, Unit: unit, Value: float64(raw) / scale,
			Source: "hwmon", CPU: kind == core.SensorTemperature && cpuChips[chip],
		}
		s.Label, _ = readTrim(c.env.SysRoot, filepath.Join(dir, prefix+"_label"))
		if s.Label == "" {
			s.Label = prefix
		}
		if kind == core.SensorTemperature {
			if !plausibleCelsius(s.Value) {
				continue
			}
			s.High = c.threshold(dir, prefix+"_max", scale)
			s.Critical = c.threshold(dir, prefix+"_crit", scale)
		}
		out = append(out, s)
	}
	return out
}

func (c *sensorsCollector) threshold(dir, name string, scale float64) *float64 {
	raw, err := readInt(c.env.SysRoot, filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	v := float64(raw) / scale
	if v <= 0 || !plausibleCelsius(v) {
		return nil // many drivers report 0 or sentinel values for "none"
	}
	return &v
}

// plausibleCelsius filters sentinel readings such as -273 or 127.5 x 1000.
func plausibleCelsius(v float64) bool { return v > -60 && v < 200 }

func (c *sensorsCollector) thermalZones() []core.Sensor {
	names, err := listDir(c.env.SysRoot, "class/thermal")
	if err != nil {
		return nil
	}
	sort.Strings(names)
	var out []core.Sensor
	for _, n := range names {
		if !strings.HasPrefix(n, "thermal_zone") {
			continue
		}
		dir := filepath.Join("class", "thermal", n)
		raw, err := readInt(c.env.SysRoot, filepath.Join(dir, "temp"))
		if err != nil {
			continue
		}
		v := float64(raw) / 1000
		if !plausibleCelsius(v) {
			continue
		}
		typ, _ := readTrim(c.env.SysRoot, filepath.Join(dir, "type"))
		s := core.Sensor{Kind: core.SensorTemperature, Chip: typ, Label: n, Value: v, Unit: "celsius",
			Source: "thermal_zone", CPU: cpuZones[typ]}
		// Trip points: "critical" and "hot" map to critical and high.
		for i := 0; i < 16; i++ {
			kind, err := readTrim(c.env.SysRoot, filepath.Join(dir, "trip_point_"+strconv.Itoa(i)+"_type"))
			if err != nil {
				break
			}
			t := c.threshold(dir, "trip_point_"+strconv.Itoa(i)+"_temp", 1000)
			switch kind {
			case "critical":
				s.Critical = t
			case "hot":
				s.High = t
			}
		}
		out = append(out, s)
	}
	return out
}
