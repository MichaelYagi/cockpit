//go:build linux

package platform

import (
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/platform/linux"
)

// Collectors returns the collector set for the build platform.
func Collectors(env collector.Env) []collector.Collector {
	return linux.Collectors(env)
}
