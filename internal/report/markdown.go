package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Markdown renders a report for people. Every string from the agent is
// escaped, since host data (device names, descriptions, event messages)
// is untrusted and could otherwise inject Markdown or HTML.
func Markdown(r *apiv1.Report) []byte {
	var w mdWriter
	w.line("# Cockpit diagnostic report")
	w.line("")
	w.kv("Generated", r.GeneratedAt.UTC().Format(time.RFC3339))
	w.kv("Agent", fmt.Sprintf("%s (API %s), up %s", r.Status.Agent.Version, r.Status.Agent.APIVersion, duration(r.Status.Agent.UptimeSeconds)))
	w.kv("Machine ID", r.MachineID)
	if r.Anonymized {
		w.kv("Anonymized", "yes: host names, addresses, MAC addresses and user names are placeholders")
	} else {
		w.kv("Anonymized", "no")
	}
	w.line("")
	w.line("Sensitive fields (serial numbers, MAC addresses, WWNs, UUIDs, command lines) are always withheld.")

	if s := r.System; s != nil {
		w.heading("System")
		if d := w.section(s.System.Status, s.System.Reason, s.System.Data != nil); d {
			v := s.System.Data
			w.kv("Host name", v.Hostname)
			w.kv("Operating system", first(v.OS.PrettyName, v.OS.Name))
			w.kv("Kernel", v.Kernel)
			w.kv("Architecture", v.Architecture)
			if v.UptimeSeconds != nil {
				w.kv("Uptime", duration(*v.UptimeSeconds))
			}
		}
	}

	if h := r.Health; h != nil {
		w.heading("Health")
		w.kv("Overall", strings.ToUpper(h.Status))
		w.line("")
		w.table([]string{"Check", "Status", "Summary"}, func(row func(...string)) {
			for _, c := range h.Checks {
				summary := c.Summary
				if c.Availability != apiv1.Available && c.Reason != "" {
					summary = c.Reason
				}
				row(c.Name, c.Status, summary)
			}
		})
		var findings []apiv1.Finding
		for _, c := range h.Checks {
			findings = append(findings, c.Findings...)
		}
		if len(findings) > 0 {
			w.line("")
			w.line("**Active findings**")
			w.line("")
			for _, f := range findings {
				w.line(fmt.Sprintf("- **%s** %s: %s (since %s)", esc(strings.ToUpper(f.Severity)), esc(f.Subject), esc(f.Summary), f.Since.UTC().Format(time.RFC3339)))
				if f.Recommendation != "" {
					w.line("  - " + esc(f.Recommendation))
				}
				keys := make([]string, 0, len(f.Evidence))
				for k := range f.Evidence {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					w.line(fmt.Sprintf("  - %s: %s", esc(k), esc(f.Evidence[k])))
				}
			}
		}
	}

	if m := r.Metrics; m != nil {
		w.heading("Current metrics")
		if c := m.CPU; c != nil && w.section(c.Status, c.Reason, c.Data != nil) {
			if c.Data.UsagePercent != nil {
				w.kv("CPU usage", fmt.Sprintf("%.1f%% of %d logical cores", *c.Data.UsagePercent, c.Data.LogicalCores))
			}
			if l := c.Data.LoadAverage; l != nil {
				w.kv("Load average", fmt.Sprintf("%.2f %.2f %.2f (1m 5m 15m)", l.One, l.Five, l.Fifteen))
			}
		}
		if mm := m.Memory; mm != nil && w.section(mm.Status, mm.Reason, mm.Data != nil) {
			d := mm.Data
			w.kv("Memory", fmt.Sprintf("%s used of %s (%.1f%%), %s available", size(d.UsedBytes), size(d.TotalBytes), d.UsedPercent, size(d.AvailableBytes)))
			if d.SwapTotalBytes > 0 {
				w.kv("Swap", fmt.Sprintf("%s used of %s (%.1f%%)", size(d.SwapUsedBytes), size(d.SwapTotalBytes), d.SwapUsedPercent))
			}
		}
		if p := m.MemoryPressure; p != nil && p.Data != nil {
			w.kv("Memory pressure (full, avg10/60/300)", fmt.Sprintf("%.2f%% %.2f%% %.2f%%", p.Data.FullAvg10Percent, p.Data.FullAvg60Percent, p.Data.FullAvg300Percent))
		}
		if s := m.Sensors; s != nil && s.Data != nil && len(*s.Data) > 0 {
			w.line("")
			w.table([]string{"Sensor", "Value", "High", "Critical"}, func(row func(...string)) {
				for _, x := range *s.Data {
					row(x.Chip+" "+x.Label, reading(x.Value, x.Unit), optReading(x.High, x.Unit), optReading(x.Critical, x.Unit))
				}
			})
		}
		if g := m.GPU; g != nil && g.Data != nil && len(*g.Data) > 0 {
			w.line("")
			w.table([]string{"GPU", "Busy", "Memory used", "Temperature", "Power"}, func(row func(...string)) {
				for _, x := range *g.Data {
					mem := "—"
					if x.MemoryUsedBytes != nil && x.MemoryTotalBytes != nil {
						mem = size(*x.MemoryUsedBytes) + " of " + size(*x.MemoryTotalBytes)
					}
					row(x.Name, optUnit(x.UtilizationPercent, "%"), mem, optUnit(x.TemperatureCelsius, " °C"), optUnit(x.PowerWatts, " W"))
				}
			})
		}
	}

	if m := r.Metrics; m != nil && m.Custom != nil && len(*m.Custom) > 0 {
		w.heading("Extension metrics")
		for _, set := range *m.Custom {
			w.line("**" + esc(set.Collector) + "**")
			w.line("")
			if set.DroppedSeries > 0 {
				w.line(esc(fmt.Sprintf("%d metrics dropped: %s", set.DroppedSeries, set.Problem)))
				w.line("")
			}
			if !w.section(set.Status, set.Reason, set.Data != nil) {
				w.line("")
				continue
			}
			w.table([]string{"Metric", "Labels", "Value"}, func(row func(...string)) {
				for _, x := range *set.Data {
					row(x.Name, labelText(x.Labels), customValue(x))
				}
			})
			w.line("")
		}
	}

	if s := r.Storage; s != nil {
		w.heading("Storage")
		if w.section(s.Filesystems.Status, s.Filesystems.Reason, s.Filesystems.Data != nil) {
			w.table([]string{"Mount point", "Type", "Used", "Size", "Used %"}, func(row func(...string)) {
				for _, f := range *s.Filesystems.Data {
					row(f.MountPoint, f.Type, size(f.UsedBytes), size(f.TotalBytes), fmt.Sprintf("%.1f%%", f.UsedPercent))
				}
			})
		}
		w.line("")
		w.line("**Disk health**")
		w.line("")
		if w.section(s.DiskHealth.Status, s.DiskHealth.Reason, s.DiskHealth.Data != nil) {
			w.table([]string{"Device", "Protocol", "SMART", "Temperature", "Endurance used", "Media errors"}, func(row func(...string)) {
				for _, d := range *s.DiskHealth.Data {
					smart := "—"
					if d.SmartPassed != nil {
						smart = map[bool]string{true: "passed", false: "FAILED"}[*d.SmartPassed]
					}
					used := "—"
					if d.PercentageUsed != nil {
						used = fmt.Sprintf("%d%%", *d.PercentageUsed)
					}
					errs := "—"
					if d.MediaErrors != nil {
						errs = fmt.Sprint(*d.MediaErrors)
					}
					row(d.Device, d.Protocol, smart, optUnit(d.TemperatureCelsius, " °C"), used, errs)
				}
			})
		}
	}

	if n := r.Network; n != nil {
		w.heading("Network")
		if w.section(n.Interfaces.Status, n.Interfaces.Reason, n.Interfaces.Data != nil) {
			w.table([]string{"Interface", "State", "Addresses", "Receive", "Transmit", "Errors (rx/tx)"}, func(row func(...string)) {
				for _, i := range *n.Interfaces.Data {
					row(i.Name, i.OperState, strings.Join(i.Addresses, ", "), rate(i.RxBytesPerSecond), rate(i.TxBytesPerSecond),
						fmt.Sprintf("%d / %d", i.RxErrors, i.TxErrors))
				}
			})
		}
		if d := n.DNS.Data; d != nil {
			w.line("")
			w.kv("DNS servers", strings.Join(d.Nameservers, ", "))
			if len(d.Search) > 0 {
				w.kv("DNS search", strings.Join(d.Search, ", "))
			}
		}
	}

	if s := r.Services; s != nil {
		w.heading("Services")
		if w.section(s.Services.Status, s.Services.Reason, s.Services.Data != nil) {
			d := s.Services.Data
			w.kv("Units", fmt.Sprintf("%d total, %d active, %d failed, %d inactive", d.Total, d.Active, d.Failed, d.Inactive))
			var failed []apiv1.Service
			for _, x := range d.Items {
				if x.ActiveState == "failed" {
					failed = append(failed, x)
				}
			}
			if len(failed) > 0 {
				w.line("")
				w.table([]string{"Failed unit", "Description", "State"}, func(row func(...string)) {
					for _, x := range failed {
						row(x.Name, x.Description, x.SubState)
					}
				})
			}
		}
	}

	if h := r.Hardware; h != nil {
		w.heading("Hardware")
		if c := h.CPU.Data; c != nil {
			w.kv("CPU", fmt.Sprintf("%s (%d logical cores)", c.ModelName, c.LogicalCores))
		}
		if m := h.Memory.Data; m != nil {
			w.kv("Memory", size(m.TotalBytes))
		}
		if b := h.Board.Data; b != nil {
			w.kv("System", strings.TrimSpace(b.SystemVendor+" "+b.ProductName))
			w.kv("BIOS", strings.TrimSpace(b.BIOSVendor+" "+b.BIOSVersion+" "+b.BIOSDate))
		}
		if g := h.GPUs.Data; g != nil {
			for _, x := range *g {
				gpu := x.Name
				if x.DriverVersion != "" {
					gpu += " (driver " + x.DriverVersion + ")"
				}
				w.kv("GPU", gpu)
			}
		}
		if d := h.BlockDevices.Data; d != nil && len(*d) > 0 {
			w.line("")
			w.table([]string{"Disk", "Model", "Transport", "Size"}, func(row func(...string)) {
				for _, x := range *d {
					row(x.Name, x.Model, x.Transport, size(x.SizeBytes))
				}
			})
		}
		w.line("")
		var parts []string
		if p := h.PCIDevices.Data; p != nil {
			parts = append(parts, fmt.Sprintf("%d PCI devices", len(*p)))
		}
		if u := h.USBDevices.Data; u != nil {
			parts = append(parts, fmt.Sprintf("%d USB devices", len(*u)))
		}
		if len(parts) > 0 {
			w.line(esc(strings.Join(parts, ", ")) + " (details in the JSON report).")
		}
	}

	w.heading("Collectors")
	w.table([]string{"Collector", "Version", "Unavailable capabilities"}, func(row func(...string)) {
		for _, c := range r.Status.Collectors {
			var missing []string
			for _, cp := range c.Capabilities {
				if cp.Status != apiv1.Available {
					s := cp.Name + ": " + cp.Status
					if cp.Reason != "" {
						s += " (" + cp.Reason + ")"
					}
					missing = append(missing, s)
				}
			}
			if len(missing) == 0 {
				missing = []string{"none"}
			}
			row(c.ID, c.Version, strings.Join(missing, "; "))
		}
	})

	if e := r.Events; e != nil {
		w.heading("Recent events")
		if len(e.Items) == 0 {
			w.line("No events recorded.")
		} else {
			w.table([]string{"Time", "Severity", "Type", "Subject", "Message"}, func(row func(...string)) {
				for _, x := range e.Items {
					row(x.Time.UTC().Format(time.RFC3339), x.Severity, x.Type, x.Subject, x.Message)
				}
			})
		}
	}

	if s := r.Settings; s != nil {
		w.heading("Health thresholds")
		w.kv("Source", s.Source)
		h := s.Health
		w.line("")
		w.table([]string{"Threshold", "Warning", "Critical"}, func(row func(...string)) {
			row("CPU temperature (°C)", num(h.CPUTemperature.Warn), num(h.CPUTemperature.Critical))
			row("Available memory below (%)", num(h.MemAvailable.Warn), num(h.MemAvailable.Critical))
			row("Memory pressure (%)", num(h.MemPressure.Warn), num(h.MemPressure.Critical))
			row("Storage used (%)", num(h.Storage.Warn), num(h.Storage.Critical))
			row("Network errors (/min)", num(h.NetworkErrors.Warn), num(h.NetworkErrors.Critical))
			row("Disk endurance used (%)", fmt.Sprint(h.Disk.WarnPercentageUsed), "—")
		})
		w.line("")
		w.kv("Confirm changes over", fmt.Sprintf("%d evaluations", h.Confirm))
	}

	if len(r.Omitted) > 0 {
		w.heading("Not included")
		for _, o := range r.Omitted {
			w.line(fmt.Sprintf("- %s: %s", esc(o.Section), esc(o.Reason)))
		}
	}
	return []byte(w.b.String())
}

