//go:build linux

package platform

import (
	"context"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/platform/linux"
)

// Collectors returns the collector set for the build platform.
func Collectors(env collector.Env) []collector.Collector {
	return linux.Collectors(env)
}

// SMARTHelper runs the privileged SMART helper once (D20) and returns the
// number of result files written.
func SMARTHelper(ctx context.Context, env collector.Env, dir string, gid int) (int, error) {
	return linux.WriteSMARTResults(ctx, env, linux.SMARTHelperOptions{Dir: dir, GID: gid})
}
