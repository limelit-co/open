// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/store"
)

// chartTools is every tool that draws in the charts page.
var chartTools = []struct {
	name, view, basis string
	windowed          bool
}{
	{"show_share_of_voice", viewShareOfVoice, "answers", true},
	{"show_visibility_trend", viewVisibilityTrend, "answers", true},
	{"show_prompt_matrix", viewPromptMatrix, "answers", false},
	{"show_top_sources", viewTopSources, "citations", true},
}

// chartCall calls a chart tool and decodes its text, which must be complete
// JSON on its own.
func chartCall(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res := call(t, s, name, args)
	if res.IsError {
		t.Fatalf("%s: %s", name, resultText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("%s: the text is not complete JSON: %v\n%s", name, err, resultText(res))
	}
	return res, out
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// decode re-reads a result's text into a typed value.
func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(resultText(res)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// addAnswer records one ok answer, naming the given brands.
func addAnswer(t *testing.T, db *store.DB, promptID, targetID int64, text string, mentions []store.Mention, cites []store.Citation) {
	t.Helper()
	ctx := context.Background()
	evalID, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordChat(ctx, store.ChatRecord{
		EvaluationID: evalID, PromptID: promptID, TargetID: targetID, Status: store.ChatOK,
		Text: text, Model: "test", Mentions: mentions, Citations: cites,
	}); err != nil {
		t.Fatal(err)
	}
}

// rival is a mention of the tracked competitor connect seeds. A mention with
// no competitor id is the property's own.
func rival(t *testing.T, db *store.DB) store.Mention {
	t.Helper()
	cs, err := db.Competitors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == "Rival" {
			id := c.ID
			return store.Mention{CompetitorID: &id, BrandName: "Rival", BrandKey: "rival"}
		}
	}
	t.Fatal("no Rival")
	return store.Mention{}
}

func firstPrompt(t *testing.T, db *store.DB) int64 {
	t.Helper()
	prompts, err := db.Prompts(context.Background(), true)
	if err != nil || len(prompts) == 0 {
		t.Fatalf("no prompt: %v", err)
	}
	return prompts[0].ID
}

func TestChartToolsPointAtTheChartsPage(t *testing.T) {
	s, _ := connect(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, c := range chartTools {
		tool := byName[c.name]
		if tool == nil {
			t.Errorf("%s is not registered", c.name)
			continue
		}
		ui, _ := tool.Meta["ui"].(map[string]any)
		if ui["resourceUri"] != chartsURI {
			t.Errorf("%s: _meta.ui.resourceUri = %v, want %s", c.name, ui["resourceUri"], chartsURI)
		}
		if tool.Meta["ui/resourceUri"] != chartsURI {
			t.Errorf("%s: the flat ui/resourceUri older hosts read is %v", c.name, tool.Meta["ui/resourceUri"])
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s draws stored numbers and should say it is read-only", c.name)
		}
	}
}

func TestTheChartsPageIsAnMCPAppResource(t *testing.T) {
	s, _ := connect(t)
	ctx := context.Background()
	list, err := s.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var listed *mcp.Resource
	for _, r := range list.Resources {
		if r.URI == chartsURI {
			listed = r
		}
	}
	if listed == nil {
		t.Fatalf("%s is not listed", chartsURI)
	}
	if listed.MIMEType != "text/html;profile=mcp-app" {
		t.Errorf("listed mime type %q", listed.MIMEType)
	}

	res, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: chartsURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("%d contents, want the one page", len(res.Contents))
	}
	page := res.Contents[0]
	if page.MIMEType != "text/html;profile=mcp-app" || page.Text != chartsPage || page.URI != chartsURI {
		t.Errorf("read returned %q %q, %d bytes", page.URI, page.MIMEType, len(page.Text))
	}
	// Hosts read _meta.ui from the contents first, so it must be there and
	// not only on the listing.
	ui, _ := page.Meta["ui"].(map[string]any)
	if ui["prefersBorder"] != true {
		t.Errorf("contents _meta.ui = %v, want prefersBorder true", page.Meta["ui"])
	}
}

// TestChartResultsCarryTheEnvelope: the page reads the text, and so does a
// model, so the text alone carries the view, the basis and the caption.
func TestChartResultsCarryTheEnvelope(t *testing.T) {
	s, _ := connect(t)
	for _, c := range chartTools {
		res, out := chartCall(t, s, c.name, nil)
		if out["view"] != c.view {
			t.Errorf("%s: view = %v, want %s", c.name, out["view"], c.view)
		}
		n, ok := out["n"].(float64)
		if !ok {
			t.Errorf("%s: no n", c.name)
		}
		// The page says "12 answers" rather than Cloud's "completed runs"
		// only when the result names what n counts.
		if out["basis_label"] != c.basis {
			t.Errorf("%s: basis_label = %v, want %s", c.name, out["basis_label"], c.basis)
		}
		if lowN, ok := out["low_n"].(bool); !ok || lowN != (n < 20) {
			t.Errorf("%s: low_n = %v with n = %v", c.name, out["low_n"], n)
		}
		if _, has := out["window_days"]; has != c.windowed {
			t.Errorf("%s: window_days present = %v, want %v", c.name, has, c.windowed)
		} else if c.windowed && out["window_days"] != float64(30) {
			t.Errorf("%s: window_days = %v, want the default 30", c.name, out["window_days"])
		}
		caption, _ := out["caption"].(string)
		if caption == "" {
			t.Errorf("%s: no caption", c.name)
		}
		if n < 20 && !strings.Contains(caption, "Still settling") {
			t.Errorf("%s: n = %v and the caption does not say it is still settling: %q", c.name, n, caption)
		}
		if strings.Contains(resultText(res), "<svg") || strings.Contains(resultText(res), "<div") {
			t.Errorf("%s: the text carries markup", c.name)
		}
	}
}

func TestChartWindowsAreBoundedAndEchoed(t *testing.T) {
	s, _ := connect(t)
	for _, c := range chartTools {
		if !c.windowed {
			continue
		}
		for _, days := range []int{1, 7, 365} {
			if _, out := chartCall(t, s, c.name, map[string]any{"days": days}); out["window_days"] != float64(days) {
				t.Errorf("%s days=%d: window_days = %v", c.name, days, out["window_days"])
			}
		}
		for _, bad := range []int{-1, 366} {
			if res := call(t, s, c.name, map[string]any{"days": bad}); !res.IsError {
				t.Errorf("%s days=%d was accepted", c.name, bad)
			}
		}
	}
}

// TestChartToolsRefuseArgumentsTheyDoNotTake: an argument Open does not take
// is refused, never ignored, because an ignored filter returns an unscoped
// number to a caller who believes it is scoped.
func TestChartToolsRefuseArgumentsTheyDoNotTake(t *testing.T) {
	s, _ := connect(t)
	for _, c := range chartTools {
		res := call(t, s, c.name, map[string]any{"segment": "enterprise"})
		if !res.IsError {
			t.Errorf("%s accepted a segment", c.name)
		}
	}
	if res := call(t, s, "show_share_of_voice", map[string]any{"segment": "enterprise"}); !strings.Contains(resultText(res), "Limelit Cloud") {
		t.Errorf("show_share_of_voice refused segment without the pointer to Cloud: %s", resultText(res))
	}
}

func TestShareOfVoiceSpeaksTheChartsPageShape(t *testing.T) {
	s, _ := connect(t)
	res, _ := chartCall(t, s, "show_share_of_voice", nil)
	out := decode[shareOfVoiceOut](t, res)

	// connect seeds one answer naming Acme once and Rival once.
	if out.N != 1 || out.Headline.BasisN != out.N || out.TrackedMentions != 2 {
		t.Errorf("n = %d, basis_n = %d, tracked_mentions = %d; want 1, 1, 2", out.N, out.Headline.BasisN, out.TrackedMentions)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows = %+v", out.Rows)
	}
	for _, r := range out.Rows {
		if r.SovPct == nil || *r.SovPct != 50 {
			t.Errorf("%s: sov_pct = %v, want 50", r.Brand, r.SovPct)
		}
		if r.IsYou != (r.Brand == "Acme") {
			t.Errorf("%s: is_you = %v", r.Brand, r.IsYou)
		}
	}
	from, err1 := time.Parse(time.RFC3339, out.Window.From)
	to, err2 := time.Parse(time.RFC3339, out.Window.To)
	if err1 != nil || err2 != nil || to.Sub(from) < 29*24*time.Hour || to.Sub(from) > 31*24*time.Hour {
		t.Errorf("window = %+v, want 30 days of RFC 3339", out.Window)
	}
	if want := "Acme has 50% of tracked-brand mentions in the last 30 days: 1 of 2, across 1 answer. Still settling: fewer than 20 answers."; out.Caption != want {
		t.Errorf("caption = %q", out.Caption)
	}
	if len(out.Engines) != 1 || out.Engines[0] != "ChatGPT" {
		t.Errorf("engines = %v", out.Engines)
	}
}

// TestShareOfVoiceWithNothingNamedIsNull: answers that named no tracked brand
// have no whole to take a share of, so the shares are null, which the page
// draws as its empty state rather than as a pie of zeros.
func TestShareOfVoiceWithNothingNamedIsNull(t *testing.T) {
	s, db := connect(t)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM mention`); err != nil {
		t.Fatal(err)
	}
	res, _ := chartCall(t, s, "show_share_of_voice", nil)
	out := decode[shareOfVoiceOut](t, res)
	if out.N != 1 || out.TrackedMentions != 0 {
		t.Fatalf("n = %d, tracked_mentions = %d", out.N, out.TrackedMentions)
	}
	for _, r := range out.Rows {
		if r.SovPct != nil {
			t.Errorf("%s: sov_pct = %v with nothing named, want null", r.Brand, *r.SovPct)
		}
	}
	if !strings.HasPrefix(out.Caption, "No tracked brand was named") {
		t.Errorf("caption = %q", out.Caption)
	}
}

func TestVisibilityTrendIsACalendarWithGaps(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	// A second answer, three days ago, that does not name the property.
	if _, err := db.ExecContext(ctx, `UPDATE chat SET created_at = datetime('now', '-1 hours')`); err != nil {
		t.Fatal(err)
	}
	targets, err := db.Targets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), targets[0].ID, "Rival only.", []store.Mention{rival(t, db)}, nil)
	if _, err := db.ExecContext(ctx, `UPDATE chat SET created_at = datetime('now', '-3 days') WHERE text = 'Rival only.'`); err != nil {
		t.Fatal(err)
	}

	for _, days := range []int{7, 30} {
		res, _ := chartCall(t, s, "show_visibility_trend", map[string]any{"days": days})
		out := decode[trendOut](t, res)

		pts := out.Visibility.Points
		if len(pts) != days+1 {
			t.Errorf("days=%d: %d points, want %d (the rolling window touches days+1 calendar days)", days, len(pts), days+1)
		}
		today := time.Now().UTC().Format(time.DateOnly)
		if pts[len(pts)-1].Date != today {
			t.Errorf("days=%d: the axis ends %s, want today %s", days, pts[len(pts)-1].Date, today)
		}
		sum, measured := 0, 0
		for i, p := range pts {
			sum += p.Basis
			if p.Value != nil {
				measured++
			} else if p.Basis != 0 {
				t.Errorf("%s: no value but a basis of %d", p.Date, p.Basis)
			}
			if i > 0 {
				prev, _ := time.Parse(time.DateOnly, pts[i-1].Date)
				cur, _ := time.Parse(time.DateOnly, p.Date)
				if cur.Sub(prev) != 24*time.Hour {
					t.Errorf("the axis skips from %s to %s", pts[i-1].Date, p.Date)
				}
			}
		}
		if sum != out.N || out.N != 2 {
			t.Errorf("days=%d: the points' bases sum to %d and n is %d, want both 2", days, sum, out.N)
		}
		if measured != 2 {
			t.Errorf("days=%d: %d measured days, want 2; a day nobody ran must be null", days, measured)
		}
		if c := out.Visibility.Current; c == nil || *c != 50 {
			t.Errorf("current = %v, want 50 (one of two answers names Acme)", deref(c))
		}
		if len(out.CitationShare.Points) != len(pts) || out.CitationShare.Current == nil || *out.CitationShare.Current != 0 {
			t.Errorf("citation share = %+v", out.CitationShare)
		}
		if out.SeriesOf != "own_brand" || out.Annotations == nil || len(out.Annotations) != 0 {
			t.Errorf("series_of = %q, annotations = %v", out.SeriesOf, out.Annotations)
		}
	}

	res, _ := chartCall(t, s, "show_visibility_trend", nil)
	out := decode[trendOut](t, res)
	if len(out.BrandSeries) != 2 {
		t.Fatalf("brand_series = %+v", out.BrandSeries)
	}
	for _, b := range out.BrandSeries {
		if b.IsYou != (b.Brand == "Acme") || len(b.Points) != 2 {
			t.Errorf("%s: is_you = %v, %d points; want one point per measured day", b.Brand, b.IsYou, len(b.Points))
		}
	}
	if want := "Acme was named in 50% of 2 answers in the last 30 days, over 2 measured days. Still settling: fewer than 20 answers."; out.Caption != want {
		t.Errorf("caption = %q", out.Caption)
	}
}

// TestBrandSeriesIsWeeklyPastNinetyDays: a year of a point per brand per day
// would cost a model more than it says, so past 90 days a point is a week,
// the share of that week's answers, dated its Monday.
func TestBrandSeriesIsWeeklyPastNinetyDays(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	targets, err := db.Targets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	// A Wednesday and a Thursday in one week, at least two weeks back: one
	// answer naming Acme on the Wednesday, two on the Thursday of which one
	// names Acme. The week is 2 of 3 answers, not the mean of 100 and 50.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	monday := today.AddDate(0, 0, -14-((int(today.Weekday())+6)%7))
	acme := store.Mention{BrandName: "Acme", BrandKey: "acme"}
	for _, a := range []struct {
		day      time.Time
		mentions []store.Mention
	}{
		{monday.AddDate(0, 0, 2), []store.Mention{acme}},
		{monday.AddDate(0, 0, 3), []store.Mention{acme}},
		{monday.AddDate(0, 0, 3), []store.Mention{rival(t, db)}},
	} {
		addAnswer(t, db, firstPrompt(t, db), targets[0].ID, "weekly "+a.day.Format(time.DateOnly), a.mentions, nil)
		if _, err := db.ExecContext(ctx, `UPDATE chat SET created_at = ? WHERE id = (SELECT MAX(id) FROM chat)`,
			a.day.Add(12*time.Hour).Format("2006-01-02 15:04:05")); err != nil {
			t.Fatal(err)
		}
	}

	res, _ := chartCall(t, s, "show_visibility_trend", map[string]any{"days": 90})
	daily := decode[trendOut](t, res)
	res, _ = chartCall(t, s, "show_visibility_trend", map[string]any{"days": 120})
	week := decode[trendOut](t, res)
	if daily.SeriesStep != "day" || week.SeriesStep != "week" || daily.BrandSeriesStep != "day" || week.BrandSeriesStep != "week" {
		t.Fatalf("steps = %q/%q at 90 days and %q/%q at 120, want day and week",
			daily.SeriesStep, daily.BrandSeriesStep, week.SeriesStep, week.BrandSeriesStep)
	}

	// The trend's own series step by week too: the first point is the
	// window's first day, then every Monday through this week, and the
	// week's figure is its answers' share, not a mean of its days.
	pts := week.Visibility.Points
	first := today.AddDate(0, 0, -120)
	if pts[0].Date != first.Format(time.DateOnly) {
		t.Errorf("the weekly axis starts %s, want the window's first day %s", pts[0].Date, first.Format(time.DateOnly))
	}
	onAxis, sum := map[string]bool{}, 0
	for i, p := range pts {
		onAxis[p.Date] = true
		sum += p.Basis
		d, _ := time.Parse(time.DateOnly, p.Date)
		if i > 0 && d.Weekday() != time.Monday {
			t.Errorf("weekly point %s is not a Monday", p.Date)
		}
		if p.Date == monday.Format(time.DateOnly) && (p.Value == nil || *p.Value != 66.67 || p.Basis != 3) {
			t.Errorf("the seeded week = %v of %d answers, want 66.67 of 3", deref(p.Value), p.Basis)
		}
	}
	if sum != week.N || len(week.CitationShare.Points) != len(pts) {
		t.Errorf("weekly bases sum to %d, n is %d; %d citation points for %d weeks", sum, week.N, len(week.CitationShare.Points), len(pts))
	}
	for _, b := range week.BrandSeries {
		for _, p := range b.Points {
			if !onAxis[p.Date] {
				t.Errorf("%s: weekly point %s is not on the trend's axis", b.Brand, p.Date)
			}
		}
	}
	res, _ = chartCall(t, s, "show_visibility_trend", map[string]any{"days": 365})
	if year := decode[trendOut](t, res); len(year.Visibility.Points) > 54 {
		t.Errorf("a year is %d points, want at most 54 weeks", len(year.Visibility.Points))
	}

	var acmeDaily, acmeWeekly []brandPoint
	for _, b := range daily.BrandSeries {
		if b.IsYou {
			acmeDaily = b.Points
		}
	}
	for _, b := range week.BrandSeries {
		if b.IsYou {
			acmeWeekly = b.Points
		}
	}
	if len(acmeDaily) != 3 {
		t.Errorf("daily points = %+v, want the three measured days", acmeDaily)
	}
	want := brandPoint{Date: monday.Format(time.DateOnly), Value: 66.67}
	if len(acmeWeekly) != 2 || acmeWeekly[0] != want {
		t.Errorf("weekly points = %+v, want %+v first, then this week", acmeWeekly, want)
	}
	for _, b := range week.BrandSeries {
		for _, p := range b.Points {
			if _, err := time.Parse(time.DateOnly, p.Date); err != nil {
				t.Errorf("%s: date %q is not YYYY-MM-DD", b.Brand, p.Date)
			}
		}
	}
}

func TestBrandSeriesStopsAtSixRivals(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	targets, err := db.Targets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var mentions []store.Mention
	for _, name := range []string{"B1", "B2", "B3", "B4", "B5", "B6", "B7", "B8"} {
		id, err := db.AddCompetitor(ctx, store.Competitor{Name: name, Domain: strings.ToLower(name) + ".com"})
		if err != nil {
			t.Fatal(err)
		}
		mentions = append(mentions, store.Mention{CompetitorID: &id, BrandName: name, BrandKey: strings.ToLower(name)})
	}
	addAnswer(t, db, firstPrompt(t, db), targets[0].ID, "Many brands.", mentions, nil)

	res, _ := chartCall(t, s, "show_visibility_trend", nil)
	out := decode[trendOut](t, res)
	own := 0
	for _, b := range out.BrandSeries {
		if b.IsYou {
			own++
		}
	}
	if len(out.BrandSeries) != 7 || own != 1 {
		t.Errorf("%d series with %d of them yours; want the property and six rivals", len(out.BrandSeries), own)
	}
}

func TestPromptMatrixSpeaksTheChartsPageShape(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	// A second prompt that was asked and showed no AI answer, and a third
	// never asked: the grid must tell the two apart.
	aio, err := db.AddTarget(ctx, store.Target{Spec: "ai_overview:dataforseo", Engine: "ai_overview", Provider: "dataforseo", Access: "scraped"})
	if err != nil {
		t.Fatal(err)
	}
	evalID, err := db.CreateEvaluation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordChat(ctx, store.ChatRecord{EvaluationID: evalID, PromptID: firstPrompt(t, db), TargetID: aio, Status: store.ChatNoAnswerSurface, Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPrompt(ctx, store.Prompt{Text: "never asked", Category: "discovery", Active: true}); err != nil {
		t.Fatal(err)
	}

	res, _ := chartCall(t, s, "show_prompt_matrix", nil)
	out := decode[matrixChartOut](t, res)
	if out.CellBasis != "all_answers" {
		t.Errorf("cell_basis = %q", out.CellBasis)
	}
	if strings.Join(out.Platforms, ",") != "chatgpt,ai_overview" || out.PlatformLabels["ai_overview"] == "" {
		t.Errorf("platforms = %v, labels = %v", out.Platforms, out.PlatformLabels)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows = %+v", out.Rows)
	}
	asked := out.Rows[0]
	if c := asked.Cells["chatgpt"]; c.Value == nil || *c.Value != 100 || c.SurfaceStatus != "answer" || c.N != 1 || c.Mentions != 1 {
		t.Errorf("chatgpt cell = %+v", c)
	}
	if c, ok := asked.Cells["ai_overview"]; !ok || c.Value != nil || c.SurfaceStatus != "no_answer_surface" {
		t.Errorf("an engine that showed no AI answer must be a no_answer_surface cell, got %+v (present %v)", c, ok)
	}
	if len(out.Rows[1].Cells) != 0 {
		t.Errorf("a prompt never asked has cells: %+v", out.Rows[1].Cells)
	}
	if out.N != 1 {
		t.Errorf("n = %d, want the 1 answer behind the grid", out.N)
	}
	if want := "Acme is named in 1 of 1 answer across 2 prompts on 2 engines. Still settling: fewer than 20 answers."; out.Caption != want {
		t.Errorf("caption = %q", out.Caption)
	}
}

// TestTwoTargetsOnOneEngineAreTwoColumns: Open never averages two targets of
// one engine together, and a shared key would let one overwrite the other.
func TestTwoTargetsOnOneEngineAreTwoColumns(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	second, err := db.AddTarget(ctx, store.Target{Spec: "chatgpt:dataforseo:online", Engine: "chatgpt", Provider: "dataforseo", Online: true, Access: "scraped"})
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), second, "Rival only.", []store.Mention{rival(t, db)}, nil)

	res, _ := chartCall(t, s, "show_prompt_matrix", nil)
	out := decode[matrixChartOut](t, res)
	if strings.Join(out.Platforms, ",") != "chatgpt,chatgpt_2" {
		t.Fatalf("platforms = %v", out.Platforms)
	}
	if out.PlatformLabels["chatgpt"] != "ChatGPT" || out.PlatformLabels["chatgpt_2"] != "ChatGPT 2" {
		t.Errorf("labels = %v", out.PlatformLabels)
	}
	cells := out.Rows[0].Cells
	if a, b := cells["chatgpt"], cells["chatgpt_2"]; a.Value == nil || *a.Value != 100 || b.Value == nil || *b.Value != 0 {
		t.Errorf("cells = %+v; each target keeps its own number", cells)
	}
}

func TestTopSourcesSpeaksTheChartsPageShape(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	targets, err := db.Targets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), targets[0].ID, "Acme.", []store.Mention{{BrandName: "Acme", BrandKey: "acme"}}, []store.Citation{
		{URL: "https://acme.com/a", Host: "acme.com", Site: "acme.com", Position: 1, SourceType: "own"},
		{URL: "https://g2.com/y", Host: "g2.com", Site: "g2.com", Position: 2, SourceType: "informational"},
	})

	res, _ := chartCall(t, s, "show_top_sources", nil)
	out := decode[topSourcesOut](t, res)
	if len(out.Sources) != 2 || out.Sources[0].Domain != "g2.com" || out.Sources[0].CitationCount != 2 {
		t.Fatalf("sources = %+v", out.Sources)
	}
	if out.Sources[1].SourceType != "own" {
		t.Errorf("acme.com source_type = %q", out.Sources[1].SourceType)
	}
	if out.N != 3 {
		t.Errorf("n = %d, want the 3 citations behind the domains shown", out.N)
	}
	if want := "g2.com is the most cited source in the last 30 days: 2 of the 3 citations across the 2 domains shown. Still settling: fewer than 20 citations."; out.Caption != want {
		t.Errorf("caption = %q", out.Caption)
	}

	if _, one := chartCall(t, s, "show_top_sources", map[string]any{"limit": 1}); len(one["sources"].([]any)) != 1 {
		t.Errorf("limit=1 returned %v", one["sources"])
	}
	for _, bad := range []int{-1, 26} {
		if res := call(t, s, "show_top_sources", map[string]any{"limit": bad}); !res.IsError {
			t.Errorf("limit=%d was accepted; Cloud refuses it, and so does this", bad)
		}
	}
}

func TestChartsWithNothingMeasuredSayWhy(t *testing.T) {
	s, db := connect(t)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM chat`); err != nil {
		t.Fatal(err)
	}
	for _, c := range chartTools {
		_, out := chartCall(t, s, c.name, nil)
		if out["n"] != float64(0) || out["low_n"] != true {
			t.Errorf("%s: n = %v, low_n = %v on an empty instance", c.name, out["n"], out["low_n"])
		}
		if caption, _ := out["caption"].(string); !strings.HasPrefix(caption, "No ") {
			t.Errorf("%s: caption on an empty instance = %q", c.name, caption)
		}
	}
}

var (
	wordAPI     = regexp.MustCompile(`(?i)\bapi\b`)
	wordScraped = regexp.MustCompile(`(?i)\bscraped\b`)
	currency    = regexp.MustCompile(`(?i)[$€£¥]\s?\d|\bUSD\b|\bpric(e|es|ing)\b`)
)

// copyViolations applies the owner's rules for a report surface: no em
// dashes, no pricing or currency, and never "api" or "scraped", because how
// an engine was reached is a property of a target and never of a report.
func copyViolations(surface string) []string {
	var bad []string
	if strings.Contains(surface, "—") || strings.Contains(surface, "&mdash;") {
		bad = append(bad, "an em dash")
	}
	if m := wordAPI.FindString(surface); m != "" {
		bad = append(bad, "the word "+m)
	}
	if m := wordScraped.FindString(surface); m != "" {
		bad = append(bad, "the word "+m)
	}
	if m := currency.FindString(surface); m != "" {
		bad = append(bad, "currency or pricing ("+m+")")
	}
	return bad
}

// The page's one inline script.
var pageScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

func TestChartCopyFollowsTheReportRules(t *testing.T) {
	s, db := connect(t)
	ctx := context.Background()
	// A scraped target with an answer of its own: its access must not reach
	// any chart.
	targetID, err := db.AddTarget(ctx, store.Target{Spec: "ai_overview:dataforseo", Engine: "ai_overview", Provider: "dataforseo", Access: "scraped"})
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), targetID, "Rival is the usual pick.", []store.Mention{rival(t, db)},
		[]store.Citation{{URL: "https://reddit.com/r/x", Host: "reddit.com", Site: "reddit.com", Position: 1, SourceType: "social"}})

	// The whole page is this repository's own, script included.
	if bad := copyViolations(chartsPage); len(bad) > 0 {
		t.Errorf("the charts page contains %s", strings.Join(bad, ", "))
	}
	for _, c := range chartTools {
		args := map[string]any{}
		if c.windowed {
			args["days"] = 90
		}
		res, _ := chartCall(t, s, c.name, args)
		for surface, text := range map[string]string{"text": resultText(res), "drawing": drawing(t, res)} {
			if bad := copyViolations(text); len(bad) > 0 {
				t.Errorf("%s %s contains %s", c.name, surface, strings.Join(bad, ", "))
			}
		}
	}
	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "show_") {
			raw, _ := json.Marshal(tool)
			if bad := copyViolations(string(raw)); len(bad) > 0 {
				t.Errorf("%s's definition contains %s", tool.Name, strings.Join(bad, ", "))
			}
		}
	}
}

