// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// View models for the measurement screens.
//
// Formatting lives here rather than in template expressions, because a
// template cannot be tested and a percentage that is rounded in two places
// eventually rounds differently in each.
//
// One rule runs through all of it: a number never appears without the
// denominator it came from. "71%" is not a headline; "71% of 28 answers" is.

import (
	"fmt"
	"html/template"
	"strings"
)

// KPIView is one headline number.
//
// It carries its own label and denominator because a card that holds only a
// number is meaningless the moment it is screenshotted, exported, or read by
// a screen reader.
type KPIView struct {
	Label string
	Value string
	// Unit is rendered smaller and tighter against the value.
	Unit string
	// Denominator is the sample the number rests on, always shown.
	Denominator string
	// Delta is the change against the previous equal-length window, already
	// formatted with its sign. Empty when there is no previous window to
	// compare against, which is not the same as no change.
	Delta string
	// DeltaKind is up, down or flat, for styling only.
	DeltaKind string
	// Help is the formula, stated plainly. It is the thing that makes a
	// number checkable.
	Help string
	// Emphasis marks the one number the page is about.
	Emphasis bool
	// CountLed swaps the headline from a percentage to a count.
	//
	// Below a useful sample the two forms say different things. "0 of 4" is
	// a complete, checkable census of four answers a reader can open. "0%"
	// is an estimate of a rate from four draws, and it is the form that
	// reads as a broken widget. The percentage is demoted, never hidden:
	// the reader paid for these answers and can see every one.
	CountLed  bool
	Count     string
	Total     string
	TotalHref string
	// Spark is the number's own history, drawn under it. Only the lead
	// card has one today.
	Spark template.HTML
	// Range is the spread behind the spark: "11 days, 0 to 72%".
	Range string
}

// StandingView is one brand in the ranking.
type StandingView struct {
	Rank       int
	Name       string
	IsOwn      bool
	Visibility string
	Share      string
	// ShareWidth is the bar fill, 0-100, as a CSS percentage string.
	ShareWidth string
	Position   string
	Detail     string
	// Absent marks a brand that never appeared. The row still renders,
	// because "you are not in this conversation" is the most useful thing
	// this tool can say and hiding it would bury it.
	Absent bool
}

// SourceView is one cited site.
type SourceView struct {
	Site       string
	SourceType string
	Citations  int
	Answers    int
	// Width is the bar fill relative to the most-cited site.
	Width string
}

// GridCellView is one prompt against one target.
type GridCellView struct {
	// NoAnswer is set when the engine was asked but showed no answer at all
	// (a Google search with no AI Overview): not a miss, and not unasked.
	NoAnswer bool
	// Ran is false when this pair has no answer at all, which renders as an
	// empty cell rather than a zero. A prompt that was never asked of an
	// engine is not a prompt that engine ignored, and the two are drawn
	// differently on purpose: a measured zero is filled and clickable, an
	// unasked cell is dashed and inert.
	Ran bool
	// Label is the fraction, "2/3", not a percentage. At the sample a new
	// install has, the count is the whole census and a percentage is an
	// estimate of a rate from a handful of draws.
	Label string
	Sub   string
	// Bin is a CSS class, not a colour. The readable ink over each step of
	// the ramp differs by theme and flips at a different step in each.
	Bin   string
	Title string
	Href  string
}

// GridRowView is one prompt across every target.
type GridRowView struct {
	PromptID int64
	Prompt   string
	Category string
	Branded  bool
	Cells    []GridCellView
	// Total is the row's own fraction across every engine, so a reader can
	// see which prompt is lost everywhere without adding up the row.
	Total string
	Href  string
}

// GridColumnView is one target's column header. The way the engine was
// reached travels on the target and never on the screen: two targets are two
// columns, never averaged, and that is the whole distinction a reader needs.
type GridColumnView struct {
	Label string
	Spec  string
	// Total is the column's own fraction across every prompt.
	Total string
}

// AnswerView is one answer in a list.
type AnswerView struct {
	ID        int64
	Prompt    string
	Engine    string
	Access    string
	Status    string
	StatusOK  bool
	Model     string
	When      string
	Preview   string
	Mentioned bool
	Mentions  int
	Citations int
	Position  string
	Href      string
}

// AnswerDetailView is one answer in full.
type AnswerDetailView struct {
	AnswerView
	// Body is the answer with every brand mention wrapped in a mark element.
	// Making the reader hunt for the brand in a wall of text is the failure
	// this screen exists to avoid.
	Body    template.HTML
	Brands  []BrandChipView
	Sources []SourceLinkView
	// FanOut is what the engine searched on the way to this answer. It is
	// frequently not the question that was asked, and that gap names the
	// query you would actually have to win.
	FanOut    []string
	InputTok  int
	OutputTok int
	Error     string
}

// BrandChipView is one brand found in an answer.
type BrandChipView struct {
	Name     string
	IsOwn    bool
	Position string
	// Times is set only when a brand was named more than once, because "1
	// time" is noise on every chip that is not repeated.
	Times string
}

