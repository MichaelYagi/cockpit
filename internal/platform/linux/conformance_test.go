//go:build linux

package linux

import (
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/collector/conformance"
)

type pathTools struct{}

func (pathTools) Find(name string) (string, bool) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(p)
	return abs, err == nil
}

// The real Linux collectors on this host keep the contract.
func TestConformanceOnThisHost(t *testing.T) {
	conformance.Run(t, Collectors(collector.DefaultEnv(slog.New(slog.DiscardHandler), pathTools{})))
}

// And against an empty fixture tree, where everything is missing.
func TestConformanceOnEmptyTree(t *testing.T) {
	conformance.Run(t, Collectors(env(t.TempDir())))
}
