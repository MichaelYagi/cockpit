// Package unsupported provides the collector set for platforms Cockpit does
// not support yet. It reports every known capability as unsupported, so the
// binary still runs and clients get an explicit answer. Rest covers the
// capabilities a partly supported platform does not implement.
package unsupported

import (
	"context"
	"runtime"
	"slices"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Collectors returns the collector set for an unsupported platform.
func Collectors(collector.Env) []collector.Collector {
	return []collector.Collector{placeholder{id: "unsupported.platform", caps: core.KnownCapabilities(),
		reason: "Cockpit has no collectors for " + runtime.GOOS + " yet."}}
}

// Rest reports every known capability not in implemented as unsupported,
// with reason (D32).
func Rest(id string, implemented []core.Capability, reason string) collector.Collector {
	var caps []core.Capability
	for _, c := range core.KnownCapabilities() {
		if !slices.Contains(implemented, c) {
			caps = append(caps, c)
		}
	}
	return placeholder{id: id, caps: caps, reason: reason}
}

type placeholder struct {
	id     string
	caps   []core.Capability
	reason string
}

func (p placeholder) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID:             p.id,
		Version:        "1.0.0",
		Capabilities:   p.caps,
		Class:          collector.Inventory,
		DefaultTimeout: time.Second,
	}
}

func (p placeholder) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	out := make(map[core.Capability]core.CapabilityStatus, len(p.caps))
	for _, c := range p.caps {
		out[c] = core.CapabilityStatus{Availability: core.Unsupported, Reason: p.reason}
	}
	return out
}

func (p placeholder) Collect(ctx context.Context) collector.Result {
	return collector.Result{CollectedAt: time.Now().UTC(), Status: p.Probe(ctx)}
}

func (placeholder) Close(context.Context) error { return nil }