// TestTheChartsPageScriptIsOnlyScript keeps the page one page with one
// script: a build step that splices a page into its own script makes the
// browser refuse all of it. This page has no build step, and this test
// keeps it that way.
func TestTheChartsPageScriptIsOnlyScript(t *testing.T) {
	page := chartsPage
	lower := strings.ToLower(page)
	if n := strings.Count(lower, "<!doctype"); n != 1 {
		t.Errorf("the page has %d doctypes, want 1", n)
	}
	if n := strings.Count(lower, "<html"); n != 1 {
		t.Errorf("the page has %d <html> tags, want 1", n)
	}
	scripts := pageScript.FindAllStringSubmatch(page, -1)
	if len(scripts) != 1 || strings.Count(page, "<script") != 1 {
		t.Fatalf("found %d scripts, want the one inline view", len(scripts))
	}
	script := strings.ToLower(scripts[0][1])
	for _, bad := range []string{"<html", "<!doctype", "<head", "<body", "<style", "</html"} {
		if strings.Contains(script, bad) {
			t.Errorf("the script contains %q: the page has been spliced into it", bad)
		}
	}
	// A weekly trend says so in the page's own words too.
	for _, want := range []string{`"Weekly, "`, `"Best week of "`, "series_step"} {
		if !strings.Contains(scripts[0][1], want) {
			t.Errorf("the script never says %s", want)
		}
	}
	for _, want := range []string{"ui/initialize", "ui/notifications/initialized", "ui/notifications/tool-input",
		"ui/notifications/tool-result", "ui/notifications/size-changed", "ui/notifications/host-context-changed",
		"tools/call", "ui/download-file", "serverTools", "downloadFile", chartMetaKey} {
		if !strings.Contains(scripts[0][1], want) {
			t.Errorf("the script never mentions %q", want)
		}
	}
	// The frame has no network: everything the page needs is inline.
	if regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?https?:`).MatchString(page) {
		t.Error("the page loads something from the network")
	}
}

// drawing is the markup a result carries for the page, which must be there
// even when it is empty: the page tells "nothing to draw" from "no drawing
// arrived" by it.
func drawing(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	m, ok := res.Meta[chartMetaKey].(map[string]any)
	if !ok {
		t.Fatalf("no %s in _meta: %v", chartMetaKey, res.Meta)
	}
	html, _ := m["html"].(string)
	return html
}

// TestChartsDrawInMetaAndNeverInTheText: the model reads the text and pays
// for every byte of it, so the SVG reaches the page through _meta only.
func TestChartsDrawInMetaAndNeverInTheText(t *testing.T) {
	s, db := connect(t)
	targets, err := db.Targets(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), targets[0].ID, "Acme, cited.", []store.Mention{{BrandName: "Acme", BrandKey: "acme"}},
		[]store.Citation{{URL: "https://acme.com/a", Host: "acme.com", Site: "acme.com", Position: 1, SourceType: "own"}})
	want := map[string][]string{
		"show_share_of_voice":   {"chart-donut", "chart-bars", ">Acme<", ">Rival<", "s-own"},
		"show_visibility_trend": {"chart-race", "series-own", "Acme"},
		"show_prompt_matrix":    {"chart-grid", "ChatGPT", "best widget tools"},
		"show_top_sources":      {"chart-bars", "acme.com", "src-own", "g2.com"},
	}
	for _, c := range chartTools {
		res, _ := chartCall(t, s, c.name, nil)
		if strings.Contains(resultText(res), "<svg") {
			t.Errorf("%s: the text carries SVG, which the model would pay for", c.name)
		}
		if raw, _ := json.Marshal(res.StructuredContent); strings.Contains(string(raw), "<svg") {
			t.Errorf("%s: structuredContent carries SVG", c.name)
		}
		if m := res.Meta[chartMetaKey].(map[string]any); m["view"] != c.view {
			t.Errorf("%s: _meta view = %v", c.name, m["view"])
		}
		html := drawing(t, res)
		if !strings.HasPrefix(html, "<svg") && !strings.HasPrefix(html, `<div class="sov">`) {
			t.Errorf("%s: the drawing is not SVG: %.80q", c.name, html)
		}
		for _, w := range want[c.name] {
			if !strings.Contains(html, w) {
				t.Errorf("%s: the drawing has no %q", c.name, w)
			}
		}
	}
}

func TestEmptyChartsSendAnEmptyDrawing(t *testing.T) {
	s, db := connect(t)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM chat`); err != nil {
		t.Fatal(err)
	}
	for _, c := range chartTools {
		res, _ := chartCall(t, s, c.name, nil)
		if html := drawing(t, res); html != "" && c.view != viewPromptMatrix {
			t.Errorf("%s: drew %.60q from nothing", c.name, html)
		}
	}
}

