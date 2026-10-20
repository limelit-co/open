// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// The drawings the chart page shows. Each is made from the JSON result it
// travels with and nothing else, by the dashboard's renderers in
// internal/chart, so the picture can never say a number the text does not.

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/limelit-co/open/internal/chart"
	"github.com/limelit-co/open/internal/metrics"
)

// sourceTags names a source class on its bar.
var sourceTags = map[string]string{
	"own":           "yours",
	"competitor":    "competitor",
	"social":        "social",
	"informational": "informational",
	"other":         "other",
}

// drawShareOfVoice is the ring beside a bar per brand. The bars name the same
// brands as the ring's slices, in the same colours, and fold the same tail
// into one row. Nothing is drawn when no tracked brand was named.
func drawShareOfVoice(out shareOfVoiceOut) template.HTML {
	if out.TrackedMentions == 0 {
		return ""
	}
	standings := make([]metrics.BrandStanding, 0, len(out.Rows))
	for _, r := range out.Rows {
		b := metrics.BrandStanding{Name: r.Brand, IsOwn: r.IsYou, Mentions: r.Mentions, Answers: r.Answers}
		if r.SovPct != nil {
			b.ShareOfVoice = *r.SovPct
		}
		standings = append(standings, b)
	}
	p := chart.NewPalette(standings)
	slices, own := chart.ShareSlices(standings, p)
	total := out.TrackedMentions

	var rows []chart.Bar
	folded := 0
	for _, b := range standings {
		if !p.Named(b.Name, b.IsOwn) {
			folded += b.Mentions
			continue
		}
		sub := fmt.Sprintf("%s of %s mentions", chart.Thousands(b.Mentions), chart.Thousands(total))
		if b.Mentions == 0 {
			sub = "never named"
		}
		rows = append(rows, chart.Bar{
			Label: b.Name, Own: b.IsOwn, Class: p.Class(b.Name, b.IsOwn),
			Value: chart.Pct(b.ShareOfVoice) + "%", Sub: sub, Frac: b.ShareOfVoice / 100,
			Title: fmt.Sprintf("%s: %s%% of tracked-brand mentions, %s", b.Name, chart.Pct(b.ShareOfVoice), sub),
		})
	}
	for _, sl := range slices {
		if !sl.Rest {
			continue
		}
		sub := fmt.Sprintf("%s of %s mentions", chart.Thousands(folded), chart.Thousands(total))
		rows = append(rows, chart.Bar{
			Label: sl.Name, Class: sl.Class, Value: chart.Pct(sl.Share) + "%", Sub: sub, Frac: sl.Share / 100,
			Title: fmt.Sprintf("%s: %s%% of tracked-brand mentions, %s", sl.Name, chart.Pct(sl.Share), sub),
		})
	}

	var b strings.Builder
	b.WriteString(`<div class="sov">`)
	b.WriteString(string(chart.Donut(slices, own)))
	// Drawn beside a 168px ring in a frame about 700px wide.
	b.WriteString(string(chart.Bars(rows, 440, "Share of voice by brand")))
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// drawTrend is the race: the brand's visibility and each leading rival's on
// one calendar axis, sized for a chat frame. The brand's line is the
// visibility series; the rivals' are brand_series, on the same dates, each
// resting on that date's answers.
func drawTrend(out trendOut) template.HTML {
	basis := map[string]int{}
	var ownPoints []chart.TrendPoint
	for _, p := range out.Visibility.Points {
		basis[p.Date] = p.Basis
		if p.Value != nil {
			ownPoints = append(ownPoints, chart.TrendPoint{Day: p.Date, Value: *p.Value, N: p.Basis})
		}
	}
	if len(ownPoints) == 0 {
		return ""
	}
	// brand_series arrives in standings order, the property and then the
	// rivals most mentioned first, which is the order colours are dealt in.
	standings := make([]metrics.BrandStanding, 0, len(out.BrandSeries))
	for _, bs := range out.BrandSeries {
		standings = append(standings, metrics.BrandStanding{Name: bs.Brand, IsOwn: bs.IsYou})
	}
	p := chart.NewPalette(standings)

	ownName := "Your brand"
	var rivals []chart.RaceSeries
	for _, bs := range out.BrandSeries {
		if bs.IsYou {
			ownName = bs.Brand
			continue
		}
		s := chart.RaceSeries{Name: bs.Brand, Class: p.Class(bs.Brand, false)}
		for _, pt := range bs.Points {
			s.Points = append(s.Points, chart.TrendPoint{Day: pt.Date, Value: pt.Value, N: basis[pt.Date]})
		}
		if len(s.Points) > 0 {
			s.Last = s.Points[len(s.Points)-1].Value
			rivals = append(rivals, s)
		}
	}
	own := chart.RaceSeries{Name: ownName, IsOwn: true, Class: "s-own", Points: ownPoints, Last: ownPoints[len(ownPoints)-1].Value}
	return chart.RaceChartNarrow(append([]chart.RaceSeries{own}, rivals...), out.SeriesStep == "week")
}

// drawGrid is the prompt by engine grid, one column per platform key, named
// by its label so a second target on one engine reads as its own column.
func drawGrid(out matrixChartOut) template.HTML {
	columns := make([]string, 0, len(out.Platforms))
	for _, key := range out.Platforms {
		columns = append(columns, out.PlatformLabels[key])
	}
	rows := make([]chart.GridRow, 0, len(out.Rows))
	for _, r := range out.Rows {
		row := chart.GridRow{Prompt: r.Text, Branded: r.Branded}
		for _, key := range out.Platforms {
			c, ok := r.Cells[key]
			cell := chart.GridCell{Asked: ok}
			switch {
			case !ok:
			case c.SurfaceStatus == "no_answer_surface":
				cell.NoAnswer = true
			default:
				cell.Answers, cell.Mentions = c.N, c.Mentions
				if c.Value != nil {
					cell.Visibility = *c.Value
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		rows = append(rows, row)
	}
	return chart.PromptGrid(columns, rows)
}

// drawSources is a bar per domain, longest first, each tagged with its class
// and coloured by it, the way the dashboard colours a source on every screen.
func drawSources(out topSourcesOut) template.HTML {
	if len(out.Sources) == 0 {
		return ""
	}
	top := out.Sources[0].CitationCount
	rows := make([]chart.Bar, 0, len(out.Sources))
	for _, s := range out.Sources {
		tag := sourceTags[s.SourceType]
		if tag == "" {
			tag = s.SourceType
		}
		frac := 0.0
		if top > 0 {
			frac = float64(s.CitationCount) / float64(top)
		}
		cites := "citations"
		if s.CitationCount == 1 {
			cites = "citation"
		}
		rows = append(rows, chart.Bar{
			Label: s.Domain, Tag: tag, Class: "src-" + s.SourceType, Own: s.SourceType == "own",
			Value: chart.Thousands(s.CitationCount), Sub: "in " + chart.AnswersWord(s.Answers), Frac: frac,
			Title: fmt.Sprintf("%s (%s): %s %s in %s", s.Domain, tag, chart.Thousands(s.CitationCount), cites, chart.AnswersWord(s.Answers)),
		})
	}
	return chart.Bars(rows, 640, "Most cited domains")
}
