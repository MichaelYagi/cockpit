package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// View renders the UI.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor = colBackground
	v.WindowTitle = "Cockpit"
	if m.system != nil && m.system.System.Data != nil {
		v.WindowTitle = "Cockpit · " + m.system.System.Data.Hostname
	}
	return v
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return "Starting Cockpit…"
	}
	if w < MinWidth || h < MinHeight {
		msg := fmt.Sprintf("Cockpit needs a terminal of at least %d×%d.\nThis one is %d×%d.\n\nResize the window, or press q to quit.", MinWidth, MinHeight, w, h)
		return clip(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, sText.Render(msg)), w, h)
	}
	header := m.header(w)
	tabs := m.tabs(w)
	footer := m.footer(w)
	bodyH := h - 3
	var body string
	switch {
	case m.showHelp:
		body = m.helpView(w, bodyH)
	case m.modal != modalNone:
		body = lipgloss.Place(w, bodyH, lipgloss.Center, lipgloss.Center, m.modalView(min(w-4, 76)))
	default:
		body = m.screenView(w, bodyH)
	}
	return clip(strings.Join([]string{header, tabs, fit(body, bodyH), footer}, "\n"), w, h)
}

// clip guarantees the output never exceeds the terminal.
func clip(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = truncate(l, w)
	}
	return strings.Join(lines, "\n")
}

// fit pads or cuts a block to exactly h lines.
func fit(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) header(w int) string {
	left := sTitle.Render("COCKPIT")
	if m.system != nil && m.system.System.Data != nil {
		s := m.system.System.Data
		parts := []string{sBold.Render(s.Hostname)}
		if s.OS.PrettyName != "" {
			parts = append(parts, s.OS.PrettyName)
		}
		if s.UptimeSeconds != nil {
			parts = append(parts, "up "+duration(time.Duration(*s.UptimeSeconds)*time.Second))
		}
		left += "  " + sMuted.Render(strings.Join(parts, sMuted.Render(" · ")))
	}
	right := sMuted.Render("health ") + badge(lvUnknown, "UNKNOWN")
	if m.health != nil {
		right = sMuted.Render("health ") + healthBadge(m.health.Status)
	}
	if m.lastErr != "" {
		right = badge(lvCrit, "DISCONNECTED")
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) tabs(w int) string {
	var parts []string
	for i, name := range screenNames {
		label := tabLabel(screen(i), name)
		if screen(i) == m.screen {
			parts = append(parts, sTabOn.Render(label))
		} else {
			parts = append(parts, sTabOff.Render(label))
		}
	}
	return truncate(strings.Join(parts, " "), w)
}

func (m *Model) footer(w int) string {
	hints := []string{"1-5 screens", "tab next", "r refresh", "? help", "q quit"}
	if m.screen == scrAccess {
		hints = []string{"↑↓ select", "n new key", "o rotate", "x revoke", "? help", "q quit"}
	}
	if m.modal != modalNone {
		hints = []string{"enter confirm", "esc cancel"}
	}
	for i, h := range hints {
		k, rest, _ := strings.Cut(h, " ")
		hints[i] = sKeyHint.Render(k) + " " + sMuted.Render(rest)
	}
	left := strings.Join(hints, sMuted.Render("  "))
	var right string
	switch {
	case m.flash != "":
		right = m.flashLevel.style().Render(m.flash)
	case m.lastErr != "":
		right = sCrit.Render("✖ " + m.lastErr)
	case !m.lastOK.IsZero():
		right = sMuted.Render("updated " + ago(m.lastOK, m.opts.Now()) + " · " + m.opts.Target)
	}
	if gap := w - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
		return left + strings.Repeat(" ", gap) + right
	}
	if right != "" && (m.flash != "" || m.lastErr != "") {
		return truncate(right, w) // messages matter more than hints
	}
	return truncate(left, w)
}

// ---- layout helpers ----

type box struct {
	title string
	lines []string
	flex  bool // may lose lines when space is short
}

