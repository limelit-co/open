// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// Charts, drawn as SVG on the server.
//
// No charting library. Every chart renders and reads without JavaScript:
// <title> elements are the tooltips and end labels are the legend.
// static/charts.js adds hover only, reads values off data-* attributes Go
// wrote, and computes nothing about the data. Everything here emits an SVG
// string the templates drop inline, so the charts inherit the page's CSS
// custom properties and follow light and dark without a second palette.
//
// Two rules this file follows.
//
// No colour is emitted from Go. A hex string baked into markup cannot be
// theme-aware, and for the grid the readable ink flips at a different step in
// each theme. Go decides which bin a value lands in; CSS decides what a bin
// looks like.
//
// The degenerate cases come first. A new install has one day of data, or
// none. A series with one point has no line to draw, and the usual way that
// fails is a divide by len(points)-1.

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/metrics"
)

// thinN is the sample under which a day's point is drawn hollow. One
// constant, shared with the LowN caption, so the chart and the copy agree
// about what "thin" means.
const thinN = metrics.LowNThreshold

// Trend geometry. The viewBox is fixed and the SVG scales inside its
// container.
const (
	trendW, trendH = 720.0, 180.0
	trendPadX      = 10.0
	trendPadTop    = 14.0
	trendPadBottom = 26.0
)

// TrendPoint is one day on the visibility trend.
type TrendPoint struct {
	Day   string
	Value float64
	N     int
}

// TrendChart draws visibility over time.
//
// The y axis is pinned to 0-100 rather than fitted to the data. A fitted axis
// turns a move from 2% to 3% into a visual doubling, which for a visibility
// metric is the most misleading thing a chart can do.
//
// Gaps are never filled. metrics.Series returns only the days that actually
// ran, and drawing a line across a day nobody measured would show a period
// that was never observed.
func TrendChart(points []TrendPoint) template.HTML {
	if len(points) == 0 {
		return ""
	}

	var b strings.Builder
	// xMidYMid meet, not none. Stretching the viewBox shears the axis glyphs
	// and scales the stroke anisotropically, so a wide container gets fat
	// horizontal strokes and squashed text.
	fmt.Fprintf(&b, `<svg class="chart chart-trend" viewBox="0 0 %.0f %.0f" preserveAspectRatio="xMidYMid meet" role="img" aria-label="%s">`,
		trendW, trendH, template.HTMLEscapeString(trendLabel(points)))

	// Gridlines carry no information a screen reader needs; the label above
	// already states the shape.
	for _, pct := range []float64{0, 25, 50, 75, 100} {
		y := trendY(pct)
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" aria-hidden="true"/>`,
			trendPadX, y, trendW-trendPadX, y)
	}

	x := func(i int) float64 {
		if len(points) == 1 {
			// A single point sits in the middle. At the left edge it reads as
			// the start of a line that failed to draw.
			return trendW / 2
		}
		span := trendW - 2*trendPadX
		return trendPadX + span*float64(i)/float64(len(points)-1)
	}

	if len(points) > 1 {
		var line, area strings.Builder
		for i, p := range points {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), trendY(p.Value))
		}
		fmt.Fprintf(&area, "M%.1f %.1f ", x(0), trendY(points[0].Value))
		for i := 1; i < len(points); i++ {
			fmt.Fprintf(&area, "L%.1f %.1f ", x(i), trendY(points[i].Value))
		}
		fmt.Fprintf(&area, "L%.1f %.1f L%.1f %.1f Z", x(len(points)-1), trendY(0), x(0), trendY(0))

		fmt.Fprintf(&b, `<path class="area" d="%s"/>`, strings.TrimSpace(area.String()))
		fmt.Fprintf(&b, `<path class="line" d="%s"/>`, strings.TrimSpace(line.String()))
	}

	// Dots last, so they sit above the line. With one point this is the whole
	// chart and it has to look deliberate.
	dotEvery := 1
	if len(points) > 45 {
		dotEvery = len(points) / 45
	}
	for i, p := range points {
		if len(points) > 1 && i%dotEvery != 0 && i != len(points)-1 {
			continue
		}
		r := 3.0
		if len(points) == 1 {
			r = 5.0
		}
		fmt.Fprintf(&b, `<circle class="dot" cx="%.1f" cy="%.1f" r="%.1f"><title>%s: %s of %s</title></circle>`,
			x(i), trendY(p.Value), r, template.HTMLEscapeString(p.Day), pct(p.Value)+"%", answersWord(p.N))
	}

	if len(points) == 1 {
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`,
			x(0), trendH-8, template.HTMLEscapeString(points[0].Day))
	} else {
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f">%s</text>`, trendPadX, trendH-8, template.HTMLEscapeString(points[0].Day))
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="end">%s</text>`, trendW-trendPadX, trendH-8, template.HTMLEscapeString(points[len(points)-1].Day))
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// trendLabel describes the series for a screen reader, which cannot see it.
func trendLabel(points []TrendPoint) string {
	if len(points) == 1 {
		return fmt.Sprintf("Visibility on %s: %s%% of %s. One day of history.",
			points[0].Day, pct(points[0].Value), answersWord(points[0].N))
	}
	first, last := points[0], points[len(points)-1]
	return fmt.Sprintf("Visibility across %d days, %s to %s. From %s%% to %s%%.",
		len(points), first.Day, last.Day, pct(first.Value), pct(last.Value))
}

func trendY(v float64) float64 {
	usable := trendH - trendPadTop - trendPadBottom
	return trendPadTop + usable*(1-clampPct(v)/100)
}

// Sparkline is a bare line for a table cell: no axes, no labels, no scale.
// It answers which way this is going and nothing more.
func Sparkline(values []float64) template.HTML {
	if len(values) < 3 {
		// Two points is a segment, not a trend, and it implies a shape the
		// data does not support.
		return ""
	}
	const w, h, pad = 68.0, 20.0, 3.0

	var d strings.Builder
	for i, v := range values {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		x := pad + (w-2*pad)*float64(i)/float64(len(values)-1)
		y := pad + (h-2*pad)*(1-clampPct(v)/100)
		fmt.Fprintf(&d, "%s%.1f %.1f ", cmd, x, y)
	}

	last := values[len(values)-1]
	lastX := w - pad
	lastY := pad + (h-2*pad)*(1-clampPct(last)/100)

	return template.HTML(fmt.Sprintf(
		`<svg class="spark" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" preserveAspectRatio="xMidYMid meet" aria-hidden="true">`+
			`<path class="spark-line" d="%s" fill="none"/>`+
			`<circle class="spark-dot" cx="%.1f" cy="%.1f" r="2"/>`+
			`</svg>`,
		w, h, w, h, strings.TrimSpace(d.String()), lastX, lastY))
}

