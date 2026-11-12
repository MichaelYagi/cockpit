package tui

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// Line charts are drawn with braille characters: each terminal cell holds
// a 2 × 4 grid of dots, so a chart w cells wide and h rows high has 2w
// points across and 4h levels up. Consecutive points are joined so the
// result reads as a line rather than a row of bars.

// brailleBits[col][row] is the dot bit for a cell's column (0 left, 1
// right) and row (0 top … 3 bottom).
var brailleBits = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// lineChart draws values (one per dot column; NaN means no data) as a line
// w cells wide and h rows high, scaled so top is the highest level. Only
// the last 2w values are drawn; fewer values are aligned right. A point
// without data leaves a gap, and a measured zero still shows a dot on the
// bottom row, so a gap is never mistaken for zero.
func lineChart(line lipgloss.Style, values []float64, w, h int, top float64) []string {
	if w < 1 || h < 1 {
		return nil
	}
	cols, levels := 2*w, 4*h
	if len(values) > cols {
		values = values[len(values)-cols:]
	}
	offset := cols - len(values)
	if top <= 0 {
		for _, v := range values {
			if !math.IsNaN(v) {
				top = math.Max(top, v)
			}
		}
	}
	cells := make([][]rune, h)
	for r := range cells {
		cells[r] = make([]rune, w)
	}
	set := func(x, level int) { // level 0 is the bottom
		row := levels - 1 - level
		cells[row/4][x/2] |= brailleBits[x%2][row%4]
	}
	prev := -1
	for i, v := range values {
		if math.IsNaN(v) {
			prev = -1
			continue
		}
		level := 0
		if top > 0 {
			level = int(math.Round(math.Max(0, math.Min(1, v/top)) * float64(levels-1)))
		}
		x := offset + i
		set(x, level)
		// Join to the previous point with a vertical run in this column.
		if prev >= 0 {
			for l := min(prev, level) + 1; l < max(prev, level); l++ {
				set(x, l)
			}
		}
		prev = level
	}
	out := make([]string, h)
	for r, row := range cells {
		var b strings.Builder
		for _, c := range row {
			if c == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(0x2800 + c)
			}
		}
		out[r] = line.Render(b.String())
	}
	return out
}
