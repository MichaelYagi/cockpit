// Package collectorsdk is the public contract for extension collectors
// (D23). An extension collector is Go code compiled into a custom Cockpit
// build with custom.Main; nothing is loaded at runtime.
//
// A collector reports generic metrics (D22): named gauges and counters
// with a unit and optional labels. The agent validates them, derives
// counter rates, enforces limits, records opted-in metrics in history and
// serves them in GET /api/v1/metrics?sections=custom.
//
// Extension code runs inside the agent with the agent's privileges. Only
// build in collectors you trust as much as any other dependency.
//
// A minimal collector:
//
//	type openFiles struct{}
//
//	func (openFiles) Descriptor() collectorsdk.Descriptor {
//		return collectorsdk.Descriptor{
//			ID: "custom.files", Version: "1.0.0", SDKVersion: collectorsdk.Version,
//			Capabilities: []string{"custom.files.open"}, Interval: 10 * time.Second,
//		}
//	}
//	func (openFiles) Probe(context.Context) map[string]collectorsdk.Status {
//		return map[string]collectorsdk.Status{"custom.files.open": {Availability: collectorsdk.Available}}
//	}
//	func (openFiles) Collect(ctx context.Context) collectorsdk.Result { ... }
//	func (openFiles) Close(context.Context) error { return nil }
package collectorsdk

import (
	"context"
	"time"
)

// Version is the SDK's major version. A collector sets
// Descriptor.SDKVersion to the Version it was written against; the agent
// refuses collectors built for a different major version.
const Version = 1

// Collector is implemented by extension collectors. The agent calls Probe
// once at start, Collect on its schedule (never concurrently with itself)
// and Close at shutdown. Each call has a deadline in ctx; a call that
// overruns or panics marks the collector's capabilities temporarily
// unavailable without affecting the rest of the agent.
type Collector interface {
	Descriptor() Descriptor
	Probe(ctx context.Context) map[string]Status
	Collect(ctx context.Context) Result
	Close(ctx context.Context) error
}

// Descriptor describes a collector.
type Descriptor struct {
	// ID is "custom.<name>", lower case, e.g. "custom.files". Metric and
	// capability names start with ID + ".".
	ID string
	// Version is the collector's own version (semver).
	Version string
	// SDKVersion must be collectorsdk.Version.
	SDKVersion int
	// Capabilities the collector can provide, e.g. "custom.files.open".
	// At least one.
	Capabilities []string
	// Interval between collections; default 10s, minimum 1s.
	Interval time.Duration
	// Timeout for one call; default 5s, at most Interval.
	Timeout time.Duration
	// Source is a short, non-sensitive description of where the data
	// comes from, shown to API clients (e.g. "procfs"). Default
	// "extension".
	Source string
	// History names metrics to record in metric history.
	History []string
}

// Availability of a capability.
type Availability string

// Availability values, as in the API.
const (
	Available              Availability = "available"
	Unsupported            Availability = "unsupported"
	PermissionDenied       Availability = "permission_denied"
	TemporarilyUnavailable Availability = "temporarily_unavailable"
	NotConfigured          Availability = "not_configured"
)

// Status is the availability of one capability. Reason is shown to API
// clients: keep it free of secrets and host identifiers.
type Status struct {
	Availability Availability
	Reason       string
}

// Kind says how a metric's value is interpreted.
type Kind string

const (
	// Gauge is a reading at a point in time.
	Gauge Kind = "gauge"
	// Counter only increases; the agent derives a per-second rate.
	Counter Kind = "counter"
)

// Metric is one value. See docs/PLUGINS.md §3.1 for the naming, unit and
// label rules; metrics that break them are dropped and reported.
type Metric struct {
	Name   string // ID + "." + metric, e.g. "custom.files.open"
	Kind   Kind
	Unit   string // bytes, bytes_per_second, percent, ratio, celsius, watts, volts, hertz, rpm, seconds, count, per_second
	Labels map[string]string
	Value  float64
}

// Result is what one Collect call produced. Partial results are normal:
// report each capability's status individually.
type Result struct {
	CollectedAt time.Time // zero: the agent uses the current time
	Metrics     []Metric
	Status      map[string]Status
}
