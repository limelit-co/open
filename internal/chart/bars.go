// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package chart

import (
	"fmt"
	"html/template"
	"strings"
)

// Bar is one row of a bar list.
type Bar struct {
	Label string
	// Tag is a quiet qualifier after the label, such as a source's class.
	Tag string
	// Value is the figure at the right, as printed; Sub follows it, quieter,
	// and is where the count behind the figure goes.
	Value string
	Sub   string
	// Frac is the bar's length, 0 to 1 of the track.
	Frac  float64
	Class string
	Own   bool
	// Title is the hover text: the whole sentence the row stands for.
	Title string
}

// Bar list geometry: a label line over a full-width track, per row.
const (
	barsRowH  = 40.0
	barsTrack = 10.0
)

// Bars draws a ranked list as labelled horizontal bars, one row each, with
// the label and the figure on a line above the bar rather than beside it, so
// a long name never squeezes the bar it labels. width is the viewBox width:
// the width the list is expected to be drawn at, so its text lands near the
// size the stylesheet sets. label is the chart's accessible name.
//
// A zero is drawn as an empty track, never left out: a row of nothing is a
// finding, and a missing row reads as a bug.
func Bars(rows []Bar, width float64, label string) template.HTML {
	if len(rows) == 0 {
		return ""
	}
	h := float64(len(rows))*barsRowH - (barsRowH - barsTrack - 20)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart chart-bars" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		width, h, template.HTMLEscapeString(label))
	for i, r := range rows {
		y := float64(i) * barsRowH
		cls := "bar-row " + r.Class
		if r.Own {
			cls += " own"
		}
		fmt.Fprintf(&b, `<g class="%s">`, cls)
		if r.Title != "" {
			fmt.Fprintf(&b, `<title>%s</title>`, template.HTMLEscapeString(r.Title))
		}
		fmt.Fprintf(&b, `<text class="bar-label" x="0" y="%.1f">%s`, y+13, template.HTMLEscapeString(truncate(r.Label, int(width/14))))
		if r.Own {
			b.WriteString(`<tspan class="bar-you" dx="6">you</tspan>`)
		}
		if r.Tag != "" {
			fmt.Fprintf(&b, `<tspan class="bar-tag" dx="8">%s</tspan>`, template.HTMLEscapeString(r.Tag))
		}
		b.WriteString(`</text>`)
		fmt.Fprintf(&b, `<text class="bar-value" x="%.0f" y="%.1f" text-anchor="end">%s`, width, y+13, template.HTMLEscapeString(r.Value))
		if r.Sub != "" {
			fmt.Fprintf(&b, `<tspan class="bar-sub" dx="6">%s</tspan>`, template.HTMLEscapeString(r.Sub))
		}
		b.WriteString(`</text>`)
		fmt.Fprintf(&b, `<rect class="bar-track" x="0" y="%.1f" width="%.0f" height="%.0f" rx="%.0f"/>`, y+20, width, barsTrack, barsTrack/2)
		if w := clampFrac(r.Frac) * width; w > 0 {
			// A sliver still gets a visible stub, so a 0.4% share does not
			// draw the same as a zero.
			fmt.Fprintf(&b, `<rect class="bar-fill" x="0" y="%.1f" width="%.1f" height="%.0f" rx="%.0f"/>`, y+20, max(w, barsTrack), barsTrack, barsTrack/2)
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func clampFrac(f float64) float64 {
	switch {
	case f < 0 || f != f:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// truncate shortens s to n runes with an ellipsis. The full text stays in
// the row's title.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
