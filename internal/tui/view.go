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
	v.BackgroundColor = m.st.c.bg
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
		return clip(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, m.st.text.Render(msg)), w, h)
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
	left := m.st.title.Render("COCKPIT")
	if m.system != nil && m.system.System.Data != nil {
		s := m.system.System.Data
		parts := []string{m.st.bold.Render(s.Hostname)}
		if s.OS.PrettyName != "" {
			parts = append(parts, s.OS.PrettyName)
		}
		if s.UptimeSeconds != nil {
			parts = append(parts, "up "+duration(time.Duration(*s.UptimeSeconds)*time.Second))
		}
		left += "  " + m.st.muted.Render(strings.Join(parts, m.st.muted.Render(" · ")))
	}
	right := m.st.muted.Render("health ") + badge(m.st, lvUnknown, "UNKNOWN")
	if m.health != nil {
		right = m.st.muted.Render("health ") + healthBadge(m.st, m.health.Status)
	}
	if m.lastErr != "" {
		right = badge(m.st, lvCrit, "DISCONNECTED")
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
			parts = append(parts, m.st.tabOn.Render(label))
		} else {
			parts = append(parts, m.st.tabOff.Render(label))
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
		hints = []string{"[ ] theme", "↑↓ select", "enter edit", "w save", "d reset", "esc discard", "+/- refresh"}
	}
	if m.modal != modalNone {
		hints = []string{"enter confirm", "esc cancel"}
	}
	for i, h := range hints {
		k, rest, _ := strings.Cut(h, " ")
		hints[i] = m.st.keyHint.Render(k) + " " + m.st.muted.Render(rest)
	}
	left := strings.Join(hints, m.st.muted.Render("  "))
	var right string
	switch {
	case m.flash != "":
		right = m.st.lv(m.flashLevel).Render(m.flash)
	case m.lastErr != "":
		right = m.st.crit.Render("✖ " + m.lastErr)
	case !m.lastOK.IsZero():
		right = m.st.muted.Render("updated " + ago(m.lastOK, m.opts.Now()) + " · " + m.opts.Target)
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
func column(st *styles, boxes []box, w, h int) string {
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
			lines = append(append([]string(nil), lines[:max(0, heights[i]-1)]...), st.muted.Render(fmt.Sprintf("… %d more", len(b.lines)-heights[i]+1)))
		}
		out = append(out, panel(st, b.title, strings.Join(lines, "\n"), w, 0, false))
	}
	if dropped := len(boxes) - keep; dropped > 0 && used < h {
		var names []string
		for _, b := range boxes[keep:] {
			name, _, _ := strings.Cut(b.title, " · ") // drop inline stats
			names = append(names, name)
		}
		out = append(out, st.muted.Render(truncate(" Not shown at this size: "+strings.Join(names, ", ")+". Enlarge the terminal or use the other screens.", w)))
	}
	return strings.Join(out, "\n")
}

// columns lays out two stacks side by side when wide enough, else one.
func columns(st *styles, left, right []box, w, h int) string {
	if w >= 100 {
		lw := w / 2
		return lipgloss.JoinHorizontal(lipgloss.Top, column(st, left, lw, h), column(st, right, w-lw, h))
	}
	return column(st, append(left, right...), w, h)
}

// unavailable explains a section without data.
func unavailable[T any](st *styles, s apiv1.Section[T], inner int) []string {
	out := []string{badge(st, lvUnknown, availabilityText(s.Status))}
	if s.Reason != "" {
		for _, l := range wrapText(s.Reason, inner) {
			out = append(out, st.muted.Render(l))
		}
	}
	return out
}

func staleMark[T any](st *styles, s apiv1.Section[T]) string {
	if s.Stale || (s.Status != apiv1.Available && s.Data != nil) {
		return " " + st.warn.Render("(stale)")
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

func kv(st *styles, k, v string) string { return st.muted.Render(pad(k, max(7, len(k)+1))) + v }

func waiting(st *styles) []string { return []string{st.muted.Render("waiting for data…")} }

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
	return columns(m.st, left, right, w, h)
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
		panel(m.st, "CPU · Memory", strings.Join(left, "\n"), lw, 0, false),
		panel(m.st, "Health", strings.Join(right, "\n"), rw, 0, false))
	rest := h - lipgloss.Height(top)
	return top + "\n" + column(m.st, []box{
		{title: "Storage", lines: m.filesystemLines(w-4, 4), flex: true},
		{title: "Network", lines: m.networkSummary(w - 4), flex: true},
	}, w, rest)
}

func (m *Model) systemLines(inner int) []string {
	if m.system == nil {
		return waiting(m.st)
	}
	s := m.system.System
	if s.Data == nil {
		return unavailable(m.st, s, inner)
	}
	d := s.Data
	out := []string{kv(m.st, "host", m.st.bold.Render(d.Hostname)), kv(m.st, "os", d.OS.PrettyName+m.st.muted.Render(" · ")+d.Architecture), kv(m.st, "kernel", d.Kernel)}
	if d.UptimeSeconds != nil {
		out = append(out, kv(m.st, "uptime", duration(time.Duration(*d.UptimeSeconds)*time.Second)))
	}
	if m.hardware != nil && m.hardware.CPU.Data != nil {
		c := m.hardware.CPU.Data
		out = append(out, kv(m.st, "cpu", fmt.Sprintf("%s · %d threads", c.ModelName, c.LogicalCores)))
	}
	if m.hardware != nil && m.hardware.Memory.Data != nil {
		out = append(out, kv(m.st, "memory", bytes(m.hardware.Memory.Data.TotalBytes)))
	}
	return out
}