// CellMinN is the sample below which a grid cell cannot reach the top of the
// heat ramp.
//
// One answer that named you is 100%, and painting it the same as five of five
// would let a single lucky answer look like dominance. Below this the fill is
// capped one step short, and the cell still prints its own fraction so the
// reader can see exactly what it rests on.
const CellMinN = 5

// cellBin picks the heat class for one grid cell.
//
// It returns a class, never a colour. The readable ink over each step differs
// by theme and flips at a different step in each, which CSS can express and a
// Go string cannot.
func cellBin(answers int, percent float64) string {
	if answers == 0 {
		return "hm-none"
	}
	// A measured zero is its own bin, drawn at full strength. Greying it is
	// the most common way a real finding reads as a broken widget.
	if percent <= 0 {
		return "hm-0"
	}
	bin := 1
	switch {
	case percent >= 80:
		bin = 5
	case percent >= 60:
		bin = 4
	case percent >= 40:
		bin = 3
	case percent >= 20:
		bin = 2
	}
	if answers < CellMinN && bin > 4 {
		bin = 4
	}
	return fmt.Sprintf("hm-%d", bin)
}

func clampPct(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// dateAxis is the x axis of a chart whose points are measured days.
//
// The demo's first week ran daily and the rest ran roughly weekly. Spacing
// points by index draws the two the same width, so a week of daily samples
// looks like it took as long as a month of weekly ones, and a reader sees a
// "dip" that is only a change of cadence. Spacing by calendar time puts each
// day where it happened; a stretch nobody measured is drawn as a shaded band
// with a dashed bridge, never as a curve.
type dateAxis struct {
	xs []float64
	// bridge[i] is true when the span from day i to day i+1 had no run.
	bridge []bool
	// ts is the parsed day, or zero when the label did not parse.
	ts []time.Time
	// perDay is how many x units one calendar day occupies.
	perDay float64
	// dated is false when the labels were not dates and the axis fell back
	// to even spacing.
	dated bool
}

// dateScale places measured days between x0 and x1 in proportion to time.
//
// A gap longer than twice the typical gap is a bridge, where typical is the
// lower median: for an odd count the middle gap, for an even count the
// smaller of the two middle gaps. Under that rule a daily series with one
// missed day stays solid, a weekly series is solid throughout, and a daily
// week followed by a month of weekly runs draws every weekly span as a
// bridge. The lower median matters with two gaps: a seeded history that
// ends, then two live daily runs a month later, has gaps of 29 and 1, and
// the mean of those (15) would let a curve be drawn across 29 unmeasured
// days. Any parse failure or a single day falls back to index spacing with
// no bridges.
func dateScale(days []string, x0, x1 float64) dateAxis {
	n := len(days)
	ax := dateAxis{xs: make([]float64, n), bridge: make([]bool, maxInt(n-1, 0)), ts: make([]time.Time, n)}
	span := x1 - x0
	if n == 0 {
		return ax
	}
	fallback := func() dateAxis {
		for i := range days {
			if n == 1 {
				ax.xs[i] = (x0 + x1) / 2
			} else {
				ax.xs[i] = x0 + span*float64(i)/float64(n-1)
			}
		}
		ax.perDay = span
		if n > 1 {
			ax.perDay = span / float64(n-1)
		}
		return ax
	}
	if n == 1 {
		if t, err := time.Parse("2006-01-02", days[0]); err == nil {
			ax.ts[0], ax.dated = t, true
		}
		return fallback()
	}
	for i, d := range days {
		t, err := time.Parse("2006-01-02", d)
		if err != nil || (i > 0 && !t.After(ax.ts[i-1])) {
			ax.ts = make([]time.Time, n)
			return fallback()
		}
		ax.ts[i] = t
	}
	ax.dated = true
	first := ax.ts[0]
	dayOf := func(t time.Time) float64 { return t.Sub(first).Hours() / 24 }
	spanDays := dayOf(ax.ts[n-1])
	if spanDays < 1 {
		spanDays = 1
	}
	ax.perDay = span / spanDays
	gaps := make([]float64, n-1)
	for i := 0; i < n; i++ {
		ax.xs[i] = x0 + span*dayOf(ax.ts[i])/spanDays
		if i > 0 {
			gaps[i-1] = dayOf(ax.ts[i]) - dayOf(ax.ts[i-1])
		}
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	typical := sorted[(len(sorted)-1)/2]
	for i, g := range gaps {
		ax.bridge[i] = g > 2*typical
	}
	return ax
}

// solidRuns splits the axis into stretches with no bridge inside them. Each
// run is [start, end] inclusive.
func solidRuns(bridge []bool, n int) [][2]int {
	if n == 0 {
		return nil
	}
	var runs [][2]int
	start := 0
	for i := 0; i < n-1; i++ {
		if i < len(bridge) && bridge[i] {
			runs = append(runs, [2]int{start, i})
			start = i + 1
		}
	}
	return append(runs, [2]int{start, n - 1})
}

// monotonePath draws a Fritsch-Carlson cubic through the points.
//
// The tangent is zero at every local extremum and a weighted harmonic mean
// elsewhere, so the curve between two neighbours never leaves their value
// range: a run of 57, 0, 66 cannot dip below zero or overshoot 66, which a
// Catmull-Rom spline would do and a reader would take for a measurement.
func monotonePath(xs, ys []float64) string {
	n := len(xs)
	if n == 0 {
		return ""
	}
	var d strings.Builder
	fmt.Fprintf(&d, "M%.1f %.1f", xs[0], ys[0])
	if n == 1 {
		return d.String()
	}
	if n == 2 {
		fmt.Fprintf(&d, " L%.1f %.1f", xs[1], ys[1])
		return d.String()
	}
	h := make([]float64, n-1)
	slope := make([]float64, n-1)
	for i := 0; i < n-1; i++ {
		h[i] = xs[i+1] - xs[i]
		if h[i] <= 0 {
			h[i] = 1e-6
		}
		slope[i] = (ys[i+1] - ys[i]) / h[i]
	}
	m := make([]float64, n)
	m[0], m[n-1] = slope[0], slope[n-2]
	for i := 1; i < n-1; i++ {
		if slope[i-1]*slope[i] <= 0 {
			m[i] = 0
			continue
		}
		w1 := 2*h[i] + h[i-1]
		w2 := h[i] + 2*h[i-1]
		m[i] = (w1 + w2) / (w1/slope[i-1] + w2/slope[i])
	}
	for i := 0; i < n-1; i++ {
		c1x, c1y := xs[i]+h[i]/3, ys[i]+m[i]*h[i]/3
		c2x, c2y := xs[i+1]-h[i]/3, ys[i+1]-m[i+1]*h[i]/3
		fmt.Fprintf(&d, " C%.1f %.1f %.1f %.1f %.1f %.1f", c1x, c1y, c2x, c2y, xs[i+1], ys[i+1])
	}
	return d.String()
}

// spreadLabels pushes end labels apart so none overlap, keeping them inside
// [lo, hi] and as close to their true y as the gap allows. Returns one y per
// input, in input order.
func spreadLabels(ys []float64, minGap, lo, hi float64) []float64 {
	n := len(ys)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ys[idx[a]] < ys[idx[b]] })
	pos := make([]float64, n)
	for k, i := range idx {
		pos[k] = math.Max(ys[i], lo)
		if k > 0 && pos[k] < pos[k-1]+minGap {
			pos[k] = pos[k-1] + minGap
		}
	}
	if pos[n-1] > hi {
		pos[n-1] = hi
		for k := n - 2; k >= 0; k-- {
			if pos[k] > pos[k+1]-minGap {
				pos[k] = pos[k+1] - minGap
			}
		}
	}
	for k, i := range idx {
		out[i] = pos[k]
	}
	return out
}