type mdWriter struct{ b strings.Builder }

func (w *mdWriter) line(s string) { w.b.WriteString(s + "\n") }

func (w *mdWriter) heading(s string) { w.line(""); w.line("## " + s); w.line("") }

// kv writes an escaped "label: value" line; empty values are skipped.
func (w *mdWriter) kv(k, v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	w.line("- **" + esc(k) + ":** " + esc(v))
}

// section reports whether data can be shown, writing the reason when not.
func (w *mdWriter) section(status, reason string, hasData bool) bool {
	if hasData {
		return true
	}
	msg := "Not available (" + status + ")"
	if reason != "" {
		msg += ": " + reason
	}
	w.line("_" + esc(msg) + "_")
	return false
}

func (w *mdWriter) table(header []string, rows func(row func(...string))) {
	cells := func(c []string) string {
		for i := range c {
			c[i] = cell(c[i])
		}
		return "| " + strings.Join(c, " | ") + " |"
	}
	w.line(cells(append([]string(nil), header...)))
	sep := make([]string, len(header))
	for i := range sep {
		sep[i] = "---"
	}
	w.line("| " + strings.Join(sep, " | ") + " |")
	rows(func(c ...string) { w.line(cells(c)) })
}

// esc makes untrusted text inert in Markdown: control and bidirectional
// formatting characters are removed, newlines become spaces, and the
// characters that can start emphasis, code, links, HTML, entities or a
// table cell are backslash-escaped. Every escaped string is placed after
// a list marker or inside a table cell, so line-start syntax (#, +, 1.)
// cannot take effect.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == ' ' || r == ' ':
		case strings.ContainsRune("\\`*_[]<>|~&", r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func cell(s string) string {
	s = esc(s)
	if s == "" {
		return "—"
	}
	return s
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func size(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func rate(v *float64) string {
	if v == nil {
		return "—"
	}
	return size(uint64(math.Max(0, *v))) + "/s"
}

func num(v float64) string { return fmt.Sprintf("%g", v) }

func optUnit(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%s", *v, unit)
}

var units = map[string]string{"celsius": " °C", "rpm": " rpm", "volts": " V"}

func reading(v float64, unit string) string { return fmt.Sprintf("%.1f%s", v, units[unit]) }

func optReading(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	return reading(*v, unit)
}

func duration(sec float64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func labelText(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, ", ")
}

func customValue(m apiv1.CustomMetric) string {
	if m.Kind == "counter" {
		if m.RatePerSecond == nil {
			return fmt.Sprintf("%g %s total", m.Value, m.Unit)
		}
		return fmt.Sprintf("%.4g %s", *m.RatePerSecond, m.RateUnit)
	}
	return fmt.Sprintf("%.4g %s", m.Value, m.Unit)
}
