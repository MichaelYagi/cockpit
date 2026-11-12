package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// A theme is a set of colors. Every theme keeps a dark background, subtle
// borders and green/amber/red for health; themes differ in their accents.
// Color is never the only signal: every status also has a symbol and a
// word.
type theme struct {
	id, name string
	single   bool // one accent hue; listed first in Settings
	bg       color.Color
	border   color.Color
	borderHi color.Color // focused panel border
	text     color.Color
	muted    color.Color
	accent   color.Color // tabs, key hints, the main chart line
	accent2  color.Color // panel titles
	accent3  color.Color // second chart line (e.g. memory, network sent)
	ok       color.Color
	warn     color.Color
	crit     color.Color
}

func hex(s string) color.Color { return lipgloss.Color(s) }

// themes lists every theme: single-color ones first, then mixed. The first
// is the default.
var themes = []theme{
	single("teal", "Teal", "#1d2025", "#3a4049", "#5c8a8a", "#4fc1b9", "#8fdcd5"),
	single("ocean", "Ocean", "#1b2029", "#34405a", "#4f6f9a", "#5aa9f0", "#a3cdf6"),
	single("violet", "Violet", "#1f1d27", "#3d3850", "#6f5f9a", "#a98bf0", "#d0bff8"),
	single("rose", "Rose", "#231d21", "#483842", "#9a5f78", "#f08bb0", "#f7c3d6"),
	single("graphite", "Graphite", "#1e1f21", "#3c3e42", "#6b6f76", "#c3c9d1", "#8f969f"),
	mixed("aurora", "Aurora", "#1a1f26", "#36404d", "#6a5fa8", "#4fd1c5", "#b392f0", "#6cb6ff"),
	mixed("lagoon", "Lagoon", "#1a2027", "#33404c", "#3f8f8a", "#6cb6ff", "#4fd1c5", "#f08bb0"),
	mixed("orchid", "Orchid", "#211d25", "#433a4c", "#8a5f9a", "#f08bb0", "#b392f0", "#4fd1c5"),
	mixed("dusk", "Dusk", "#201d27", "#3f3850", "#9a5f86", "#a98bf0", "#f08bb0", "#6cb6ff"),
}

func single(id, name, bg, border, borderHi, accent, light string) theme {
	t := mixed(id, name, bg, border, borderHi, accent, accent, light)
	t.single = true
	return t
}

func mixed(id, name, bg, border, borderHi, accent, accent2, accent3 string) theme {
	return theme{id: id, name: name, bg: hex(bg), border: hex(border), borderHi: hex(borderHi),
		text: hex("#d0d6de"), muted: hex("#8a939e"), accent: hex(accent), accent2: hex(accent2), accent3: hex(accent3),
		ok: hex("#7ccf8a"), warn: hex("#e9b85b"), crit: hex("#f0716b")}
}

// ThemeIDs lists the theme IDs, for flag validation and help.
func ThemeIDs() []string {
	ids := make([]string, len(themes))
	for i, t := range themes {
		ids[i] = t.id
	}
	return ids
}

// themeByID returns the theme with this ID, or the default.
func themeByID(id string) theme {
	for _, t := range themes {
		if t.id == id {
			return t
		}
	}
	return themes[0]
}

// styles are the lipgloss styles of one theme. Models hold their own; the
// helpers that draw take them as their first argument.
type styles struct {
	c                                    theme
	text, muted, accent, accent3, bold   lipgloss.Style
	title, ok, warn, crit, tabOn, tabOff lipgloss.Style
	keyHint                              lipgloss.Style
}

func newStyles(t theme) *styles {
	n := lipgloss.NewStyle
	return &styles{
		c:       t,
		text:    n().Foreground(t.text),
		muted:   n().Foreground(t.muted),
		accent:  n().Foreground(t.accent),
		accent3: n().Foreground(t.accent3),
		bold:    n().Foreground(t.text).Bold(true),
		title:   n().Foreground(t.accent2).Bold(true),
		ok:      n().Foreground(t.ok),
		warn:    n().Foreground(t.warn),
		crit:    n().Foreground(t.crit),
		tabOn:   n().Foreground(t.bg).Background(t.accent).Bold(true).Padding(0, 1),
		tabOff:  n().Foreground(t.muted).Padding(0, 1),
		keyHint: n().Foreground(t.accent).Bold(true),
	}
}

