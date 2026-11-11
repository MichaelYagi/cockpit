package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/collectortest"
	"github.com/MichaelYagi/cockpit/internal/core"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNewRejectsInvalidAndDuplicate(t *testing.T) {
	bad := collectortest.New("Bad ID")
	if _, err := New([]collector.Collector{bad}, quiet); err == nil {
		t.Error("invalid descriptor accepted")
	}
	a, b := collectortest.New("test.a"), collectortest.New("test.a")
	if _, err := New([]collector.Collector{a, b}, quiet); err == nil {
		t.Error("duplicate id accepted")
	}
}

func TestStartIsolatesFailures(t *testing.T) {
	ok := collectortest.New("test.ok", core.CapCPUUsage, core.CapCPULoad)
	ok.ProbeStatus = map[core.Capability]core.CapabilityStatus{
		core.CapCPULoad: {Availability: core.PermissionDenied, Reason: "no access"},
	}
	panics := collectortest.New("test.panics", core.CapMemoryUsage)
	panics.ProbePanics = true
	slow := collectortest.New("test.slow", core.CapSensorsThermal)
	slow.Desc.DefaultTimeout = 20 * time.Millisecond
	slow.ProbeDelay = time.Hour // returns when ctx is cancelled

	e, err := New([]collector.Collector{slow, panics, ok}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	e.Start(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Start took %v; slow collector blocked startup", d)
	}

	got := map[string]core.CollectorStatus{}
	ids := []string{}
	for _, s := range e.Collectors() {
		got[s.ID] = s
		ids = append(ids, s.ID)
	}
	if want := []string{"test.ok", "test.panics", "test.slow"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("collectors not sorted: %v", ids)
	}
	if s := got["test.ok"].Capabilities[core.CapCPUUsage]; s.Availability != core.Available {
		t.Errorf("ok usage: %+v", s)
	}
	if s := got["test.ok"].Capabilities[core.CapCPULoad]; s.Availability != core.PermissionDenied {
		t.Errorf("ok load: %+v", s)
	}
	if s := got["test.panics"].Capabilities[core.CapMemoryUsage]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("panicking collector: %+v", s)
	}
	if s := got["test.slow"].Capabilities[core.CapSensorsThermal]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("slow collector: %+v", s)
	}
}

func TestMissingCapabilityReported(t *testing.T) {
	f := collectortest.New("test.partial", core.CapCPUUsage)
	f.ProbeStatus = map[core.Capability]core.CapabilityStatus{core.CapCPUUsage: {Availability: "bogus"}}
	e, err := New([]collector.Collector{f}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e.Start(context.Background())
	if s := e.Collectors()[0].Capabilities[core.CapCPUUsage]; s.Availability != core.TemporarilyUnavailable {
		t.Errorf("invalid availability not normalized: %+v", s)
	}
}

func TestCloseClosesAll(t *testing.T) {
	a := collectortest.New("test.a")
	b := collectortest.New("test.b")
	b.CloseErr = errors.New("boom")
	e, err := New([]collector.Collector{a, b}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(context.Background()); err == nil {
		t.Error("close error not reported")
	}
	if !a.Closed() || !b.Closed() {
		t.Error("not all collectors closed")
	}
}
