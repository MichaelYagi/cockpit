//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// ---- power supplies (D25) ----

type powerCollector struct {
	base
	env collector.Env
}

func newPower(env collector.Env) *powerCollector {
	c := &powerCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *powerCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.power", Version: version, Source: "sysfs/power_supply",
		Capabilities: []core.Capability{core.CapPowerSupplies},
		Class:        collector.Sampler, DefaultInterval: 10 * time.Second, DefaultTimeout: time.Second,
		MinInterval: 10 * time.Second,
	}
}

func (c *powerCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	names, err := listDir(c.env.SysRoot, "class/power_supply")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.Status[core.CapPowerSupplies] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "This system does not report power supplies."}
		} else {
			res.Status[core.CapPowerSupplies] = status(err, "power supply information")
		}
		return res
	}
	var out core.PowerSupplies
	for _, n := range names {
		if p, ok := c.supply(n); ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		res.Status[core.CapPowerSupplies] = core.CapabilityStatus{Availability: core.Unsupported,
			Reason: "No batteries or power adapters are reported (common on desktops, servers and virtual machines)."}
		return res
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	res.Status[core.CapPowerSupplies] = available()
	res.Records = []core.Record{out}
	return res
}

// supply reads one power supply. Units in sysfs: µW, µWh, µV, µA, µAh.
func (c *powerCollector) supply(name string) (core.PowerSupply, bool) {
	dir := filepath.Join("class/power_supply", name)
	typ, err := readTrim(c.env.SysRoot, dir+"/type")
	if err != nil {
		return core.PowerSupply{}, false
	}
	p := core.PowerSupply{Name: name, Type: strings.ToLower(typ)}
	str := func(f string) string { s, _ := readTrim(c.env.SysRoot, dir+"/"+f); return s }
	num := func(f string) *float64 {
		s, err := readTrim(c.env.SysRoot, dir+"/"+f)
		if err != nil {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		return &v
	}
	scaled := func(v *float64, by float64) *float64 {
		if v == nil {
			return nil
		}
		x := *v / by
		return &x
	}
	if o := num("online"); o != nil {
		on := *o != 0
		p.Online = &on
	}
	p.Status = strings.ReplaceAll(strings.ToLower(str("status")), " ", "_")
	p.Health = strings.ToLower(str("health"))
	p.Model = str("model_name") // serial_number is never read (D9)
	p.CapacityPercent = num("capacity")
	p.PowerWatts = scaled(num("power_now"), 1e6)
	p.EnergyNowWh = scaled(num("energy_now"), 1e6)
	p.EnergyFullWh = scaled(num("energy_full"), 1e6)
	// Batteries that report charge (µAh) and current (µA) instead.
	if volts := scaled(num("voltage_now"), 1e6); volts != nil {
		if p.PowerWatts == nil {
			if amps := scaled(num("current_now"), 1e6); amps != nil {
				w := *amps * *volts
				p.PowerWatts = &w
			}
		}
		if p.EnergyNowWh == nil {
			if ah := scaled(num("charge_now"), 1e6); ah != nil {
				wh := *ah * *volts
				p.EnergyNowWh = &wh
			}
		}
		if p.EnergyFullWh == nil {
			if ah := scaled(num("charge_full"), 1e6); ah != nil {
				wh := *ah * *volts
				p.EnergyFullWh = &wh
			}
		}
	}
	if p.PowerWatts != nil && *p.PowerWatts < 0 {
		w := -*p.PowerWatts // some drivers report discharge as negative
		p.PowerWatts = &w
	}
	if p.Status == "discharging" && p.EnergyNowWh != nil && p.PowerWatts != nil && *p.PowerWatts > 0.1 {
		h := *p.EnergyNowWh / *p.PowerWatts
		p.HoursRemaining = &h
	}
	return p, true
}
