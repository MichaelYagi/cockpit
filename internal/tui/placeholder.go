// Package tui is Cockpit's terminal user interface.
//
// It is an API client: it may import only pkg/client, pkg/apiv1 and UI
// libraries. This is enforced by internal/archtest.
package tui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// PrintSummary fetches the agent status and prints a plain-text summary
// (cockpit tui --once).
func PrintSummary(ctx context.Context, c *client.Client, w io.Writer) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	sanitize(st) // API text is untrusted, here as in the interactive UI
	fmt.Fprintf(w, "Cockpit agent %s (API %s)\n", st.Agent.Version, st.Agent.APIVersion)
	fmt.Fprintf(w, "  machine:  %s\n", st.MachineID)
	fmt.Fprintf(w, "  uptime:   %s\n", (time.Duration(st.Agent.UptimeSeconds) * time.Second).String())
	if len(st.Collectors) == 0 {
		fmt.Fprintln(w, "  collectors: none")
	}
	for _, col := range st.Collectors {
		ext := ""
		if col.External {
			ext = fmt.Sprintf(" (extension, SDK v%d)", col.SDKVersion)
		}
		fmt.Fprintf(w, "  collector %s %s%s\n", col.ID, col.Version, ext)
		for _, cp := range col.Capabilities {
			line := fmt.Sprintf("    %-22s %s", cp.Name, label(cp.Status))
			if cp.Reason != "" {
				line += " — " + cp.Reason
			}
			fmt.Fprintln(w, line)
		}
	}
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