func (m *Model) cpuLines(inner int) []string {
	if m.metrics == nil || m.metrics.CPU == nil {
		return waiting(m.st)
	}
	s := *m.metrics.CPU
	if s.Data == nil {
		return unavailable(m.st, s, inner)
	}
	d := s.Data
	barW := max(8, inner-16)
	var out []string
	if d.UsagePercent != nil {
		u := *d.UsagePercent
		out = append(out, kv(m.st, "usage", bar(m.st, u, barW, percentLevel(u, 85, 95))+" "+pct(u))+staleMark(m.st, s))
	} else {
		out = append(out, kv(m.st, "usage", m.st.muted.Render("measuring…")))
	}
	out = append(out, kv(m.st, "trend", sparkline(m.st.series(0), m.cpuHist, inner-7, 100)))
	if d.LoadAverage != nil {
		out = append(out, kv(m.st, "load", fmt.Sprintf("%.2f  %.2f  %.2f", d.LoadAverage.One, d.LoadAverage.Five, d.LoadAverage.Fifteen)+m.st.muted.Render("  1m 5m 15m")))
	}
	if len(d.PerCoreUsagePercent) > 0 {
		var cells strings.Builder
		for _, c := range d.PerCoreUsagePercent {
			i := int(c / 100 * float64(len(sparkRunes)-1))
			cells.WriteString(m.st.lv(percentLevel(c, 85, 95)).Render(string(sparkRunes[max(0, min(i, len(sparkRunes)-1))])))
		}
		out = append(out, kv(m.st, "cores", truncate(cells.String(), inner-7)))
	}
	return out
}

func (m *Model) memoryLines(inner int) []string {
	if m.metrics == nil || m.metrics.Memory == nil {
		return waiting(m.st)
	}
	s := *m.metrics.Memory
	if s.Data == nil {
		return unavailable(m.st, s, inner)
	}
	d := s.Data
	barW := max(8, inner-16)
	out := []string{
		kv(m.st, "used", bar(m.st, d.UsedPercent, barW, percentLevel(d.UsedPercent, 90, 95))+" "+pct(d.UsedPercent)) + staleMark(m.st, s),
		kv(m.st, "avail", bytes(d.AvailableBytes)+m.st.muted.Render(" of "+bytes(d.TotalBytes))),
	}
	if d.SwapTotalBytes > 0 {
		out = append(out, kv(m.st, "swap", bar(m.st, d.SwapUsedPercent, barW, percentLevel(d.SwapUsedPercent, 80, 95))+" "+pct(d.SwapUsedPercent)))
	} else {
		out = append(out, kv(m.st, "swap", m.st.muted.Render("none")))
	}
	if p := m.metrics.MemoryPressure; p != nil {
		if p.Data != nil {
			out = append(out, kv(m.st, "stall", fmt.Sprintf("%.1f%% full · %.1f%% some", p.Data.FullAvg60Percent, p.Data.SomeAvg60Percent)+m.st.muted.Render(" (60s)")))
		} else {
			out = append(out, kv(m.st, "stall", m.st.muted.Render(availabilityText(p.Status))))
		}
	}
	out = append(out, kv(m.st, "trend", sparkline(m.st.series(1), m.memHist, inner-7, 100)))
	return out
}

func (m *Model) healthSummary(inner int) []string {
	if m.health == nil {
		return waiting(m.st)
	}
	out := []string{healthBadge(m.st, m.health.Status) + m.st.muted.Render("  evaluated "+ago(m.health.EvaluatedAt, m.opts.Now()))}
	for _, c := range m.health.Checks {
		detail := c.Summary
		if c.Status == apiv1.HealthUnknown {
			detail = availabilityText(c.Availability)
		}
		out = append(out, pad(badge(m.st, healthLevel(c.Status), pad(strings.ToUpper(c.Status), 8)), 11)+pad(c.Name, 18)+m.st.muted.Render(detail))
	}
	return out
}

func (m *Model) filesystemLines(inner, limit int) []string {
	if m.metrics == nil || m.metrics.Filesystems == nil {
		return waiting(m.st)
	}
	s := *m.metrics.Filesystems
	if s.Data == nil {
		return unavailable(m.st, s, inner)
	}
	fss := append([]apiv1.Filesystem(nil), (*s.Data)...)
	sort.SliceStable(fss, func(i, j int) bool { return fss[i].UsedPercent > fss[j].UsedPercent })
	nameW := min(18, max(8, inner/4))
	barW := max(6, inner-nameW-34) // name, bar, percent, "used / total"
	var out []string
	for i, f := range fss {
		if limit > 0 && i == limit {
			out = append(out, m.st.muted.Render(fmt.Sprintf("… %d more on the Storage screen", len(fss)-limit)))
			break
		}
		size := bytes(f.UsedBytes) + " / " + bytes(f.TotalBytes)
		out = append(out, pad(f.MountPoint, nameW)+" "+bar(m.st, f.UsedPercent, barW, percentLevel(f.UsedPercent, 85, 95))+" "+pct(f.UsedPercent)+" "+m.st.muted.Render(padLeft(size, 23)))
	}
	if len(out) == 0 {
		out = append(out, m.st.muted.Render("no filesystems"))
	}
	return out
}

func (m *Model) networkSummary(inner int) []string {
	if m.metrics == nil || m.metrics.Network == nil {
		return waiting(m.st)
	}
	s := *m.metrics.Network
	if s.Data == nil {
		return unavailable(m.st, s, inner)
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
		kv(m.st, "rx", padLeft(rate(&rx), 12)+" "+sparkline(m.st.series(0), m.rxHist, sw, 0)),
		kv(m.st, "tx", padLeft(rate(&tx), 12)+" "+sparkline(m.st.series(1), m.txHist, sw, 0)),
	}
	ifaces := append([]apiv1.NetworkInterface(nil), (*s.Data)...)
	sort.SliceStable(ifaces, func(i, j int) bool { return ifaces[i].OperState == "up" && ifaces[j].OperState != "up" })
	for _, n := range ifaces {
		if n.Loopback {
			continue
		}
		out = append(out, m.st.muted.Render(pad(n.Name, 12))+pad(stateText(m.st, n.OperState), 10)+m.st.muted.Render("↓ ")+padLeft(rate(n.RxBytesPerSecond), 11)+m.st.muted.Render("  ↑ ")+padLeft(rate(n.TxBytesPerSecond), 11))
	}
	return out
}

func stateText(st *styles, s string) string {
	switch s {
	case "up":
		return badge(st, lvOK, "up")
	case "down":
		return st.muted.Render("○ down")
	}
	return st.muted.Render(s)
}

