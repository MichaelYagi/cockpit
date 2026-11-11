// Package collectortest provides configurable fake collectors for tests.
package collectortest

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Fake is a collector whose behavior is set by its fields.
type Fake struct {
	Desc collector.Descriptor

	// ProbeStatus is returned by Probe. Missing capabilities default to available.
	ProbeStatus map[core.Capability]core.CapabilityStatus
	// ProbeDelay makes Probe block (until ctx is done) for this long.
	ProbeDelay time.Duration
	// ProbePanics makes Probe panic.
	ProbePanics bool
	// CloseErr is returned by Close.
	CloseErr error
	// CollectFunc, if set, implements Collect. Otherwise Collect returns
	// the probe statuses and no records.
	CollectFunc func(ctx context.Context) collector.Result

	closed atomic.Bool
}

// New returns a fake with a valid descriptor for the given capabilities.
func New(id string, caps ...core.Capability) *Fake {
	if len(caps) == 0 {
		caps = []core.Capability{core.CapCPUUsage}
	}
	return &Fake{Desc: collector.Descriptor{
		ID:              id,
		Version:         "0.0.1",
		Capabilities:    caps,
		Class:           collector.Sampler,
		DefaultInterval: time.Second,
		DefaultTimeout:  time.Second,
	}}
}

func (f *Fake) Descriptor() collector.Descriptor { return f.Desc }

func (f *Fake) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	if f.ProbePanics {
		panic("fake probe panic")
	}
	if f.ProbeDelay > 0 {
		select {
		case <-time.After(f.ProbeDelay):
		case <-ctx.Done():
		}
	}
	out := make(map[core.Capability]core.CapabilityStatus, len(f.Desc.Capabilities))
	for _, c := range f.Desc.Capabilities {
		if s, ok := f.ProbeStatus[c]; ok {
			out[c] = s
		} else {
			out[c] = core.CapabilityStatus{Availability: core.Available}
		}
	}
	return out
}

func (f *Fake) Collect(ctx context.Context) collector.Result {
	if f.CollectFunc != nil {
		return f.CollectFunc(ctx)
	}
	return collector.Result{CollectedAt: time.Now(), Status: f.Probe(ctx)}
}

func (f *Fake) Close(context.Context) error {
	f.closed.Store(true)
	return f.CloseErr
}

// Closed reports whether Close was called.
func (f *Fake) Closed() bool { return f.closed.Load() }
