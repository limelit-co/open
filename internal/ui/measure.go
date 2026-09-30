// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// The measurement screens: overview, the prompt by engine grid, the answers
// behind every number, and the sources.
//
// Every handler here reads internal/metrics and nothing else. The point of
// that package is that one number cannot differ between two surfaces, and a
// handler that computed its own total would defeat it.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/metrics"
)

// windowChoices are the periods the switch offers.
var windowChoices = []int{7, 30, 90}

// windowFrom reads the window off the query string, defaulting to 30 days.
// The second value is false when the reader did not choose: the caller may
// then widen an empty default to a window that has answers.
func windowFrom(r *http.Request) (int, bool) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		return 30, false
	}
	for _, ok := range windowChoices {
		if days == ok {
			return days, true
		}
	}
	return 30, false
}

// widenThinDefault picks the window to show when the reader did not choose
// one and the default cannot show a trend: the narrowest choice with at least
// two measured days, or failing that with any answers at all.
//
// An install whose runs stopped five weeks ago has a month of history and
// nothing in the last 30 days; one that ran yesterday for the first time in a
// month has a single day there. Opening either on the 30-day view would show
// "No answers yet" or one dot on an empty axis while the history sits one
// click away. The window switch shows which view is open. An explicit choice
// is never overridden.
func (a *App) widenThinDefault(ctx context.Context, days int, chosen bool, measuredDays int) int {
	if chosen || measuredDays >= 2 {
		return days
	}
	best := days
	for _, d := range windowChoices {
		if d <= days {
			continue
		}
		series, err := a.metrics.Series(ctx, metrics.Window{Days: d})
		if err != nil {
			continue
		}
		if len(series) >= 2 {
			return d
		}
		if len(series) > measuredDays && best == days {
			best = d
		}
	}
	return best
}

func windowOptions(path string, current int) []WindowOption {
	out := make([]WindowOption, 0, len(windowChoices))
	for _, d := range windowChoices {
		out = append(out, WindowOption{
			Label:   fmt.Sprintf("%d days", d),
			Days:    d,
			Href:    fmt.Sprintf("%s?days=%d", path, d),
			Current: d == current,
		})
	}
	return out
}