func (m *Model) temperatureLines(inner int) []string {
	if m.metrics == nil || m.metrics.Sensors == nil {
		return waiting(m.st)
	}
	s := *m.metrics.Sensors
	if s.Data == nil {
		return unavailable(m.st, s, inner)
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
		out = append(out, pad(name, max(10, inner-12))+" "+m.st.lv(l).Render(padLeft(fmt.Sprintf("%.1f °C", sn.Value), 10)))
	}
	if len(out) == 0 {
		out = append(out, m.st.muted.Render("no temperature sensors"))
	}
	return out
}

func (m *Model) healthScreen(w, h int) string {
	inner := w - 4
	if m.health == nil {
		return column(m.st, []box{{title: "Health", lines: waiting(m.st)}}, w, h)
	}
	var lines []string
	lines = append(lines, healthBadge(m.st, m.health.Status)+m.st.muted.Render("  evaluated "+ago(m.health.EvaluatedAt, m.opts.Now())), "")
	for _, c := range m.health.Checks {
		lines = append(lines, badge(m.st, healthLevel(c.Status), pad(strings.ToUpper(c.Status), 8))+" "+m.st.bold.Render(c.Name))
		if c.Status == apiv1.HealthUnknown {
			lines = append(lines, "  "+m.st.muted.Render(availabilityText(c.Availability)+": "+c.Reason))
		} else if len(c.Findings) == 0 {
			lines = append(lines, "  "+m.st.muted.Render(c.Summary))
		}
		for _, f := range c.Findings {
			sev := map[string]level{"warning": lvWarn, "critical": lvCrit}[f.Severity]
			lines = append(lines, "  "+badge(m.st, sev, f.Summary)+m.st.muted.Render("  since "+ago(f.Since, m.opts.Now())))
			var ev []string
			for k, v := range f.Evidence {
				ev = append(ev, k+"="+v)
			}
			sort.Strings(ev)
			for _, l := range wrapText(strings.Join(ev, "  "), inner-4) {
				lines = append(lines, "    "+m.st.muted.Render(l))
			}
			for _, l := range wrapText("→ "+f.Recommendation, inner-4) {
				lines = append(lines, "    "+m.st.accent.Render(l))
			}
		}
	}
	boxes := []box{{title: "Health checks", lines: lines, flex: true}}
	evLines := m.eventLines(20)
	if len(evLines) == 0 {
		evLines = []string{m.st.muted.Render("no events yet")}
	}
	boxes = append(boxes, box{title: "Recent events", lines: evLines, flex: true})
	return column(m.st, boxes, w, h)
}

func (m *Model) storageScreen(w, h int) string {
	inner := w - 4
	fsLines := m.filesystemLines(inner, 0)
	var devLines, healthLines []string
	switch {
	case m.storage == nil:
		devLines, healthLines = waiting(m.st), waiting(m.st)
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
			devLines = unavailable(m.st, d, inner)
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
				healthLines = append(healthLines, pad(dh.Device, 12)+badge(m.st, l, word)+m.st.muted.Render(extra))
			}
		} else {
			healthLines = unavailable(m.st, d, inner)
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
	return column(m.st, boxes, w, h)
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
		line := pad(p.Name, 14) + badge(m.st, l, p.State)
		if p.SizeBytes != nil && p.AllocatedBytes != nil && *p.SizeBytes > 0 {
			pct := float64(*p.AllocatedBytes) / float64(*p.SizeBytes) * 100
			line += "  " + bar(m.st, pct, 16, percentLevel(pct, 80, 90)) + fmt.Sprintf(" %5.1f%% of %s", pct, bytes(*p.SizeBytes))
		}
		if p.ReadErrors != nil && p.WriteErrors != nil && p.ChecksumErrors != nil {
			if n := *p.ReadErrors + *p.WriteErrors + *p.ChecksumErrors; n > 0 {
				line += "  " + m.st.warn.Render(fmt.Sprintf("errors r%d w%d c%d", *p.ReadErrors, *p.WriteErrors, *p.ChecksumErrors))
			}
		}
		lines = append(lines, line)
	}
	if a := z.ARC; a != nil {
		arc := fmt.Sprintf("ARC %s of %s max", bytes(a.SizeBytes), bytes(a.MaxBytes))
		if a.HitPercent != nil {
			arc += fmt.Sprintf(" · %.1f%% hits", *a.HitPercent)
		}
		lines = append(lines, m.st.muted.Render(arc))
	}
	if z.DetailReason != "" {
		lines = append(lines, m.st.muted.Render(truncate(z.DetailReason, w)))
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
					line += bar(m.st, *s.CapacityPercent, 12, percentLevel(100-*s.CapacityPercent, 80, 90)) + fmt.Sprintf(" %3.0f%%", *s.CapacityPercent)
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
					lines = append(lines, pad(s.Name, 8)+badge(m.st, l, word))
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
			line := pad("UPS "+x.Name, 14) + badge(m.st, l, word)
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
		return column(m.st, []box{{title: "Interfaces", lines: waiting(m.st)}}, w, h)
	}
	s := *m.metrics.Network
	if s.Data == nil {
		return column(m.st, []box{{title: "Interfaces", lines: unavailable(m.st, s, inner)}}, w, h)
	}
	head := m.st.muted.Render(pad("NAME", 14) + pad("STATE", 10) + padLeft("SPEED", 10) + padLeft("RX/s", 13) + padLeft("TX/s", 13) + padLeft("RX TOTAL", 12) + padLeft("TX TOTAL", 12) + padLeft("ERR/DROP", 10))
	lines := []string{head}
	for _, n := range *s.Data {
		speed := "—"
		if n.SpeedMbps != nil {
			speed = fmt.Sprintf("%d Mb/s", *n.SpeedMbps)
		}
		errs := n.RxErrors + n.TxErrors + n.RxDropped + n.TxDropped
		errText := m.st.muted.Render(padLeft(fmt.Sprint(errs), 10))
		if errs > 0 {
			errText = m.st.warn.Render(padLeft(fmt.Sprint(errs), 10))
		}
		name := n.Name
		if n.Loopback && n.Name != "lo" {
			name += m.st.muted.Render(" (loopback)")
		}
		lines = append(lines, pad(name, 14)+pad(stateText(m.st, n.OperState), 10)+padLeft(speed, 10)+padLeft(rate(n.RxBytesPerSecond), 13)+padLeft(rate(n.TxBytesPerSecond), 13)+
			padLeft(bytes(n.RxBytes), 12)+padLeft(bytes(n.TxBytes), 12)+errText)
	}
	sw := max(10, inner-16)
	trend := []string{kv(m.st, "rx", sparkline(m.st.series(0), m.rxHist, sw, 0)), kv(m.st, "tx", sparkline(m.st.series(1), m.txHist, sw, 0))}
	return column(m.st, []box{
		{title: "Interfaces" + staleMark(m.st, s), lines: lines, flex: true},
		{title: "Throughput (all interfaces)", lines: trend},
		{title: "Addresses, routes and DNS", lines: m.netConfigLines(inner), flex: true},
	}, w, h)
}

