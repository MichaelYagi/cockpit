// Package extension adapts public SDK collectors (pkg/collectorsdk, D23)
// to the internal collector contract, so they get the same scheduling,
// timeouts, panic recovery and availability reporting as built-ins.
package extension

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

const (
	defaultInterval = 10 * time.Second
	minInterval     = time.Second
	defaultTimeout  = 5 * time.Second
)

var idPattern = regexp.MustCompile(`^custom\.[a-z][a-z0-9_]*$`)

// Adapt wraps SDK collectors. It rejects collectors for another SDK major
// version and descriptors that break the naming rules, so a bad build
// fails at start instead of reporting nothing.
func Adapt(cs []collectorsdk.Collector) ([]collector.Collector, error) {
	out := make([]collector.Collector, 0, len(cs))
	var errs []error
	for _, c := range cs {
		a, err := adapt(c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, a)
	}
	return out, errors.Join(errs...)
}

type adapter struct {
	c    collectorsdk.Collector
	desc collector.Descriptor
}

func adapt(c collectorsdk.Collector) (*adapter, error) {
	d := c.Descriptor()
	fail := func(format string, args ...any) error {
		return fmt.Errorf("extension collector %q: %s", d.ID, fmt.Sprintf(format, args...))
	}
	switch {
	case !idPattern.MatchString(d.ID):
		return nil, fail(`ID must look like "custom.name" (lower case)`)
	case d.SDKVersion != collectorsdk.Version:
		return nil, fail("built for collector SDK version %d; this agent supports version %d", d.SDKVersion, collectorsdk.Version)
	case d.Version == "":
		return nil, fail("version is required")
	case len(d.Capabilities) == 0:
		return nil, fail("at least one capability is required")
	}
	interval, timeout := d.Interval, d.Timeout
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < minInterval {
		return nil, fail("interval %s is below the minimum of %s", interval, minInterval)
	}
	if timeout == 0 {
		timeout = min(defaultTimeout, interval)
	}
	if timeout <= 0 || timeout > interval {
		return nil, fail("timeout must be positive and no longer than the interval")
	}
	source := d.Source
	if source == "" {
		source = "extension"
	}
	caps := make([]core.Capability, len(d.Capabilities))
	for i, name := range d.Capabilities {
		if !strings.HasPrefix(name, d.ID+".") {
			return nil, fail("capability %q must start with %q", name, d.ID+".")
		}
		caps[i] = core.Capability(name)
	}
	desc := collector.Descriptor{
		ID: d.ID, Version: d.Version, Source: source, Capabilities: caps,
		Class: collector.Sampler, DefaultInterval: interval, DefaultTimeout: timeout, MinInterval: minInterval,
		GenericMetrics: true, HistoryMetrics: append([]string(nil), d.History...),
		External: true, SDKVersion: d.SDKVersion,
	}
	if err := desc.Validate(); err != nil {
		return nil, err
	}
	return &adapter{c: c, desc: desc}, nil
}

func (a *adapter) Descriptor() collector.Descriptor { return a.desc }

func (a *adapter) Probe(ctx context.Context) map[core.Capability]core.CapabilityStatus {
	return statuses(a.c.Probe(ctx))
}

func (a *adapter) Collect(ctx context.Context) collector.Result {
	r := a.c.Collect(ctx)
	ms := make(core.GenericMetrics, len(r.Metrics))
	for i, m := range r.Metrics {
		ms[i] = core.GenericMetric{Name: m.Name, Kind: core.MetricKind(m.Kind), Unit: m.Unit, Labels: m.Labels, Value: m.Value}
	}
	return collector.Result{CollectedAt: r.CollectedAt, Records: []core.Record{ms}, Status: statuses(r.Status)}
}

func (a *adapter) Close(ctx context.Context) error { return a.c.Close(ctx) }

// statuses converts SDK statuses; invalid availabilities are normalized
// by the engine.
func statuses(in map[string]collectorsdk.Status) map[core.Capability]core.CapabilityStatus {
	out := make(map[core.Capability]core.CapabilityStatus, len(in))
	for k, v := range in {
		out[core.Capability(k)] = core.CapabilityStatus{Availability: core.Availability(v.Availability), Reason: v.Reason}
	}
	return out
}
