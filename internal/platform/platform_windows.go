//go:build windows

package platform

import (
	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/platform/windows"
)

// Collectors returns the collector set for the build platform (D32).
func Collectors(env collector.Env) []collector.Collector {
	return windows.Collectors(env)
}
