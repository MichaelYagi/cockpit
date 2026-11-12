package tui

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
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
	if m.modal == modalReveal {
		// Release the mouse so the terminal can select the new key.
		v.MouseMode = tea.MouseModeNone
	}
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
	for i := range screenNames {
		label := tabLabel(screen(i), w)
		if screen(i) == m.screen {
			parts = append(parts, sTabOn.Render(label))
		} else {
			parts = append(parts, sTabOff.Render(label))
		}
	}
	_, gap := tabNames(w)
	return truncate(strings.Join(parts, strings.Repeat(" ", gap)), w)
}

func (m *Model) footer(w int) string {
	hints := []string{"0-9 S screens", "tab next", "r refresh", "? help", "q quit"}
	switch m.screen {
	case scrAccess:
		hints = []string{"↑↓ select", "n new key", "o rotate", "x revoke", "d remove", "? help", "q quit"}
	case scrProcesses:
		hints = []string{"s sort:" + processSorts[m.procSort], "/ search", "k kernel threads", "c containers", "q quit"}
		if m.procContainers {
			hints = []string{"c processes", "tab next", "r refresh", "? help", "q quit"}
		}
		if m.searching {
			hints = []string{"type to search", "enter done", "esc clear"}
		}
	case scrServices:
		label := "failed only"
		if m.svcFailed {
			label = "show all"
		}
		hints = []string{"f " + label, "tab next", "? help", "q quit"}
	case scrPerformance:
		hints = []string{"t range:" + rangeLabel(historyRanges[m.histRange]), "tab next", "? help", "q quit"}
	case scrEvents:
		f := severityFilters[m.evFilter]
		if f == "" {
			f = "all"
		}
		hints = []string{"v severity:" + f, "m older", "tab next", "q quit"}
	case scrSettings:
		hints = []string{"↑↓ select", "enter edit", "w save", "d reset", "esc discard", "+/- refresh"}
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
			name, _, _ := strings.Cut(b.title, " · ") // drop inline stats
			names = append(names, name)
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
	case scrPerformance:
		return m.performanceScreen(w, h)
	case scrSettings:
		return m.settingsScreen(w, h)
	case scrHardware:
		return m.hardwareScreen(w, h)
	case scrProcesses:
		return m.processesScreen(w, h)
	case scrServices:
		return m.servicesScreen(w, h)
	case scrEvents:
		return m.eventsScreen(w, h)
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
	if g := m.gpuLines(inner); g != nil {
		left = append(left, box{title: "GPU", lines: g, flex: true})
	}
	if p := m.powerLines(inner); p != nil {
		left = append(left, box{title: "Power", lines: p})
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
	barW := max(6, inner-nameW-34) // name, bar, percent, "used / total"
	var out []string
	for i, f := range fss {
		if limit > 0 && i == limit {
			out = append(out, sMuted.Render(fmt.Sprintf("… %d more on the Storage screen", len(fss)-limit)))
			break
		}
		size := bytes(f.UsedBytes) + " / " + bytes(f.TotalBytes)
		out = append(out, pad(f.MountPoint, nameW)+" "+bar(f.UsedPercent, barW, percentLevel(f.UsedPercent, 85, 95))+" "+pct(f.UsedPercent)+" "+sMuted.Render(padLeft(size, 23)))
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
	evLines := m.eventLines(20)
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
	boxes := []box{
		{title: "Filesystems", lines: fsLines, flex: true},
		{title: "Disks", lines: devLines, flex: true},
		{title: "Disk health (SMART)", lines: healthLines, flex: true},
	}
	if z := m.zfsLines(inner); z != nil {
		boxes = append(boxes, box{title: "ZFS pools", lines: z})
	}
	return column(boxes, w, h)
}

// zfsLines lists ZFS pools (D25); nil when ZFS is not in use.
func (m *Model) zfsLines(w int) []string {
	if m.storage == nil || m.storage.ZFS.Data == nil || len(m.storage.ZFS.Data.Pools) == 0 {
		return nil
	}
	z := m.storage.ZFS.Data
	var lines []string
	for _, p := range z.Pools {
		l := lvOK
		switch p.State {
		case "ONLINE":
		case "DEGRADED":
			l = lvWarn
		case "OFFLINE", "REMOVED":
			l = lvUnknown
		default:
			l = lvCrit
		}
		line := pad(p.Name, 14) + badge(l, p.State)
		if p.SizeBytes != nil && p.AllocatedBytes != nil && *p.SizeBytes > 0 {
			pct := float64(*p.AllocatedBytes) / float64(*p.SizeBytes) * 100
			line += "  " + bar(pct, 16, percentLevel(pct, 80, 90)) + fmt.Sprintf(" %5.1f%% of %s", pct, bytes(*p.SizeBytes))
		}
		if p.ReadErrors != nil && p.WriteErrors != nil && p.ChecksumErrors != nil {
			if n := *p.ReadErrors + *p.WriteErrors + *p.ChecksumErrors; n > 0 {
				line += "  " + sWarn.Render(fmt.Sprintf("errors r%d w%d c%d", *p.ReadErrors, *p.WriteErrors, *p.ChecksumErrors))
			}
		}
		lines = append(lines, line)
	}
	if a := z.ARC; a != nil {
		arc := fmt.Sprintf("ARC %s of %s max", bytes(a.SizeBytes), bytes(a.MaxBytes))
		if a.HitPercent != nil {
			arc += fmt.Sprintf(" · %.1f%% hits", *a.HitPercent)
		}
		lines = append(lines, sMuted.Render(arc))
	}
	if z.DetailReason != "" {
		lines = append(lines, sMuted.Render(truncate(z.DetailReason, w)))
	}
	return lines
}

// powerLines shows batteries, mains and UPS (D25); nil when there are none.
func (m *Model) powerLines(w int) []string {
	if m.metrics == nil {
		return nil
	}
	var lines []string
	if p := m.metrics.Power; p != nil && p.Data != nil {
		for _, s := range *p.Data {
			switch s.Type {
			case "battery":
				line := pad(s.Name, 8)
				if s.CapacityPercent != nil {
					line += bar(*s.CapacityPercent, 12, percentLevel(100-*s.CapacityPercent, 80, 90)) + fmt.Sprintf(" %3.0f%%", *s.CapacityPercent)
				}
				if s.Status != "" {
					line += "  " + strings.ReplaceAll(s.Status, "_", " ")
				}
				if s.HoursRemaining != nil {
					line += fmt.Sprintf("  %s left", (time.Duration(*s.HoursRemaining * float64(time.Hour))).Round(time.Minute))
				}
				if s.PowerWatts != nil {
					line += fmt.Sprintf("  %.1f W", *s.PowerWatts)
				}
				lines = append(lines, truncate(line, w))
			case "mains":
				if s.Online != nil {
					word, l := "on mains", lvOK
					if !*s.Online {
						word, l = "on battery", lvWarn
					}
					lines = append(lines, pad(s.Name, 8)+badge(l, word))
				}
			}
		}
	}
	if u := m.metrics.UPS; u != nil && u.Data != nil {
		for _, x := range *u.Data {
			l, word := lvOK, "on line"
			switch {
			case x.LowBattery:
				l, word = lvCrit, "LOW BATTERY"
			case x.OnBattery:
				l, word = lvWarn, "on battery"
			}
			line := pad("UPS "+x.Name, 14) + badge(l, word)
			if x.ChargePercent != nil {
				line += fmt.Sprintf("  %.0f%%", *x.ChargePercent)
			}
			if x.RuntimeSeconds != nil {
				line += "  " + (time.Duration(*x.RuntimeSeconds) * time.Second).String() + " runtime"
			}
			if x.LoadPercent != nil {
				line += fmt.Sprintf("  load %.0f%%", *x.LoadPercent)
			}
			lines = append(lines, truncate(line, w))
		}
	}
	return lines
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
	return column([]box{
		{title: "Interfaces" + staleMark(s), lines: lines, flex: true},
		{title: "Throughput (all interfaces)", lines: trend},
		{title: "Addresses, routes and DNS", lines: m.netConfigLines(inner), flex: true},
	}, w, h)
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
		if note := m.noNetworkNote(); note != "" {
			srv = append(srv, "")
			for i, l := range wrapText(note, w-6) {
				prefix := "  "
				if i == 0 {
					prefix = sWarn.Render("▲ ")
				}
				srv = append(srv, prefix+sWarn.Render(l))
			}
		}
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
		lines = []string{"Select what the key may read (space toggles, a selects all):", ""}
		desc := []string{"system identity and hardware inventory", "metrics, storage, network rates, sensors, health, events", "interface addresses, routes and DNS", "process list (users and resource use)"}
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
		switch m.pendingAction {
		case "rotate":
			verb, effect = "Rotate", "A new key is issued and the old key stops working immediately."
		case "remove":
			verb, effect = "Remove", "The revoked key is deleted from the list. This cannot be undone."
		}
		title = verb + " key " + m.pendingID
		lines = append(lines, wrapText(effect, inner)...)
		lines = append(lines, "", fmt.Sprintf("Type the first %d characters of the key ID to confirm:", confirmChars), sAccent.Render("› ")+m.input+sAccent.Render("█"), "", sMuted.Render("enter confirm · esc cancel"))
	case modalEditSetting:
		f := settingFields[m.setField]
		title = "Edit threshold"
		lines = []string{f.label + sMuted.Render("  (current "+fmtSetting(f, m.draft)+" "+f.unit+")"), "", sAccent.Render("› ") + m.input + sAccent.Render("█") + " " + sMuted.Render(f.unit), "", sMuted.Render("enter set · esc cancel")}
	case modalConfirmSettings:
		if m.pendingAction == "reset" {
			title = "Reset thresholds"
			lines = wrapText("Return every health threshold to the values in the configuration file? This applies immediately.", inner)
		} else {
			title = "Save thresholds"
			lines = wrapText("Apply the edited thresholds now? They take effect at the next health evaluation and are kept across restarts.", inner)
			for _, c := range m.settingsChanges() {
				lines = append(lines, sAccent.Render("  "+c))
			}
		}
		lines = append(lines, "", sMuted.Render("y apply · n cancel"))
	case modalReveal:
		title = "New API key — shown only once"
		k := m.revealed
		lines = []string{
			sBold.Render(k.APIKey.Name) + sMuted.Render("  ("+strings.Join(k.APIKey.Scopes, ", ")+")"),
			"",
			sWarn.Render(k.Key),
			"",
		}
		lines = append(lines, wrapText("Copy this key now: press c, or select it with the mouse. When you press enter it is erased from this screen and can never be shown again. Send it as: Authorization: Bearer <key>", inner)...)
		if addr := m.tcpAddress(); addr != "" {
			lines = append(lines, "", sMuted.Render("Try it: ")+"curl -H \"Authorization: Bearer <key>\" \\", "           http://"+addr+"/api/v1/health")
		} else if note := m.noNetworkNote(); note != "" {
			lines = append(lines, "")
			for _, l := range wrapText(note, inner) {
				lines = append(lines, sWarn.Render(l))
			}
		}
		if m.copied {
			lines = append(lines, "", sOK.Render("Sent to the clipboard.")+sMuted.Render(" If paste does not work, your terminal blocks clipboard access; select the key instead."))
		}
		lines = append(lines, "", sMuted.Render("c copy · enter done"))
	}
	return panel(title, strings.Join(lines, "\n"), w, 0, true)
}

func (m *Model) helpView(w, h int) string {
	lines := []string{
		sBold.Render("Navigation"),
		kv("0-9 S", "switch screen") + "    " + kv("tab", "next screen"),
		kv("←/→", "previous / next") + "    " + kv("click", "tab bar"),
		kv("r", "refresh now") + "        " + kv("?", "toggle help"),
		kv("q", "quit"),
		"",
		sBold.Render("Processes"),
		kv("s", "change sort") + "        " + kv("/", "search"),
		kv("k", "kernel threads") + "     " + kv("esc", "clear search"),
		kv("c", "containers / processes"),
		"",
		sBold.Render("Performance · Events · Settings"),
		kv("t", "chart range") + "        " + kv("v", "event severity") + "    " + kv("m", "older events"),
		kv("enter", "edit threshold") + "   " + kv("w", "save") + "  " + kv("d", "reset to file"),
		"",
		sBold.Render("Services"),
		kv("f", "failed only / all"),
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

// ---- Phase 2 screens ----

func optPct(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func (m *Model) gpuLines(inner int) []string {
	if m.metrics == nil || m.metrics.GPU == nil || m.metrics.GPU.Data == nil || len(*m.metrics.GPU.Data) == 0 {
		return nil
	}
	var out []string
	barW := max(8, inner-16)
	for _, g := range *m.metrics.GPU.Data {
		out = append(out, sBold.Render(g.Name)+sMuted.Render("  "+g.Driver+" "+g.DriverVersion))
		if g.UtilizationPercent != nil {
			out = append(out, kv("busy", bar(*g.UtilizationPercent, barW, percentLevel(*g.UtilizationPercent, 90, 98))+" "+pct(*g.UtilizationPercent)))
		}
		if g.MemoryTotalBytes != nil && g.MemoryUsedBytes != nil && *g.MemoryTotalBytes > 0 {
			p := float64(*g.MemoryUsedBytes) / float64(*g.MemoryTotalBytes) * 100
			out = append(out, kv("vram", bar(p, barW, percentLevel(p, 90, 97))+" "+pct(p)))
		}
		var extra []string
		if g.TemperatureCelsius != nil {
			extra = append(extra, percentLevel(*g.TemperatureCelsius, 83, 90).style().Render(fmt.Sprintf("%.0f °C", *g.TemperatureCelsius)))
		}
		if g.PowerWatts != nil {
			p := fmt.Sprintf("%.0f W", *g.PowerWatts)
			if g.PowerLimitWatts != nil {
				p += fmt.Sprintf(" of %.0f W", *g.PowerLimitWatts)
			}
			extra = append(extra, p)
		}
		if g.FanPercent != nil {
			extra = append(extra, "fan "+optPct(g.FanPercent))
		}
		if g.FrequencyMHz != nil {
			f := fmt.Sprintf("%.0f MHz", *g.FrequencyMHz)
			if g.MaxFrequencyMHz != nil {
				f += fmt.Sprintf(" of %.0f", *g.MaxFrequencyMHz)
			}
			extra = append(extra, f)
		}
		if len(extra) > 0 {
			out = append(out, kv("", strings.Join(extra, sMuted.Render(" · "))))
		}
		switch {
		case g.MetricsSource == "":
			out = append(out, sMuted.Render("no metrics source for this GPU"))
		case g.UtilizationPercent == nil && (g.MetricsSource == "i915" || g.MetricsSource == "xe"):
			out = append(out, sMuted.Render("busy % needs perf access; not available to the agent"))
		}
	}
	return out
}

func (m *Model) hardwareScreen(w, h int) string {
	if m.hardware == nil {
		return column([]box{{title: "Hardware", lines: waiting()}}, w, h)
	}
	hw := m.hardware
	inner := w - 4
	var cpu []string
	if c := hw.CPU.Data; c != nil {
		cpu = append(cpu, kvw("model", sBold.Render(c.ModelName)), kvw("vendor", c.Vendor+sMuted.Render(fmt.Sprintf("  family %s model %s stepping %s", c.Family, c.Model, c.Stepping))))
		topo := fmt.Sprintf("%d socket(s) · %d cores · %d threads", c.Sockets, c.Cores, c.LogicalCores)
		if c.MaxFrequencyMHz != nil {
			topo += fmt.Sprintf(" · up to %.2f GHz", *c.MaxFrequencyMHz/1000)
		}
		cpu = append(cpu, kvw("layout", topo))
		var caches []string
		for _, ca := range c.Caches {
			caches = append(caches, fmt.Sprintf("L%d %s %s", ca.Level, strings.ToLower(ca.Type[:min(1, len(ca.Type))]), bytes(ca.SizeBytes)))
		}
		if len(caches) > 0 {
			cpu = append(cpu, kvw("caches", strings.Join(caches, sMuted.Render(" · "))))
		}
		features := strings.Join(c.Flags, " ")
		if c.Virtualization != "" {
			features = c.Virtualization + "  " + features
		}
		if c.Hypervisor {
			features += sMuted.Render("  (running in a virtual machine)")
		}
		cpu = append(cpu, kvw("features", features))
	} else {
		cpu = unavailable(hw.CPU, inner)
	}

	var mem []string
	if d := hw.Memory.Data; d != nil {
		mem = append(mem, kvw("total", bytes(d.TotalBytes))+sMuted.Render("  swap "+bytes(d.SwapTotalBytes)))
	}
	if mm := hw.MemoryModules; mm.Data != nil {
		for _, mod := range *mm.Data {
			mem = append(mem, pad(mod.Locator, 12)+pad(bytes(mod.SizeBytes), 11)+pad(mod.Type, 7)+pad(fmt.Sprintf("%d MT/s", mod.SpeedMTs), 11)+sMuted.Render(mod.Manufacturer+" "+mod.PartNumber))
		}
	} else {
		mem = append(mem, sMuted.Render("modules: "+availabilityText(mm.Status)+" — "+mm.Reason))
	}

	var board []string
	if b := hw.Board.Data; b != nil {
		board = append(board, kvw("system", strings.TrimSpace(b.SystemVendor+" "+b.ProductName)), kvw("board", strings.TrimSpace(b.BoardVendor+" "+b.BoardName)),
			kvw("firmware", strings.TrimSpace(b.BIOSVendor+" "+b.BIOSVersion+" "+b.BIOSDate)))
		if b.ChassisType != "" {
			board = append(board, kvw("chassis", b.ChassisType))
		}
	} else {
		board = unavailable(hw.Board, inner)
	}

	var gpus []string
	if g := hw.GPUs; g.Data != nil {
		for _, d := range *g.Data {
			mem := ""
			if d.MemoryTotalBytes != nil {
				mem = "  " + bytes(*d.MemoryTotalBytes)
			}
			gpus = append(gpus, sBold.Render(d.Name)+sMuted.Render(mem+"  "+d.Driver+" "+d.DriverVersion+"  "+d.PCIAddress))
		}
	} else {
		gpus = unavailable(g, inner)
	}

	var devs []string
	if p := hw.PCIDevices; p.Data != nil {
		for _, d := range *p.Data {
			name := d.DeviceName
			if name == "" {
				name = d.VendorID + ":" + d.DeviceID
			}
			devs = append(devs, sMuted.Render(pad(d.Address, 14))+pad(d.ClassName, 26)+pad(strings.TrimSpace(d.VendorName+" "+name), max(10, inner-56))+sMuted.Render(d.Driver))
		}
	} else {
		devs = unavailable(p, inner)
	}
	if u := hw.USBDevices; u.Data != nil {
		for _, d := range *u.Data {
			devs = append(devs, sMuted.Render(pad("usb "+d.Path, 14))+pad(strings.TrimSpace(d.Manufacturer+" "+d.Product), max(10, inner-14)))
		}
	} else {
		devs = append(devs, sMuted.Render("USB: "+availabilityText(u.Status)+" — "+u.Reason))
	}

	return column([]box{
		{title: "Processor", lines: cpu},
		{title: "Graphics", lines: gpus},
		{title: "Memory", lines: mem, flex: true},
		{title: "System and firmware", lines: board},
		{title: "Devices (PCI, USB)", lines: devs, flex: true},
	}, w, h)
}

func (m *Model) processesScreen(w, h int) string {
	if m.procContainers {
		return m.containersView(w, h)
	}
	inner := w - 4
	title := "Processes · sorted by " + processSorts[m.procSort]
	if m.procSearch != "" || m.searching {
		cursor := ""
		if m.searching {
			cursor = sAccent.Render("█")
		}
		title += " · search: " + m.procSearch + cursor
	}
	if m.procKernel {
		title += " · with kernel threads"
	}
	var lines []string
	switch {
	case m.procsErr != "":
		lines = []string{sCrit.Render("✖ " + m.procsErr)}
		if strings.Contains(m.procsErr, "processes:read") {
			lines = append(lines, sMuted.Render("Create a key with the processes:read scope, or use the local admin socket."))
		}
	case m.procs == nil:
		lines = waiting()
	case m.procs.Status != apiv1.Available:
		lines = []string{badge(lvUnknown, availabilityText(m.procs.Status)), sMuted.Render(m.procs.Reason)}
	default:
		p := m.procs
		nameW := max(10, inner-63)
		lines = append(lines, sMuted.Render(padLeft("PID", 7)+"  "+pad("USER", 12)+padLeft("CPU%", 7)+padLeft("MEM%", 7)+padLeft("RSS", 11)+padLeft("THR", 5)+"  "+pad("S", 3)+pad("NAME", nameW)))
		for _, pr := range p.Items {
			cpu := "—"
			cl := lvNeutral
			if pr.CPUPercent != nil {
				cpu = fmt.Sprintf("%.1f", *pr.CPUPercent)
				cl = percentLevel(*pr.CPUPercent, 50, 90)
			}
			state := pr.State
			if state == "D" || state == "Z" {
				state = sWarn.Render(state)
			}
			name := pr.Name
			if pr.KernelThread {
				name = sMuted.Render("[" + name + "]")
			}
			lines = append(lines, padLeft(fmt.Sprint(pr.PID), 7)+"  "+pad(pr.User, 12)+cl.style().Render(padLeft(cpu, 7))+padLeft(fmt.Sprintf("%.1f", pr.MemoryPercent), 7)+
				padLeft(bytes(pr.RSSBytes), 11)+padLeft(fmt.Sprint(pr.Threads), 5)+"  "+pad(state, 3)+pad(name, nameW))
		}
		note := fmt.Sprintf("%d processes", p.Total)
		if p.Total > len(p.Items) {
			note = fmt.Sprintf("top %d of %d processes", len(p.Items), p.Total)
		}
		note += fmt.Sprintf(" · CPU %% of one core over %.1fs · command lines are not shown", p.WindowSeconds)
		lines = append(lines, sMuted.Render(note))
		if p.Partial {
			lines = append(lines, sWarn.Render("▲ "+p.Reason))
		}
	}
	return column([]box{{title: title, lines: lines, flex: true}}, w, h)
}

func (m *Model) servicesScreen(w, h int) string {
	inner := w - 4
	var lines []string
	title := "Services"
	switch {
	case m.svcErr != "":
		lines = []string{sCrit.Render("✖ " + m.svcErr)}
	case m.services == nil:
		lines = waiting()
	case m.services.Services.Data == nil:
		lines = unavailable(m.services.Services, inner)
	default:
		d := m.services.Services.Data
		title = fmt.Sprintf("Services · %d total · %d active · %d inactive · ", d.Total, d.Active, d.Inactive)
		if d.Failed > 0 {
			title += sCrit.Render(fmt.Sprintf("%d failed", d.Failed))
		} else {
			title += sOK.Render("none failed")
		}
		items := append([]apiv1.Service(nil), d.Items...)
		sort.SliceStable(items, func(i, j int) bool {
			rank := func(s apiv1.Service) int {
				switch s.ActiveState {
				case "failed":
					return 0
				case "active":
					return 1
				}
				return 2
			}
			return rank(items[i]) < rank(items[j])
		})
		nameW := min(40, max(20, inner/3))
		for _, s := range items {
			if m.svcFailed && s.ActiveState != "failed" {
				continue
			}
			var st string
			switch s.ActiveState {
			case "failed":
				st = badge(lvCrit, pad("failed", 9))
			case "active":
				st = badge(lvOK, pad("active", 9))
			case "activating", "deactivating", "reloading":
				st = badge(lvWarn, pad(s.ActiveState, 9))
			default:
				st = sMuted.Render("○ " + pad(s.ActiveState, 9))
			}
			lines = append(lines, pad(s.Name, nameW)+" "+st+" "+sMuted.Render(pad(s.SubState, 9)+" "+s.Description))
		}
		if len(lines) == 0 {
			lines = []string{sOK.Render("● No failed services.")}
		}
	}
	return column([]box{{title: title, lines: lines, flex: true}}, w, h)
}

// eventLines renders the newest events.
func (m *Model) eventLines(limit int) []string {
	var out []string
	for i, e := range m.eventList {
		if limit > 0 && i == limit {
			break
		}
		sev := map[string]level{"info": lvNeutral, "warning": lvWarn, "critical": lvCrit}[e.Severity]
		out = append(out, sMuted.Render(pad(e.Time.Local().Format("Jan 02 15:04:05"), 16))+badge(sev, pad(e.Severity, 8))+" "+e.Message)
	}
	if len(out) == 0 {
		out = []string{sMuted.Render("no events yet")}
	}
	return out
}

func (m *Model) eventsScreen(w, h int) string {
	status := badge(lvOK, "live")
	switch {
	case m.streamLive:
	case m.streamErr != "":
		status = badge(lvWarn, "reconnecting") + sMuted.Render(" — "+m.streamErr)
	default:
		status = badge(lvUnknown, "connecting")
	}
	filter := "all severities"
	if f := severityFilters[m.evFilter]; f != "" {
		filter = f + " and above"
	}
	more := ""
	if m.evMore {
		more = " · m for older"
	}
	title := "Events · " + status + sMuted.Render(fmt.Sprintf(" · %s · %d shown, newest first%s", filter, len(m.eventList), more))
	return column([]box{{title: title, lines: m.eventLines(0), flex: true}}, w, h)
}

func (m *Model) netConfigLines(inner int) []string {
	if m.network == nil {
		return waiting()
	}
	n := m.network
	var out []string
	withheld := false
	if d := n.Interfaces.Data; d != nil {
		for _, ni := range *d {
			if len(ni.Addresses) > 0 {
				out = append(out, sMuted.Render(pad(ni.Name, 12))+strings.Join(ni.Addresses, "  "))
			}
			withheld = withheld || slices.Contains(ni.Redacted, "addresses")
		}
	}
	if withheld || n.Routes.Status == apiv1.PermissionDenied && strings.Contains(n.Routes.Reason, "network:read") {
		// D27: the key lacks network:read; say so once instead of per item.
		return append(out, sMuted.Render("Addresses, routes and DNS need a key with the network:read scope."))
	}
	if r := n.Routes.Data; r != nil {
		for _, rt := range *r {
			if !rt.Default {
				continue
			}
			via := rt.Gateway
			if via == "" {
				via = "direct"
			}
			out = append(out, sMuted.Render(pad("default", 12))+"via "+via+sMuted.Render(fmt.Sprintf(" dev %s metric %d (%s)", rt.Interface, rt.Metric, rt.Family)))
		}
	} else {
		out = append(out, sMuted.Render("routes: "+availabilityText(n.Routes.Status)))
	}
	if d := n.DNS.Data; d != nil {
		dns := strings.Join(d.Nameservers, ", ")
		if dns == "" {
			dns = sWarn.Render("no nameservers configured")
		}
		line := sMuted.Render(pad("dns", 12)) + dns
		if d.Resolver == "systemd-resolved" {
			line += sMuted.Render("  (via systemd-resolved)")
		}
		if len(d.Search) > 0 {
			line += sMuted.Render("  search " + strings.Join(d.Search, " "))
		}
		out = append(out, line)
	} else {
		out = append(out, sMuted.Render("dns: "+availabilityText(n.DNS.Status)))
	}
	_ = inner
	return out
}

// kvw is kv with a fixed label width of 9 (labels up to "features").
func kvw(k, v string) string { return sMuted.Render(pad(k, 9)) + v }

// ---- Phase 3 screens ----

func rangeLabel(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// chart draws points as a line chart w columns by h rows over [from, to].
// Time spans without data stay empty (gaps are visible, not zero).
func chart(points []apiv1.HistoryPoint, w, h int, top float64, from, to time.Time) []string {
	if w < 1 || h < 1 {
		return nil
	}
	cols := 2 * w
	sum := make([]float64, cols)
	n := make([]int, cols)
	span := to.Sub(from)
	for _, p := range points {
		if p.Time.Before(from) || p.Time.After(to) || span <= 0 {
			continue
		}
		c := int(float64(p.Time.Sub(from)) / float64(span) * float64(cols))
		c = min(max(c, 0), cols-1)
		sum[c] += p.Avg
		n[c]++
	}
	values := make([]float64, cols)
	for c := range values {
		values[c] = math.NaN()
		if n[c] > 0 {
			values[c] = sum[c] / float64(n[c])
		}
	}
	return lineChart(values, w, h, top)
}

// sumSeries adds series point-by-point (same timestamps).
func sumSeries(series []apiv1.HistorySeries) []apiv1.HistoryPoint {
	byT := map[time.Time]float64{}
	for _, s := range series {
		for _, p := range s.Points {
			byT[p.Time] += p.Avg
		}
	}
	out := make([]apiv1.HistoryPoint, 0, len(byT))
	for t, v := range byT {
		out = append(out, apiv1.HistoryPoint{Time: t, Min: v, Avg: v, Max: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func fmtValue(v float64, unit string) string {
	switch unit {
	case "percent":
		return fmt.Sprintf("%.1f%%", v)
	case "bytes_per_second":
		return rate(&v)
	case "bytes":
		return bytes(uint64(math.Max(0, v)))
	case "celsius":
		return fmt.Sprintf("%.0f °C", v)
	case "watts":
		return fmt.Sprintf("%.0f W", v)
	case "volts":
		return fmt.Sprintf("%.2f V", v)
	case "hertz":
		return fmt.Sprintf("%.0f Hz", v)
	case "rpm":
		return fmt.Sprintf("%.0f rpm", v)
	case "seconds":
		return fmt.Sprintf("%.2f s", v)
	case "per_second":
		return fmt.Sprintf("%.2f/s", v)
	case "seconds_per_second":
		return fmt.Sprintf("%.2f s/s", v)
	case "count":
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprintf("%.2f", v)
}

var chartTitles = map[string]string{
	"cpu.usage_percent": "CPU usage", "memory.used_percent": "Memory used", "gpu.0.utilization_percent": "GPU busy",
	"sensors.cpu_temperature_celsius": "CPU temperature", "cpu.load_1m": "Load (1m)",
}

func (m *Model) performanceScreen(w, h int) string {
	inner := w - 4
	switch {
	case m.histErr != "":
		return column([]box{{title: "Performance", lines: []string{badge(lvUnknown, "history unavailable"), sMuted.Render(m.histErr)}}}, w, h)
	case m.histData == nil:
		return column([]box{{title: "Performance", lines: waiting()}}, w, h)
	}
	d := m.histData
	type plot struct {
		title  string
		unit   string
		points []apiv1.HistoryPoint
		top    float64
	}
	var plots []plot
	var rx, tx []apiv1.HistorySeries
	for _, s := range d.Series {
		switch {
		case strings.HasSuffix(s.Name, ".rx_bytes_per_second"):
			rx = append(rx, s)
		case strings.HasSuffix(s.Name, ".tx_bytes_per_second"):
			tx = append(tx, s)
		default:
			top := 0.0
			if s.Unit == "percent" {
				top = 100
			}
			plots = append(plots, plot{title: chartTitles[s.Name], unit: s.Unit, points: s.Points, top: top})
		}
	}
	if len(rx) > 0 {
		plots = append(plots, plot{title: "Network received (all interfaces)", unit: "bytes_per_second", points: sumSeries(rx)})
	}
	if len(tx) > 0 {
		plots = append(plots, plot{title: "Network sent (all interfaces)", unit: "bytes_per_second", points: sumSeries(tx)})
	}

	twoCols := w >= 120
	cw := w
	if twoCols {
		cw = w / 2
	}
	chartW := max(10, cw-4-11) // after a scale label of up to 10 characters
	const rows = 4
	var boxes []box
	for _, p := range plots {
		if len(p.points) == 0 {
			continue
		}
		lo, hi, sum := math.Inf(1), math.Inf(-1), 0.0
		for _, pt := range p.points {
			lo, hi, sum = math.Min(lo, pt.Min), math.Max(hi, pt.Max), sum+pt.Avg
		}
		avg := sum / float64(len(p.points))
		top := p.top
		if top <= 0 {
			top = hi
		}
		lines := chart(p.points, chartW, rows, top, d.From, d.To)
		for i := range lines {
			label := ""
			switch i {
			case 0:
				label = fmtValue(top, p.unit)
			case rows - 1:
				label = fmtValue(0, p.unit)
			}
			lines[i] = sMuted.Render(padLeft(label, 10)) + " " + lines[i]
		}
		axis := pad(d.From.Local().Format("Jan 02 15:04"), chartW/2) + padLeft(d.To.Local().Format("15:04"), chartW-chartW/2)
		lines = append(lines, strings.Repeat(" ", 11)+sMuted.Render(axis))
		title := fmt.Sprintf("%s · avg %s · min %s · max %s", p.title, fmtValue(avg, p.unit), fmtValue(lo, p.unit), fmtValue(hi, p.unit))
		boxes = append(boxes, box{title: title, lines: lines})
	}
	if b, ok := m.customBox(cw - 4); ok {
		boxes = append(boxes, b)
	}
	if len(boxes) == 0 {
		return column([]box{{title: "Performance", lines: []string{sMuted.Render("No history recorded for this range yet. Samples are taken every 10 seconds.")}}}, w, h)
	}
	header := sMuted.Render(fmt.Sprintf(" Last %s · %s resolution · t to change the range", rangeLabel(historyRanges[m.histRange]), d.Resolution))
	h--
	if !twoCols {
		return header + "\n" + column(boxes, w, h)
	}
	var left, right []box
	for i, b := range boxes {
		if i%2 == 0 {
			left = append(left, b)
		} else {
			right = append(right, b)
		}
	}
	_ = inner
	return header + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, column(left, cw, h), column(right, w-cw, h))
}

func fmtSetting(f settingField, h *apiv1.HealthSettings) string {
	if h == nil {
		return "—"
	}
	v := f.get(h)
	if f.whole {
		return strconv.Itoa(int(v))
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// settingsChanges lists edited fields as "label: old -> new".
func (m *Model) settingsChanges() []string {
	if m.draft == nil || m.settings == nil {
		return nil
	}
	cur := m.settings.Health
	var out []string
	for _, f := range settingFields {
		if f.get(m.draft) != f.get(&cur) {
			out = append(out, fmt.Sprintf("%s: %s → %s %s", f.label, fmtSetting(f, &cur), fmtSetting(f, m.draft), f.unit))
		}
	}
	return out
}

func (m *Model) settingsScreen(w, h int) string {
	local := []string{
		kv("refresh", m.opts.Refresh.String()) + sMuted.Render("  (this session; + and - change it)"),
		kv("agent", m.opts.Target),
	}
	var lines []string
	switch {
	case !m.isAdmin():
		lines = []string{sMuted.Render("Health thresholds can be changed only through the local admin socket.")}
	case m.settingsErr != "":
		lines = []string{sCrit.Render("✖ " + m.settingsErr)}
	case m.settings == nil:
		lines = waiting()
	default:
		shown := &m.settings.Health
		if m.draft != nil {
			shown = m.draft
		}
		src := "from the configuration file"
		if m.settings.Source == "override" {
			src = "changed at runtime"
			if m.settings.UpdatedAt != nil {
				src += " on " + m.settings.UpdatedAt.Local().Format("2006-01-02 15:04")
			}
			src += " (d resets to the file)"
		}
		lines = append(lines, sMuted.Render("Thresholds "+src+"."), "")
		for i, f := range settingFields {
			cursor := "  "
			if i == m.setField {
				cursor = sAccent.Render("▶ ")
			}
			val := fmtSetting(f, shown)
			if m.draft != nil && f.get(m.draft) != f.get(&m.settings.Health) {
				val = sWarn.Render(val + " *")
			}
			lines = append(lines, cursor+pad(f.label, 36)+padLeft(val, 10)+" "+sMuted.Render(f.unit))
		}
		if m.draft != nil {
			lines = append(lines, "", sWarn.Render("* unsaved — w to save, esc to discard"))
		}
	}
	return column([]box{{title: "Terminal UI", lines: local}, {title: "Health thresholds", lines: lines, flex: true}}, w, h)
}

// customBox lists generic metrics from extension collectors (D22).
func (m *Model) customBox(w int) (box, bool) {
	if m.metrics == nil || m.metrics.Custom == nil || len(*m.metrics.Custom) == 0 {
		return box{}, false
	}
	var lines []string
	for _, set := range *m.metrics.Custom {
		head := sAccent.Render(set.Collector)
		if set.Data == nil || set.Status != apiv1.Available {
			reason := set.Reason
			if reason == "" {
				reason = set.Status
			}
			lines = append(lines, head+"  "+badge(lvUnknown, set.Status)+" "+sMuted.Render(truncate(reason, max(10, w-30))))
			continue
		}
		if set.DroppedSeries > 0 {
			head += sMuted.Render(fmt.Sprintf("  %d dropped: %s", set.DroppedSeries, truncate(set.Problem, max(10, w-40))))
		}
		lines = append(lines, head)
		for _, x := range *set.Data {
			name := strings.TrimPrefix(x.Name, set.Collector+".")
			if len(x.Labels) > 0 {
				keys := make([]string, 0, len(x.Labels))
				for k := range x.Labels {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, len(keys))
				for i, k := range keys {
					parts[i] = k + "=" + x.Labels[k]
				}
				name += " {" + strings.Join(parts, ", ") + "}"
			}
			val := fmtValue(x.Value, x.Unit)
			if x.Kind == "counter" {
				val = "—"
				if x.RatePerSecond != nil {
					val = fmtValue(*x.RatePerSecond, x.RateUnit)
				}
			}
			nameW := max(10, w-14)
			lines = append(lines, "  "+pad(truncate(name, nameW), nameW)+padLeft(val, 12))
		}
	}
	return box{title: "Extension metrics", lines: lines}, true
}

// containersView lists containers found through cgroup v2 (D24).
func (m *Model) containersView(w, h int) string {
	inner := w - 4
	title := "Containers"
	var lines []string
	switch {
	case m.containersErr != "":
		lines = []string{sCrit.Render("✖ " + m.containersErr)}
		if strings.Contains(m.containersErr, "processes:read") {
			lines = append(lines, sMuted.Render("Create a key with the processes:read scope, or use the local admin socket."))
		}
	case m.containers == nil:
		lines = waiting()
	case m.containers.Containers.Data == nil:
		lines = unavailable(m.containers.Containers, inner)
	case len(m.containers.Containers.Data.Items) == 0:
		lines = []string{sMuted.Render("No Docker, Podman, containerd or CRI-O containers are running.")}
	default:
		d := m.containers.Containers.Data
		title = fmt.Sprintf("Containers · %d running", d.Total)
		opt := func(v *uint64, f func(uint64) string) string {
			if v == nil {
				return "—"
			}
			return f(*v)
		}
		lines = append(lines, sMuted.Render(pad("RUNTIME", 11)+pad("ID", 14)+padLeft("CPU%", 7)+padLeft("MEMORY", 11)+padLeft("LIMIT", 11)+
			padLeft("READ/s", 11)+padLeft("WRITE/s", 11)+padLeft("PIDS", 6)+"  "+"OWNER"))
		for _, c := range d.Items {
			cpu, cl := "—", lvNeutral
			if c.CPUPercent != nil {
				cpu = fmt.Sprintf("%.1f", *c.CPUPercent)
				cl = percentLevel(*c.CPUPercent, 50, 90)
			}
			owner := "root"
			if c.UID != nil {
				owner = fmt.Sprintf("uid %d (rootless)", *c.UID)
			}
			limit := "none"
			if c.MemoryLimitBytes != nil {
				limit = bytes(*c.MemoryLimitBytes)
			}
			lines = append(lines, pad(c.Runtime, 11)+pad(c.ID[:min(12, len(c.ID))], 14)+cl.style().Render(padLeft(cpu, 7))+
				padLeft(opt(c.MemoryBytes, bytes), 11)+padLeft(limit, 11)+padLeft(rate(c.IOReadBytesPerSecond), 11)+padLeft(rate(c.IOWriteBytesPerSecond), 11)+
				padLeft(opt(c.PIDs, func(n uint64) string { return strconv.FormatUint(n, 10) }), 6)+"  "+owner)
		}
		lines = append(lines, sMuted.Render(truncate("CPU % of one core · "+d.NamesReason, inner)))
	}
	return column([]box{{title: title, lines: lines, flex: true}}, w, h)
}