// axisLabel is one x label with its position.
type axisLabel struct {
	X    float64
	Text string
}

// xLabels picks the dates to print: the first and last measured day, and
// every measured day in between that sits at least minGap units from the
// label before it and from the last. A weekly series labels every run; a
// daily one labels every few days; an isolated run between two gaps gets
// its date because it is the only thing in that stretch. Undated axes label
// only the first and last point.
func xLabels(ax dateAxis, days []string, minGap float64) []axisLabel {
	n := len(days)
	if n == 0 {
		return nil
	}
	if !ax.dated {
		if n == 1 {
			return []axisLabel{{ax.xs[0], days[0]}}
		}
		return []axisLabel{{ax.xs[0], days[0]}, {ax.xs[n-1], days[n-1]}}
	}
	if n == 1 {
		return []axisLabel{{ax.xs[0], shortDate(ax.ts[0])}}
	}
	lastX := ax.xs[n-1]
	out := []axisLabel{{ax.xs[0], shortDate(ax.ts[0])}}
	placed := ax.xs[0]
	for i := 1; i < n-1; i++ {
		if ax.xs[i]-placed < minGap || lastX-ax.xs[i] < minGap {
			continue
		}
		out = append(out, axisLabel{ax.xs[i], shortDate(ax.ts[i])})
		placed = ax.xs[i]
	}
	return append(out, axisLabel{lastX, shortDate(ax.ts[n-1])})
}

