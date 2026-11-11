// Package unsupported provides the collector set for platforms Cockpit does
// not support yet. It reports every known capability as unsupported, so the
// binary still runs and clients get an explicit answer.
package unsupported

import (
	"context"
	"runtime"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Collectors returns the collector set for an unsupported platform.
func Collectors(collector.Env) []collector.Collector {
	return []collector.Collector{placeholder{}}
}

type placeholder struct{}

func (placeholder) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID:             "unsupported.platform",
		Version:        "1.0.0",
		Capabilities:   core.KnownCapabilities(),
		Class:          collector.Inventory,
		DefaultTimeout: time.Second,
	}
}

func (placeholder) Probe(context.Context) map[core.Capability]core.CapabilityStatus {
	out := make(map[core.Capability]core.CapabilityStatus)
	for _, c := range core.KnownCapabilities() {
		out[c] = core.CapabilityStatus{
			Availability: core.Unsupported,
			Reason:       "Cockpit has no collectors for " + runtime.GOOS + " yet.",
		}
	}
	return out
}

func (p placeholder) Collect(ctx context.Context) collector.Result {
	return collector.Result{CollectedAt: time.Now().UTC(), Status: p.Probe(ctx)}
}

func (placeholder) Close(context.Context) error { return nil }
