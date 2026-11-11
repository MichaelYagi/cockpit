package app

import (
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/config"
)

func TestCollectorSettings(t *testing.T) {
	off := false
	c := config.Collection{
		Defaults: config.CollectorSettings{Interval: 2 * time.Second, Timeout: time.Second},
		Collectors: map[string]config.CollectorOverride{
			"linux.memory":  {Interval: 5 * time.Second},
			"linux.sensors": {Enabled: &off},
		},
	}
	resolve := CollectorSettings(c)
	sampler := collector.Descriptor{ID: "linux.cpu", Class: collector.Sampler, DefaultInterval: 10 * time.Second, DefaultTimeout: 3 * time.Second}
	if s := resolve(sampler); s.Interval != 2*time.Second || s.Timeout != time.Second || !s.Enabled {
		t.Errorf("sampler uses config defaults: %+v", s)
	}
	inv := collector.Descriptor{ID: "linux.smart", Class: collector.Inventory, DefaultInterval: time.Hour, DefaultTimeout: 30 * time.Second}
	if s := resolve(inv); s.Interval != time.Hour || s.Timeout != 30*time.Second {
		t.Errorf("inventory keeps own defaults: %+v", s)
	}
	sampler.ID = "linux.memory"
	if s := resolve(sampler); s.Interval != 5*time.Second || s.Timeout != time.Second {
		t.Errorf("override: %+v", s)
	}
	sampler.ID = "linux.sensors"
	if s := resolve(sampler); s.Enabled {
		t.Error("disabled override ignored")
	}
}
