package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func result(st map[core.Capability]core.CapabilityStatus, recs ...core.Record) func(context.Context) collector.Result {
	return func(context.Context) collector.Result { return collector.Result{Status: st, Records: recs} }
}

// The suite must catch each kind of violation, or a green run means nothing.
func TestCheckFindsViolations(t *testing.T) {
	ok := collectortest.New("test.ok", core.CapCPUUsage)
	ok.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Available}}, &core.CPUCounters{})
	if p := Check(ok); len(p) != 0 {
		t.Errorf("good collector: %v", p)
	}

	cases := map[string]func(f *collectortest.Fake){
		"does not report declared": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{})
		},
		"reports undeclared": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Available}, core.CapCPULoad: {Availability: core.Available}}, &core.CPUCounters{})
		},
		"without a reason": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.PermissionDenied}})
		},
		"invalid availability": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: "maybe", Reason: "x"}})
		},
		"control character": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Unsupported, Reason: "no\x1b[31m"}})
		},
		"returned no records": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Available}})
		},
		"does not match GenericMetrics": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Available}}, core.GenericMetrics{})
		},
		"panicked on a cancelled context": func(f *collectortest.Fake) {
			f.CollectFunc = func(ctx context.Context) collector.Result {
				if ctx.Err() != nil {
					panic("boom")
				}
				return collector.Result{Status: map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Unsupported, Reason: "x"}}}
			}
		},
		"Close": func(f *collectortest.Fake) {
			f.CollectFunc = result(map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: core.Unsupported, Reason: "x"}})
			f.CloseErr = errors.New("stuck")
		},
	}
	for want, mutate := range cases {
		f := collectortest.New("test.bad", core.CapCPUUsage)
		mutate(f)
		p := Check(f)
		if !strings.Contains(strings.Join(p, "\n"), want) {
			t.Errorf("%s: not detected; got %v", want, p)
		}
	}
}
