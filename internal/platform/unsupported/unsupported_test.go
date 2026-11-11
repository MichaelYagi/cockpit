package unsupported

import (
	"context"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

func TestEverythingUnsupported(t *testing.T) {
	cs := Collectors(collector.Env{})
	if len(cs) != 1 {
		t.Fatalf("want 1 collector, got %d", len(cs))
	}
	c := cs[0]
	if err := c.Descriptor().Validate(); err != nil {
		t.Fatal(err)
	}
	st := c.Probe(context.Background())
	for _, capability := range core.KnownCapabilities() {
		s, ok := st[capability]
		if !ok || s.Availability != core.Unsupported || s.Reason == "" {
			t.Errorf("%s: got %+v", capability, s)
		}
	}
}
