// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// The chart tools render as an MCP App: each points at one ui:// page that a
// host able to draw (Claude Desktop, Claude, ChatGPT) loads in a sandboxed
// frame and hands the tool's result to.
//
// Two readers get one result, and each gets what it can use. The text is
// complete JSON: the figures, the n behind them and a caption that reads
// them, so the model, and any client that cannot draw, reads the chart
// correctly without seeing it. The drawing rides in the result's _meta,
// which a host hands the page and keeps out of the model's context, so the
// model pays nothing for markup it cannot see. Each drawing is made from the
// JSON it travels with, by internal/chart, the renderers the dashboard uses,
// so the picture and the numbers cannot disagree.
//
// The page (charts.html) is this repository's own: plain HTML, CSS and a
// small script, no build step. Limelit Cloud registers the same four tool
// names with the same arguments, so a call written against one server is a
// valid call against the other.

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"math"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/store"
)

// chartsPage is the one page every chart tool draws in.
//
//go:embed charts.html
var chartsPage string

const (
	// chartsURI is versioned in the path: a host may cache a ui:// page, and
	// a page that changes what it reads must change its name.
	chartsURI  = "ui://limelit-open/charts/v1.html"
	chartsMIME = "text/html;profile=mcp-app"

	// chartMetaKey is where a result carries its drawing, inside _meta.
	chartMetaKey = "co.limelit/chart"

	chartMaxDays     = 365
	chartNamedRivals = 6
	// chartDailyDays is the longest window the trend draws a point per day
	// for. Past it every series on the trend steps by week, so a year stays
	// a payload a model can afford: a year of days is two 366-point series
	// before a single brand line.
	chartDailyDays = 90
)

// The views the page draws, named in every result.
const (
	viewShareOfVoice    = "share_of_voice"
	viewVisibilityTrend = "visibility_trend"
	viewPromptMatrix    = "prompt_matrix"
	viewTopSources      = "top_sources"
)

// chartBasis is what every chart result carries: which view draws it, the
// basis behind its figures, and one sentence that reads it.
type chartBasis struct {
	View       string `json:"view" jsonschema:"which chart draws this result"`
	N          int    `json:"n" jsonschema:"the basis behind the figures"`
	BasisLabel string `json:"basis_label" jsonschema:"what n counts, as a plural noun: answers or citations"`
	LowN       bool   `json:"low_n" jsonschema:"true when n is under 20: the figures are still settling"`
	Caption    string `json:"caption" jsonschema:"one sentence that reads the chart, with its n"`
}

func basisFor(view string, n int, label string) chartBasis {
	return chartBasis{View: view, N: n, BasisLabel: label, LowN: n < metrics.LowNThreshold}
}

// settling is the clause a low basis adds to a caption.
func settling(lowN bool, unit string) string {
	if !lowN {
		return ""
	}
	return " Still settling: fewer than 20 " + unit + "."
}

// chartResult wraps a drawing for the page. The SDK fills the text and the
// structured content from the tool's output; the drawing rides in _meta
// only. An empty drawing is still sent, so the page can tell "nothing to
// draw" from "no drawing arrived".
func chartResult(view string, drawing template.HTML) *mcp.CallToolResult {
	return &mcp.CallToolResult{Meta: mcp.Meta{
		chartMetaKey: map[string]any{"view": view, "html": string(drawing)},
	}}
}

type chartWindowArgs struct {
	Days int `json:"days,omitempty" jsonschema:"trailing window in days (1..365); omit for 30"`
}

