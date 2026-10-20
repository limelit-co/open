// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

// The richer charts: the competitor race, the share-of-voice donut, the
// visibility-by-engine bars, the citation mix, and the fan-out word cloud.
//
// All server-rendered SVG, all reading CSS custom properties, so they follow
// the theme and never carry a colour of their own. Every one of them is drawn
// from real rows: nothing here fills a gap, extends a line, or invents a
// series to make a chart look fuller than the data is.

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
)

// SeriesClass assigns a stable colour class to a brand.
//
// Alphabetical over the full set of names, never by rank or by the filtered
// subset, so a brand keeps its colour across every screen and every week. The
// property is always the brand colour and never takes a series slot.
func SeriesClass(name string, all []string, isOwn bool) string {
	if isOwn {
		return "s-own"
	}
	sorted := make([]string, 0, len(all))
	for _, n := range all {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	idx := 0
	for i, n := range sorted {
		if n == name {
			idx = i
			break
		}
	}
	return fmt.Sprintf("s-%d", idx%8+1)
}

// RaceSeries is one brand's line on the competitor race.
type RaceSeries struct {
	Name   string
	IsOwn  bool
	Class  string
	Points []TrendPoint
	// Last is the most recent value, for the end label.
	Last float64
}

// Race geometry. The plot is x 44..1028, y 24..360. The bands under it are
// disjoint on purpose: ticks 360..365, date glyphs around 370..380, the
// answers strip 388..408, so a full-height count bar never touches a date.
const (
	raceW, raceH        = 1160.0, 418.0
	racePadL, racePadR  = 44.0, 132.0
	racePadT, racePlotB = 24.0, 360.0
	raceDateY           = 378.0
	raceStripBase       = 408.0
	raceStripH          = 20.0
)

func raceY(v float64) float64 {
	return racePadT + (racePlotB-racePadT)*(1-clampPct(v)/100)
}

// RaceChart draws every tracked brand's visibility over time on one chart.
//
// One line at zero says nothing; the same zero drawn under three competitors
// at 82, 71 and 57 says exactly where you stand. The property's line is drawn
// last and heaviest so it stays on top and reads without a legend, which is
// the redundant encoding a colourblind reader needs.
//
// The x axis is calendar time (see dateScale). Days nobody measured are
// shaded and bridged with a dashed straight segment; a curve is only ever
// drawn through consecutive measured days. Points resting on fewer than
// thinN answers are hollow. Values at the right edge replace a legend.
func RaceChart(series []RaceSeries) template.HTML {
	if len(series) == 0 {
		return ""
	}

	// The day list: the longest series first, then any day only another
	// series has, so every point has a place on the axis.
	longest := 0
	for i, s := range series {
		if len(s.Points) > len(series[longest].Points) {
			longest = i
		}
	}
	if len(series[longest].Points) == 0 {
		return ""
	}
	dayIdx := map[string]int{}
	var days []string
	add := func(d string) {
		if _, ok := dayIdx[d]; !ok {
			dayIdx[d] = len(days)
			days = append(days, d)
		}
	}
	for _, p := range series[longest].Points {
		add(p.Day)
	}
	for _, s := range series {
		for _, p := range s.Points {
			add(p.Day)
		}
	}
	n := len(days)
	ax := dateScale(days, racePadL, raceW-racePadR)

	// Answers per day, for the strip and the ticks. Every brand's point on a
	// day rests on the same answers, so the max is the day's count.
	dayN := make([]int, n)
	for _, s := range series {
		for _, p := range s.Points {
			if i, ok := dayIdx[p.Day]; ok && p.N > dayN[i] {
				dayN[i] = p.N
			}
		}
	}

	var own *RaceSeries
	rivals := make([]RaceSeries, 0, len(series))
	for i := range series {
		if series[i].IsOwn {
			own = &series[i]
			continue
		}
		rivals = append(rivals, series[i])
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-race" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		raceW, raceH, template.HTMLEscapeString(raceLabel(series, ax, days)))

	// Gap bands: the stretches nobody measured.
	b.WriteString(`<g class="bands" aria-hidden="true">`)
	for i := 0; i < n-1; i++ {
		if !ax.bridge[i] {
			continue
		}
		x0 := ax.xs[i] + ax.perDay/2
		x1 := ax.xs[i+1] - ax.perDay/2
		if x1 <= x0 {
			continue
		}
		fmt.Fprintf(&b, `<rect class="gap-band" x="%.1f" y="%.0f" width="%.1f" height="%.0f" rx="3"><title>No run between %s and %s</title></rect>`,
			x0, racePadT, x1-x0, racePlotB-racePadT, template.HTMLEscapeString(days[i]), template.HTMLEscapeString(days[i+1]))
	}
	b.WriteString(`</g>`)

	// Gridlines, with the labels outside the plot so they centre on their
	// line rather than colliding with the first point.
	b.WriteString(`<g class="gridlines" aria-hidden="true">`)
	for _, v := range []float64{100, 75, 50, 25, 0} {
		y := raceY(v)
		cls := "grid"
		if v == 0 {
			cls = "grid grid-base"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" vector-effect="non-scaling-stroke"/>`, cls, racePadL, raceW-racePadR, y, y)
		label := fmt.Sprintf("%.0f", v)
		if v == 100 {
			label += "%"
		}
		fmt.Fprintf(&b, `<text class="axis axis-y" x="%.0f" y="%.1f" text-anchor="end" dominant-baseline="middle">%s</text>`, racePadL-10, y, label)
	}
	b.WriteString(`</g>`)

	// One tick per measured day, carrying what the hover needs.
	b.WriteString(`<g class="ticks" aria-hidden="true">`)
	for i := 0; i < n; i++ {
		thin := ""
		if dayN[i] < thinN {
			thin = " data-thin"
		}
		fmt.Fprintf(&b, `<line class="tick" x1="%.1f" x2="%.1f" y1="%.0f" y2="%.0f" vector-effect="non-scaling-stroke" data-i="%d" data-x="%.1f" data-day="%s" data-n="%d"%s/>`,
			ax.xs[i], ax.xs[i], racePlotB, racePlotB+5, i, ax.xs[i], template.HTMLEscapeString(dayLabel(ax, i, days[i])), dayN[i], thin)
	}
	for _, l := range xLabels(ax, days, 70) {
		fmt.Fprintf(&b, `<text class="axis axis-x" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, l.X, raceDateY, template.HTMLEscapeString(l.Text))
	}
	b.WriteString(`</g>`)

	// The answers strip: how much each day's point rests on.
	nMax := 0
	for _, v := range dayN {
		if v > nMax {
			nMax = v
		}
	}
	if nMax > 0 {
		b.WriteString(`<g class="n-strip" aria-label="Answers per measured day">`)
		fmt.Fprintf(&b, `<text class="axis axis-strip" x="%.0f" y="%.1f" dominant-baseline="middle">answers per day</text>`, raceW-racePadR+14, raceStripBase-raceStripH*0.3)
		w := math.Max(4, math.Min(14, ax.perDay*0.7))
		for i := 0; i < n; i++ {
			h := math.Max(1.5, raceStripH*math.Sqrt(float64(dayN[i])/float64(nMax)))
			cls := "n-bar"
			if dayN[i] < thinN {
				cls += " n-low"
			}
			fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1.5"><title>%s: %s</title></rect>`,
				cls, ax.xs[i]-w/2, raceStripBase-h, w, h, template.HTMLEscapeString(days[i]), AnswersWord(dayN[i]))
		}
		b.WriteString(`</g>`)
	}

	// Rivals, in series order. The two leading by last value are grouped so
	// CSS can give them a little more weight than the rest of the field.
	lead := map[string]bool{}
	ranked := append([]RaceSeries(nil), rivals...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Last > ranked[j].Last })
	for i := 0; i < len(ranked) && i < 2; i++ {
		lead[ranked[i].Name] = true
	}
	b.WriteString(`<g class="rivals">`)
	for _, s := range rivals {
		if len(s.Points) == 0 {
			continue
		}
		if lead[s.Name] {
			b.WriteString(`<g class="rival-lead">`)
		}
		raceSeries(&b, s, ax, dayIdx, false)
		if lead[s.Name] {
			b.WriteString(`</g>`)
		}
	}
	b.WriteString(`</g>`)

	if own != nil && len(own.Points) > 0 {
		b.WriteString(`<g class="own">`)
		raceSeries(&b, *own, ax, dayIdx, true)
		b.WriteString(`</g>`)
	}

	// End labels, spread so they never overlap, with a lead line where a
	// label had to move off its point.
	type endLabel struct {
		s        RaceSeries
		x, y     float64
		hasPoint bool
	}
	ends := make([]endLabel, 0, len(series))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		last := s.Points[len(s.Points)-1]
		i, ok := dayIdx[last.Day]
		if !ok {
			continue
		}
		ends = append(ends, endLabel{s: s, x: ax.xs[i], y: raceY(last.Value), hasPoint: true})
	}
	ys := make([]float64, len(ends))
	for i, e := range ends {
		ys[i] = e.y
	}
	spread := spreadLabels(ys, 14, racePadT, racePlotB)
	b.WriteString(`<g class="ends">`)
	for i, e := range ends {
		cls := e.s.Class
		if e.s.IsOwn {
			cls += " end-own"
		}
		// A lead line joins a point to a label that had to move. It is
		// only drawn when the point is on the last measured day, which the
		// axis places at the plot's right edge; a lone point in the middle
		// of a one-day axis would otherwise get a long coloured line to the
		// gutter that reads as a trend.
		if math.Abs(spread[i]-e.y) > 1 && e.x >= raceW-racePadR-0.5 {
			fmt.Fprintf(&b, `<line class="lead %s" x1="%.1f" y1="%.1f" x2="%.0f" y2="%.1f" vector-effect="non-scaling-stroke"/>`,
				e.s.Class, e.x+8, e.y, raceW-racePadR+8, spread[i])
		}
		fmt.Fprintf(&b, `<text class="end %s" x="%.0f" y="%.1f" dominant-baseline="middle"><tspan class="end-val">%s%%</tspan><tspan class="end-name" dx="7">%s</tspan></text>`,
			cls, raceW-racePadR+14, spread[i], Pct(e.s.Last), template.HTMLEscapeString(e.s.Name))
	}
	b.WriteString(`</g>`)

	fmt.Fprintf(&b, `<line class="cursor" x1="0" x2="0" y1="%.0f" y2="%.0f" vector-effect="non-scaling-stroke" aria-hidden="true"/>`, racePadT, racePlotB)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// raceSeries writes one brand: a curve per solid run, one dashed path for
// the bridges, a dot per measured day, and for the property an area under
// each run plus a halo on the latest point.
func raceSeries(b *strings.Builder, s RaceSeries, ax dateAxis, dayIdx map[string]int, isOwn bool) {
	// Points in axis order, skipping any whose day is not on the axis.
	type pt struct {
		i    int
		x, y float64
		p    TrendPoint
	}
	pts := make([]pt, 0, len(s.Points))
	for _, p := range s.Points {
		i, ok := dayIdx[p.Day]
		if !ok {
			continue
		}
		pts = append(pts, pt{i: i, x: ax.xs[i], y: raceY(p.Value), p: p})
	}
	sort.Slice(pts, func(a, c int) bool { return pts[a].i < pts[c].i })
	if len(pts) == 0 {
		return
	}

	cls := "series " + s.Class
	if isOwn {
		cls += " series-own"
	}

	// A run breaks at a bridge on the axis, and also where this series is
	// missing a day the axis has: a curve never spans an unmeasured day.
	var runs [][]pt
	cur := []pt{pts[0]}
	for k := 1; k < len(pts); k++ {
		prev, next := pts[k-1], pts[k]
		bridged := next.i != prev.i+1
		for i := prev.i; i < next.i && i < len(ax.bridge); i++ {
			if ax.bridge[i] {
				bridged = true
			}
		}
		if bridged {
			runs = append(runs, cur)
			cur = nil
		}
		cur = append(cur, next)
	}
	runs = append(runs, cur)

	var line, bridges strings.Builder
	for ri, run := range runs {
		xs := make([]float64, len(run))
		ys := make([]float64, len(run))
		for k, p := range run {
			xs[k], ys[k] = p.x, p.y
		}
		path := monotonePath(xs, ys)
		if isOwn && len(run) >= 2 {
			fmt.Fprintf(b, `<path class="area area-own" d="%s L%.1f %.0f L%.1f %.0f Z"/>`, path, xs[len(xs)-1], racePlotB, xs[0], racePlotB)
		}
		if len(run) >= 2 {
			if line.Len() > 0 {
				line.WriteByte(' ')
			}
			line.WriteString(path)
		}
		if ri < len(runs)-1 {
			next := runs[ri+1][0]
			last := run[len(run)-1]
			fmt.Fprintf(&bridges, "M%.1f %.1f L%.1f %.1f ", last.x, last.y, next.x, next.y)
		}
	}
	if line.Len() > 0 {
		if isOwn {
			fmt.Fprintf(b, `<path class="%s line" pathLength="1" d="%s"/>`, cls, line.String())
		} else {
			fmt.Fprintf(b, `<path class="%s line" d="%s"/>`, cls, line.String())
		}
	}
	if bridges.Len() > 0 {
		fmt.Fprintf(b, `<path class="%s line bridge" d="%s"/>`, cls, strings.TrimSpace(bridges.String()))
	}

	last := pts[len(pts)-1]
	if isOwn {
		fmt.Fprintf(b, `<circle class="halo" cx="%.1f" cy="%.1f" r="10"/>`, last.x, last.y)
	}
	for k, p := range pts {
		r := 3.5
		if isOwn {
			r = 4
		}
		dcls := cls + " dot"
		thin := ""
		if p.p.N < thinN {
			dcls += " dot-thin"
			thin = ", thin sample"
		}
		if isOwn && k == len(pts)-1 {
			dcls += " dot-last"
			r = 5
		}
		fmt.Fprintf(b, `<circle class="%s" cx="%.1f" cy="%.1f" r="%.1f" data-i="%d" data-brand="%s" data-v="%s" data-n="%d"><title>%s, %s: %s%% of %s%s</title></circle>`,
			dcls, p.x, p.y, r, p.i, template.HTMLEscapeString(s.Name), Pct(p.p.Value), p.p.N,
			template.HTMLEscapeString(s.Name), template.HTMLEscapeString(p.p.Day), Pct(p.p.Value), AnswersWord(p.p.N), thin)
	}
}

func raceLabel(series []RaceSeries, ax dateAxis, days []string) string {
	parts := make([]string, 0, len(series))
	for _, s := range series {
		parts = append(parts, fmt.Sprintf("%s %s%%", s.Name, Pct(s.Last)))
	}
	span := ""
	if n := len(days); n > 0 {
		first, last := days[0], days[n-1]
		if ax.dated {
			first, last = shortDate(ax.ts[0]), shortDate(ax.ts[n-1])
		}
		if n == 1 {
			span = fmt.Sprintf(", one measured day, %s", first)
		} else {
			span = fmt.Sprintf(", %d measured days from %s to %s", n, first, last)
		}
	}
	return "Visibility by brand" + span + ". Latest: " + strings.Join(parts, ", ") + "."
}

// DonutSlice is one brand's share.
type DonutSlice struct {
	Name  string
	IsOwn bool
	Class string
	Share float64
	// Rest marks the one slice that sums every brand outside the race, and
	// Count is how many brands it stands for.
	Rest  bool
	Count int
}

// DonutOwn is the centre of the donut: the property's own share and the
// counts it rests on.
type DonutOwn struct {
	Share    float64
	Mentions int
	Total    int
	// HasData is false when the property was never named. The ring then
	// says so rather than showing a blank centre.
	HasData bool
}

// arcPath is one arc of a ring, from angle a0 to a1 in radians, as a path.
//
// A path rather than a dashed circle, because a dash of length zero draws
// nothing and a dash of length two draws a sliver that a hover cannot find.
// Angles are clockwise from 3 o'clock, as SVG measures them.
func arcPath(cx, cy, r, a0, a1 float64) string {
	large := 0
	if a1-a0 > math.Pi {
		large = 1
	}
	return fmt.Sprintf("M%.2f %.2f A%.1f %.1f 0 %d 1 %.2f %.2f",
		cx+r*math.Cos(a0), cy+r*math.Sin(a0), r, r, large, cx+r*math.Cos(a1), cy+r*math.Sin(a1))
}

// Donut draws share of voice as parts of a whole.
//
// A donut is right here and wrong for visibility. Share of voice is
// genuinely a partition: every tracked brand's mentions add to 100%, so the
// arcs mean something. Visibility is not a partition (every brand can be at
// 80% at once) and a pie of it would lie. The centre carries the property's
// own share so the one number the reader came for is not hidden in a slice.
//
// The property draws first at twelve o'clock, then the rest in the order
// given, so the reader's eye starts on the one slice that is theirs.
func Donut(slices []DonutSlice, own DonutOwn) template.HTML {
	const size, r, stroke = 200.0, 80.0, 16.0
	cx, cy := size/2, size/2
	circ := 2 * math.Pi * r

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-donut" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		size, size, template.HTMLEscapeString(donutLabel(slices)))
	fmt.Fprintf(&b, `<circle class="donut-track" cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke-width="%.0f"/>`, cx, cy, r, stroke)

	ordered := make([]DonutSlice, 0, len(slices))
	for _, s := range slices {
		if s.IsOwn {
			ordered = append(ordered, s)
		}
	}
	for _, s := range slices {
		if !s.IsOwn {
			ordered = append(ordered, s)
		}
	}
	total := 0.0
	for _, s := range ordered {
		if s.Share > 0 {
			total += s.Share
		}
	}
	start := 0.0
	for _, s := range ordered {
		if s.Share <= 0 || total <= 0 {
			continue
		}
		frac := s.Share / total
		cls := "arc " + s.Class
		switch {
		case s.IsOwn:
			cls += " series-own"
		case s.Rest:
			cls += " arc-rest"
		}
		title := fmt.Sprintf("%s: %s%% of all tracked-brand mentions", template.HTMLEscapeString(s.Name), Pct(s.Share))
		if frac >= 0.999 {
			fmt.Fprintf(&b, `<circle class="%s" cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke-width="%.0f" data-name="%s" data-v="%s"><title>%s</title></circle>`,
				cls, cx, cy, r, stroke, template.HTMLEscapeString(s.Name), Pct(s.Share), title)
			start += frac
			continue
		}
		// A hairline gap between arcs so adjacent slices of similar colour
		// stay separable, shrinking with the slice so a sliver keeps most of
		// its length.
		length := frac * circ
		gap := math.Min(2.5, length*0.35)
		gapAng := gap / r
		a0 := -math.Pi/2 + start*2*math.Pi + gapAng/2
		a1 := a0 + frac*2*math.Pi - gapAng
		fmt.Fprintf(&b, `<path class="%s" d="%s" fill="none" stroke-width="%.0f" pathLength="1" style="--d:%.3f;--t:%.3f" data-name="%s" data-v="%s"><title>%s</title></path>`,
			cls, arcPath(cx, cy, r, a0, a1), stroke, start, frac, template.HTMLEscapeString(s.Name), Pct(s.Share), title)
		start += frac
	}

	if own.HasData {
		fmt.Fprintf(&b, `<text class="donut-value" x="%.0f" y="94" text-anchor="middle">%s%%</text>`, cx, Pct(own.Share))
		fmt.Fprintf(&b, `<text class="donut-label" x="%.0f" y="112" text-anchor="middle">your share</text>`, cx)
		fmt.Fprintf(&b, `<text class="donut-denom" x="%.0f" y="130" text-anchor="middle">%s of %s</text>`, cx, Thousands(own.Mentions), Thousands(own.Total))
	} else {
		fmt.Fprintf(&b, `<text class="donut-value" x="%.0f" y="94" text-anchor="middle">0%%</text>`, cx)
		fmt.Fprintf(&b, `<text class="donut-label" x="%.0f" y="112" text-anchor="middle">never named</text>`, cx)
		fmt.Fprintf(&b, `<text class="donut-denom" x="%.0f" y="130" text-anchor="middle">of %s mentions</text>`, cx, Thousands(own.Total))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func donutLabel(slices []DonutSlice) string {
	parts := make([]string, 0, len(slices))
	for _, s := range slices {
		parts = append(parts, fmt.Sprintf("%s %s%%", s.Name, Pct(s.Share)))
	}
	return "Share of voice: " + strings.Join(parts, ", ") + "."
}

// EngineGroup is one engine's row on the strip plot.
type EngineGroup struct {
	Engine  string
	Access  string
	Answers int
	Bars    []EngineBar
}

// EngineBar is one brand on one engine.
type EngineBar struct {
	Name       string
	IsOwn      bool
	Class      string
	Visibility float64
	Mentions   int
}

// Strip geometry: a label column, a 0..100 track, a value column.
const (
	stripW      = 780.0
	stripRowH   = 48.0
	stripPadT   = 26.0
	stripPadB   = 6.0
	stripLabelX = 144.0
	stripX0     = 156.0
	stripX1     = 636.0
	stripValX   = 652.0
)

func stripX(v float64) float64 {
	return stripX0 + (stripX1-stripX0)*clampPct(v)/100
}

// EngineBars draws visibility by engine as a strip plot: one row per engine,
// a 0..100 track, every tracked brand as a dot on it, yours the large one.
//
// It replaced grouped bars. Twenty-two bars in a 76-unit group are 1.5 units
// wide each, and a group of eleven answers drew as wide as one of 273. A dot
// per brand on a shared scale reads at any brand count, and the row order,
// your strongest engine first, is itself the finding. The name stays for
// the callers and the test that pins a measured zero.
func EngineBars(groups []EngineGroup) template.HTML {
	if len(groups) == 0 {
		return ""
	}
	bars := 0
	for _, g := range groups {
		if len(g.Bars) > bars {
			bars = len(g.Bars)
		}
	}
	if bars == 0 {
		return ""
	}

	ownOf := func(g EngineGroup) (EngineBar, bool) {
		for _, b := range g.Bars {
			if b.IsOwn {
				return b, true
			}
		}
		return EngineBar{}, false
	}
	rows := append([]EngineGroup(nil), groups...)
	sort.SliceStable(rows, func(i, j int) bool {
		oi, _ := ownOf(rows[i])
		oj, _ := ownOf(rows[j])
		if oi.Visibility != oj.Visibility {
			return oi.Visibility > oj.Visibility
		}
		return rows[i].Answers > rows[j].Answers
	})

	h := stripPadT + stripRowH*float64(len(rows)) + stripPadB
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-engines" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		stripW, h, template.HTMLEscapeString(stripLabel(rows, ownOf)))

	// The scale, once, along the top.
	b.WriteString(`<g class="gridlines" aria-hidden="true">`)
	for _, v := range []float64{0, 25, 50, 75, 100} {
		x := stripX(v)
		cls := "grid"
		if v == 0 {
			cls = "grid grid-base"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.1f" x2="%.1f" y1="22" y2="%.0f" vector-effect="non-scaling-stroke"/>`, cls, x, x, h-stripPadB)
		label := fmt.Sprintf("%.0f", v)
		anchor := "middle"
		switch v {
		case 0:
			anchor = "start"
		case 100:
			anchor, label = "end", "100%"
		}
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="14" text-anchor="%s">%s</text>`, x, anchor, label)
	}
	b.WriteString(`</g>`)

	for ri, g := range rows {
		cy := stripPadT + stripRowH*float64(ri) + stripRowH/2
		own, hasOwn := ownOf(g)
		rowCls := "row"
		if g.Answers < thinN {
			rowCls += " row-thin"
		}
		fmt.Fprintf(&b, `<g class="%s" style="--k:%d">`, rowCls, ri)
		fmt.Fprintf(&b, `<line class="track" x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f"/>`, stripX0, stripX1, cy, cy)

		// Left: the engine and what the row rests on.
		label := engineLabel(g.Engine)
		if len(label) > 20 {
			label = label[:19] + "."
		}
		fmt.Fprintf(&b, `<text class="eng" x="%.0f" y="%.1f" text-anchor="end">%s</text>`, stripLabelX, cy-4, template.HTMLEscapeString(label))
		count := AnswersWord(g.Answers)
		countCls := ""
		if g.Answers < thinN {
			count += ", thin"
			countCls = ` class="thin"`
		}
		fmt.Fprintf(&b, `<text class="eng-sub" x="%.0f" y="%.1f" text-anchor="end"><tspan%s>%s</tspan></text>`,
			stripLabelX, cy+11, countCls, count)

		// Dots in three passes so the property is always on top: the field,
		// then the hued rivals, then you.
		var bestRival *EngineBar
		for i := range g.Bars {
			bar := &g.Bars[i]
			if bar.IsOwn {
				continue
			}
			if bestRival == nil || bar.Visibility > bestRival.Visibility {
				bestRival = bar
			}
		}
		dot := func(bar EngineBar, cls string, r float64) {
			x := stripX(bar.Visibility)
			if bar.Visibility <= 0 {
				cls += " pt-zero"
				if bar.IsOwn {
					cls += " bar-zero"
				} else {
					r = 2.5
				}
			}
			fmt.Fprintf(&b, `<circle class="%s" r="%.1f" cx="%.1f" cy="%.1f" data-brand="%s" data-v="%s"><title>%s on %s: %s%% of %s</title></circle>`,
				cls, r, x, cy, template.HTMLEscapeString(bar.Name), Pct(bar.Visibility),
				template.HTMLEscapeString(bar.Name), template.HTMLEscapeString(engineLabel(g.Engine)), Pct(bar.Visibility), AnswersWord(g.Answers))
		}
		for _, bar := range g.Bars {
			if !bar.IsOwn && bar.Class == "s-field" {
				dot(bar, "pt pt-field", 3.5)
			}
		}
		for _, bar := range g.Bars {
			if !bar.IsOwn && bar.Class != "s-field" {
				dot(bar, "pt "+bar.Class, 4.5)
			}
		}
		if hasOwn {
			dot(own, "pt pt-own "+own.Class+" series-own", 7)
		}

		// A rival ahead of you on this row is named above its dot. Near
		// either end of the track the label hangs inward so it stays over
		// the track rather than over the columns beside it.
		if hasOwn && bestRival != nil && bestRival.Visibility > own.Visibility {
			lx := stripX(bestRival.Visibility)
			anchor := "middle"
			switch {
			case lx > stripX1-50:
				anchor = "end"
			case lx < stripX0+50:
				anchor = "start"
			}
			fmt.Fprintf(&b, `<text class="lead-val %s" x="%.1f" y="%.1f" text-anchor="%s">%s %s%%</text>`,
				bestRival.Class, lx, cy-11, anchor, template.HTMLEscapeString(bestRival.Name), Pct(bestRival.Visibility))
		}

		// Right: your value and your rank on this engine.
		if hasOwn {
			rank := 1
			for _, bar := range g.Bars {
				if !bar.IsOwn && bar.Visibility > own.Visibility {
					rank++
				}
			}
			if own.Visibility <= 0 && own.Mentions == 0 {
				fmt.Fprintf(&b, `<text class="val val-zero" x="%.0f" y="%.1f">0%%</text>`, stripValX, cy-3)
				fmt.Fprintf(&b, `<text class="val-sub" x="%.0f" y="%.1f">not named</text>`, stripValX, cy+11)
			} else {
				fmt.Fprintf(&b, `<text class="val" x="%.0f" y="%.1f">%s%%</text>`, stripValX, cy-3, Pct(own.Visibility))
				sub := fmt.Sprintf("%s of %d", ordinal(rank), len(g.Bars))
				if rank > 1 && bestRival != nil {
					sub = fmt.Sprintf("%s, %s %s%%", ordinal(rank), bestRival.Name, Pct(bestRival.Visibility))
				}
				fmt.Fprintf(&b, `<text class="val-sub" x="%.0f" y="%.1f">%s</text>`, stripValX, cy+11, template.HTMLEscapeString(sub))
			}
		}
		b.WriteString(`</g>`)
	}
	// One floating label the hover moves between dots. Empty until then.
	b.WriteString(`<text class="ptlabel" x="0" y="0" aria-hidden="true"></text>`)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func stripLabel(rows []EngineGroup, ownOf func(EngineGroup) (EngineBar, bool)) string {
	parts := make([]string, 0, len(rows))
	for _, g := range rows {
		if own, ok := ownOf(g); ok {
			parts = append(parts, fmt.Sprintf("%s %s%% of %s", engineLabel(g.Engine), Pct(own.Visibility), AnswersWord(g.Answers)))
		}
	}
	if len(parts) == 0 {
		return "Visibility by engine, one dot per brand."
	}
	return "Your visibility by engine, strongest first: " + strings.Join(parts, "; ") + "."
}

// ordinal is 1st, 2nd, 3rd, 4th, 11th, 21st.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// MixDay is one day's citation mix.
type MixDay struct {
	Day    string
	Counts map[string]int
	Total  int
}

// MixOrder is the stacking order, bottom to top. Own first so your share sits
// on the baseline where it can be read without a legend.
var MixOrder = []string{"own", "competitor", "informational", "social", "other"}

// CitationMixChart draws citations by source type per day as 100% stacked
// bars: who the engines trust, and whether that is changing.
//
// Stacked bars rather than a stacked area, because the area needs enough days
// to read as a shape and lies about the days between measurements. A bar per
// measured day says exactly what was measured and nothing about what was not.
// The bars sit on the same calendar axis as the race so the two charts agree
// about time. Your share is printed over each bar and the day's total under
// its date, when the days are far enough apart for the digits to fit.
func CitationMixChart(days []MixDay) template.HTML {
	if len(days) == 0 {
		return ""
	}
	const w, h, padTop, padBottom, padX = 560.0, 200.0, 22.0, 38.0, 16.0
	plotH := h - padTop - padBottom
	labels := make([]string, len(days))
	for i, d := range days {
		labels[i] = d.Day
	}
	ax := dateScale(labels, padX, w-padX)
	dayW := ax.perDay
	if len(days) == 1 {
		dayW = w - 2*padX
	}
	barW := math.Max(8, math.Min(28, dayW*0.7))
	// Per-bar digits need about 24 units of room at this size. Denser than
	// that only the days that carry a date label get them, and the <title>
	// carries the rest.
	labelled := map[float64]bool{}
	xl := xLabels(ax, labels, 64)
	for _, l := range xl {
		labelled[l.X] = true
	}
	digits := func(i int) bool { return dayW >= 24 || labelled[ax.xs[i]] }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-mix" viewBox="0 0 %.0f %.0f" preserveAspectRatio="xMidYMid meet" role="img" aria-label="Cited sources by type per day, as a share of each day's citations">`, w, h)

	for di, d := range days {
		if d.Total == 0 {
			continue
		}
		x := ax.xs[di] - barW/2
		fmt.Fprintf(&b, `<g class="col" data-day="%s">`, template.HTMLEscapeString(d.Day))
		yCursor := padTop + plotH
		for _, kind := range MixOrder {
			n := d.Counts[kind]
			if n == 0 {
				continue
			}
			height := plotH * float64(n) / float64(d.Total)
			yCursor -= height
			fmt.Fprintf(&b, `<rect class="mix mix-%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f"><title>%s, %s: %d of %d citations</title></rect>`,
				kind, x, yCursor, barW, height,
				template.HTMLEscapeString(d.Day), kind, n, d.Total)
		}
		if digits(di) {
			ownShare := float64(d.Counts["own"]) / float64(d.Total) * 100
			fmt.Fprintf(&b, `<text class="mix-val" x="%.1f" y="%.1f" text-anchor="middle">%s%%</text>`, ax.xs[di], padTop-5, Pct(ownShare))
			fmt.Fprintf(&b, `<text class="axis axis-sub" x="%.1f" y="%.0f" text-anchor="middle">%d</text>`, ax.xs[di], h-3, d.Total)
		}
		b.WriteString(`</g>`)
	}
	for _, l := range xl {
		fmt.Fprintf(&b, `<text class="axis axis-x" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`, l.X, h-14, template.HTMLEscapeString(l.Text))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// CloudWord is one term in the fan-out cloud.
type CloudWord struct {
	Term  string
	Count int
	// Size is the font size in px, from the count.
	Size int
	// Weight thins the least frequent words so the frequent ones read first.
	Weight int
}

// WordCloud sizes the terms the engines actually searched for.
//
// Square-root scaling, because a linear scale makes the top word a poster and
// the rest illegible, and a log scale flattens the difference the reader came
// to see. Frequency is encoded once, as size. Encoding it a second time as
// colour or opacity would say the same thing twice while looking like it said
// two things.
func WordCloud(words []CloudWord) []CloudWord {
	if len(words) == 0 {
		return nil
	}
	maxN, minN := words[0].Count, words[0].Count
	for _, w := range words {
		if w.Count > maxN {
			maxN = w.Count
		}
		if w.Count < minN {
			minN = w.Count
		}
	}
	const minPx, maxPx = 12.0, 30.0
	out := make([]CloudWord, len(words))
	for i, w := range words {
		t := 1.0
		if maxN > minN {
			t = (math.Sqrt(float64(w.Count)) - math.Sqrt(float64(minN))) / (math.Sqrt(float64(maxN)) - math.Sqrt(float64(minN)))
		}
		out[i] = CloudWord{
			Term: w.Term, Count: w.Count,
			Size:   int(math.Round(minPx + t*(maxPx-minPx))),
			Weight: 500 + int(math.Round(t*2))*100,
		}
	}
	// Alphabetical, so the layout is stable between renders and a word does
	// not jump when its count changes by one.
	sort.Slice(out, func(i, j int) bool { return out[i].Term < out[j].Term })
	return out
}
