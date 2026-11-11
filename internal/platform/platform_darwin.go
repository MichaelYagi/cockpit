//go:build darwin

package platform

import (
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/platform/darwin"
)

// Collectors returns the collector set for the build platform (D32).
func Collectors(env collector.Env) []collector.Collector {
	return darwin.Collectors(env)
}