func registerCharts(s *mcp.Server, d Deps) {
	ui := mcp.Meta{"ui": map[string]any{"prefersBorder": true}}
	s.AddResource(&mcp.Resource{
		URI:         chartsURI,
		Name:        "limelit-charts",
		Title:       "Limelit charts",
		Description: "The page the show_ tools draw their charts in.",
		MIMEType:    chartsMIME,
		Meta:        ui,
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		// Hosts read _meta.ui from the contents first and the listing as a
		// fallback, so it is on both.
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: chartsURI, MIMEType: chartsMIME, Text: chartsPage, Meta: ui,
		}}}, nil
	})

	// The nested key is the MCP Apps form; the flat one is the form older
	// hosts read.
	meta := mcp.Meta{
		"ui":             map[string]any{"resourceUri": chartsURI},
		"ui/resourceUri": chartsURI,
	}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:  "show_share_of_voice",
		Title: "Share of voice",
		Description: "Draw share of voice as a chart in the conversation: every tracked brand's part of all " +
			"tracked-brand mentions over the last days (default 30), as a ring and a bar per brand. It reads " +
			"the same standings as get_overview_kpis. n counts the answers in the window; low_n is true under 20.",
		Annotations: readOnly,
		Meta:        meta,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in shareOfVoiceArgs) (*mcp.CallToolResult, shareOfVoiceOut, error) {
		out, err := shareOfVoice(ctx, d, in)
		if err != nil {
			return nil, out, err
		}
		return chartResult(viewShareOfVoice, drawShareOfVoice(out)), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "show_visibility_trend",
		Title: "Visibility over time",
		Description: "Draw visibility over time as a chart in the conversation: the share of answers naming " +
			"the brand on each measured day of the last days (default 30), with a line per leading rival " +
			"(brand_series). The series of get_kpi_history on a calendar axis: a day nobody ran is a gap, not " +
			"a zero, and past 90 days a point is a week. n counts the answers in the window; low_n is true under 20.",
		Annotations: readOnly,
		Meta:        meta,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chartWindowArgs) (*mcp.CallToolResult, trendOut, error) {
		out, err := visibilityTrend(ctx, d, in)
		if err != nil {
			return nil, out, err
		}
		return chartResult(viewVisibilityTrend, drawTrend(out)), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "show_prompt_matrix",
		Title: "Visibility by prompt and engine",
		Description: "Draw the prompt by engine grid as a chart in the conversation: for each tracked prompt on " +
			"each engine, how many of its stored answers name the brand. The scope of get_matrix: every stored " +
			"answer, no window (cell_basis all_answers). n counts the answers in the grid; low_n is true under 20.",
		Annotations: readOnly,
		Meta:        meta,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, matrixChartOut, error) {
		out, err := promptMatrix(ctx, d)
		if err != nil {
			return nil, out, err
		}
		return chartResult(viewPromptMatrix, drawGrid(out)), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "show_top_sources",
		Title: "Most cited sources",
		Description: "Draw the most cited sites as a chart in the conversation: the domains the engines cite " +
			"most over the last days (default 30), limit of them (default 10, at most 25), each marked yours, " +
			"a tracked competitor, social, informational or other. The ranking of list_top_sources. n counts " +
			"the citations behind the domains shown; low_n is true under 20.",
		Annotations: readOnly,
		Meta:        meta,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topSourcesArgs) (*mcp.CallToolResult, topSourcesOut, error) {
		out, err := topSources(ctx, d, in)
		if err != nil {
			return nil, out, err
		}
		return chartResult(viewTopSources, drawSources(out)), out, nil
	})
}

// ---- Shared -----------------------------------------------------------------

// ownName is the property's name for a caption, or a stand-in on an instance
// with no brand saved yet.
func ownName(ctx context.Context, db *store.DB) (string, error) {
	p, err := db.Property(ctx)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Name == "") {
		return "Your brand", nil
	}
	if err != nil {
		return "", err
	}
	return p.Name, nil
}

// windowEngines names the engines behind a window's answers, by what they
// are and never by how they were reached.
func windowEngines(ctx context.Context, m *metrics.Service, w metrics.Window) ([]string, error) {
	cells, err := m.EngineBreakdown(ctx, w)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, c := range cells {
		if c.Answers == 0 || seen[c.Engine] {
			continue
		}
		seen[c.Engine] = true
		out = append(out, engineName(c.Engine))
	}
	return out, nil
}

func engineName(id string) string {
	if e, ok := engines.Lookup(id); ok {
		return e.Label
	}
	return id
}

func windowPhrase(days int) string {
	if days == 1 {
		return "the last day"
	}
	return fmt.Sprintf("the last %d days", days)
}

func pctText(v float64) string {
	if v > 0 && v < 10 {
		return fmt.Sprintf("%.1f%%", v)
	}
	return fmt.Sprintf("%.0f%%", v)
}

