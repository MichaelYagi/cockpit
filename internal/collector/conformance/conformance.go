// Package conformance checks that a platform's collectors keep the
// collector contract (docs/PLUGINS.md §3). Every platform package runs it
// against its real collectors on a real host in CI, so macOS and Windows
// behavior is checked where it can actually be exercised.
package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// maxReason bounds status reasons shown to clients.
const maxReason = 300

// Run checks every collector, one subtest each. Call it from a _test.go
// file with the platform's collectors built from a real or fixture Env.
func Run(t *testing.T, cs []collector.Collector) {
	t.Helper()
	if len(cs) == 0 {
		t.Fatal("no collectors")
	}
	seen := map[string]bool{}
	for _, c := range cs {
		id := c.Descriptor().ID
		t.Run(id, func(t *testing.T) {
			if seen[id] {
				t.Errorf("duplicate collector ID %s", id)
			}
			seen[id] = true
			for _, p := range Check(c) {
				t.Error(p)
			}
		})
	}
}

// Check returns every contract violation of one collector.
func Check(c collector.Collector) []string {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	d := c.Descriptor()
	if err := d.Validate(); err != nil {
		return []string{"invalid descriptor: " + err.Error()}
	}
	declared := map[core.Capability]bool{}
	for _, cp := range d.Capabilities {
		declared[cp] = true
	}

	probe, ok := call(d, func(ctx context.Context) map[core.Capability]core.CapabilityStatus { return c.Probe(ctx) })
	if !ok {
		return append(problems, fmt.Sprintf("Probe did not return within its timeout of %s", d.DefaultTimeout))
	}
	checkStatuses(fail, "Probe", declared, probe)

	res, ok := call(d, func(ctx context.Context) collector.Result { return c.Collect(ctx) })
	if !ok {
		return append(problems, fmt.Sprintf("Collect did not return within its timeout of %s", d.DefaultTimeout))
	}
	checkStatuses(fail, "Collect", declared, res.Status)
	available := false
	for _, s := range res.Status {
		available = available || s.Availability == core.Available
	}
	if _, lister := c.(collector.ProcessLister); available && len(res.Records) == 0 && !lister {
		fail("Collect reports an available capability but returned no records")
	}
	for _, r := range res.Records {
		if r == nil {
			fail("Collect returned a nil record")
			continue
		}
		if _, generic := r.(core.GenericMetrics); generic != d.GenericMetrics {
			fail("record %T does not match GenericMetrics=%v", r, d.GenericMetrics)
		}
	}

	// A cancelled context returns promptly and never panics.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	func() {
		defer func() {
			if p := recover(); p != nil {
				fail("Collect panicked on a cancelled context: %v", p)
			}
		}()
		_ = c.Collect(ctx)
	}()
	if took := time.Since(start); took > time.Second {
		fail("Collect took %v after cancellation", took)
	}
	if err := c.Close(context.Background()); err != nil {
		fail("Close: %v", err)
	}
	return problems
}

// call runs fn with the collector's timeout; ok is false if it overran.
func call[T any](d collector.Descriptor, fn func(context.Context) T) (T, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), d.DefaultTimeout)
	defer cancel()
	done := make(chan T, 1)
	go func() { done <- fn(ctx) }()
	select {
	case v := <-done:
		return v, true
	case <-time.After(d.DefaultTimeout + 2*time.Second):
		var zero T
		return zero, false
	}
}

// checkStatuses requires a valid status, with a safe reason when not
// available, for exactly the declared capabilities.
func checkStatuses(fail func(string, ...any), what string, declared map[core.Capability]bool, got map[core.Capability]core.CapabilityStatus) {
	for cp := range declared {
		if _, ok := got[cp]; !ok {
			fail("%s does not report declared capability %s", what, cp)
		}
	}
	for cp, s := range got {
		if !declared[cp] {
			fail("%s reports undeclared capability %s", what, cp)
		}
		if !s.Availability.Valid() {
			fail("%s: %s has invalid availability %q", what, cp, s.Availability)
		}
		if s.Availability != core.Available && strings.TrimSpace(s.Reason) == "" {
			fail("%s: %s is %s without a reason", what, cp, s.Availability)
		}
		if len(s.Reason) > maxReason {
			fail("%s: %s reason is longer than %d bytes", what, cp, maxReason)
		}
		for _, r := range s.Reason {
			if unicode.IsControl(r) {
				fail("%s: %s reason contains control character %U", what, cp, r)
				break
			}
		}
	}
}