func (m *Model) accessScreen(w, h int) string {
	var srv []string
	switch {
	case m.status == nil:
		srv = waiting(m.st)
	case m.status.API == nil:
		srv = []string{
			kv(m.st, "agent", m.status.Agent.Version+m.st.muted.Render(" · API "+m.status.Agent.APIVersion)),
			kv(m.st, "via", m.opts.Target),
			m.st.muted.Render("Connected with an API key. Server details and key management"),
			m.st.muted.Render("are available only through the local admin socket."),
		}
	default:
		a := m.status.API
		srv = append(srv, kv(m.st, "agent", m.status.Agent.Version+m.st.muted.Render(" · API "+m.status.Agent.APIVersion+" · up "+duration(time.Duration(m.status.Agent.UptimeSeconds)*time.Second))))
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
			srv = append(srv, kv(m.st, "listen", l.Address+m.st.muted.Render("  "+t)))
		}
		r := a.Requests
		srv = append(srv, kv(m.st, "reqs", fmt.Sprintf("%d total", r.Total)+m.st.muted.Render(fmt.Sprintf("  2xx %d · 4xx %d · 5xx %d · auth failures %d · rate limited %d", r.Status2xx, r.Status4xx, r.Status5xx, r.AuthFailures, r.RateLimited))))
		srv = append(srv, kv(m.st, "clients", fmt.Sprintf("%d active", a.ActiveClients)+m.st.muted.Render(" (keys used in the last 5 minutes)")))
	}

	var keyLines []string
	switch {
	case !m.isAdmin():
		keyLines = []string{m.st.muted.Render("Key management requires the local admin socket (run cockpit tui on the agent's host).")}
	case m.keysErr != "":
		keyLines = []string{m.st.crit.Render("✖ " + m.keysErr)}
	case len(m.keys) == 0:
		keyLines = []string{m.st.muted.Render("No API keys yet. Press n to create one.")}
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
		keyLines = append(keyLines, m.st.muted.Render(pad("  ID", cw[0]+2)+pad("NAME", cw[1])+pad("SCOPES", cw[2])+pad("STATUS", cw[3])+pad("LAST USED", cw[4])+"EXPIRES"))
		for i, k := range m.keys {
			st := badge(m.st, lvOK, "active")
			switch {
			case k.RevokedAt != nil:
				st = badge(m.st, lvUnknown, "revoked")
			case !k.Active:
				st = badge(m.st, lvWarn, "expired")
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
				cursor = m.st.accent.Render("▶ ")
			}
			keyLines = append(keyLines, cursor+pad(k.ID, cw[0])+pad(k.Name, cw[1])+pad(scopeText(k.Scopes), cw[2])+pad(st, cw[3])+pad(m.st.muted.Render(last), cw[4])+m.st.muted.Render(exp))
		}
		keyLines = append(keyLines, "", m.st.muted.Render("Keys are shown in full only once, when created or rotated."))
	}
	return column(m.st, []box{{title: "API server", lines: srv}, {title: "API keys", lines: keyLines, flex: true}}, w, h)
}

func (m *Model) modalView(w int) string {
	inner := w - 4
	var title string
	var lines []string
	switch m.modal {
	case modalCreateName:
		title = "New API key · name"
		lines = []string{"Name for the key (e.g. grafana, backup-script):", "", m.st.accent.Render("› ") + m.input + m.st.accent.Render("█"), "", m.st.muted.Render("enter next · esc cancel")}
	case modalCreateScopes:
		title = "New API key · scopes"
		lines = []string{"Select what the key may read (space toggles, a selects all):", ""}
		desc := []string{"system identity and hardware inventory", "metrics, storage, network rates, sensors, health, events", "interface addresses, routes and DNS", "process list (users and resource use)"}
		for i, s := range issuableScopes {
			box := "[ ]"
			if m.scopeOn[i] {
				box = m.st.ok.Render("[x]")
			}
			cur := "  "
			if m.scopeCursor == i {
				cur = m.st.accent.Render("▶ ")
			}
			lines = append(lines, cur+box+" "+pad(s, 14)+m.st.muted.Render(desc[i]))
		}
		lines = append(lines, "", m.st.muted.Render("Keys are read-only. Admin access is never granted to keys."), m.st.muted.Render("enter create · esc cancel"))
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
		lines = append(lines, "", "Type the key ID to confirm:", m.st.accent.Render("› ")+m.input+m.st.accent.Render("█"), "", m.st.muted.Render("enter confirm · esc cancel"))
	case modalEditSetting:
		f := settingFields[m.setField]
		title = "Edit threshold"
		lines = []string{f.label + m.st.muted.Render("  (current "+fmtSetting(f, m.draft)+" "+f.unit+")"), "", m.st.accent.Render("› ") + m.input + m.st.accent.Render("█") + " " + m.st.muted.Render(f.unit), "", m.st.muted.Render("enter set · esc cancel")}
	case modalConfirmSettings:
		if m.pendingAction == "reset" {
			title = "Reset thresholds"
			lines = wrapText("Return every health threshold to the values in the configuration file? This applies immediately.", inner)
		} else {
			title = "Save thresholds"
			lines = wrapText("Apply the edited thresholds now? They take effect at the next health evaluation and are kept across restarts.", inner)
			for _, c := range m.settingsChanges() {
				lines = append(lines, m.st.accent.Render("  "+c))
			}
		}
		lines = append(lines, "", m.st.muted.Render("y apply · n cancel"))
	case modalReveal:
		title = "New API key — shown only once"
		k := m.revealed
		lines = []string{
			m.st.bold.Render(k.APIKey.Name) + m.st.muted.Render("  ("+strings.Join(k.APIKey.Scopes, ", ")+")"),
			"",
			m.st.warn.Render(k.Key),
			"",
		}
		lines = append(lines, wrapText("Copy this key now: press c, or select it with the mouse. When you press enter it is erased from this screen and can never be shown again. Send it as: Authorization: Bearer <key>", inner)...)
		if m.copied {
			lines = append(lines, "", m.st.ok.Render("Sent to the clipboard.")+m.st.muted.Render(" If paste does not work, your terminal blocks clipboard access; select the key instead."))
		}
		lines = append(lines, "", m.st.muted.Render("c copy · enter done"))
	}
	return panel(m.st, title, strings.Join(lines, "\n"), w, 0, true)
}

