package core

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Generic metrics (D22) let extension collectors report data without a
// dedicated record type. They are the only open record type; built-in
// domains keep their typed records.

// MetricKind says how a generic metric's value is interpreted.
type MetricKind string

const (
	// Gauge is a reading at a point in time.
	Gauge MetricKind = "gauge"
	// Counter only increases; the engine derives a per-second rate.
	Counter MetricKind = "counter"
)

// Limits for generic metrics (D22).
const (
	MaxMetricNameLen      = 128
	MaxMetricLabels       = 8
	MaxLabelValueLen      = 128
	MaxSeriesPerCollector = 500
)

// metricUnits are the allowed units; counterUnits may be used by counters,
// with the rate unit they produce.
var (
	metricUnits = map[string]bool{
		"bytes": true, "bytes_per_second": true, "percent": true, "ratio": true, "celsius": true, "watts": true,
		"volts": true, "hertz": true, "rpm": true, "seconds": true, "count": true, "per_second": true,
	}
	counterUnits = map[string]string{"bytes": "bytes_per_second", "count": "per_second", "seconds": "seconds_per_second"}

	metricNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
	labelKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// MetricUnits lists the allowed units, sorted.
func MetricUnits() []string {
	out := make([]string, 0, len(metricUnits))
	for u := range metricUnits {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// RateUnit is the unit of a counter's per-second rate.
func RateUnit(unit string) string { return counterUnits[unit] }

// GenericMetric is one reported value.
type GenericMetric struct {
	Name   string // "<collector id>.<metric>", e.g. "custom.example.open_files"
	Kind   MetricKind
	Unit   string
	Labels map[string]string
	Value  float64
}

// GenericMetrics is the record type extension collectors return.
type GenericMetrics []GenericMetric

func (GenericMetrics) isRecord() {}

// Validate checks a metric against the D22 rules. namespace is the
// collector's ID; the name must start with it.
func (m GenericMetric) Validate(namespace string) error {
	switch {
	case len(m.Name) > MaxMetricNameLen || !metricNamePattern.MatchString(m.Name):
		return fmt.Errorf("metric name %q is not a lower-case dotted name of at most %d characters", trimForError(m.Name), MaxMetricNameLen)
	case !strings.HasPrefix(m.Name, namespace+"."):
		return fmt.Errorf("metric %q must start with the collector ID %q", m.Name, namespace+".")
	case m.Kind != Gauge && m.Kind != Counter:
		return fmt.Errorf("metric %q has kind %q; use gauge or counter", m.Name, trimForError(string(m.Kind)))
	case !metricUnits[m.Unit]:
		return fmt.Errorf("metric %q has unknown unit %q", m.Name, trimForError(m.Unit))
	case m.Kind == Counter && counterUnits[m.Unit] == "":
		return fmt.Errorf("counter %q must use bytes, count or seconds", m.Name)
	case math.IsNaN(m.Value) || math.IsInf(m.Value, 0):
		return fmt.Errorf("metric %q has a non-finite value", m.Name)
	case m.Kind == Counter && m.Value < 0:
		return fmt.Errorf("counter %q is negative", m.Name)
	case len(m.Labels) > MaxMetricLabels:
		return fmt.Errorf("metric %q has %d labels; at most %d are allowed", m.Name, len(m.Labels), MaxMetricLabels)
	}
	for k, v := range m.Labels {
		if !labelKeyPattern.MatchString(k) {
			return fmt.Errorf("metric %q has an invalid label name %q", m.Name, trimForError(k))
		}
		if len(v) > MaxLabelValueLen {
			return fmt.Errorf("metric %q label %q is longer than %d bytes", m.Name, k, MaxLabelValueLen)
		}
	}
	return nil
}

// SeriesKey identifies a metric's series: its name and sorted labels.
func (m GenericMetric) SeriesKey() string {
	if len(m.Labels) == 0 {
		return m.Name
	}
	keys := make([]string, 0, len(m.Labels))
	for k := range m.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(m.Name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", k, m.Labels[k])
	}
	b.WriteByte('}')
	return b.String()
}

func trimForError(s string) string {
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

// CustomMetric is a generic metric after the engine processed it.
type CustomMetric struct {
	Name   string
	Kind   MetricKind
	Unit   string
	Labels map[string]string
	Value  float64
	// RatePerSecond is set for counters from the second sample on.
	RatePerSecond *float64
	// History is set when the collector asked for this metric to be
	// recorded (D18 limits still apply).
	History bool
}

// CustomSet holds one collector's generic metrics.
type CustomSet struct {
	Collector string
	Meta      SectionMeta
	Metrics   []CustomMetric // sorted by series key; nil until first collected
	// Dropped counts metrics rejected in the last collection (invalid or
	// over the series limit).
	Dropped int
	// Problem is a safe description of the first rejected metric.
	Problem string
}
