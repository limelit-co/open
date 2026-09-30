// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

// storedAnswer records one answer the way an older build did: with the rows
// its own matcher derived, which is what re-analysis has to correct.
func storedAnswer(t *testing.T, db *store.DB, promptID, targetID int64, text string, own int) int64 {
	t.Helper()
	var ms []store.Mention
	for i := 0; i < own; i++ {
		ms = append(ms, store.Mention{BrandKey: "kindletopdf", BrandName: "Kindle to PDF", OffsetStart: 0, OffsetEnd: 13})
	}
	id, err := db.RecordChat(context.Background(), store.ChatRecord{
		PromptID: promptID, TargetID: targetID, Status: store.ChatOK, Text: text,
		Mentions:  ms,
		Citations: []store.Citation{{URL: "https://www.epubor.com/guide", Host: "epubor.com", Site: "epubor.com", Title: "Guide", Position: 1, SourceType: "other"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func countRows(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStoredAnswersAreReadAgainWhenTheRulesOrBrandsChange. An install that
// counted "BitRecover Kindle to PDF Converter" as its own brand is corrected
// on its next start, a prompt that is really the category leaves the branded
// set, a competitor added later is counted in the answers already stored,
// and nothing is read again when nothing changed.
func TestStoredAnswersAreReadAgainWhenTheRulesOrBrandsChange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SaveProperty(ctx, store.Property{Name: "kindletopdf.com", Domain: "kindletopdf.com", Aliases: []string{"Kindle to PDF"}}); err != nil {
		t.Fatal(err)
	}
	category, _ := db.AddPrompt(ctx, store.Prompt{Text: "convert Kindle to PDF.", Branded: true, Active: true})
	question, _ := db.AddPrompt(ctx, store.Prompt{Text: "Is kindletopdf.com any good?", Branded: true, Active: true})
	targetID, _ := db.AddTarget(ctx, store.Target{Spec: "chatgpt:stub", Engine: "chatgpt", Provider: provider.StubName, Access: "api"})
	rival := storedAnswer(t, db, category, targetID, "BitRecover Kindle to PDF Converter is safe. Epubor Ultimate removes DRM.", 1)
	real := storedAnswer(t, db, question, targetID, "It is a Chrome extension called **Kindle to PDF**.", 1)

	r := New(db, provider.NewRegistry(), provider.StaticCredentials(nil), quietLog())
	r.NewAnalyzer = func(c context.Context) (Analyzer, error) { return NewStoreAnalyzer(c, db) }

	res, ran, err := r.EnsureAnalyzed(ctx)
	if err != nil || !ran {
		t.Fatalf("first start: ran=%v err=%v", ran, err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM mention WHERE chat_id = ?`, rival); n != 0 {
		t.Errorf("the rival's product still counts: %d mentions", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM mention WHERE chat_id = ? AND competitor_id IS NULL`, real); n != 1 {
		t.Errorf("the real mention was lost: %d", n)
	}
	if n := countRows(t, db, `SELECT branded FROM prompt WHERE id = ?`, category); n != 0 {
		t.Error("the category question is still branded")
	}
	if n := countRows(t, db, `SELECT branded FROM prompt WHERE id = ?`, question); n != 1 {
		t.Error("the question about the brand stopped being branded")
	}
	if n := countRows(t, db, `SELECT count(*) FROM citation WHERE chat_id = ? AND url = 'https://www.epubor.com/guide' AND position = 1`, rival); n != 1 {
		t.Errorf("the stored citation was not kept: %d", n)
	}
	if res.Answers != 2 || res.PromptsReclassified != 1 {
		t.Errorf("result = %+v", res)
	}

	if _, ran, err := r.EnsureAnalyzed(ctx); err != nil || ran {
		t.Fatalf("nothing changed, yet ran=%v err=%v", ran, err)
	}

	epubor, err := db.AddCompetitor(ctx, store.Competitor{Name: "epubor.com", Domain: "epubor.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ran, err := r.EnsureAnalyzed(ctx); err != nil || !ran {
		t.Fatalf("a new competitor: ran=%v err=%v", ran, err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM mention WHERE chat_id = ? AND competitor_id = ?`, rival, epubor); n != 1 {
		t.Errorf("the new competitor is not counted in the stored answer: %d", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM citation WHERE chat_id = ? AND source_type = 'competitor'`, rival); n != 1 {
		t.Errorf("the stored citation to the new competitor was not reclassified: %d", n)
	}
}

// TestReadingAgainWaitsForAPassInFlight. It shares the pass's lock, so it
// never rewrites rows a pass is writing; the pass does it as it ends.
func TestReadingAgainWaitsForAPassInFlight(t *testing.T) {
	r, _, _ := seed(t, 1, provider.StubConfig{})
	r.NewAnalyzer = func(c context.Context) (Analyzer, error) { return NewStoreAnalyzer(c, r.db) }
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	if _, _, err := r.EnsureAnalyzed(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("err = %v", err)
	}
}