// overview is the screen the product is judged on.
func (a *App) overview(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	base, counts, err := a.base(r, "Overview", "overview")
	if err != nil {
		a.fail(w, r, err)
		return
	}

	days, chosen := windowFrom(r)
	window := metrics.Window{Days: days}

	ov, err := a.metrics.Overview(ctx, window)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !chosen {
		series, err := a.metrics.Series(ctx, window)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if wider := a.widenThinDefault(ctx, days, chosen, len(series)); wider != days {
			days, window = wider, metrics.Window{Days: wider}
			if ov, err = a.metrics.Overview(ctx, window); err != nil {
				a.fail(w, r, err)
				return
			}
		}
	}

	page := MeasurePage{
		Base:       base,
		WindowDays: days,
		Windows:    windowOptions("/overview", days),
		HasData:    ov.Answers > 0,
		LowN:       ov.LowN,
		ThinN:      thinN,
		Answers:    ov.Answers,
		Counts:     CountsView{Prompts: counts.Prompts, Competitors: counts.Competitors, Targets: counts.Targets, Chats: counts.Chats},
	}

	targets, err := a.db.Targets(ctx, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, t := range targets {
		page.Targets = append(page.Targets, TargetView{
			ID: t.ID, Spec: t.Spec, EngineLabel: engineLabel(t.Engine), Access: t.Access,
		})
	}

	if !page.HasData {
		a.write(w, r, "overview", page)
		return
	}

	// The previous equal-length window, so the headline can carry a move
	// rather than a bare number.
	var (
		previous    float64
		hadPrevious bool
	)
	if days > 0 {
		prior, err := a.metrics.Overview(ctx, metrics.Window{Days: days * 2})
		if err == nil && prior.Answers > ov.Answers {
			// The wider window minus the current one is the window before it.
			olderAnswers := prior.Answers - ov.Answers
			olderMentions := prior.AnswersWithMentions - ov.AnswersWithMentions
			if olderAnswers > 0 {
				previous = float64(olderMentions) / float64(olderAnswers) * 100
				hadPrevious = true
			}
		}
	}
	deltaText, deltaKind := delta(ov.Visibility, previous, hadPrevious)

	// Below a useful sample the count IS the headline. "0 of 4" is a
	// complete census of four answers the reader can open; "0%" is an
	// estimate of a rate from four draws, and it is the form that reads as a
	// broken widget. The percentage is demoted to the footer, never hidden.
	countLed := ov.LowN

	page.KPIs = []KPIView{
		{
			Label: "Visibility", Value: pct(ov.Visibility), Unit: "%",
			CountLed:    countLed,
			Count:       fmt.Sprintf("%d", ov.AnswersWithMentions),
			Total:       fmt.Sprintf("%d", ov.Answers),
			TotalHref:   "/chats",
			Denominator: fmt.Sprintf("%s of %s answers", thousands(ov.AnswersWithMentions), thousands(ov.Answers)),
			Delta:       deltaText, DeltaKind: deltaKind, Emphasis: true,
			Help: "The share of answers that name you. Branded prompts are left out, and an answer surface that did not render is left out entirely.",
		},
		{
			Label: "Share of voice", Value: pct(ov.ShareOfVoice), Unit: "%",
			Denominator: "of every tracked brand mention",
			Help:        "Your mentions divided by all tracked brands' mentions in the same answers. One answer naming you twice counts twice.",
		},
		{
			Label: "Average position", Value: position(ov.MeanPosition),
			Denominator: "where you appear in a ranked list",
			Help:        "The mean rank of your brand in numbered or bulleted lists. Mentions in prose have no rank and are not averaged in.",
		},
		{
			Label: "Citation share", Value: pct(ov.CitationShare), Unit: "%",
			Denominator: "of cited sources are yours",
			Help:        "The share of all cited URLs that point at your own domains.",
		},
	}

	for _, c := range ov.Categories {
		page.Categories = append(page.Categories, KPIView{
			Label:       c.Category,
			Value:       pct(c.Visibility),
			Unit:        "%",
			Denominator: fmt.Sprintf("%d of %s", c.Mentions, answersWord(c.Answers)),
			Help:        categoryHelp(c.Category),
		})
	}

	series, err := a.metrics.Series(ctx, window)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	points := make([]TrendPoint, 0, len(series))
	for _, p := range series {
		points = append(points, TrendPoint{Day: p.Day, Value: p.Visibility, N: p.Answers})
	}
	page.Trend, page.TrendPoints = TrendChart(points), len(points)
	if len(points) >= 3 {
		lo, hi := points[0].Value, points[0].Value
		for _, p := range points {
			lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
		}
		page.KPIs[0].Spark = SparkArea(points)
		page.KPIs[0].Range = fmt.Sprintf("%d days, %s to %s%%", len(points), pct(lo), pct(hi))
	}

	standings, err := a.metrics.Standings(ctx, window)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page.Standings = standingViews(standings)

	sources, err := a.metrics.Sources(ctx, window, 8)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page.Sources = sourceViews(sources)

	grid, err := a.metrics.Matrix(ctx, window)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page.Columns, page.Rows = gridViews(grid)

	if err := a.richCharts(ctx, window, &page, standings); err != nil {
		a.fail(w, r, err)
		return
	}

	a.write(w, r, "overview", page)
}

// richCharts fills the competitor race, the donut, the engine strips, the
// citation mix and the fan-out cloud. Every one is drawn from rows the runner
// wrote; nothing here fills a gap or invents a series.
func (a *App) richCharts(ctx context.Context, window metrics.Window, page *MeasurePage, standings []metrics.BrandStanding) error {
	// The race carries the property plus the leading rivals. Twenty lines
	// is spaghetti; seven reads. The cut is by current visibility in
	// standings order and it is stated on the page, because a chart that
	// silently drops series reads as "these are all of them". The same
	// seven are the donut's named slices and the hued dots on the engine
	// strips, so one vocabulary of colour runs through the page.
	const raceRivals = 6
	inRace := map[string]bool{}
	var raceNames []string
	for _, b := range standings {
		if b.IsOwn {
			inRace[b.Name] = true
			continue
		}
		if len(raceNames) < raceRivals {
			inRace[b.Name] = true
			raceNames = append(raceNames, b.Name)
		}
	}
	// Hues are assigned alphabetically among the drawn rivals, so six brands
	// get six distinct hues. A brand outside the race is drawn in the field
	// grey wherever it appears.
	classOf := func(name string, isOwn bool) string {
		if isOwn {
			return "s-own"
		}
		if !inRace[name] {
			return "s-field"
		}
		return seriesClass(name, raceNames, false)
	}

	// The race.
	brandDays, err := a.metrics.BrandSeries(ctx, window)
	if err != nil {
		return err
	}
	byBrand := map[string]*RaceSeries{}
	for _, bd := range brandDays {
		s, ok := byBrand[bd.Name]
		if !ok {
			s = &RaceSeries{Name: bd.Name, IsOwn: bd.IsOwn, Class: classOf(bd.Name, bd.IsOwn)}
			byBrand[bd.Name] = s
		}
		s.Points = append(s.Points, TrendPoint{Day: bd.Day, Value: bd.Visibility, N: bd.Answers})
		s.Last = bd.Visibility
	}
	race := make([]RaceSeries, 0, raceRivals+1)
	for _, b := range standings {
		s, ok := byBrand[b.Name]
		if !ok || !inRace[b.Name] {
			continue
		}
		race = append(race, *s)
		page.RaceLegend = append(page.RaceLegend, LegendItem{Name: s.Name, Class: s.Class, IsOwn: s.IsOwn, Value: pct(s.Last) + "%"})
		if s.IsOwn {
			for _, p := range s.Points {
				page.RaceAnswers += p.N
			}
		}
	}
	page.RaceBrands = len(byBrand)
	page.RaceOmitted = len(byBrand) - len(race)
	page.Race = RaceChart(race)

	// The donut. The race's brands are named slices; every other brand
	// with a share is summed into one "other brands" slice, because at
	// 2*pi*80 units of ring a 0.3% share is a quarter of a unit long and
	// draws as nothing while sitting in the legend as if it were there.
	var (
		slices []DonutSlice
		own    DonutOwn
		rest   DonutSlice
	)
	for _, b := range standings {
		own.Total += b.Mentions
		if b.IsOwn {
			own.Share, own.Mentions, own.HasData = b.ShareOfVoice, b.Mentions, b.Mentions > 0
		}
		if inRace[b.Name] {
			cls := classOf(b.Name, b.IsOwn)
			if b.ShareOfVoice > 0 {
				slices = append(slices, DonutSlice{Name: b.Name, IsOwn: b.IsOwn, Class: cls, Share: b.ShareOfVoice})
			}
			page.DonutLegend = append(page.DonutLegend, LegendItem{Name: b.Name, Class: cls, IsOwn: b.IsOwn, Value: pct(b.ShareOfVoice) + "%"})
			continue
		}
		if b.ShareOfVoice > 0 {
			rest.Share += b.ShareOfVoice
			rest.Count++
			page.DonutTail = append(page.DonutTail, LegendItem{Name: b.Name, Class: "s-rest", Value: pct(b.ShareOfVoice) + "%"})
		}
	}
	if rest.Count > 0 {
		rest.Name = fmt.Sprintf("%d other brands", rest.Count)
		if rest.Count == 1 {
			rest.Name = page.DonutTail[0].Name
		}
		rest.Class, rest.Rest = "s-rest", true
		slices = append(slices, rest)
		page.DonutRest = &LegendItem{Name: rest.Name, Class: "s-rest", Value: pct(rest.Share) + "%"}
	}
	page.Donut = Donut(slices, own)

	// Visibility by engine.
	cells, err := a.metrics.EngineBreakdown(ctx, window)
	if err != nil {
		return err
	}
	groupIdx := map[string]int{}
	var groups []EngineGroup
	for _, c := range cells {
		key := c.Engine + "/" + c.Access
		gi, ok := groupIdx[key]
		if !ok {
			gi = len(groups)
			groupIdx[key] = gi
			groups = append(groups, EngineGroup{Engine: c.Engine, Access: c.Access, Answers: c.Answers})
		}
		groups[gi].Bars = append(groups[gi].Bars, EngineBar{
			Name: c.Name, IsOwn: c.IsOwn, Class: classOf(c.Name, c.IsOwn),
			Visibility: c.Visibility, Mentions: c.Mentions,
		})
	}
	page.Engines = EngineBars(groups)
	page.EnginesLegend = page.RaceLegend

	// The citation mix.
	mixRows, err := a.metrics.CitationMix(ctx, window)
	if err != nil {
		return err
	}
	dayIdx := map[string]int{}
	var mixDays []MixDay
	totals := map[string]int{}
	grand := 0
	for _, m := range mixRows {
		di, ok := dayIdx[m.Day]
		if !ok {
			di = len(mixDays)
			dayIdx[m.Day] = di
			mixDays = append(mixDays, MixDay{Day: m.Day, Counts: map[string]int{}})
		}
		mixDays[di].Counts[m.SourceType] += m.Citations
		mixDays[di].Total += m.Citations
		totals[m.SourceType] += m.Citations
		grand += m.Citations
	}
	page.Mix = CitationMixChart(mixDays)
	for _, kind := range mixOrder {
		if n := totals[kind]; n > 0 {
			share := 0.0
			if grand > 0 {
				share = float64(n) / float64(grand) * 100
			}
			page.MixLegend = append(page.MixLegend, MixLegend{Kind: kind, Count: n, Share: pct(share) + "%"})
		}
	}

	// The fan-out cloud.
	words, err := a.metrics.FanOutWords(ctx, window, 36)
	if err != nil {
		return err
	}
	cloud := make([]CloudWord, 0, len(words))
	for _, w := range words {
		cloud = append(cloud, CloudWord{Term: w.Term, Count: w.Count})
	}
	page.Cloud = WordCloud(cloud)
	return nil
}

// standingViews ranks the brands and keeps the property visible at zero.
func standingViews(in []metrics.BrandStanding) []StandingView {
	out := make([]StandingView, 0, len(in))
	for i, b := range in {
		v := StandingView{
			Rank:       i + 1,
			Name:       b.Name,
			IsOwn:      b.IsOwn,
			Visibility: pct(b.Visibility),
			Share:      pct(b.ShareOfVoice),
			ShareWidth: fmt.Sprintf("%.1f%%", b.ShareOfVoice),
			Position:   position(b.MeanPosition),
			Detail:     fmt.Sprintf("%d in %s", b.Mentions, answersWord(b.Answers)),
			Absent:     b.Mentions == 0,
		}
		if v.Absent {
			v.Rank = 0
			v.Detail = "never named"
			// A zero-width bar is invisible, and an invisible row reads as a
			// rendering bug rather than as the finding it is.
			v.ShareWidth = "0%"
		}
		out = append(out, v)
	}
	return out
}

func sourceViews(in []metrics.SourceRow) []SourceView {
	out := make([]SourceView, 0, len(in))
	top := 0
	for _, s := range in {
		if s.Citations > top {
			top = s.Citations
		}
	}
	for _, s := range in {
		width := "0%"
		if top > 0 {
			width = fmt.Sprintf("%.1f%%", float64(s.Citations)/float64(top)*100)
		}
		out = append(out, SourceView{
			Site: s.Site, SourceType: s.SourceType,
			Citations: s.Citations, Answers: s.Answers, Width: width,
		})
	}
	return out
}

// gridViews builds the prompt by engine grid.
//
// This is the screen the product is shown for: one glance answers which
// engine is losing you which question, which nothing else in this category
// puts on one page.
//
// Cells carry a fraction, not a percentage. "0/1" is a complete census of one
// answer the reader can open; "0%" is an estimate of a rate from one draw.
func gridViews(m metrics.Matrix) ([]GridColumnView, []GridRowView) {
	columns := make([]GridColumnView, 0, len(m.Targets))
	for _, t := range m.Targets {
		columns = append(columns, GridColumnView{Label: engineLabel(t.Engine), Spec: t.Spec})
	}

	colMentions := make([]int, len(m.Targets))
	colAnswers := make([]int, len(m.Targets))

	rows := make([]GridRowView, 0, len(m.Rows))
	for _, r := range m.Rows {
		row := GridRowView{
			PromptID: r.PromptID, Prompt: r.Prompt,
			Category: r.Category, Branded: r.Branded,
			Href: fmt.Sprintf("/chats?prompt=%d", r.PromptID),
		}
		rowMentions, rowAnswers := 0, 0

		for i, t := range m.Targets {
			cell, ok := r.Cells[t.ID]
			if ok && cell.Answers == 0 && cell.NoSurface > 0 {
				row.Cells = append(row.Cells, GridCellView{
					NoAnswer: true, Bin: cellBin(0, 0),
					Title: fmt.Sprintf("%s: asked %s, and it showed no AI answer; not counted against you",
						engineLabel(t.Engine), timesWord(cell.NoSurface)),
				})
				continue
			}
			if !ok || cell.Answers == 0 {
				row.Cells = append(row.Cells, GridCellView{
					Ran: false, Bin: cellBin(0, 0),
					Title: engineLabel(t.Engine) + ": not asked yet",
				})
				continue
			}
			rowMentions, rowAnswers = rowMentions+cell.Mentions, rowAnswers+cell.Answers
			colMentions[i], colAnswers[i] = colMentions[i]+cell.Mentions, colAnswers[i]+cell.Answers

			sub := ""
			if cell.MeanPosition > 0 {
				sub = "#" + position(cell.MeanPosition)
			}
			row.Cells = append(row.Cells, GridCellView{
				Ran:   true,
				Label: fmt.Sprintf("%d/%d", cell.Mentions, cell.Answers),
				Sub:   sub,
				Bin:   cellBin(cell.Answers, cell.Visibility),
				Title: fmt.Sprintf("%s: named in %d of %s%s",
					engineLabel(t.Engine), cell.Mentions, answersWord(cell.Answers), positionSuffix(cell.MeanPosition)),
				Href: fmt.Sprintf("/chats?prompt=%d&target=%d", r.PromptID, t.ID),
			})
		}
		row.Total = fmt.Sprintf("%d/%d", rowMentions, rowAnswers)
		rows = append(rows, row)
	}

	// Worst first, so the prompts you are losing are the ones on screen.
	// Branded prompts sink to the bottom: they are excluded from the headline
	// and a high score there is not the same kind of win.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Branded != rows[j].Branded {
			return !rows[i].Branded
		}
		return gridRate(rows[i].Total) < gridRate(rows[j].Total)
	})

	for i := range columns {
		columns[i].Total = fmt.Sprintf("%d/%d", colMentions[i], colAnswers[i])
	}
	return columns, rows
}

