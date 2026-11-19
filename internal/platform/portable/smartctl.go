package portable

// smartctl --json output (D20), and running optional tools, shared by
// every platform.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// smartctl exit status bits (see smartctl(8)).
const (
	SmartBitCmdline    = 1 << 0
	SmartBitOpenFailed = 1 << 1
)

// MaxToolOutput is the most RunTool keeps of a tool's output.
const MaxToolOutput = 1 << 20

// RunTool executes a tool without a shell and returns stdout (at most 1
// MiB) and the exit code. stderr is discarded: it may contain device
// details. env replaces the environment when not nil.
func RunTool(ctx context.Context, env []string, path string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if env != nil {
		cmd.Env = env
	}
	hideWindow(cmd)
	var out LimitedBuffer
	out.Max = MaxToolOutput
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out.Bytes(), 0, nil
}

// LimitedBuffer keeps the first Max bytes written and drops the rest.
type LimitedBuffer struct {
	bytes.Buffer
	Max int
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	if room := b.Max - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil // drop the rest; output is bounded
	}
	return b.Buffer.Write(p)
}

// deniedMessage reports a smartctl message about missing privileges, as
// Linux, macOS and Windows word it.
func deniedMessage(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "permission denied") || strings.Contains(l, "requires root") ||
		strings.Contains(l, "access denied") || strings.Contains(l, "administrator")
}

// smartctlJSON is the subset of `smartctl --json` output Cockpit uses.
type smartctlJSON struct {
	Smartctl struct {
		Messages []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *uint64 `json:"hours"`
	} `json:"power_on_time"`
	NVMe *struct {
		CriticalWarning *int    `json:"critical_warning"`
		PercentageUsed  *int    `json:"percentage_used"`
		MediaErrors     *uint64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	ATA *struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value uint64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// ParseSmartctl interprets smartctl JSON output and exit code. perm reports
// a permission failure; ok reports usable health data.
func ParseSmartctl(stdout []byte, code int) (h core.DiskHealth, perm, ok bool) {
	var j smartctlJSON
	if err := json.Unmarshal(stdout, &j); err != nil {
		return h, false, false
	}
	if code&(SmartBitCmdline|SmartBitOpenFailed) != 0 {
		for _, m := range j.Smartctl.Messages {
			if deniedMessage(m.String) {
				return h, true, false
			}
		}
		return h, false, false
	}
	if j.SmartStatus == nil {
		return h, false, false // device does not support SMART (e.g. virtual disk)
	}
	passed := j.SmartStatus.Passed
	h.Passed = &passed
	h.Protocol = j.Device.Protocol
	if j.Temperature != nil {
		h.TemperatureCelsius = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		h.PowerOnHours = j.PowerOnTime.Hours
	}
	if n := j.NVMe; n != nil {
		h.CriticalWarning, h.PercentageUsed, h.MediaErrors = n.CriticalWarning, n.PercentageUsed, n.MediaErrors
	}
	if a := j.ATA; a != nil {
		for _, attr := range a.Table {
			v := attr.Raw.Value
			switch attr.ID {
			case 5:
				h.ReallocatedSectors = &v
			case 197:
				h.PendingSectors = &v
			}
		}
	}
	return h, false, true
}
