// Package tui is Cockpit's terminal user interface.
//
// It is an API client: it may import only pkg/client, pkg/apiv1 and UI
// libraries. This is enforced by internal/archtest.
//
// Phase 0 provides a plain-text placeholder that proves the TUI reaches the
// agent through the API. The Bubble Tea interface arrives in Phase 1.
package tui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// RunPlaceholder fetches the agent status and prints a summary.
func RunPlaceholder(ctx context.Context, c *client.Client, w io.Writer) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Cockpit agent %s (API %s)\n", st.Agent.Version, st.Agent.APIVersion)
	fmt.Fprintf(w, "  machine:  %s\n", st.MachineID)
	fmt.Fprintf(w, "  uptime:   %s\n", (time.Duration(st.Agent.UptimeSeconds) * time.Second).String())
	if len(st.Collectors) == 0 {
		fmt.Fprintln(w, "  collectors: none yet (Linux collectors arrive in Phase 1)")
	}
	for _, col := range st.Collectors {
		fmt.Fprintf(w, "  collector %s %s\n", col.ID, col.Version)
		for _, cp := range col.Capabilities {
			line := fmt.Sprintf("    %-22s %s", cp.Name, label(cp.Status))
			if cp.Reason != "" {
				line += " — " + cp.Reason
			}
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w, "\nThe interactive terminal UI is not implemented yet (Phase 1).")
	return nil
}

// label spells out availability so status never relies on color alone.
func label(status string) string {
	switch status {
	case apiv1.Available:
		return "available"
	case apiv1.Unsupported:
		return "unsupported"
	case apiv1.PermissionDenied:
		return "permission denied"
	case apiv1.TemporarilyUnavailable:
		return "temporarily unavailable"
	case apiv1.NotConfigured:
		return "not configured"
	}
	return status
}
