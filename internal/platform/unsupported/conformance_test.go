package unsupported

import (
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, Collectors(collector.Env{}))
}
