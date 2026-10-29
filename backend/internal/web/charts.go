package web

import (
	"fmt"
	"html/template"
	"strings"
)

// barChart renders a simple vertical-bar SVG.
func barChart(points []ChartPoint, w, h int, color string) template.HTML {
	if len(points) == 0 {
		return ""
	}
	pad := 28
	innerW := w - pad*2
	innerH := h - pad*2
	max := 0.0
	for _, p := range points {
		if p.Value > max {
			max = p.Value
		}
	}
	if max == 0 {
		max = 1
	}
	barW := float64(innerW) / float64(len(points))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx;">`, w, h, w)
	// baseline + y max label
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#e5e7eb"/>`, pad, h-pad, w-pad, h-pad)
	fmt.Fprintf(&b, `<text x="4" y="%d" font-size="10" fill="#6b7280">%.0f</text>`, pad+4, max)
	fmt.Fprintf(&b, `<text x="4" y="%d" font-size="10" fill="#6b7280">0</text>`, h-pad+4)
	for i, p := range points {
		x := float64(pad) + float64(i)*barW
		height := (p.Value / max) * float64(innerH)
		y := float64(h-pad) - height
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" rx="2"><title>%s: %.0f</title></rect>`,
			x+1, y, barW-2, height, color, p.Label, p.Value)
		if len(points) <= 20 && i%max2(len(points)/8, 1) == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="9" fill="#9ca3af" text-anchor="middle">%s</text>`,
				x+barW/2, h-pad+14, p.Label)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// hBarChart renders a horizontal bar chart (good for status breakdown).
func hBarChart(bars []ChartBar, w, h int) template.HTML {
	if len(bars) == 0 {
		return ""
	}
	pad := 8
	innerW := w - 160
	rowH := (h - pad*2) / len(bars)
	if rowH < 24 {
		rowH = 24
	}
	max := 0.0
	for _, b := range bars {
		if b.Value > max {
			max = b.Value
		}
	}
	if max == 0 {
		max = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx;">`, w, pad*2+rowH*len(bars), w)
	for i, bar := range bars {
		y := pad + i*rowH
		fmt.Fprintf(&b, `<text x="0" y="%d" font-size="12" fill="#374151">%s</text>`, y+rowH/2+4, bar.Label)
		length := (bar.Value / max) * float64(innerW)
		fmt.Fprintf(&b, `<rect x="140" y="%d" width="%.1f" height="%d" fill="%s" rx="3"><title>%s: %.0f</title></rect>`,
			y+rowH/4, length, rowH/2, bar.Color, bar.Label, bar.Value)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="11" fill="#374151" font-weight="600">%.0f</text>`,
			140+length+6, y+rowH/2+4, bar.Value)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// lineChart renders a smooth line + area.
func lineChart(points []ChartPoint, w, h int, color string) template.HTML {
	if len(points) == 0 {
		return ""
	}
	pad := 28
	innerW := w - pad*2
	innerH := h - pad*2
	max := 0.0
	for _, p := range points {
		if p.Value > max {
			max = p.Value
		}
	}
	if max == 0 {
		max = 1
	}
	step := float64(innerW) / float64(max2(len(points)-1, 1))
	var line, area strings.Builder
	line.WriteString("M")
	area.WriteString(fmt.Sprintf("M %d,%d L", pad, h-pad))
	for i, p := range points {
		x := float64(pad) + float64(i)*step
		y := float64(h-pad) - (p.Value/max)*float64(innerH)
		if i > 0 {
			line.WriteString(" L")
			area.WriteString(" L")
		}
		fmt.Fprintf(&line, " %.1f,%.1f", x, y)
		fmt.Fprintf(&area, " %.1f,%.1f", x, y)
	}
	fmt.Fprintf(&area, " L %.1f,%d Z", float64(pad)+float64(len(points)-1)*step, h-pad)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx;">`, w, h, w)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#e5e7eb"/>`, pad, h-pad, w-pad, h-pad)
	fmt.Fprintf(&b, `<text x="4" y="%d" font-size="10" fill="#6b7280">$%.0f</text>`, pad+4, max)
	fmt.Fprintf(&b, `<path d="%s" fill="%s" fill-opacity="0.08"/>`, area.String(), color)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round"/>`, line.String(), color)
	for i, p := range points {
		x := float64(pad) + float64(i)*step
		y := float64(h-pad) - (p.Value/max)*float64(innerH)
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.5" fill="%s"><title>%s: $%.2f</title></circle>`, x, y, color, p.Label, p.Value)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