func answersText(n int) string {
	if n == 1 {
		return "1 answer"
	}
	return fmt.Sprintf("%d answers", n)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ---- Share of voice ---------------------------------------------------------

type shareOfVoiceArgs struct {
	Days    int    `json:"days,omitempty" jsonschema:"trailing window in days (1..365); omit for 30"`
	Segment string `json:"segment,omitempty" jsonschema:"Cloud only. Passing this is an error rather than being ignored"`
}

type shareOfVoiceOut struct {
	chartBasis
	WindowDays      int         `json:"window_days"`
	Window          chartWindow `json:"window"`
	Headline        sovHeadline `json:"headline"`
	Rows            []sovRow    `json:"rows" jsonschema:"every brand named in the window, most mentioned first; yours is always present"`
	TrackedMentions int         `json:"tracked_mentions" jsonschema:"every tracked brand's mentions in the window: the denominator of each share"`
	Engines         []string    `json:"engines" jsonschema:"the engines whose answers are counted"`
}

type chartWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type sovHeadline struct {
	BasisN int `json:"basis_n" jsonschema:"the answers behind the window, the same as n"`
}

type sovRow struct {
	Brand    string   `json:"brand"`
	IsYou    bool     `json:"is_you"`
	SovPct   *float64 `json:"sov_pct" jsonschema:"share of every tracked-brand mention; null when no tracked brand was named"`
	Mentions int      `json:"mentions"`
	Answers  int      `json:"answers" jsonschema:"answers naming this brand"`
}

func shareOfVoice(ctx context.Context, d Deps, in shareOfVoiceArgs) (shareOfVoiceOut, error) {
	if in.Segment != "" {
		return shareOfVoiceOut{}, rejectCloudArg("segment")
	}
	days, err := windowDays(in.Days, 30, chartMaxDays)
	if err != nil {
		return shareOfVoiceOut{}, err
	}
	w := metrics.Window{Days: days}
	ov, err := d.Metrics.Overview(ctx, w)
	if err != nil {
		return shareOfVoiceOut{}, err
	}
	standings, err := d.Metrics.Standings(ctx, w)
	if err != nil {
		return shareOfVoiceOut{}, err
	}
	engineList, err := windowEngines(ctx, d.Metrics, w)
	if err != nil {
		return shareOfVoiceOut{}, err
	}

	now := time.Now().UTC()
	out := shareOfVoiceOut{
		chartBasis: basisFor(viewShareOfVoice, ov.Answers, "answers"),
		WindowDays: days,
		Window:     chartWindow{From: now.AddDate(0, 0, -days).Format(time.RFC3339), To: now.Format(time.RFC3339)},
		Headline:   sovHeadline{BasisN: ov.Answers},
		Rows:       []sovRow{},
		Engines:    engineList,
	}
	for _, b := range standings {
		out.TrackedMentions += b.Mentions
	}
	var own sovRow
	for _, b := range standings {
		row := sovRow{Brand: b.Name, IsYou: b.IsOwn, Mentions: b.Mentions, Answers: b.Answers}
		// With nothing named there is no whole to take a share of: null, not
		// a row of zeros the page would draw as a pie.
		if out.TrackedMentions > 0 {
			share := b.ShareOfVoice
			row.SovPct = &share
		}
		out.Rows = append(out.Rows, row)
		if b.IsOwn {
			own = row
		}
	}

	window := windowPhrase(days)
	switch {
	case out.N == 0:
		out.Caption = fmt.Sprintf("No answers in %s yet, so there is no share of voice to show.", window)
	case out.TrackedMentions == 0:
		out.Caption = fmt.Sprintf("No tracked brand was named in %s across %s.", window, answersText(out.N)) + settling(out.LowN, "answers")
	default:
		name := own.Brand
		if name == "" {
			name = "Your brand"
		}
		share := 0.0
		if own.SovPct != nil {
			share = *own.SovPct
		}
		out.Caption = fmt.Sprintf("%s has %s of tracked-brand mentions in %s: %d of %d, across %s.",
			name, pctText(share), window, own.Mentions, out.TrackedMentions, answersText(out.N)) + settling(out.LowN, "answers")
	}
	return out, nil
}

// ---- Visibility trend -------------------------------------------------------

type trendOut struct {
	chartBasis
	WindowDays int `json:"window_days"`
	// SeriesOf says whose line visibility is, as Cloud's result does.
	SeriesOf string `json:"series_of" jsonschema:"whose line visibility is: own_brand"`
	// SeriesStep says what one point of visibility, citation_share and
	// brand_series is.
	SeriesStep    string            `json:"series_step" jsonschema:"day: a point per calendar day (windows up to 90 days); week: a point per week, dated its Monday (the first one the window's first day), the share of that week's answers or citations"`
	Visibility    trendSeries       `json:"visibility" jsonschema:"the property's visibility per calendar day; value is null on a day nobody ran"`
	CitationShare trendSeries       `json:"citation_share" jsonschema:"the share of cited sources that are the property's, per calendar day"`
	Annotations   []trendAnnotation `json:"annotations" jsonschema:"days a collector changed; always empty here, where each target is one collector"`
	BrandSeries   []brandSeries     `json:"brand_series" jsonschema:"the property and the six most mentioned rivals, one series each, measured days or weeks only"`
	// BrandSeriesStep is SeriesStep, kept for pages that read it.
	BrandSeriesStep string   `json:"brand_series_step" jsonschema:"the same as series_step"`
	Engines         []string `json:"engines" jsonschema:"the engines whose answers are counted"`
}

type trendSeries struct {
	Points  []trendPoint `json:"points"`
	Current *float64     `json:"current" jsonschema:"the whole window's figure; null when nothing was measured"`
}

type trendPoint struct {
	Date  string   `json:"date"`
	Value *float64 `json:"value"`
	Basis int      `json:"basis" jsonschema:"the answers behind this day"`
}

type trendAnnotation struct {
	Date      string   `json:"date"`
	Platforms []string `json:"platforms"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Label     string   `json:"label,omitempty"`
}

type brandSeries struct {
	Brand  string       `json:"brand"`
	IsYou  bool         `json:"is_you"`
	Points []brandPoint `json:"points"`
}

type brandPoint struct {
	Date  string  `json:"date" jsonschema:"YYYY-MM-DD; for a week, its Monday, or the window's first day when the Monday falls before it"`
	Value float64 `json:"value" jsonschema:"the share of that day's (or week's) answers naming this brand"`
}

func visibilityTrend(ctx context.Context, d Deps, in chartWindowArgs) (trendOut, error) {
	days, err := windowDays(in.Days, 30, chartMaxDays)
	if err != nil {
		return trendOut{}, err
	}
	w := metrics.Window{Days: days}
	ov, err := d.Metrics.Overview(ctx, w)
	if err != nil {
		return trendOut{}, err
	}
	series, err := d.Metrics.Series(ctx, w)
	if err != nil {
		return trendOut{}, err
	}
	mix, err := d.Metrics.CitationMix(ctx, w)
	if err != nil {
		return trendOut{}, err
	}
	standings, err := d.Metrics.Standings(ctx, w)
	if err != nil {
		return trendOut{}, err
	}
	brandDays, err := d.Metrics.BrandSeries(ctx, w)
	if err != nil {
		return trendOut{}, err
	}
	engineList, err := windowEngines(ctx, d.Metrics, w)
	if err != nil {
		return trendOut{}, err
	}
	name, err := ownName(ctx, d.DB)
	if err != nil {
		return trendOut{}, err
	}

	// The calendar axis. The window is the last days*24 hours, so its first
	// answers can fall on the calendar day before today minus days-1; the
	// axis starts there, and stretches to any measured day outside it, so
	// every answer in n sits on a day of the axis.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -days)
	for _, p := range series {
		if t, err := time.Parse(time.DateOnly, p.Day); err == nil && t.Before(first) {
			first = t
		}
	}
	ownCites, allCites := map[string]int{}, map[string]int{}
	totalOwn, totalAll := 0, 0
	for _, m := range mix {
		allCites[m.Day] += m.Citations
		totalAll += m.Citations
		if m.SourceType == "own" {
			ownCites[m.Day] += m.Citations
			totalOwn += m.Citations
		}
	}

	out := trendOut{
		chartBasis:    basisFor(viewVisibilityTrend, ov.Answers, "answers"),
		WindowDays:    days,
		SeriesOf:      "own_brand",
		Visibility:    trendSeries{Points: []trendPoint{}},
		CitationShare: trendSeries{Points: []trendPoint{}},
		Annotations:   []trendAnnotation{},
		BrandSeries:   []brandSeries{},
		Engines:       engineList,
	}
	out.SeriesStep = "day"
	if days > chartDailyDays {
		out.SeriesStep = "week"
	}
	out.BrandSeriesStep = out.SeriesStep

	// Each point is a bucket of calendar days: one day, or a week from its
	// Monday (the first week from the window's first day). A bucket's value
	// is its own share, its answers or citations over its whole span, so a
	// busy day weighs more than a quiet one.
	type bucket struct{ answers, mentions, own, cites int }
	keyOf := func(day string) string { return day }
	if out.SeriesStep == "week" {
		keyOf = func(day string) string { return weekOf(day, first) }
	}
	buckets := map[string]*bucket{}
	at := func(day string) *bucket {
		k := keyOf(day)
		if buckets[k] == nil {
			buckets[k] = &bucket{}
		}
		return buckets[k]
	}
	measured := 0
	for _, p := range series {
		if p.Answers > 0 {
			measured++
		}
		b := at(p.Day)
		b.answers += p.Answers
		b.mentions += p.Mentions
	}
	for day, n := range allCites {
		b := at(day)
		b.cites += n
		b.own += ownCites[day]
	}
	step := 1
	if out.SeriesStep == "week" {
		step = 7
	}
	for t := first; !t.After(today); {
		day := t.Format(time.DateOnly)
		b := buckets[keyOf(day)]
		if b == nil {
			b = &bucket{}
		}
		vis := trendPoint{Date: day, Basis: b.answers}
		if b.answers > 0 {
			v := round2(float64(b.mentions) / float64(b.answers) * 100)
			vis.Value = &v
		}
		share := trendPoint{Date: day, Basis: b.cites}
		if b.cites > 0 {
			v := round2(float64(b.own) / float64(b.cites) * 100)
			share.Value = &v
		}
		out.Visibility.Points = append(out.Visibility.Points, vis)
		out.CitationShare.Points = append(out.CitationShare.Points, share)
		if step == 1 {
			t = t.AddDate(0, 0, 1)
		} else {
			// The next Monday.
			t = monday(t).AddDate(0, 0, 7)
		}
	}
	if ov.Answers > 0 {
		v := ov.Visibility
		out.Visibility.Current = &v
	}
	if totalAll > 0 {
		v := round2(float64(totalOwn) / float64(totalAll) * 100)
		out.CitationShare.Current = &v
	}

	// One series per brand for the property and the leading rivals, in
	// standings order, measured days only: a line per brand across a full
	// year of empty days would cost the model far more than it says, and
	// past chartDailyDays a point is a week.
	idx := map[string]int{}
	rivals := 0
	for _, b := range standings {
		if !b.IsOwn {
			if rivals == chartNamedRivals {
				continue
			}
			rivals++
		}
		idx[b.Name] = len(out.BrandSeries)
		out.BrandSeries = append(out.BrandSeries, brandSeries{Brand: b.Name, IsYou: b.IsOwn, Points: []brandPoint{}})
	}
	for _, bd := range brandDays {
		if i, ok := idx[bd.Name]; ok {
			out.BrandSeries[i].Points = append(out.BrandSeries[i].Points, brandPoint{Date: bd.Day, Value: bd.Visibility})
		}
	}
	if out.SeriesStep == "week" {
		weights := map[string]int{}
		for _, p := range series {
			weights[p.Day] = p.Answers
		}
		for i := range out.BrandSeries {
			out.BrandSeries[i].Points = weekly(out.BrandSeries[i].Points, weights, first)
		}
	}

	window := windowPhrase(days)
	if out.N == 0 {
		out.Caption = fmt.Sprintf("No answers in %s yet, so there is no visibility to chart.", window)
	} else {
		dayWord := "days"
		if measured == 1 {
			dayWord = "day"
		}
		out.Caption = fmt.Sprintf("%s was named in %s of %s in %s, over %d measured %s.",
			name, pctText(ov.Visibility), answersText(out.N), window, measured, dayWord) + settling(out.LowN, "answers")
	}
	return out, nil
}

// weekly folds daily points into one per week, Monday to Sunday: the share of
// the week's answers naming the brand, so a day with ten answers weighs ten
// times a day with one. answers is each day's answer count; a week whose
// Monday falls before first is dated first, the window's first day, so every
// point sits on the trend's axis.
func weekly(points []brandPoint, answers map[string]int, first time.Time) []brandPoint {
	type acc struct{ named, total float64 }
	var order []string
	weeks := map[string]*acc{}
	for _, p := range points {
		key := weekOf(p.Date, first)
		if key == "" {
			continue
		}
		w, ok := weeks[key]
		if !ok {
			w = &acc{}
			weeks[key] = w
			order = append(order, key)
		}
		n := float64(answers[p.Date])
		w.named += p.Value * n
		w.total += n
	}
	out := make([]brandPoint, 0, len(order))
	for _, key := range order {
		if w := weeks[key]; w.total > 0 {
			out = append(out, brandPoint{Date: key, Value: round2(w.named / w.total)})
		}
	}
	return out
}

// monday is the Monday on or before t.
func monday(t time.Time) time.Time {
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

// weekOf names the week a day falls in by its Monday, or by first when the
// Monday falls before it, so every week sits on the trend's axis. It is ""
// for a day it cannot read.
func weekOf(day string, first time.Time) string {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return ""
	}
	m := monday(t)
	if m.Before(first) {
		m = first
	}
	return m.Format(time.DateOnly)
}

// ---- Prompt matrix ----------------------------------------------------------

type matrixChartOut struct {
	chartBasis
	// CellBasis says what one cell is. Here it is the share of all of the
	// prompt's stored answers on the engine; Cloud's cell is the latest
	// answer, and says "latest_answer".
	CellBasis      string            `json:"cell_basis" jsonschema:"all_answers: a cell is the share of every stored answer for the prompt on the engine"`
	Platforms      []string          `json:"platforms" jsonschema:"one key per column, an engine id; a second target on the same engine gets the key engine_2"`
	PlatformLabels map[string]string `json:"platform_labels" jsonschema:"the display name of each column key"`
	Rows           []matrixChartRow  `json:"rows"`
}

type matrixChartRow struct {
	PromptID int64                      `json:"prompt_id"`
	Text     string                     `json:"text"`
	Category string                     `json:"category"`
	Branded  bool                       `json:"branded" jsonschema:"true when the prompt names the property; excluded from the headline, shown here"`
	Cells    map[string]matrixChartCell `json:"cells" jsonschema:"a column with no cell was never asked"`
}

type matrixChartCell struct {
	Value         *float64 `json:"value" jsonschema:"the share of answers naming the brand, 0 to 100; null when the engine showed no AI answer"`
	SurfaceStatus string   `json:"surface_status" jsonschema:"answer, or no_answer_surface when the engine showed no AI answer (not a miss)"`
	LatestStatus  string   `json:"latest_status"`
	N             int      `json:"n" jsonschema:"the answers behind this cell"`
	Mentions      int      `json:"mentions" jsonschema:"how many of them name the brand"`
}

func promptMatrix(ctx context.Context, d Deps) (matrixChartOut, error) {
	m, err := d.Metrics.Matrix(ctx, metrics.Window{})
	if err != nil {
		return matrixChartOut{}, err
	}
	name, err := ownName(ctx, d.DB)
	if err != nil {
		return matrixChartOut{}, err
	}

	out := matrixChartOut{
		CellBasis:      "all_answers",
		Platforms:      []string{},
		PlatformLabels: map[string]string{},
		Rows:           []matrixChartRow{},
	}
	// Two targets on one engine are two columns, never averaged into one, so
	// the second gets its own key rather than overwriting the first.
	keys := make([]string, len(m.Targets))
	perEngine := map[string]int{}
	for i, t := range m.Targets {
		perEngine[t.Engine]++
		key, label := t.Engine, engineName(t.Engine)
		if k := perEngine[t.Engine]; k > 1 {
			key, label = fmt.Sprintf("%s_%d", t.Engine, k), fmt.Sprintf("%s %d", label, k)
		}
		keys[i] = key
		out.Platforms = append(out.Platforms, key)
		out.PlatformLabels[key] = label
	}

	n, mentions := 0, 0
	for _, r := range m.Rows {
		row := matrixChartRow{PromptID: r.PromptID, Text: r.Prompt, Category: r.Category, Branded: r.Branded, Cells: map[string]matrixChartCell{}}
		for i, t := range m.Targets {
			cell, ok := r.Cells[t.ID]
			switch {
			case !ok || (cell.Answers == 0 && cell.NoSurface == 0):
				continue
			case cell.Answers == 0:
				row.Cells[keys[i]] = matrixChartCell{SurfaceStatus: "no_answer_surface", LatestStatus: "completed"}
			default:
				v := cell.Visibility
				row.Cells[keys[i]] = matrixChartCell{
					Value: &v, SurfaceStatus: "answer", LatestStatus: "completed",
					N: cell.Answers, Mentions: cell.Mentions,
				}
				n += cell.Answers
				mentions += cell.Mentions
			}
		}
		out.Rows = append(out.Rows, row)
	}
	out.chartBasis = basisFor(viewPromptMatrix, n, "answers")

	engineWord, promptWord := "engines", "prompts"
	if len(out.Platforms) == 1 {
		engineWord = "engine"
	}
	if len(out.Rows) == 1 {
		promptWord = "prompt"
	}
	if n == 0 {
		out.Caption = "No stored answers yet, so the grid is empty."
	} else {
		out.Caption = fmt.Sprintf("%s is named in %d of %s across %d %s on %d %s.",
			name, mentions, answersText(n), len(out.Rows), promptWord, len(out.Platforms), engineWord) + settling(out.LowN, "answers")
	}
	return out, nil
}

// ---- Top sources ------------------------------------------------------------

type topSourcesArgs struct {
	Days  int `json:"days,omitempty" jsonschema:"trailing window in days (1..365); omit for 30"`
	Limit int `json:"limit,omitempty" jsonschema:"domains to show (1..25); omit for 10"`
}

type topSourcesOut struct {
	chartBasis
	WindowDays int            `json:"window_days"`
	Sources    []topSourceRow `json:"sources" jsonschema:"most cited first"`
	Engines    []string       `json:"engines" jsonschema:"the engines whose answers are counted"`
}

type topSourceRow struct {
	Domain        string `json:"domain"`
	SourceType    string `json:"source_type" jsonschema:"own, competitor, social, informational or other"`
	CitationCount int    `json:"citation_count"`
	Answers       int    `json:"answers" jsonschema:"answers citing this domain"`
}

func topSources(ctx context.Context, d Deps, in topSourcesArgs) (topSourcesOut, error) {
	days, err := windowDays(in.Days, 30, chartMaxDays)
	if err != nil {
		return topSourcesOut{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	// Refused, not clamped, as Cloud refuses it.
	if limit < 1 || limit > 25 {
		return topSourcesOut{}, errors.New("limit must be between 1 and 25")
	}
	w := metrics.Window{Days: days}
	rows, err := d.Metrics.Sources(ctx, w, limit)
	if err != nil {
		return topSourcesOut{}, err
	}
	engineList, err := windowEngines(ctx, d.Metrics, w)
	if err != nil {
		return topSourcesOut{}, err
	}

	out := topSourcesOut{WindowDays: days, Sources: []topSourceRow{}, Engines: engineList}
	n := 0
	for _, r := range rows {
		out.Sources = append(out.Sources, topSourceRow{Domain: r.Site, SourceType: r.SourceType, CitationCount: r.Citations, Answers: r.Answers})
		n += r.Citations
	}
	out.chartBasis = basisFor(viewTopSources, n, "citations")

	window := windowPhrase(days)
	if len(out.Sources) == 0 {
		out.Caption = fmt.Sprintf("No citations in %s yet.", window)
	} else {
		top := out.Sources[0]
		shown := fmt.Sprintf("the %d domains shown", len(out.Sources))
		if len(out.Sources) == 1 {
			shown = "the one domain shown"
		}
		out.Caption = fmt.Sprintf("%s is the most cited source in %s: %d of the %d citations across %s.",
			top.Domain, window, top.CitationCount, n, shown) + settling(out.LowN, "citations")
	}
	return out, nil
}