func (m *Model) helpView(w, h int) string {
	lines := []string{
		m.st.bold.Render("Navigation"),
		kv(m.st, "0-9 S", "switch screen") + "    " + kv(m.st, "tab", "next screen"),
		kv(m.st, "←/→", "previous / next") + "    " + kv(m.st, "click", "tab bar"),
		kv(m.st, "r", "refresh now") + "        " + kv(m.st, "?", "toggle help"),
		kv(m.st, "q", "quit"),
		"",
		m.st.bold.Render("Processes"),
		kv(m.st, "s", "change sort") + "        " + kv(m.st, "/", "search"),
		kv(m.st, "k", "kernel threads") + "     " + kv(m.st, "esc", "clear search"),
		kv(m.st, "c", "containers / processes"),
		"",
		m.st.bold.Render("Performance · Events · Settings"),
		kv(m.st, "t", "chart range") + "        " + kv(m.st, "v", "event severity") + "    " + kv(m.st, "m", "older events"),
		kv(m.st, "enter", "edit threshold") + "   " + kv(m.st, "w", "save") + "  " + kv(m.st, "d", "reset to file"),
		"",
		m.st.bold.Render("Services"),
		kv(m.st, "f", "failed only / all"),
		"",
		m.st.bold.Render("API Access"),
		kv(m.st, "↑/↓", "select key") + "        " + kv(m.st, "n", "create key"),
		kv(m.st, "o", "rotate key") + "         " + kv(m.st, "x", "revoke key"),
		"",
		m.st.bold.Render("Status symbols"),
		badge(m.st, lvOK, "OK") + "   " + badge(m.st, lvWarn, "WARNING") + "   " + badge(m.st, lvCrit, "CRITICAL") + "   " + badge(m.st, lvUnknown, "UNKNOWN / unavailable"),
		"",
		m.st.muted.Render("Data comes from the Cockpit API at " + m.opts.Target + "."),
	}
	return column(m.st, []box{{title: "Help", lines: lines}}, w, h)
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
		out = append(out, m.st.bold.Render(g.Name)+m.st.muted.Render("  "+g.Driver+" "+g.DriverVersion))
		if g.UtilizationPercent != nil {
			out = append(out, kv(m.st, "busy", bar(m.st, *g.UtilizationPercent, barW, percentLevel(*g.UtilizationPercent, 90, 98))+" "+pct(*g.UtilizationPercent)))
		}
		if g.MemoryTotalBytes != nil && g.MemoryUsedBytes != nil && *g.MemoryTotalBytes > 0 {
			p := float64(*g.MemoryUsedBytes) / float64(*g.MemoryTotalBytes) * 100
			out = append(out, kv(m.st, "vram", bar(m.st, p, barW, percentLevel(p, 90, 97))+" "+pct(p)))
		}
		var extra []string
		if g.TemperatureCelsius != nil {
			extra = append(extra, m.st.lv(percentLevel(*g.TemperatureCelsius, 83, 90)).Render(fmt.Sprintf("%.0f °C", *g.TemperatureCelsius)))
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
			out = append(out, kv(m.st, "", strings.Join(extra, m.st.muted.Render(" · "))))
		}
		switch {
		case g.MetricsSource == "":
			out = append(out, m.st.muted.Render("no metrics source for this GPU"))
		case g.UtilizationPercent == nil && (g.MetricsSource == "i915" || g.MetricsSource == "xe"):
			out = append(out, m.st.muted.Render("busy % needs perf access; not available to the agent"))
		}
	}
	return out
}