// shortDate is "Jul 13": a date a person reads, not a database column.
func shortDate(t time.Time) string {
	return t.Format("Jan 2")
}

// dayLabel is "Sat, Jul 18" when the day parsed, else the label as stored.
func dayLabel(ax dateAxis, i int, raw string) string {
	if ax.dated && i < len(ax.ts) {
		return ax.ts[i].Format("Mon, Jan 2")
	}
	return raw
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SparkArea is the lead KPI's history: the same visibility series as the
// race's own line, drawn as a filled hairline under the number.
//
// It is the one chart allowed to stretch (preserveAspectRatio none) because
// it has no glyphs and its strokes hold their width through
// vector-effect. The y axis is pinned to 0..100 like every other chart here,
// so a move from 2% to 4% cannot look like a doubling.
func SparkArea(points []TrendPoint) template.HTML {
	if len(points) < 3 {
		return ""
	}
	const w, h, pad = 400.0, 48.0, 4.0
	days := make([]string, len(points))
	for i, p := range points {
		days[i] = p.Day
	}
	ax := dateScale(days, pad, w-pad)
	y := func(v float64) float64 { return pad + (h-2*pad)*(1-clampPct(v)/100) }

	var b, line, bridges strings.Builder
	fmt.Fprintf(&b, `<svg class="kpi-spark" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" aria-hidden="true">`, w, h)
	for _, run := range solidRuns(ax.bridge, len(points)) {
		xs := ax.xs[run[0] : run[1]+1]
		ys := make([]float64, 0, len(xs))
		for i := run[0]; i <= run[1]; i++ {
			ys = append(ys, y(points[i].Value))
		}
		path := monotonePath(xs, ys)
		if len(xs) >= 2 {
			fmt.Fprintf(&b, `<path class="spark-area" d="%s L%.1f %.1f L%.1f %.1f Z"/>`, path, xs[len(xs)-1], h-pad, xs[0], h-pad)
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(path)
		if run[1] < len(points)-1 {
			fmt.Fprintf(&bridges, "M%.1f %.1f L%.1f %.1f ", xs[len(xs)-1], ys[len(ys)-1], ax.xs[run[1]+1], y(points[run[1]+1].Value))
		}
	}
	fmt.Fprintf(&b, `<path class="spark-line" vector-effect="non-scaling-stroke" d="%s"/>`, line.String())
	if bridges.Len() > 0 {
		fmt.Fprintf(&b, `<path class="spark-line bridge" vector-effect="non-scaling-stroke" d="%s"/>`, strings.TrimSpace(bridges.String()))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
