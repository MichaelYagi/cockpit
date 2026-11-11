package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/platform"
)

// SMARTResultsDir is where the SMART helper writes results (D20).
func SMARTResultsDir(p config.Paths) string { return filepath.Join(p.StateDir, "smart") }

// RunSMARTHelper runs the privileged SMART helper once (D20): smartctl for
// each disk, results written to dir, readable by group (empty: keep the
// default group). smartctl is looked up in the system directories only,
// never through $PATH, because the helper runs as root.
func RunSMARTHelper(ctx context.Context, dir, group string, log *slog.Logger) (int, error) {
	gid := -1
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return 0, fmt.Errorf("group %q: %w (create it with systemd-sysusers, or pass --group \"\")", group, err)
		}
		if gid, err = strconv.Atoi(g.Gid); err != nil {
			return 0, fmt.Errorf("group %q has a non-numeric id %q", group, g.Gid)
		}
	}
	env := collector.DefaultEnv(log, systemTools{})
	return platform.SMARTHelper(ctx, env, dir, gid)
}

// systemTools finds tools in fixed system directories only.
type systemTools struct{}

func (systemTools) Find(name string) (string, bool) {
	for _, d := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}
