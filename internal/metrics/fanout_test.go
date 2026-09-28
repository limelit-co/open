// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package metrics

import (
	"context"
	"testing"

	"github.com/limelit-co/open/internal/store"
)

// TestAnswerCountsSurviveMultipleOwnMentions. Property.Names() returns several
// variants, so "Acme (acme.com) does X" is two own-mentions in one answer. A
// LEFT JOIN onto mention fans that answer out into two rows, and a COUNT(*)
// over it counts mentions where it means answers.
func TestAnswerCountsSurviveMultipleOwnMentions(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.RecordChat(context.Background(), store.ChatRecord{
		EvaluationID: f.evalID, PromptID: f.prompts["discovery"], TargetID: f.target,
		Status: store.ChatOK, Text: "Acme (acme.com) leads.",
		Mentions: []store.Mention{
			{BrandName: "Acme", BrandKey: "acme"},
			{BrandName: "acme.com", BrandKey: "acme.com"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ov, err := f.svc.Overview(ctx, Window{})
	if err != nil {
		t.Fatal(err)
	}
	if ov.Answers != 1 {
		t.Fatalf("overview answers = %d, want 1", ov.Answers)
	}

	// The invariant: the categories have to add up to the headline.
	var sum int
	for _, c := range ov.Categories {
		sum += c.Answers
	}
	if sum != ov.Answers {
		t.Errorf("categories sum to %d answers, headline says %d", sum, ov.Answers)
	}
	for _, c := range ov.Categories {
		if c.Visibility > 100 {
			t.Errorf("category %s visibility = %v, impossible", c.Category, c.Visibility)
		}
		if c.Category == "discovery" && (c.Answers != 1 || c.Visibility != 100) {
			t.Errorf("discovery = %+v, want 1 answer at 100%%", c)
		}
	}

	pts, err := f.svc.Series(ctx, Window{Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Answers != 1 || pts[0].Visibility != 100 {
		t.Errorf("series = %+v, want one point, 1 answer, 100%%", pts)
	}

	m, err := f.svc.Matrix(ctx, Window{})
	if err != nil {
		t.Fatal(err)
	}
	cell := m.Rows[0].Cells[f.target]
	if cell.Answers != 1 || cell.Visibility != 100 {
		t.Errorf("matrix cell = %+v, want 1 answer at 100%%", cell)
	}
}
