// Package main is an example Cockpit build with one extension collector
// (docs/PLUGINS.md §8). It reports kernel file handle usage and the
// context switch rate, which the built-in collectors do not cover.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/collectorsdk"
)

const (
	capHandles  = "custom.files.handles"
	capSwitches = "custom.files.context_switches"
)

// files reads /proc. procRoot is "/proc" in production and a fixture
// directory in tests.
type files struct{ procRoot string }

func (files) Descriptor() collectorsdk.Descriptor {
	return collectorsdk.Descriptor{
		ID: "custom.files", Version: "1.0.0", SDKVersion: collectorsdk.Version,
		Capabilities: []string{capHandles, capSwitches},
		Interval:     10 * time.Second,
		Timeout:      time.Second,
		Source:       "procfs",
		History:      []string{"custom.files.used_percent", "custom.files.context_switches"},
	}
}

func (f files) Probe(context.Context) map[string]collectorsdk.Status {
	r := f.collect()
	return r.Status
}

func (f files) Collect(ctx context.Context) collectorsdk.Result {
	if ctx.Err() != nil {
		return collectorsdk.Result{}
	}
	return f.collect()
}

func (files) Close(context.Context) error { return nil }

func (f files) collect() collectorsdk.Result {
	res := collectorsdk.Result{Status: map[string]collectorsdk.Status{}}

	// /proc/sys/fs/file-nr: allocated, unused (always 0 since 2.6), max.
	if allocated, max, err := f.fileNR(); err != nil {
		res.Status[capHandles] = unavailable(err)
	} else {
		res.Status[capHandles] = collectorsdk.Status{Availability: collectorsdk.Available}
		res.Metrics = append(res.Metrics, collectorsdk.Metric{Name: "custom.files.open", Kind: collectorsdk.Gauge, Unit: "count", Value: allocated})
		// Recent kernels report an unlimited maximum as LONG_MAX; a
		// percentage of that means nothing, so it is left out.
		if max > 0 && max < 1<<62 {
			res.Metrics = append(res.Metrics, collectorsdk.Metric{Name: "custom.files.max", Kind: collectorsdk.Gauge, Unit: "count", Value: max})
			res.Metrics = append(res.Metrics, collectorsdk.Metric{Name: "custom.files.used_percent", Kind: collectorsdk.Gauge, Unit: "percent",
				Value: allocated / max * 100})
		}
	}

	// /proc/stat "ctxt": context switches since boot, a counter.
	if ctxt, err := f.contextSwitches(); err != nil {
		res.Status[capSwitches] = unavailable(err)
	} else {
		res.Status[capSwitches] = collectorsdk.Status{Availability: collectorsdk.Available}
		res.Metrics = append(res.Metrics, collectorsdk.Metric{Name: "custom.files.context_switches", Kind: collectorsdk.Counter, Unit: "count", Value: ctxt})
	}
	return res
}

func (f files) fileNR() (allocated, max float64, err error) {
	b, err := os.ReadFile(filepath.Join(f.procRoot, "sys/fs/file-nr"))
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) != 3 {
		return 0, 0, fmt.Errorf("unexpected file-nr format")
	}
	if allocated, err = strconv.ParseFloat(fields[0], 64); err == nil {
		max, err = strconv.ParseFloat(fields[2], 64)
	}
	return allocated, max, err
}

func (f files) contextSwitches() (float64, error) {
	b, err := os.ReadFile(filepath.Join(f.procRoot, "stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ctxt "); ok {
			return strconv.ParseFloat(strings.TrimSpace(v), 64)
		}
	}
	return 0, fmt.Errorf("no ctxt line")
}

// unavailable turns a read error into a status with a safe reason.
func unavailable(err error) collectorsdk.Status {
	switch {
	case os.IsNotExist(err):
		return collectorsdk.Status{Availability: collectorsdk.Unsupported, Reason: "This kernel does not provide the counter."}
	case os.IsPermission(err):
		return collectorsdk.Status{Availability: collectorsdk.PermissionDenied, Reason: "Reading the counter is not permitted."}
	}
	return collectorsdk.Status{Availability: collectorsdk.TemporarilyUnavailable, Reason: "The counter could not be parsed."}
}
