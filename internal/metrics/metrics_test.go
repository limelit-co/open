// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package metrics

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/limelit-co/open/internal/store"
)

// fixture builds an instance with one property, two competitors, four prompts
// of different shapes and one target.
type fixture struct {
	db      *store.DB
	svc     *Service
	prompts map[string]int64
	rival   int64
	target  int64
	evalID  int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "limelit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	rival, err := db.AddCompetitor(ctx, store.Competitor{Name: "Rival", Domain: "rival.com"})
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{db: db, svc: New(db), rival: rival, prompts: map[string]int64{}}
	for _, p := range []store.Prompt{
		{Text: "best widget tools", Category: "discovery", Active: true},
		{Text: "acme vs rival", Category: "comparison", Active: true},
		{Text: "is acme any good", Category: "brand", Branded: true, Active: true},
		{Text: "how to widget", Category: "use case", Active: true},
	} {
		id, err := db.AddPrompt(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		f.prompts[p.Category] = id
	}

	if f.target, err = db.AddTarget(ctx, store.Target{
		Spec: "chatgpt:openai", Engine: "chatgpt", Provider: "openai", Access: "api",
	}); err != nil {
		t.Fatal(err)
	}
	if f.evalID, err = db.CreateEvaluation(ctx, 4); err != nil {
		t.Fatal(err)
	}
	return f
}

// chat writes one answer. own and rivalRank are 0 when that brand is absent
// and the rank otherwise; a negative value means mentioned but unranked.
func (f *fixture) chat(t *testing.T, category, status string, own, rivalRank int, cites ...store.Citation) int64 {
	t.Helper()
	rec := store.ChatRecord{
		EvaluationID: f.evalID,
		PromptID:     f.prompts[category],
		TargetID:     f.target,
		Status:       status,
		Text:         "answer",
		Model:        "test",
		Citations:    cites,
	}
	if own != 0 {
		rec.Mentions = append(rec.Mentions, store.Mention{BrandName: "Acme", BrandKey: "acme", ListRank: rank(own)})
	}
	if rivalRank != 0 {
		id := f.rival
		rec.Mentions = append(rec.Mentions, store.Mention{CompetitorID: &id, BrandName: "Rival", BrandKey: "rival", ListRank: rank(rivalRank)})
	}
	id, err := f.db.RecordChat(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rank(v int) *int {
	if v < 0 {
		return nil
	}
	return &v
}

func cite(site, kind string, pos int) store.Citation {
	return store.Citation{URL: "https://" + site + "/x", Host: site, Site: site, Position: pos, SourceType: kind}
}

// TestVisibilityExcludesBrandedPrompts is the rule the headline rests on:
// asking an engine about yourself and counting the answer measures the
// question, not the market.
func TestVisibilityExcludesBrandedPrompts(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 2) // mentioned
	f.chat(t, "use case", store.ChatOK, 0, 1)  // not mentioned
	f.chat(t, "brand", store.ChatOK, 1, 0)     // branded: must not count

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Answers != 2 {
		t.Fatalf("answers = %d, want 2 (the branded answer must be excluded)", got.Answers)
	}
	if got.Visibility != 50 {
		t.Fatalf("visibility = %v, want 50", got.Visibility)
	}
}

// TestNoAnswerSurfaceIsNotABrandMiss guards the second denominator rule. A
// search surface that did not render says nothing about the brand, so
// counting it as an answer that ignored us would report a decline that never
// happened.
func TestNoAnswerSurfaceIsNotABrandMiss(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0)
	f.chat(t, "use case", store.ChatNoAnswerSurface, 0, 0)
	f.chat(t, "comparison", store.ChatFailed, 0, 0)

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Answers != 1 {
		t.Fatalf("answers = %d, want 1", got.Answers)
	}
	if got.Visibility != 100 {
		t.Fatalf("visibility = %v, want 100", got.Visibility)
	}
}

// TestEmptyWindowIsZeroNotNaN covers the state every new install is in.
func TestEmptyWindowIsZeroNotNaN(t *testing.T) {
	f := newFixture(t)
	got, err := f.svc.Overview(context.Background(), Window{Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]float64{
		"visibility": got.Visibility, "sov": got.ShareOfVoice,
		"citations": got.CitationShare, "position": got.MeanPosition,
	} {
		if v != 0 {
			t.Errorf("%s = %v on an empty window, want 0", name, v)
		}
	}
	if !got.LowN {
		t.Error("an empty window must report LowN")
	}
	if len(got.Categories) != 0 {
		t.Errorf("categories = %v, want none", got.Categories)
	}
}

// TestCategoryVisibilitySeparatesDiscoveryFromComparison is the distinction
// that stops a comparison prompt naming a rival from reading as a loss: an
// answer to "acme vs rival" lists rivals by construction.
func TestCategoryVisibilitySeparatesDiscoveryFromComparison(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 2)
	f.chat(t, "comparison", store.ChatOK, 0, 1)

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CategoryVisibility{}
	for _, c := range got.Categories {
		by[c.Category] = c
	}
	if by["discovery"].Visibility != 100 {
		t.Errorf("discovery = %v, want 100", by["discovery"].Visibility)
	}
	if by["comparison"].Visibility != 0 {
		t.Errorf("comparison = %v, want 0", by["comparison"].Visibility)
	}
	if got.Visibility != 50 {
		t.Errorf("headline = %v, want 50", got.Visibility)
	}
}

// TestMeanPositionIgnoresUnrankedMentions: a brand named in prose has no
// rank, and averaging it in as zero would invent a first place.
func TestMeanPositionIgnoresUnrankedMentions(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 2, 0)
	f.chat(t, "use case", store.ChatOK, 4, 0)
	f.chat(t, "comparison", store.ChatOK, -1, 0) // mentioned, unranked

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.MeanPosition != 3 {
		t.Fatalf("mean position = %v, want 3", got.MeanPosition)
	}
	if got.Visibility != 100 {
		t.Fatalf("visibility = %v, want 100: an unranked mention is still a mention", got.Visibility)
	}
}