func (m *Model) hardwareScreen(w, h int) string {
	if m.hardware == nil {
		return column(m.st, []box{{title: "Hardware", lines: waiting(m.st)}}, w, h)
	}
	hw := m.hardware
	inner := w - 4
	var cpu []string
	if c := hw.CPU.Data; c != nil {
		cpu = append(cpu, kvw(m.st, "model", m.st.bold.Render(c.ModelName)), kvw(m.st, "vendor", c.Vendor+m.st.muted.Render(fmt.Sprintf("  family %s model %s stepping %s", c.Family, c.Model, c.Stepping))))
		topo := fmt.Sprintf("%d socket(s) · %d cores · %d threads", c.Sockets, c.Cores, c.LogicalCores)
		if c.MaxFrequencyMHz != nil {
			topo += fmt.Sprintf(" · up to %.2f GHz", *c.MaxFrequencyMHz/1000)
		}
		cpu = append(cpu, kvw(m.st, "layout", topo))
		var caches []string
		for _, ca := range c.Caches {
			caches = append(caches, fmt.Sprintf("L%d %s %s", ca.Level, strings.ToLower(ca.Type[:min(1, len(ca.Type))]), bytes(ca.SizeBytes)))
		}
		if len(caches) > 0 {
			cpu = append(cpu, kvw(m.st, "caches", strings.Join(caches, m.st.muted.Render(" · "))))
		}
		features := strings.Join(c.Flags, " ")
		if c.Virtualization != "" {
			features = c.Virtualization + "  " + features
		}
		if c.Hypervisor {
			features += m.st.muted.Render("  (running in a virtual machine)")
		}
		cpu = append(cpu, kvw(m.st, "features", features))
	} else {
		cpu = unavailable(m.st, hw.CPU, inner)
	}

	var mem []string
	if d := hw.Memory.Data; d != nil {
		mem = append(mem, kvw(m.st, "total", bytes(d.TotalBytes))+m.st.muted.Render("  swap "+bytes(d.SwapTotalBytes)))
	}
	if mm := hw.MemoryModules; mm.Data != nil {
		for _, mod := range *mm.Data {
			mem = append(mem, pad(mod.Locator, 12)+pad(bytes(mod.SizeBytes), 11)+pad(mod.Type, 7)+pad(fmt.Sprintf("%d MT/s", mod.SpeedMTs), 11)+m.st.muted.Render(mod.Manufacturer+" "+mod.PartNumber))
		}
	} else {
		mem = append(mem, m.st.muted.Render("modules: "+availabilityText(mm.Status)+" — "+mm.Reason))
	}

	var board []string
	if b := hw.Board.Data; b != nil {
		board = append(board, kvw(m.st, "system", strings.TrimSpace(b.SystemVendor+" "+b.ProductName)), kvw(m.st, "board", strings.TrimSpace(b.BoardVendor+" "+b.BoardName)),
			kvw(m.st, "firmware", strings.TrimSpace(b.BIOSVendor+" "+b.BIOSVersion+" "+b.BIOSDate)))
		if b.ChassisType != "" {
			board = append(board, kvw(m.st, "chassis", b.ChassisType))
		}
	} else {
		board = unavailable(m.st, hw.Board, inner)
	}

	var gpus []string
	if g := hw.GPUs; g.Data != nil {
		for _, d := range *g.Data {
			mem := ""
			if d.MemoryTotalBytes != nil {
				mem = "  " + bytes(*d.MemoryTotalBytes)
			}
			gpus = append(gpus, m.st.bold.Render(d.Name)+m.st.muted.Render(mem+"  "+d.Driver+" "+d.DriverVersion+"  "+d.PCIAddress))
		}
	} else {
		gpus = unavailable(m.st, g, inner)
	}

	var devs []string
	if p := hw.PCIDevices; p.Data != nil {
		for _, d := range *p.Data {
			name := d.DeviceName
			if name == "" {
				name = d.VendorID + ":" + d.DeviceID
			}
			devs = append(devs, m.st.muted.Render(pad(d.Address, 14))+pad(d.ClassName, 26)+pad(strings.TrimSpace(d.VendorName+" "+name), max(10, inner-56))+m.st.muted.Render(d.Driver))
		}
	} else {
		devs = unavailable(m.st, p, inner)
	}
	if u := hw.USBDevices; u.Data != nil {
		for _, d := range *u.Data {
			devs = append(devs, m.st.muted.Render(pad("usb "+d.Path, 14))+pad(strings.TrimSpace(d.Manufacturer+" "+d.Product), max(10, inner-14)))
		}
	} else {
		devs = append(devs, m.st.muted.Render("USB: "+availabilityText(u.Status)+" — "+u.Reason))
	}

	return column(m.st, []box{
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
			cursor = m.st.accent.Render("█")
		}
		title += " · search: " + m.procSearch + cursor
	}
	if m.procKernel {
		title += " · with kernel threads"
	}
	var lines []string
	switch {
	case m.procsErr != "":
		lines = []string{m.st.crit.Render("✖ " + m.procsErr)}
		if strings.Contains(m.procsErr, "processes:read") {
			lines = append(lines, m.st.muted.Render("Create a key with the processes:read scope, or use the local admin socket."))
		}
	case m.procs == nil:
		lines = waiting(m.st)
	case m.procs.Status != apiv1.Available:
		lines = []string{badge(m.st, lvUnknown, availabilityText(m.procs.Status)), m.st.muted.Render(m.procs.Reason)}
	default:
		p := m.procs
		nameW := max(10, inner-63)
		lines = append(lines, m.st.muted.Render(padLeft("PID", 7)+"  "+pad("USER", 12)+padLeft("CPU%", 7)+padLeft("MEM%", 7)+padLeft("RSS", 11)+padLeft("THR", 5)+"  "+pad("S", 3)+pad("NAME", nameW)))
		for _, pr := range p.Items {
			cpu := "—"
			cl := lvNeutral
			if pr.CPUPercent != nil {
				cpu = fmt.Sprintf("%.1f", *pr.CPUPercent)
				cl = percentLevel(*pr.CPUPercent, 50, 90)
			}
			state := pr.State
			if state == "D" || state == "Z" {
				state = m.st.warn.Render(state)
			}
			name := pr.Name
			if pr.KernelThread {
				name = m.st.muted.Render("[" + name + "]")
			}
			lines = append(lines, padLeft(fmt.Sprint(pr.PID), 7)+"  "+pad(pr.User, 12)+m.st.lv(cl).Render(padLeft(cpu, 7))+padLeft(fmt.Sprintf("%.1f", pr.MemoryPercent), 7)+
				padLeft(bytes(pr.RSSBytes), 11)+padLeft(fmt.Sprint(pr.Threads), 5)+"  "+pad(state, 3)+pad(name, nameW))
		}
		note := fmt.Sprintf("%d processes", p.Total)
		if p.Total > len(p.Items) {
			note = fmt.Sprintf("top %d of %d processes", len(p.Items), p.Total)
		}
		note += fmt.Sprintf(" · CPU %% of one core over %.1fs · command lines are not shown", p.WindowSeconds)
		lines = append(lines, m.st.muted.Render(note))
		if p.Partial {
			lines = append(lines, m.st.warn.Render("▲ "+p.Reason))
		}
	}
	return column(m.st, []box{{title: title, lines: lines, flex: true}}, w, h)
}