// series is the style of the i-th line in a group of charts.
func (st *styles) series(i int) lipgloss.Style {
	switch i % 3 {
	case 1:
		return st.accent3
	case 2:
		return st.title.UnsetBold()
	}
	return st.accent
}

// level is a severity used for coloring and labels.
type level int

const (
	lvNeutral level = iota
	lvOK
	lvWarn
	lvCrit
	lvUnknown
)

// lv is the style for a level.
func (st *styles) lv(l level) lipgloss.Style {
	switch l {
	case lvOK:
		return st.ok
	case lvWarn:
		return st.warn
	case lvCrit:
		return st.crit
	case lvUnknown:
		return st.muted
	}
	return st.accent
}

// badge renders a status as symbol + word, so it reads without color.
func badge(st *styles, l level, word string) string {
	sym := map[level]string{lvOK: "●", lvWarn: "▲", lvCrit: "✖", lvUnknown: "?", lvNeutral: "•"}[l]
	return st.lv(l).Render(sym + " " + word)
}

func healthLevel(status string) level {
	switch status {
	case apiv1.HealthOK:
		return lvOK
	case apiv1.HealthWarning:
		return lvWarn
	case apiv1.HealthCritical:
		return lvCrit
	}
	return lvUnknown
}

func healthBadge(st *styles, status string) string {
	return badge(st, healthLevel(status), strings.ToUpper(status))
}

// availabilityText explains why a section has no data.
func availabilityText(status string) string {
	switch status {
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

// percentLevel maps a usage percentage to a level using health defaults.
func percentLevel(p, warn, crit float64) level {
	switch {
	case p >= crit:
		return lvCrit
	case p >= warn:
		return lvWarn
	}
	return lvOK
}

// bar draws a horizontal usage bar of the given width, e.g. ███████░░░.
func bar(st *styles, percent float64, width int, l level) string {
	if width < 1 {
		return ""
	}
	if math.IsNaN(percent) {
		percent = 0
	}
	percent = math.Max(0, math.Min(100, percent))
	filled := int(math.Round(percent / 100 * float64(width)))
	return st.lv(l).Render(strings.Repeat("█", filled)) + st.muted.Render(strings.Repeat("░", width-filled))
}

// sparkRunes draw one bar per value, e.g. per-core CPU.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last 2×width values as a one-row line chart, scaled
// to max (or the data's own maximum when max <= 0).
func sparkline(line lipgloss.Style, values []float64, width int, max float64) string {
	if width < 1 {
		return ""
	}
	return lineChart(line, values, width, 1, max)[0]
}

// panel draws a bordered box with a title.
func panel(st *styles, title, body string, width, height int, focused bool) string {
	border := st.c.border
	if focused {
		border = st.c.borderHi
	}
	inner := width - 4 // border + padding
	if inner < 1 {
		inner = 1
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = truncate(l, inner)
	}
	content := st.title.Render(truncate(title, inner)) + "\n" + strings.Join(lines, "\n")
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(width)
	if height > 0 {
		frame = frame.Height(height)
		// Clip content that would overflow the panel.
		maxLines := height - 2
		if cl := strings.Split(content, "\n"); maxLines > 0 && len(cl) > maxLines {
			content = strings.Join(cl[:maxLines], "\n")
		}
	}
	return frame.Render(content)
}

// truncate shortens a (possibly styled) line to width cells.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// pad right-pads or truncates s to exactly width cells.
func pad(s string, width int) string {
	s = truncate(s, width)
	if w := lipgloss.Width(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

func padLeft(s string, width int) string {
	s = truncate(s, width)
	if w := lipgloss.Width(s); w < width {
		s = strings.Repeat(" ", width-w) + s
	}
	return s
}

// ---- formatting ----

func bytes(n uint64) string {
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

func rate(bps *float64) string {
	if bps == nil {
		return "—"
	}
	return bytes(uint64(math.Max(0, *bps))) + "/s"
}

func pct(p float64) string { return fmt.Sprintf("%5.1f%%", p) }

func duration(d time.Duration) string {
	d = d.Round(time.Minute)
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

func ago(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02")
}