// TestStandingsAlwaysIncludeTheProperty. "You are not in this conversation"
// is the most useful thing this tool can say, and an absent row would bury it.
func TestStandingsAlwaysIncludeTheProperty(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 0, 1)
	f.chat(t, "use case", store.ChatOK, 0, 1)

	rows, err := f.svc.Standings(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	var own *BrandStanding
	for i := range rows {
		if rows[i].IsOwn {
			own = &rows[i]
		}
	}
	if own == nil {
		t.Fatal("the property is missing from standings when it was never mentioned")
	}
	if own.Mentions != 0 || own.Visibility != 0 {
		t.Errorf("own row = %+v, want zeros", *own)
	}
	if rows[0].Name != "Rival" || rows[0].ShareOfVoice != 100 {
		t.Errorf("rival row = %+v, want 100%% share of voice", rows[0])
	}
}

func TestShareOfVoiceSplitsMentionsNotAnswers(t *testing.T) {
	f := newFixture(t)
	// One answer names us once and the rival twice: 1 of 3 mentions, but
	// both brands appear in 1 of 1 answers.
	id := f.rival
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: "answer",
		Mentions: []store.Mention{
			{BrandName: "Acme", BrandKey: "acme"},
			{CompetitorID: &id, BrandName: "Rival", BrandKey: "rival"},
			{CompetitorID: &id, BrandName: "Rival", BrandKey: "rival"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ShareOfVoice != 33.33 {
		t.Errorf("share of voice = %v, want 33.33", got.ShareOfVoice)
	}
	if got.Visibility != 100 {
		t.Errorf("visibility = %v, want 100", got.Visibility)
	}
}

func TestSourcesRankBySite(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0,
		cite("acme.com", "own", 1), cite("g2.com", "informational", 2))
	f.chat(t, "use case", store.ChatOK, 0, 0,
		cite("g2.com", "informational", 1), cite("reddit.com", "social", 2))

	rows, err := f.svc.Sources(context.Background(), Window{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Site != "g2.com" || rows[0].Citations != 2 {
		t.Fatalf("sources = %+v, want g2.com first with 2", rows)
	}

	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CitationShare != 25 {
		t.Errorf("citation share = %v, want 25 (1 own of 4)", got.CitationShare)
	}
}

// TestSeriesHandlesASinglePoint is the state of every install on day one.
func TestSeriesHandlesASinglePoint(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0)
	f.chat(t, "use case", store.ChatOK, 0, 0)

	points, err := f.svc.Series(context.Background(), Window{Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if points[0].Visibility != 50 || points[0].Answers != 2 {
		t.Fatalf("point = %+v, want 50%% of 2", points[0])
	}
}

// TestMatrixKeepsBrandedPromptsTagged: the grid is where you go to see
// everything, so a branded prompt belongs there, labelled.
func TestMatrixKeepsBrandedPromptsTagged(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0)
	f.chat(t, "brand", store.ChatOK, 1, 0)

	m, err := f.svc.Matrix(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rows) != 4 {
		t.Fatalf("rows = %d, want one per prompt (4)", len(m.Rows))
	}
	if len(m.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(m.Targets))
	}

	var branded, run, empty int
	for _, r := range m.Rows {
		if r.Branded {
			branded++
		}
		if c, ok := r.Cells[f.target]; ok {
			run++
			if c.Visibility != 100 {
				t.Errorf("cell %+v, want 100", c)
			}
		} else {
			empty++
		}
	}
	if branded != 1 {
		t.Errorf("branded rows = %d, want 1", branded)
	}
	if run != 2 || empty != 2 {
		t.Errorf("run/empty = %d/%d, want 2/2: a prompt with no answer is an empty cell, not a zero", run, empty)
	}
}

func TestLowNFlipsAtTheThreshold(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < LowNThreshold; i++ {
		f.chat(t, "discovery", store.ChatOK, 1, 0)
	}
	got, err := f.svc.Overview(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Answers != LowNThreshold || got.LowN {
		t.Fatalf("answers = %d low_n = %v, want %d and false", got.Answers, got.LowN, LowNThreshold)
	}
}

func TestBrandSeriesAlwaysIncludesTheProperty(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 0, 1)
	f.chat(t, "use case", store.ChatOK, 0, 1)

	rows, err := f.svc.BrandSeries(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	var own, rival int
	for _, r := range rows {
		if r.IsOwn {
			own++
			if r.Visibility != 0 || r.Answers != 2 {
				t.Errorf("own row = %+v, want 0%% of 2", r)
			}
		}
		if r.Name == "Rival" {
			rival++
			if r.Visibility != 100 {
				t.Errorf("rival row = %+v, want 100%%", r)
			}
		}
	}
	if own == 0 {
		t.Fatal("the property is missing from the race: a race without your line does not answer the question")
	}
	if rival == 0 {
		t.Fatal("the rival is missing")
	}
}

// TestBrandSeriesCountsAnswersNotMentions guards the fan-out bug in this
// query too: two own-mentions in one answer is one answer.
func TestBrandSeriesCountsAnswersNotMentions(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: "Acme (acme.com) leads.",
		Mentions: []store.Mention{{BrandName: "Acme", BrandKey: "acme"}, {BrandName: "Acme", BrandKey: "acme"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.svc.BrandSeries(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.IsOwn && r.Visibility != 100 {
			t.Fatalf("own visibility = %v on one answer naming us twice, want 100", r.Visibility)
		}
	}
}

func TestEngineBreakdownCoversEveryBrandOnEveryEngine(t *testing.T) {
	f := newFixture(t)
	second, err := f.db.AddTarget(context.Background(), store.Target{
		Spec: "claude:anthropic", Engine: "claude", Provider: "anthropic", Access: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.chat(t, "discovery", store.ChatOK, 1, 1)
	// A rival-only answer on the second engine.
	rid := f.rival
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["use case"], TargetID: second,
		Status: store.ChatOK, Text: "x",
		Mentions: []store.Mention{{CompetitorID: &rid, BrandName: "Rival", BrandKey: "rival"}},
	}); err != nil {
		t.Fatal(err)
	}

	cells, err := f.svc.EngineBreakdown(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	// 2 engines x 2 brands, zeros filled in.
	if len(cells) != 4 {
		t.Fatalf("cells = %d, want 4", len(cells))
	}
	got := map[string]float64{}
	for _, c := range cells {
		got[c.Engine+"/"+c.Name] = c.Visibility
	}
	if got["chatgpt/Acme"] != 100 || got["claude/Acme"] != 0 {
		t.Errorf("own by engine = chatgpt %v, claude %v; want 100 and 0", got["chatgpt/Acme"], got["claude/Acme"])
	}
	if got["claude/Rival"] != 100 {
		t.Errorf("rival on claude = %v, want 100", got["claude/Rival"])
	}
}

func TestCitationMixGroupsBySourceType(t *testing.T) {
	f := newFixture(t)
	f.chat(t, "discovery", store.ChatOK, 1, 0,
		cite("acme.com", "own", 1), cite("g2.com", "informational", 2), cite("reddit.com", "social", 3))
	rows, err := f.svc.CitationMix(context.Background(), Window{})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	kinds := map[string]bool{}
	for _, r := range rows {
		total += r.Citations
		kinds[r.SourceType] = true
	}
	if total != 3 || len(kinds) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestFanOutWordsDropsStopwordsAndCounts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: "x",
		FanOut: []string{"best widget tools 2026", "widget tracking software", "the widget market"},
	}); err != nil {
		t.Fatal(err)
	}
	words, err := f.svc.FanOutWords(context.Background(), Window{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(words) == 0 || words[0].Term != "widget" || words[0].Count != 3 {
		t.Fatalf("words = %+v, want widget x3 first", words)
	}
	for _, w := range words {
		if w.Term == "the" || w.Term == "best" || w.Term == "tools" || w.Term == "2026" {
			t.Errorf("stopword %q survived", w.Term)
		}
	}
}
