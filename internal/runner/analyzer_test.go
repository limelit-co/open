// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package runner

import (
	"context"
	"testing"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

func TestStoreAnalyzerDerivesMentionsAndCitations(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddCompetitor(ctx, store.Competitor{Name: "Globex", Domain: "globex.com"}); err != nil {
		t.Fatal(err)
	}

	analyzer, err := NewStoreAnalyzer(ctx, db)
	if err != nil {
		t.Fatalf("NewStoreAnalyzer: %v", err)
	}

	text := "Best tools:\n\n1. Globex is the enterprise pick.\n2. Acme suits small teams.\n"
	mentions, cites := analyzer.Analyze(text, []provider.Citation{
		{URL: "https://www.acme.com/pricing", Position: 1},
		{URL: "https://globex.com/compare", Position: 2},
		{URL: "https://en.wikipedia.org/wiki/CRM", Position: 3},
	})

	if len(mentions) != 2 {
		t.Fatalf("derived %d mentions: %+v", len(mentions), mentions)
	}
	byName := map[string]store.Mention{}
	for _, m := range mentions {
		byName[m.BrandName] = m
	}
	if got := byName["Globex"]; got.ListRank == nil || *got.ListRank != 1 {
		t.Errorf("Globex rank = %v, want 1", got.ListRank)
	}
	if got := byName["Acme"]; got.CompetitorID != nil {
		t.Error("the property's own mention was attributed to a competitor")
	}
	if got := byName["Globex"]; got.CompetitorID == nil {
		t.Error("a competitor mention has no competitor id")
	}
	// The offsets are what the dashboard highlights, so they have to select
	// the brand rather than a neighbouring word.
	if text[byName["Acme"].OffsetStart:byName["Acme"].OffsetEnd] != "Acme" {
		t.Error("the mention offsets do not select the brand")
	}

	if len(cites) != 3 {
		t.Fatalf("derived %d citations", len(cites))
	}
	wantTypes := []string{"own", "competitor", "informational"}
	for i, want := range wantTypes {
		if cites[i].SourceType != want {
			t.Errorf("citation %d classified as %q, want %q", i, cites[i].SourceType, want)
		}
	}
	if cites[0].Site != "acme.com" {
		t.Errorf("www was not normalised away: %q", cites[0].Site)
	}
}

func TestAnalyzerSeesCompetitorsAsTheyAreWhenThePassStarts(t *testing.T) {
	// Rebuilding per answer would let a competitor added mid-run apply to
	// some answers and not others, which makes one evaluation disagree with
	// itself.
	db := newTestDB(t)
	ctx := context.Background()
	db.SaveProperty(ctx, store.Property{Name: "Acme", Domain: "acme.com"})

	before, err := NewStoreAnalyzer(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	db.AddCompetitor(ctx, store.Competitor{Name: "Globex", Domain: "globex.com"})

	if m, _ := before.Analyze("Globex leads.", nil); len(m) != 0 {
		t.Errorf("an analyzer built before the competitor existed found %+v", m)
	}
	after, _ := NewStoreAnalyzer(ctx, db)
	if m, _ := after.Analyze("Globex leads.", nil); len(m) != 1 {
		t.Errorf("an analyzer built after found %d mentions", len(m))
	}
}

func TestRunnerStoresDerivedRowsFromRealText(t *testing.T) {
	// End to end through the runner: a stub answer that names two tracked
	// brands must land as mention rows attached to the right chat.
	r, db, _ := seed(t, 1, provider.StubConfig{
		Answer:    "1. Globex is the enterprise pick.\n2. Acme suits small teams.\n",
		Citations: []provider.Citation{{URL: "https://acme.com/pricing", Position: 1}},
	})
	ctx := context.Background()
	if _, err := db.AddCompetitor(ctx, store.Competitor{Name: "Globex", Domain: "globex.com"}); err != nil {
		t.Fatal(err)
	}
	r.NewAnalyzer = func(c context.Context) (Analyzer, error) { return NewStoreAnalyzer(c, db) }

	if _, err := r.Run(ctx, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var mentions, own int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mention`).Scan(&mentions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM citation WHERE source_type = 'own'`).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if mentions != 2 {
		t.Errorf("stored %d mentions, want both brands", mentions)
	}
	if own != 1 {
		t.Errorf("stored %d own citations", own)
	}

	// Every mention has to hang off a real chat, or the evidence link on the
	// dashboard would go nowhere.
	var orphans int
	db.QueryRow(`SELECT COUNT(*) FROM mention m LEFT JOIN chat c ON c.id = m.chat_id WHERE c.id IS NULL`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("%d mentions point at no chat", orphans)
	}
}
