//go:build !linux

package platform

import (
	"context"
	"errors"

	"github.com/MichaelYagi/cockpit/internal/collector"
)

// SMARTHelper is available on Linux only.
func SMARTHelper(context.Context, collector.Env, string, int) (int, error) {
	return 0, errors.New("the SMART helper is available on Linux only")
}