func (m *Model) servicesScreen(w, h int) string {
	inner := w - 4
	var lines []string
	title := "Services"
	switch {
	case m.svcErr != "":
		lines = []string{m.st.crit.Render("✖ " + m.svcErr)}
	case m.services == nil:
		lines = waiting(m.st)
	case m.services.Services.Data == nil:
		lines = unavailable(m.st, m.services.Services, inner)
	default:
		d := m.services.Services.Data
		title = fmt.Sprintf("Services · %d total · %d active · %d inactive · ", d.Total, d.Active, d.Inactive)
		if d.Failed > 0 {
			title += m.st.crit.Render(fmt.Sprintf("%d failed", d.Failed))
		} else {
			title += m.st.ok.Render("none failed")
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
				st = badge(m.st, lvCrit, pad("failed", 9))
			case "active":
				st = badge(m.st, lvOK, pad("active", 9))
			case "activating", "deactivating", "reloading":
				st = badge(m.st, lvWarn, pad(s.ActiveState, 9))
			default:
				st = m.st.muted.Render("○ " + pad(s.ActiveState, 9))
			}
			lines = append(lines, pad(s.Name, nameW)+" "+st+" "+m.st.muted.Render(pad(s.SubState, 9)+" "+s.Description))
		}
		if len(lines) == 0 {
			lines = []string{m.st.ok.Render("● No failed services.")}
		}
	}
	return column(m.st, []box{{title: title, lines: lines, flex: true}}, w, h)
}

// eventLines renders the newest events.
func (m *Model) eventLines(limit int) []string {
	var out []string
	for i, e := range m.eventList {
		if limit > 0 && i == limit {
			break
		}
		sev := map[string]level{"info": lvNeutral, "warning": lvWarn, "critical": lvCrit}[e.Severity]
		out = append(out, m.st.muted.Render(pad(e.Time.Local().Format("Jan 02 15:04:05"), 16))+badge(m.st, sev, pad(e.Severity, 8))+" "+e.Message)
	}
	if len(out) == 0 {
		out = []string{m.st.muted.Render("no events yet")}
	}
	return out
}

func (m *Model) eventsScreen(w, h int) string {
	status := badge(m.st, lvOK, "live")
	switch {
	case m.streamLive:
	case m.streamErr != "":
		status = badge(m.st, lvWarn, "reconnecting") + m.st.muted.Render(" — "+m.streamErr)
	default:
		status = badge(m.st, lvUnknown, "connecting")
	}
	filter := "all severities"
	if f := severityFilters[m.evFilter]; f != "" {
		filter = f + " and above"
	}
	more := ""
	if m.evMore {
		more = " · m for older"
	}
	title := "Events · " + status + m.st.muted.Render(fmt.Sprintf(" · %s · %d shown, newest first%s", filter, len(m.eventList), more))
	return column(m.st, []box{{title: title, lines: m.eventLines(0), flex: true}}, w, h)
}

func (m *Model) netConfigLines(inner int) []string {
	if m.network == nil {
		return waiting(m.st)
	}
	n := m.network
	var out []string
	withheld := false
	if d := n.Interfaces.Data; d != nil {
		for _, ni := range *d {
			if len(ni.Addresses) > 0 {
				out = append(out, m.st.muted.Render(pad(ni.Name, 12))+strings.Join(ni.Addresses, "  "))
			}
			withheld = withheld || slices.Contains(ni.Redacted, "addresses")
		}
	}
	if withheld || n.Routes.Status == apiv1.PermissionDenied && strings.Contains(n.Routes.Reason, "network:read") {
		// D27: the key lacks network:read; say so once instead of per item.
		return append(out, m.st.muted.Render("Addresses, routes and DNS need a key with the network:read scope."))
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
			out = append(out, m.st.muted.Render(pad("default", 12))+"via "+via+m.st.muted.Render(fmt.Sprintf(" dev %s metric %d (%s)", rt.Interface, rt.Metric, rt.Family)))
		}
	} else {
		out = append(out, m.st.muted.Render("routes: "+availabilityText(n.Routes.Status)))
	}
	if d := n.DNS.Data; d != nil {
		dns := strings.Join(d.Nameservers, ", ")
		if dns == "" {
			dns = m.st.warn.Render("no nameservers configured")
		}
		line := m.st.muted.Render(pad("dns", 12)) + dns
		if d.Resolver == "systemd-resolved" {
			line += m.st.muted.Render("  (via systemd-resolved)")
		}
		if len(d.Search) > 0 {
			line += m.st.muted.Render("  search " + strings.Join(d.Search, " "))
		}
		out = append(out, line)
	} else {
		out = append(out, m.st.muted.Render("dns: "+availabilityText(n.DNS.Status)))
	}
	_ = inner
	return out
}

// kvw is kv with a fixed label width of 9 (labels up to "features").
func kvw(st *styles, k, v string) string { return st.muted.Render(pad(k, 9)) + v }

// ---- Phase 3 screens ----

