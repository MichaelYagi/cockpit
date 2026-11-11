package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func validDescriptor() Descriptor {
	return Descriptor{
		ID:              "linux.cpu",
		Version:         "1.0.0",
		Capabilities:    []core.Capability{core.CapCPUUsage},
		Class:           Sampler,
		DefaultInterval: 2 * time.Second,
		DefaultTimeout:  time.Second,
	}
}

func TestDescriptorValidate(t *testing.T) {
	if err := validDescriptor().Validate(); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Descriptor)
		want   string
	}{
		{"bad id", func(d *Descriptor) { d.ID = "CPU" }, "id"},
		{"no version", func(d *Descriptor) { d.Version = "" }, "version"},
		{"no capabilities", func(d *Descriptor) { d.Capabilities = nil }, "capability"},
		{"unknown capability", func(d *Descriptor) { d.Capabilities = []core.Capability{"cpu.magic"} }, "unknown capability"},
		{"no timeout", func(d *Descriptor) { d.DefaultTimeout = 0 }, "timeout"},
		{"sampler without interval", func(d *Descriptor) { d.DefaultInterval = 0 }, "interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDescriptor()
			tt.mutate(&d)
			err := d.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestInventoryNeedsNoInterval(t *testing.T) {
	d := validDescriptor()
	d.Class = Inventory
	d.DefaultInterval = 0
	if err := d.Validate(); err != nil {
		t.Fatalf("inventory without interval rejected: %v", err)
	}
}
