package extension

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

type fake struct {
	d collectorsdk.Descriptor
	r collectorsdk.Result
}

func (f fake) Descriptor() collectorsdk.Descriptor { return f.d }
func (f fake) Probe(context.Context) map[string]collectorsdk.Status {
	return map[string]collectorsdk.Status{"custom.demo.x": {Availability: collectorsdk.PermissionDenied, Reason: "no"}}
}
func (f fake) Collect(context.Context) collectorsdk.Result { return f.r }
func (f fake) Close(context.Context) error                 { return nil }

func good() collectorsdk.Descriptor {
	return collectorsdk.Descriptor{ID: "custom.demo", Version: "1.0.0", SDKVersion: collectorsdk.Version, Capabilities: []string{"custom.demo.x"}}
}

func TestAdaptDefaultsAndConversion(t *testing.T) {
	d := good()
	d.History = []string{"custom.demo.open"}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cs, err := Adapt([]collectorsdk.Collector{fake{d: d, r: collectorsdk.Result{CollectedAt: at,
		Metrics: []collectorsdk.Metric{{Name: "custom.demo.open", Kind: collectorsdk.Gauge, Unit: "count", Labels: map[string]string{"a": "b"}, Value: 3}},
		Status:  map[string]collectorsdk.Status{"custom.demo.x": {Availability: collectorsdk.Available}}}}})
	if err != nil || len(cs) != 1 {
		t.Fatal(err)
	}
	desc := cs[0].Descriptor()
	if desc.DefaultInterval != 10*time.Second || desc.DefaultTimeout != 5*time.Second || desc.MinInterval != time.Second ||
		!desc.GenericMetrics || !desc.External || desc.SDKVersion != 1 || desc.Source != "extension" || desc.HistoryMetrics[0] != "custom.demo.open" {
		t.Errorf("descriptor %+v", desc)
	}
	if s := cs[0].Probe(context.Background())[core.Capability("custom.demo.x")]; s.Availability != core.PermissionDenied || s.Reason != "no" {
		t.Errorf("probe %+v", s)
	}
	r := cs[0].Collect(context.Background())
	ms := r.Records[0].(core.GenericMetrics)
	if !r.CollectedAt.Equal(at) || len(ms) != 1 || ms[0].Kind != core.Gauge || ms[0].Labels["a"] != "b" || ms[0].Value != 3 {
		t.Errorf("result %+v", r)
	}
}

func TestAdaptRejects(t *testing.T) {
	cases := map[string]func(*collectorsdk.Descriptor){
		"SDK version":             func(d *collectorsdk.Descriptor) { d.SDKVersion = 2 },
		`look like "custom.name"`: func(d *collectorsdk.Descriptor) { d.ID = "linux.cpu" },
		"version is required":     func(d *collectorsdk.Descriptor) { d.Version = "" },
		"at least one capability": func(d *collectorsdk.Descriptor) { d.Capabilities = nil },
		"must start with":         func(d *collectorsdk.Descriptor) { d.Capabilities = []string{"custom.other.x"} },
		"below the minimum":       func(d *collectorsdk.Descriptor) { d.Interval = 100 * time.Millisecond },
		"no longer than":          func(d *collectorsdk.Descriptor) { d.Interval, d.Timeout = 2*time.Second, 3*time.Second },
		"history metric":          func(d *collectorsdk.Descriptor) { d.History = []string{"other.x"} },
	}
	for want, mutate := range cases {
		d := good()
		mutate(&d)
		if _, err := Adapt([]collectorsdk.Collector{fake{d: d}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// Adapted extension collectors keep the platform contract too.
func TestAdaptedConformance(t *testing.T) {
	d := good()
	d.Interval = time.Second
	cs, err := Adapt([]collectorsdk.Collector{fake{d: d, r: collectorsdk.Result{
		Metrics: []collectorsdk.Metric{{Name: "custom.demo.x", Kind: collectorsdk.Gauge, Unit: "count", Value: 1}},
		Status:  map[string]collectorsdk.Status{"custom.demo.x": {Availability: collectorsdk.Available}}}}})
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, cs)
}