// gridRate turns a "2/3" total back into a rate for sorting. A row nobody has
// run sorts last among the unbranded rather than first: it is not a loss.
func gridRate(total string) float64 {
	var mentions, answers int
	if _, err := fmt.Sscanf(total, "%d/%d", &mentions, &answers); err != nil || answers == 0 {
		return 2
	}
	return float64(mentions) / float64(answers)
}

func positionSuffix(pos float64) string {
	if pos <= 0 {
		return ""
	}
	return fmt.Sprintf(", average position %.1f", pos)
}

func engineLabel(id string) string {
	if e, ok := engines.Lookup(id); ok {
		return e.Label
	}
	return id
}

// answers is the evidence list: every stored answer, newest first.
func (a *App) answers(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	base, counts, err := a.base(r, "Answers", "chats")
	if err != nil {
		a.fail(w, r, err)
		return
	}

	filter := metrics.ChatFilter{Limit: 100}
	page := AnswersPage{Base: base, Counts: CountsView{
		Prompts: counts.Prompts, Competitors: counts.Competitors,
		Targets: counts.Targets, Chats: counts.Chats,
	}}

	q := r.URL.Query()
	if id, err := strconv.ParseInt(q.Get("prompt"), 10, 64); err == nil {
		filter.PromptID, page.Filtered = id, true
	}
	if id, err := strconv.ParseInt(q.Get("target"), 10, 64); err == nil {
		filter.TargetID, page.Filtered = id, true
	}
	switch q.Get("show") {
	case "mentioned":
		yes := true
		filter.Mentioned, page.Filtered, page.FilterName = &yes, true, "answers that name you"
	case "missed":
		no := false
		filter.Mentioned, page.Filtered, page.FilterName = &no, true, "answers that left you out"
	case "failed":
		filter.Status, page.Filtered, page.FilterName = "failed", true, "failed answers"
	}

	rows, err := a.metrics.Chats(ctx, filter)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	total, err := a.metrics.CountChats(ctx, metrics.ChatFilter{})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page.Total = total
	for _, c := range rows {
		page.Answers = append(page.Answers, answerView(c))
	}

	base.Title = "Answers"
	page.Filters = []WindowOption{
		{Label: "All", Href: "/chats", Current: q.Get("show") == ""},
		{Label: "Named you", Href: "/chats?show=mentioned", Current: q.Get("show") == "mentioned"},
		{Label: "Left you out", Href: "/chats?show=missed", Current: q.Get("show") == "missed"},
		{Label: "Failed", Href: "/chats?show=failed", Current: q.Get("show") == "failed"},
	}
	a.write(w, r, "chats", page)
}