func rangeLabel(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// chart draws points as a line chart w columns by h rows over [from, to].
// Time spans without data stay empty (gaps are visible, not zero).
func chart(line lipgloss.Style, points []apiv1.HistoryPoint, w, h int, top float64, from, to time.Time) []string {
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
	return lineChart(line, values, w, h, top)
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
		return column(m.st, []box{{title: "Performance", lines: []string{badge(m.st, lvUnknown, "history unavailable"), m.st.muted.Render(m.histErr)}}}, w, h)
	case m.histData == nil:
		return column(m.st, []box{{title: "Performance", lines: waiting(m.st)}}, w, h)
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
	for i, p := range plots {
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
		lines := chart(m.st.series(i), p.points, chartW, rows, top, d.From, d.To)
		for r := range lines {
			label := ""
			switch r {
			case 0:
				label = fmtValue(top, p.unit)
			case rows - 1:
				label = fmtValue(0, p.unit)
			}
			lines[r] = m.st.muted.Render(padLeft(label, 10)) + " " + lines[r]
		}
		axis := pad(d.From.Local().Format("Jan 02 15:04"), chartW/2) + padLeft(d.To.Local().Format("15:04"), chartW-chartW/2)
		lines = append(lines, strings.Repeat(" ", 11)+m.st.muted.Render(axis))
		title := fmt.Sprintf("%s · avg %s · min %s · max %s", p.title, fmtValue(avg, p.unit), fmtValue(lo, p.unit), fmtValue(hi, p.unit))
		boxes = append(boxes, box{title: title, lines: lines})
	}
	if b, ok := m.customBox(cw - 4); ok {
		boxes = append(boxes, b)
	}
	if len(boxes) == 0 {
		return column(m.st, []box{{title: "Performance", lines: []string{m.st.muted.Render("No history recorded for this range yet. Samples are taken every 10 seconds.")}}}, w, h)
	}
	header := m.st.muted.Render(fmt.Sprintf(" Last %s · %s resolution · t to change the range", rangeLabel(historyRanges[m.histRange]), d.Resolution))
	h--
	if !twoCols {
		return header + "\n" + column(m.st, boxes, w, h)
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
	return header + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, column(m.st, left, cw, h), column(m.st, right, w-cw, h))
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
		kv(m.st, "refresh", m.opts.Refresh.String()) + m.st.muted.Render("  (this session; + and - change it)"),
		kv(m.st, "agent", m.opts.Target),
	}
	var lines []string
	switch {
	case !m.isAdmin():
		lines = []string{m.st.muted.Render("Health thresholds can be changed only through the local admin socket.")}
	case m.settingsErr != "":
		lines = []string{m.st.crit.Render("✖ " + m.settingsErr)}
	case m.settings == nil:
		lines = waiting(m.st)
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
		lines = append(lines, m.st.muted.Render("Thresholds "+src+"."), "")
		for i, f := range settingFields {
			cursor := "  "
			if i == m.setField {
				cursor = m.st.accent.Render("▶ ")
			}
			val := fmtSetting(f, shown)
			if m.draft != nil && f.get(m.draft) != f.get(&m.settings.Health) {
				val = m.st.warn.Render(val + " *")
			}
			lines = append(lines, cursor+pad(f.label, 36)+padLeft(val, 10)+" "+m.st.muted.Render(f.unit))
		}
		if m.draft != nil {
			lines = append(lines, "", m.st.warn.Render("* unsaved — w to save, esc to discard"))
		}
	}
	return column(m.st, []box{{title: "Theme", lines: m.themeLines(w - 4)}, {title: "Terminal UI", lines: local}, {title: "Health thresholds", lines: lines, flex: true}}, w, h)
}

// themeLines lists every theme with a swatch drawn in its own colors:
// single-color themes, then mixed ones, side by side when there is room.
func (m *Model) themeLines(inner int) []string {
	var single, mixed []string
	for _, t := range themes {
		ts := newStyles(t)
		cursor, name := "  ", t.name
		if t.id == m.st.c.id {
			cursor, name = m.st.accent.Render("▶ "), m.st.bold.Render(t.name)
		}
		row := cursor + pad(name, 10) + ts.accent.Render("██") + " " + ts.title.Render("██") + " " + ts.accent3.Render("██") + "  " +
			ts.ok.Render("●") + ts.warn.Render("▲") + ts.crit.Render("✖") + "  " + ts.series(0).Render("⣀⡠⠔⠊") + ts.series(1).Render("⠉⠒⢄⣀")
		if t.single {
			single = append(single, row)
		} else {
			mixed = append(mixed, row)
		}
	}
	single = append([]string{m.st.muted.Render("Single color")}, single...)
	mixed = append([]string{m.st.muted.Render("Mixed")}, mixed...)
	lines := []string{m.st.muted.Render("[ and ] switch theme. It is saved for you on this machine.")}
	const colW = 46
	if inner < 2*colW {
		return append(append(lines, single...), mixed...)
	}
	for i := 0; i < max(len(single), len(mixed)); i++ {
		l, r := "", ""
		if i < len(single) {
			l = single[i]
		}
		if i < len(mixed) {
			r = mixed[i]
		}
		lines = append(lines, pad(l, colW)+r)
	}
	return lines
}

// customBox lists generic metrics from extension collectors (D22).
func (m *Model) customBox(w int) (box, bool) {
	if m.metrics == nil || m.metrics.Custom == nil || len(*m.metrics.Custom) == 0 {
		return box{}, false
	}
	var lines []string
	for _, set := range *m.metrics.Custom {
		head := m.st.accent.Render(set.Collector)
		if set.Data == nil || set.Status != apiv1.Available {
			reason := set.Reason
			if reason == "" {
				reason = set.Status
			}
			lines = append(lines, head+"  "+badge(m.st, lvUnknown, set.Status)+" "+m.st.muted.Render(truncate(reason, max(10, w-30))))
			continue
		}
		if set.DroppedSeries > 0 {
			head += m.st.muted.Render(fmt.Sprintf("  %d dropped: %s", set.DroppedSeries, truncate(set.Problem, max(10, w-40))))
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
		lines = []string{m.st.crit.Render("✖ " + m.containersErr)}
		if strings.Contains(m.containersErr, "processes:read") {
			lines = append(lines, m.st.muted.Render("Create a key with the processes:read scope, or use the local admin socket."))
		}
	case m.containers == nil:
		lines = waiting(m.st)
	case m.containers.Containers.Data == nil:
		lines = unavailable(m.st, m.containers.Containers, inner)
	case len(m.containers.Containers.Data.Items) == 0:
		lines = []string{m.st.muted.Render("No Docker, Podman, containerd or CRI-O containers are running.")}
	default:
		d := m.containers.Containers.Data
		title = fmt.Sprintf("Containers · %d running", d.Total)
		opt := func(v *uint64, f func(uint64) string) string {
			if v == nil {
				return "—"
			}
			return f(*v)
		}
		lines = append(lines, m.st.muted.Render(pad("RUNTIME", 11)+pad("ID", 14)+padLeft("CPU%", 7)+padLeft("MEMORY", 11)+padLeft("LIMIT", 11)+
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
			lines = append(lines, pad(c.Runtime, 11)+pad(c.ID[:min(12, len(c.ID))], 14)+m.st.lv(cl).Render(padLeft(cpu, 7))+
				padLeft(opt(c.MemoryBytes, bytes), 11)+padLeft(limit, 11)+padLeft(rate(c.IOReadBytesPerSecond), 11)+padLeft(rate(c.IOWriteBytesPerSecond), 11)+
				padLeft(opt(c.PIDs, func(n uint64) string { return strconv.FormatUint(n, 10) }), 6)+"  "+owner)
		}
		lines = append(lines, m.st.muted.Render(truncate("CPU % of one core · "+d.NamesReason, inner)))
	}
	return column(m.st, []box{{title: title, lines: lines, flex: true}}, w, h)
}