// SourceLinkView is one citation.
type SourceLinkView struct {
	Position   int
	URL        string
	Site       string
	Title      string
	SourceType string
}

// LegendItem is one entry under a multi-series chart.
type LegendItem struct {
	Name  string
	Class string
	IsOwn bool
	Value string
}

// MixLegend is one source class in the citation mix legend.
type MixLegend struct {
	Kind  string
	Count int
	Share string
}

// MeasurePage is the overview.
type MeasurePage struct {
	Base
	WindowDays int
	Windows    []WindowOption
	// HasData is false before the first answer lands. The screen then
	// teaches rather than showing a wall of zeros.
	HasData bool
	// LowN captions every number rather than warning on each one. A new
	// install is legitimately here on its first day and should not be
	// covered in warning badges.
	LowN        bool
	Answers     int
	KPIs        []KPIView
	Trend       template.HTML
	TrendPoints int
	// Race is every brand's visibility over time on one chart.
	Race       template.HTML
	RaceLegend []LegendItem
	// RaceOmitted is how many tracked brands the race does not draw. Stated
	// on the page so a capped chart never reads as the whole field.
	RaceOmitted int
	// RaceBrands is every tracked brand with a point in the window, and
	// RaceAnswers is how many answers the property's line rests on.
	RaceBrands  int
	RaceAnswers int
	// ThinN is the sample under which a point is drawn hollow, for the copy.
	ThinN int
	// Donut is share of voice as parts of a whole. DonutLegend names the
	// race's brands; DonutRest is the one slice that sums everyone else, and
	// DonutTail lists who is in it.
	Donut       template.HTML
	DonutLegend []LegendItem
	DonutRest   *LegendItem
	DonutTail   []LegendItem
	// Engines is visibility by engine, one bar per brand.
	Engines       template.HTML
	EnginesLegend []LegendItem
	// Mix is cited sources by type per day.
	Mix       template.HTML
	MixLegend []MixLegend
	// Cloud is the vocabulary of the engines' own searches.
	Cloud      []CloudWord
	Categories []KPIView
	Standings  []StandingView
	Sources    []SourceView
	Columns    []GridColumnView
	Rows       []GridRowView
	Counts     CountsView
	Targets    []TargetView
}

// WindowOption is one entry in the window switch.
type WindowOption struct {
	Label   string
	Days    int
	Href    string
	Current bool
}

// AnswersPage is the evidence list.
type AnswersPage struct {
	Base
	Answers []AnswerView
	Total   int
	// Filtered says whether a filter is on, so an empty list can tell the
	// difference between "nothing has run" and "nothing matches".
	Filtered   bool
	FilterName string
	Filters    []WindowOption
	Counts     CountsView
}

// AnswerPage is one answer in full.
type AnswerPage struct {
	Base
	Answer AnswerDetailView
}

// CitationsPage is the sources screen.
type CitationsPage struct {
	Base
	Sources []SourceView
	Total   int
	Answers int
	LowN    bool
	Kinds   []KPIView
}

// pct formats a percentage the way every surface here formats it: no decimal
// unless the number is small enough that the decimal is the difference
// between "nothing" and "something".
func pct(v float64) string {
	if v > 0 && v < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
	}
	return fmt.Sprintf("%.0f", v)
}

// thousands writes an integer with separators: 1,297 not 1297. fmt has no
// verb for it.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// answersWord keeps the denominator readable at n=1.
func answersWord(n int) string {
	if n == 1 {
		return "1 answer"
	}
	return fmt.Sprintf("%d answers", n)
}

// delta formats a change in percentage points.
//
// With no earlier window it returns a label saying so rather than "no
// change". "No change" is a claim about a comparison that was never made, and
// on a first run it is the most confident wrong thing the screen could say.
func delta(now, before float64, hadPrevious bool) (string, string) {
	if !hadPrevious {
		return "first window", "none"
	}
	diff := now - before
	switch {
	case diff > 0.05:
		return "\u25b2 " + pct(diff) + " pts", "up"
	case diff < -0.05:
		return "\u25bc " + pct(-diff) + " pts", "down"
	default:
		return "no change", "flat"
	}
}

// position renders a mean rank, or says plainly that there is none. Rounding
// a mean to an integer destroys the difference between 1.0 and 1.4, which on
// a ranking is the whole signal.
func position(v float64) string {
	if v <= 0 {
		return "not ranked"
	}
	return fmt.Sprintf("%.1f", v)
}

// categoryHelp explains why a prompt shape is scored on its own.
func categoryHelp(name string) string {
	switch name {
	case "discovery":
		return "Open questions that name nobody. The only shape that measures whether an engine reaches for you unprompted."
	case "comparison":
		return "Questions that name a rival. An answer lists rivals by construction, so a low score here is not the same as a low score on discovery."
	case "use case":
		return "Questions about the job, not the category."
	case "brand":
		return "Questions that name you. Excluded from the headline: asking an engine about yourself measures the question, not the market."
	default:
		return "Prompts with no category set."
	}
}
