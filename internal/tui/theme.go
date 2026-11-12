package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Palette: dark charcoal background, subtle borders, teal for neutral
// information, green/amber/red for health. Color is never the only signal:
// every status also has a symbol and a word.
var (
	colBackground = lipgloss.Color("#1d2025")
	colBorder     = lipgloss.Color("#3a4049")
	colBorderHi   = lipgloss.Color("#5c8a8a")
	colText       = lipgloss.Color("#d0d6de")
	colMuted      = lipgloss.Color("#8a939e")
	colAccent     = lipgloss.Color("#4fc1b9") // teal: neutral information
	colOK         = lipgloss.Color("#7ccf8a")
	colWarn       = lipgloss.Color("#e9b85b")
	colCrit       = lipgloss.Color("#f0716b")
)

var (
	sText    = lipgloss.NewStyle().Foreground(colText)
	sMuted   = lipgloss.NewStyle().Foreground(colMuted)
	sAccent  = lipgloss.NewStyle().Foreground(colAccent)
	sBold    = lipgloss.NewStyle().Foreground(colText).Bold(true)
	sTitle   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	sOK      = lipgloss.NewStyle().Foreground(colOK)
	sWarn    = lipgloss.NewStyle().Foreground(colWarn)
	sCrit    = lipgloss.NewStyle().Foreground(colCrit)
	sTabOn   = lipgloss.NewStyle().Foreground(colBackground).Background(colAccent).Bold(true).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	sKeyHint = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
)

// level is a severity used for coloring and labels.
type level int

const (
	lvNeutral level = iota
	lvOK
	lvWarn
	lvCrit
	lvUnknown
)

func (l level) style() lipgloss.Style {
	switch l {
	case lvOK:
		return sOK
	case lvWarn:
		return sWarn
	case lvCrit:
		return sCrit
	case lvUnknown:
		return sMuted
	}
	return sAccent
}

// badge renders a status as symbol + word, so it reads without color.
func badge(l level, word string) string {
	sym := map[level]string{lvOK: "●", lvWarn: "▲", lvCrit: "✖", lvUnknown: "?", lvNeutral: "•"}[l]
	return l.style().Render(sym + " " + word)
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

func healthBadge(status string) string {
	return badge(healthLevel(status), strings.ToUpper(status))
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
func bar(percent float64, width int, l level) string {
	if width < 1 {
		return ""
	}
	if math.IsNaN(percent) {
		percent = 0
	}
	percent = math.Max(0, math.Min(100, percent))
	filled := int(math.Round(percent / 100 * float64(width)))
	return l.style().Render(strings.Repeat("█", filled)) + sMuted.Render(strings.Repeat("░", width-filled))
}

// sparkRunes draw one bar per value, e.g. per-core CPU.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last 2×width values as a one-row line chart, scaled
// to max (or the data's own maximum when max <= 0).
func sparkline(values []float64, width int, max float64) string {
	if width < 1 {
		return ""
	}
	return lineChart(values, width, 1, max)[0]
}

// panel draws a bordered box with a title.
func panel(title, body string, width, height int, focused bool) string {
	border := colBorder
	if focused {
		border = colBorderHi
	}
	inner := width - 4 // border + padding
	if inner < 1 {
		inner = 1
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = truncate(l, inner)
	}
	content := sTitle.Render(truncate(title, inner)) + "\n" + strings.Join(lines, "\n")
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(width)
	if height > 0 {
		st = st.Height(height)
		// Clip content that would overflow the panel.
		maxLines := height - 2
		if cl := strings.Split(content, "\n"); maxLines > 0 && len(cl) > maxLines {
			content = strings.Join(cl[:maxLines], "\n")
		}
	}
	return st.Render(content)
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