func answerView(c metrics.ChatSummary) AnswerView {
	return AnswerView{
		ID: c.ID, Prompt: c.Prompt, Engine: engineLabel(c.Engine), Access: c.Access,
		Status: c.Status, StatusOK: c.Status == "ok", Model: c.Model,
		When: shortTime(c.CreatedAt), Preview: c.Preview,
		Mentioned: c.Mentioned, Mentions: c.Mentions, Citations: c.Citations,
		Position: position(c.Position),
		Href:     fmt.Sprintf("/chats/%d", c.ID),
	}
}

// answer is one answer in full, with the brand highlighted in the text.
func (a *App) answer(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	base, _, err := a.base(r, "Answer", "chats")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := a.metrics.Chat(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	view := AnswerDetailView{
		AnswerView: answerView(d.ChatSummary),
		FanOut:     d.FanOut,
		InputTok:   d.InputTok,
		OutputTok:  d.OutputTok,
		Error:      d.Error,
	}
	view.Body = renderAnswer(d.Text, d.Brands)
	view.Brands = brandChips(d.Brands)
	for _, s := range d.Sources {
		view.Sources = append(view.Sources, SourceLinkView{
			Position: s.Position, URL: s.URL, Site: s.Site,
			Title: s.Title, SourceType: s.SourceType,
		})
	}

	base.Title = "Answer"
	a.write(w, r, "chat", AnswerPage{Base: base, Answer: view})
}

func rankLabel(pos int) string {
	if pos <= 0 {
		return ""
	}
	return "#" + strconv.Itoa(pos)
}

// citations is the sources screen.
func (a *App) citations(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	base, _, err := a.base(r, "Citations", "citations")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	days, _ := windowFrom(r)
	window := metrics.Window{Days: days}

	sources, err := a.metrics.Sources(ctx, window, 60)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ov, err := a.metrics.Overview(ctx, window)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	page := CitationsPage{
		Base: base, Sources: sourceViews(sources),
		Answers: ov.Answers, LowN: ov.LowN,
	}
	byKind := map[string]int{}
	for _, s := range sources {
		byKind[s.SourceType] += s.Citations
		page.Total += s.Citations
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return byKind[kinds[i]] > byKind[kinds[j]] })
	for _, k := range kinds {
		share := 0.0
		if page.Total > 0 {
			share = float64(byKind[k]) / float64(page.Total) * 100
		}
		page.Kinds = append(page.Kinds, KPIView{
			Label: k, Value: pct(share), Unit: "%",
			Denominator: fmt.Sprintf("%d of %d citations", byKind[k], page.Total),
		})
	}
	a.write(w, r, "citations", page)
}

// shortTime trims the stored timestamp to something a person reads.
func shortTime(ts string) string {
	if len(ts) >= 16 {
		return ts[:16]
	}
	return ts
}

// timesWord is "once", "twice", or "N times".
func timesWord(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}