// column stacks boxes in height h, shrinking flexible boxes if needed.
func column(boxes []box, w, h int) string {
	heights := make([]int, len(boxes))
	total := 0
	for i, b := range boxes {
		heights[i] = len(b.lines)
		total += len(b.lines) + 3
	}
	for total > h {
		best := -1
		for i, b := range boxes {
			if b.flex && heights[i] > 1 && (best < 0 || heights[i] > heights[best]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		heights[best]--
		total--
	}
	// Still too tall: drop whole boxes from the end rather than cut one
	// in half, and say so.
	used, keep := 0, 0
	for i := range boxes {
		if used+heights[i]+3 > h {
			break
		}
		used += heights[i] + 3
		keep++
	}
	// Leave a line for the "not shown" note when something was dropped.
	if keep < len(boxes) && used >= h && keep > 1 {
		keep--
		used -= heights[keep] + 3
	}
	var out []string
	for i, b := range boxes[:keep] {
		lines := b.lines
		if len(lines) > heights[i] {
			lines = append(append([]string(nil), lines[:max(0, heights[i]-1)]...), sMuted.Render(fmt.Sprintf("… %d more", len(b.lines)-heights[i]+1)))
		}
		out = append(out, panel(b.title, strings.Join(lines, "\n"), w, 0, false))
	}
	if dropped := len(boxes) - keep; dropped > 0 && used < h {
		var names []string
		for _, b := range boxes[keep:] {
			names = append(names, b.title)
		}
		out = append(out, sMuted.Render(truncate(" Not shown at this size: "+strings.Join(names, ", ")+". Enlarge the terminal or use the other screens.", w)))
	}
	return strings.Join(out, "\n")
}

// columns lays out two stacks side by side when wide enough, else one.
func columns(left, right []box, w, h int) string {
	if w >= 100 {
		lw := w / 2
		return lipgloss.JoinHorizontal(lipgloss.Top, column(left, lw, h), column(right, w-lw, h))
	}
	return column(append(left, right...), w, h)
}

// unavailable explains a section without data.
func unavailable[T any](s apiv1.Section[T], inner int) []string {
	out := []string{badge(lvUnknown, availabilityText(s.Status))}
	if s.Reason != "" {
		for _, l := range wrapText(s.Reason, inner) {
			out = append(out, sMuted.Render(l))
		}
	}
	return out
}

func staleMark[T any](s apiv1.Section[T]) string {
	if s.Stale || (s.Status != apiv1.Available && s.Data != nil) {
		return " " + sWarn.Render("(stale)")
	}
	return ""
}

func wrapText(s string, width int) []string {
	if width < 10 {
		width = 10
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func kv(k, v string) string { return sMuted.Render(pad(k, max(7, len(k)+1))) + v }

func waiting() []string { return []string{sMuted.Render("waiting for data…")} }

// ---- screens ----

func (m *Model) screenView(w, h int) string {
	switch m.screen {
	case scrHealth:
		return m.healthScreen(w, h)
	case scrStorage:
		return m.storageScreen(w, h)
	case scrNetwork:
		return m.networkScreen(w, h)
	case scrAccess:
		return m.accessScreen(w, h)
	}
	return m.overview(w, h)
}

func (m *Model) overview(w, h int) string {
	if w < 100 {
		return m.compactOverview(w, h)
	}
	cw := w
	if w >= 100 {
		cw = w / 2
	}
	inner := cw - 4
	left := []box{
		{title: "System", lines: m.systemLines(inner)},
		{title: "CPU", lines: m.cpuLines(inner)},
		{title: "Memory", lines: m.memoryLines(inner)},
	}
	right := []box{
		{title: "Health", lines: m.healthSummary(inner), flex: true},
		{title: "Storage", lines: m.filesystemLines(inner, 6), flex: true},
		{title: "Network", lines: m.networkSummary(inner), flex: true},
		{title: "Temperature", lines: m.temperatureLines(inner), flex: true},
	}
	return columns(left, right, w, h)
}

// compactOverview fits the essentials into 80x24: CPU/memory and health
// side by side, then storage and network. Host, OS and uptime are in the
// header.
func (m *Model) compactOverview(w, h int) string {
	lw := w / 2
	rw := w - lw
	var left []string
	left = append(left, m.cpuLines(lw-4)...)
	if len(left) > 3 {
		left = left[:3] // usage, trend, load
	}
	mem := m.memoryLines(lw - 4)
	if len(mem) > 2 {
		mem = mem[:2] // used, available
	}
	left = append(left, mem...)
	right := m.healthSummary(rw - 4)
	for len(left) < len(right) {
		left = append(left, "")
	}
	for len(right) < len(left) {
		right = append(right, "")
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		panel("CPU · Memory", strings.Join(left, "\n"), lw, 0, false),
		panel("Health", strings.Join(right, "\n"), rw, 0, false))
	rest := h - lipgloss.Height(top)
	return top + "\n" + column([]box{
		{title: "Storage", lines: m.filesystemLines(w-4, 4), flex: true},
		{title: "Network", lines: m.networkSummary(w - 4), flex: true},
	}, w, rest)
}

func (m *Model) systemLines(inner int) []string {
	if m.system == nil {
		return waiting()
	}
	s := m.system.System
	if s.Data == nil {
		return unavailable(s, inner)
	}
	d := s.Data
	out := []string{kv("host", sBold.Render(d.Hostname)), kv("os", d.OS.PrettyName+sMuted.Render(" · ")+d.Architecture), kv("kernel", d.Kernel)}
	if d.UptimeSeconds != nil {
		out = append(out, kv("uptime", duration(time.Duration(*d.UptimeSeconds)*time.Second)))
	}
	if m.hardware != nil && m.hardware.CPU.Data != nil {
		c := m.hardware.CPU.Data
		out = append(out, kv("cpu", fmt.Sprintf("%s · %d threads", c.ModelName, c.LogicalCores)))
	}
	if m.hardware != nil && m.hardware.Memory.Data != nil {
		out = append(out, kv("memory", bytes(m.hardware.Memory.Data.TotalBytes)))
	}
	return out
}

func (m *Model) cpuLines(inner int) []string {
	if m.metrics == nil || m.metrics.CPU == nil {
		return waiting()
	}
	s := *m.metrics.CPU
	if s.Data == nil {
		return unavailable(s, inner)
	}
	d := s.Data
	barW := max(8, inner-16)
	var out []string
	if d.UsagePercent != nil {
		u := *d.UsagePercent
		out = append(out, kv("usage", bar(u, barW, percentLevel(u, 85, 95))+" "+pct(u))+staleMark(s))
	} else {
		out = append(out, kv("usage", sMuted.Render("measuring…")))
	}
	out = append(out, kv("trend", sparkline(m.cpuHist, inner-7, 100)))
	if d.LoadAverage != nil {
		out = append(out, kv("load", fmt.Sprintf("%.2f  %.2f  %.2f", d.LoadAverage.One, d.LoadAverage.Five, d.LoadAverage.Fifteen)+sMuted.Render("  1m 5m 15m")))
	}
	if len(d.PerCoreUsagePercent) > 0 {
		var cells strings.Builder
		for _, c := range d.PerCoreUsagePercent {
			i := int(c / 100 * float64(len(sparkRunes)-1))
			cells.WriteString(percentLevel(c, 85, 95).style().Render(string(sparkRunes[max(0, min(i, len(sparkRunes)-1))])))
		}
		out = append(out, kv("cores", truncate(cells.String(), inner-7)))
	}
	return out
}

func (m *Model) memoryLines(inner int) []string {
	if m.metrics == nil || m.metrics.Memory == nil {
		return waiting()
	}
	s := *m.metrics.Memory
	if s.Data == nil {
		return unavailable(s, inner)
	}
	d := s.Data
	barW := max(8, inner-16)
	out := []string{
		kv("used", bar(d.UsedPercent, barW, percentLevel(d.UsedPercent, 90, 95))+" "+pct(d.UsedPercent)) + staleMark(s),
		kv("avail", bytes(d.AvailableBytes)+sMuted.Render(" of "+bytes(d.TotalBytes))),
	}
	if d.SwapTotalBytes > 0 {
		out = append(out, kv("swap", bar(d.SwapUsedPercent, barW, percentLevel(d.SwapUsedPercent, 80, 95))+" "+pct(d.SwapUsedPercent)))
	} else {
		out = append(out, kv("swap", sMuted.Render("none")))
	}
	if p := m.metrics.MemoryPressure; p != nil {
		if p.Data != nil {
			out = append(out, kv("stall", fmt.Sprintf("%.1f%% full · %.1f%% some", p.Data.FullAvg60Percent, p.Data.SomeAvg60Percent)+sMuted.Render(" (60s)")))
		} else {
			out = append(out, kv("stall", sMuted.Render(availabilityText(p.Status))))
		}
	}
	out = append(out, kv("trend", sparkline(m.memHist, inner-7, 100)))
	return out
}

func (m *Model) healthSummary(inner int) []string {
	if m.health == nil {
		return waiting()
	}
	out := []string{healthBadge(m.health.Status) + sMuted.Render("  evaluated "+ago(m.health.EvaluatedAt, m.opts.Now()))}
	for _, c := range m.health.Checks {
		detail := c.Summary
		if c.Status == apiv1.HealthUnknown {
			detail = availabilityText(c.Availability)
		}
		out = append(out, pad(badge(healthLevel(c.Status), pad(strings.ToUpper(c.Status), 8)), 11)+pad(c.Name, 18)+sMuted.Render(detail))
	}
	return out
}

func (m *Model) filesystemLines(inner, limit int) []string {
	if m.metrics == nil || m.metrics.Filesystems == nil {
		return waiting()
	}
	s := *m.metrics.Filesystems
	if s.Data == nil {
		return unavailable(s, inner)
	}
	fss := append([]apiv1.Filesystem(nil), (*s.Data)...)
	sort.SliceStable(fss, func(i, j int) bool { return fss[i].UsedPercent > fss[j].UsedPercent })
	nameW := min(18, max(8, inner/4))
	barW := max(6, inner-nameW-20)
	var out []string
	for i, f := range fss {
		if limit > 0 && i == limit {
			out = append(out, sMuted.Render(fmt.Sprintf("… %d more on the Storage screen", len(fss)-limit)))
			break
		}
		out = append(out, pad(f.MountPoint, nameW)+" "+bar(f.UsedPercent, barW, percentLevel(f.UsedPercent, 85, 95))+" "+pct(f.UsedPercent)+" "+sMuted.Render(padLeft(bytes(f.AvailableBytes), 9)))
	}
	if len(out) == 0 {
		out = append(out, sMuted.Render("no filesystems"))
	}
	return out
}

func (m *Model) networkSummary(inner int) []string {
	if m.metrics == nil || m.metrics.Network == nil {
		return waiting()
	}
	s := *m.metrics.Network
	if s.Data == nil {
		return unavailable(s, inner)
	}
	var rx, tx float64
	for _, n := range *s.Data {
		if !n.Loopback && n.RxBytesPerSecond != nil {
			rx += *n.RxBytesPerSecond
		}
		if !n.Loopback && n.TxBytesPerSecond != nil {
			tx += *n.TxBytesPerSecond
		}
	}
	sw := max(4, inner-20)
	out := []string{
		kv("rx", padLeft(rate(&rx), 12)+" "+sparkline(m.rxHist, sw, 0)),
		kv("tx", padLeft(rate(&tx), 12)+" "+sparkline(m.txHist, sw, 0)),
	}
	ifaces := append([]apiv1.NetworkInterface(nil), (*s.Data)...)
	sort.SliceStable(ifaces, func(i, j int) bool { return ifaces[i].OperState == "up" && ifaces[j].OperState != "up" })
	for _, n := range ifaces {
		if n.Loopback {
			continue
		}
		out = append(out, sMuted.Render(pad(n.Name, 12))+pad(stateText(n.OperState), 10)+sMuted.Render("↓ ")+padLeft(rate(n.RxBytesPerSecond), 11)+sMuted.Render("  ↑ ")+padLeft(rate(n.TxBytesPerSecond), 11))
	}
	return out
}

func stateText(s string) string {
	switch s {
	case "up":
		return badge(lvOK, "up")
	case "down":
		return sMuted.Render("○ down")
	}
	return sMuted.Render(s)
}

func (m *Model) temperatureLines(inner int) []string {
	if m.metrics == nil || m.metrics.Sensors == nil {
		return waiting()
	}
	s := *m.metrics.Sensors
	if s.Data == nil {
		return unavailable(s, inner)
	}
	var out []string
	for _, sn := range *s.Data {
		if sn.Kind != "temperature" {
			continue
		}
		l := percentLevel(sn.Value, 85, 95)
		if sn.Critical != nil && sn.Value >= *sn.Critical {
			l = lvCrit
		}
		name := sn.Chip + " " + sn.Label
		if sn.CPU {
			name = "CPU " + sn.Label
		}
		out = append(out, pad(name, max(10, inner-12))+" "+l.style().Render(padLeft(fmt.Sprintf("%.1f °C", sn.Value), 10)))
	}
	if len(out) == 0 {
		out = append(out, sMuted.Render("no temperature sensors"))
	}
	return out
}

func (m *Model) healthScreen(w, h int) string {
	inner := w - 4
	if m.health == nil {
		return column([]box{{title: "Health", lines: waiting()}}, w, h)
	}
	var lines []string
	lines = append(lines, healthBadge(m.health.Status)+sMuted.Render("  evaluated "+ago(m.health.EvaluatedAt, m.opts.Now())), "")
	for _, c := range m.health.Checks {
		lines = append(lines, badge(healthLevel(c.Status), pad(strings.ToUpper(c.Status), 8))+" "+sBold.Render(c.Name))
		if c.Status == apiv1.HealthUnknown {
			lines = append(lines, "  "+sMuted.Render(availabilityText(c.Availability)+": "+c.Reason))
		} else if len(c.Findings) == 0 {
			lines = append(lines, "  "+sMuted.Render(c.Summary))
		}
		for _, f := range c.Findings {
			sev := map[string]level{"warning": lvWarn, "critical": lvCrit}[f.Severity]
			lines = append(lines, "  "+badge(sev, f.Summary)+sMuted.Render("  since "+ago(f.Since, m.opts.Now())))
			var ev []string
			for k, v := range f.Evidence {
				ev = append(ev, k+"="+v)
			}
			sort.Strings(ev)
			for _, l := range wrapText(strings.Join(ev, "  "), inner-4) {
				lines = append(lines, "    "+sMuted.Render(l))
			}
			for _, l := range wrapText("→ "+f.Recommendation, inner-4) {
				lines = append(lines, "    "+sAccent.Render(l))
			}
		}
	}
	boxes := []box{{title: "Health checks", lines: lines, flex: true}}
	var evLines []string
	if m.events != nil {
		for _, e := range m.events.Items {
			sev := map[string]level{"info": lvNeutral, "warning": lvWarn, "critical": lvCrit}[e.Severity]
			evLines = append(evLines, sMuted.Render(pad(e.Time.Local().Format("Jan 02 15:04:05"), 16))+badge(sev, pad(e.Severity, 8))+" "+e.Message)
		}
	}
	if len(evLines) == 0 {
		evLines = []string{sMuted.Render("no events yet")}
	}
	boxes = append(boxes, box{title: "Recent events", lines: evLines, flex: true})
	return column(boxes, w, h)
}

func (m *Model) storageScreen(w, h int) string {
	inner := w - 4
	fsLines := m.filesystemLines(inner, 0)
	var devLines, healthLines []string
	switch {
	case m.storage == nil:
		devLines, healthLines = waiting(), waiting()
	default:
		if d := m.storage.BlockDevices; d.Data != nil {
			for _, b := range *d.Data {
				kind := "SSD"
				if b.Rotational != nil && *b.Rotational {
					kind = "HDD"
				}
				devLines = append(devLines, pad(b.Name, 12)+pad(b.Model, 28)+pad(b.Transport, 8)+pad(kind, 5)+padLeft(bytes(b.SizeBytes), 11))
			}
		} else {
			devLines = unavailable(d, inner)
		}
		if d := m.storage.DiskHealth; d.Data != nil {
			for _, dh := range *d.Data {
				l, word := lvOK, "PASSED"
				if dh.SmartPassed != nil && !*dh.SmartPassed {
					l, word = lvCrit, "FAILED"
				}
				extra := ""
				if dh.TemperatureCelsius != nil {
					extra += fmt.Sprintf("  %.0f °C", *dh.TemperatureCelsius)
				}
				if dh.PercentageUsed != nil {
					extra += fmt.Sprintf("  %d%% worn", *dh.PercentageUsed)
				}
				if dh.PowerOnHours != nil {
					extra += fmt.Sprintf("  %dh on", *dh.PowerOnHours)
				}
				healthLines = append(healthLines, pad(dh.Device, 12)+badge(l, word)+sMuted.Render(extra))
			}
		} else {
			healthLines = unavailable(d, inner)
		}
	}
	return column([]box{
		{title: "Filesystems", lines: fsLines, flex: true},
		{title: "Disks", lines: devLines, flex: true},
		{title: "Disk health (SMART)", lines: healthLines, flex: true},
	}, w, h)
}

func (m *Model) networkScreen(w, h int) string {
	inner := w - 4
	if m.metrics == nil || m.metrics.Network == nil {
		return column([]box{{title: "Interfaces", lines: waiting()}}, w, h)
	}
	s := *m.metrics.Network
	if s.Data == nil {
		return column([]box{{title: "Interfaces", lines: unavailable(s, inner)}}, w, h)
	}
	head := sMuted.Render(pad("NAME", 14) + pad("STATE", 10) + padLeft("SPEED", 10) + padLeft("RX/s", 13) + padLeft("TX/s", 13) + padLeft("RX TOTAL", 12) + padLeft("TX TOTAL", 12) + padLeft("ERR/DROP", 10))
	lines := []string{head}
	for _, n := range *s.Data {
		speed := "—"
		if n.SpeedMbps != nil {
			speed = fmt.Sprintf("%d Mb/s", *n.SpeedMbps)
		}
		errs := n.RxErrors + n.TxErrors + n.RxDropped + n.TxDropped
		errText := sMuted.Render(padLeft(fmt.Sprint(errs), 10))
		if errs > 0 {
			errText = sWarn.Render(padLeft(fmt.Sprint(errs), 10))
		}
		name := n.Name
		if n.Loopback && n.Name != "lo" {
			name += sMuted.Render(" (loopback)")
		}
		lines = append(lines, pad(name, 14)+pad(stateText(n.OperState), 10)+padLeft(speed, 10)+padLeft(rate(n.RxBytesPerSecond), 13)+padLeft(rate(n.TxBytesPerSecond), 13)+
			padLeft(bytes(n.RxBytes), 12)+padLeft(bytes(n.TxBytes), 12)+errText)
	}
	sw := max(10, inner-16)
	trend := []string{kv("rx", sparkline(m.rxHist, sw, 0)), kv("tx", sparkline(m.txHist, sw, 0))}
	return column([]box{{title: "Interfaces" + staleMark(s), lines: lines, flex: true}, {title: "Throughput (all interfaces)", lines: trend}}, w, h)
}

func (m *Model) accessScreen(w, h int) string {
	var srv []string
	switch {
	case m.status == nil:
		srv = waiting()
	case m.status.API == nil:
		srv = []string{
			kv("agent", m.status.Agent.Version+sMuted.Render(" · API "+m.status.Agent.APIVersion)),
			kv("via", m.opts.Target),
			sMuted.Render("Connected with an API key. Server details and key management"),
			sMuted.Render("are available only through the local admin socket."),
		}
	default:
		a := m.status.API
		srv = append(srv, kv("agent", m.status.Agent.Version+sMuted.Render(" · API "+m.status.Agent.APIVersion+" · up "+duration(time.Duration(m.status.Agent.UptimeSeconds)*time.Second))))
		for _, l := range a.Listeners {
			t := l.Transport
			switch {
			case l.TLS:
				t += " (TLS)"
			case l.Transport == "tcp":
				t += " (loopback, key required)"
			default:
				t += " (local admin)"
			}
			srv = append(srv, kv("listen", l.Address+sMuted.Render("  "+t)))
		}
		r := a.Requests
		srv = append(srv, kv("reqs", fmt.Sprintf("%d total", r.Total)+sMuted.Render(fmt.Sprintf("  2xx %d · 4xx %d · 5xx %d · auth failures %d · rate limited %d", r.Status2xx, r.Status4xx, r.Status5xx, r.AuthFailures, r.RateLimited))))
		srv = append(srv, kv("clients", fmt.Sprintf("%d active", a.ActiveClients)+sMuted.Render(" (keys used in the last 5 minutes)")))
	}

	var keyLines []string
	switch {
	case !m.isAdmin():
		keyLines = []string{sMuted.Render("Key management requires the local admin socket (run cockpit tui on the agent's host).")}
	case m.keysErr != "":
		keyLines = []string{sCrit.Render("✖ " + m.keysErr)}
	case len(m.keys) == 0:
		keyLines = []string{sMuted.Render("No API keys yet. Press n to create one.")}
	default:
		// Column widths: ID, name, scopes, status, last used.
		cw := [5]int{13, 20, 26, 11, 13}
		scopeText := func(s []string) string { return strings.Join(s, ",") }
		if w-4 < 104 {
			cw = [5]int{13, 15, 15, 10, 10}
			scopeText = func(s []string) string { // "metrics:read" -> "metrics"
				short := make([]string, len(s))
				for i, x := range s {
					short[i] = strings.TrimSuffix(x, ":read")
				}
				return strings.Join(short, ",")
			}
		}
		keyLines = append(keyLines, sMuted.Render(pad("  ID", cw[0]+2)+pad("NAME", cw[1])+pad("SCOPES", cw[2])+pad("STATUS", cw[3])+pad("LAST USED", cw[4])+"EXPIRES"))
		for i, k := range m.keys {
			st := badge(lvOK, "active")
			switch {
			case k.RevokedAt != nil:
				st = badge(lvUnknown, "revoked")
			case !k.Active:
				st = badge(lvWarn, "expired")
			}
			last := "never"
			if k.LastUsedAt != nil {
				last = ago(*k.LastUsedAt, m.opts.Now())
			}
			exp := "never"
			if k.ExpiresAt != nil {
				exp = k.ExpiresAt.Local().Format("2006-01-02")
			}
			cursor := "  "
			if i == m.selected {
				cursor = sAccent.Render("▶ ")
			}
			keyLines = append(keyLines, cursor+pad(k.ID, cw[0])+pad(k.Name, cw[1])+pad(scopeText(k.Scopes), cw[2])+pad(st, cw[3])+pad(sMuted.Render(last), cw[4])+sMuted.Render(exp))
		}
		keyLines = append(keyLines, "", sMuted.Render("Keys are shown in full only once, when created or rotated."))
	}
	return column([]box{{title: "API server", lines: srv}, {title: "API keys", lines: keyLines, flex: true}}, w, h)
}

func (m *Model) modalView(w int) string {
	inner := w - 4
	var title string
	var lines []string
	switch m.modal {
	case modalCreateName:
		title = "New API key · name"
		lines = []string{"Name for the key (e.g. grafana, backup-script):", "", sAccent.Render("› ") + m.input + sAccent.Render("█"), "", sMuted.Render("enter next · esc cancel")}
	case modalCreateScopes:
		title = "New API key · scopes"
		lines = []string{"Select what the key may read (space toggles):", ""}
		desc := []string{"system identity and hardware inventory", "metrics, storage, network, sensors, health, events"}
		for i, s := range issuableScopes {
			box := "[ ]"
			if m.scopeOn[i] {
				box = sOK.Render("[x]")
			}
			cur := "  "
			if m.scopeCursor == i {
				cur = sAccent.Render("▶ ")
			}
			lines = append(lines, cur+box+" "+pad(s, 14)+sMuted.Render(desc[i]))
		}
		lines = append(lines, "", sMuted.Render("Keys are read-only. Admin access is never granted to keys."), sMuted.Render("enter create · esc cancel"))
	case modalConfirm:
		verb := "Revoke"
		effect := "The key stops working immediately. This cannot be undone."
		if m.pendingAction == "rotate" {
			verb, effect = "Rotate", "A new key is issued and the old key stops working immediately."
		}
		title = verb + " key " + m.pendingID
		lines = append(lines, wrapText(effect, inner)...)
		lines = append(lines, "", "Type the key ID to confirm:", sAccent.Render("› ")+m.input+sAccent.Render("█"), "", sMuted.Render("enter confirm · esc cancel"))
	case modalReveal:
		title = "New API key — shown only once"
		k := m.revealed
		lines = []string{
			sBold.Render(k.APIKey.Name) + sMuted.Render("  ("+strings.Join(k.APIKey.Scopes, ", ")+")"),
			"",
			sWarn.Render(k.Key),
			"",
		}
		lines = append(lines, wrapText("Copy this key now. When you press enter it is erased from this screen and can never be shown again. Send it as: Authorization: Bearer <key>", inner)...)
		lines = append(lines, "", sMuted.Render("enter done"))
	}
	return panel(title, strings.Join(lines, "\n"), w, 0, true)
}

func (m *Model) helpView(w, h int) string {
	lines := []string{
		sBold.Render("Navigation"),
		kv("1-5", "switch screen") + "      " + kv("tab", "next screen"),
		kv("←/→", "previous / next") + "    " + kv("click", "tab bar"),
		kv("r", "refresh now") + "        " + kv("?", "toggle help"),
		kv("q", "quit"),
		"",
		sBold.Render("API Access"),
		kv("↑/↓", "select key") + "        " + kv("n", "create key"),
		kv("o", "rotate key") + "         " + kv("x", "revoke key"),
		"",
		sBold.Render("Status symbols"),
		badge(lvOK, "OK") + "   " + badge(lvWarn, "WARNING") + "   " + badge(lvCrit, "CRITICAL") + "   " + badge(lvUnknown, "UNKNOWN / unavailable"),
		"",
		sMuted.Render("Data comes from the Cockpit API at " + m.opts.Target + "."),
	}
	return column([]box{{title: "Help", lines: lines}}, w, h)
}
