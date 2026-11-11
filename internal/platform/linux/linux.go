// Package linux contains Cockpit's Linux collectors.
//
// Collectors read through the roots in collector.Env so they can be tested
// against fixture trees under testdata/. Phase 0 ships no collectors; they
// arrive in Phase 1.
package linux

import "github.com/MichaelYagi/cockpit/internal/collector"

// Collectors returns the Linux collector set, listed explicitly (no init()
// registration).
func Collectors(env collector.Env) []collector.Collector {
	_ = env
	return nil
}