func TestWeeklyTrendDrawingSaysWeek(t *testing.T) {
	s, _ := connect(t)
	res, _ := chartCall(t, s, "show_visibility_trend", map[string]any{"days": 120})
	html := drawing(t, res)
	if !strings.Contains(html, "answers per week") || !strings.Contains(html, "week of ") || !strings.Contains(html, "Week of ") {
		t.Errorf("a weekly trend's drawing does not say week: %.200q", html)
	}
	res, _ = chartCall(t, s, "show_visibility_trend", nil)
	if html := drawing(t, res); !strings.Contains(html, "answers per day") || strings.Contains(html, "week of") {
		t.Error("a daily trend's drawing does not say day")
	}
}

func TestGridDrawingNamesASecondTargetsColumn(t *testing.T) {
	s, db := connect(t)
	second, err := db.AddTarget(context.Background(), store.Target{Spec: "chatgpt:dataforseo:online", Engine: "chatgpt", Provider: "dataforseo", Online: true, Access: "scraped"})
	if err != nil {
		t.Fatal(err)
	}
	addAnswer(t, db, firstPrompt(t, db), second, "Rival only.", []store.Mention{rival(t, db)}, nil)
	res, _ := chartCall(t, s, "show_prompt_matrix", nil)
	html := drawing(t, res)
	for _, w := range []string{">ChatGPT<", ">ChatGPT 2<", ">1/1<", ">0/1<"} {
		if !strings.Contains(html, w) {
			t.Errorf("the grid has no %q", w)
		}
	}
}

// TestChartsPageTokensMatchTheDashboard keeps the page's palette the
// dashboard's. Every token the page declares must carry app.css's value, in
// the light block and in the dark one.
func TestChartsPageTokensMatchTheDashboard(t *testing.T) {
	raw, err := os.ReadFile("../ui/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	block := func(src, open, end string) map[string]string {
		i := strings.Index(src, open)
		if i < 0 {
			t.Fatalf("no %q block", open)
		}
		body := src[i+len(open):]
		body = body[:strings.Index(body, end)]
		out := map[string]string{}
		for _, m := range regexp.MustCompile(`(--[a-z0-9-]+):\s*([^;]+);`).FindAllStringSubmatch(body, -1) {
			out[m[1]] = strings.TrimSpace(m[2])
		}
		return out
	}
	pairs := []struct {
		name            string
		page, dashboard map[string]string
	}{
		{name: "light", page: block(chartsPage, ":root {", "\n}"), dashboard: block(css, ":root {", "\n}")},
		{name: "dark", page: block(chartsPage, `:root[data-theme="dark"] {`, "\n}"), dashboard: block(css, `:root:not([data-theme="light"]) {`, "\n  }")},
	}
	for _, p := range pairs {
		if len(p.page) < 30 {
			t.Fatalf("%s: only %d tokens found on the page", p.name, len(p.page))
		}
		for token, value := range p.page {
			if got, ok := p.dashboard[token]; !ok {
				t.Errorf("%s: the page declares %s, which the dashboard does not", p.name, token)
			} else if got != value {
				t.Errorf("%s: %s is %q on the page and %q in app.css", p.name, token, value, got)
			}
		}
	}
}
