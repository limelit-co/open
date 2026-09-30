// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package promptpack

import (
	"strings"
	"testing"
)

func TestBuildIsDeterministic(t *testing.T) {
	in := Input{Brand: "Acme", Category: "CRM", Competitors: []string{"Globex", "Initech"}}
	first, second := Build(in), Build(in)
	if len(first) != len(second) {
		t.Fatalf("two builds returned %d and %d prompts", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("prompt %d differs between builds: %q then %q", i, first[i].Text, second[i].Text)
		}
	}
}

func TestBuildCoversEveryShapeOfIntent(t *testing.T) {
	// A pack of twelve rewordings of "best X" measures one thing twelve
	// times. Each category has to be present.
	got := Build(Input{Brand: "Acme", Category: "CRM", Competitors: []string{"Globex"}})
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.Category] = true
	}
	for _, want := range []string{CategoryDiscovery, CategoryUseCase, CategoryComparison, CategoryBrand} {
		if !seen[want] {
			t.Errorf("no prompt in category %q", want)
		}
	}
	if len(got) < 10 || len(got) > 16 {
		t.Errorf("pack has %d prompts, want a starter set of roughly a dozen", len(got))
	}
}

func TestBrandedPromptsAreTagged(t *testing.T) {
	// Branded prompts are excluded from the headline number, so mistagging
	// one either inflates visibility or hides a real answer.
	for _, p := range Build(Input{Brand: "Acme", Category: "CRM", Competitors: []string{"Globex"}}) {
		mentionsBrand := strings.Contains(strings.ToLower(p.Text), "acme")
		if mentionsBrand != p.Branded {
			t.Errorf("prompt %q: branded=%v, but it %s the brand", p.Text, p.Branded, map[bool]string{true: "names", false: "does not name"}[mentionsBrand])
		}
	}
}

func TestDiscoveryPromptsNeverNameTheBrand(t *testing.T) {
	// Discovery is the only shape that measures whether an engine reaches for
	// you unprompted, so a brand name in one would defeat its purpose.
	for _, p := range Build(Input{Brand: "Acme", Category: "CRM", Competitors: []string{"Globex"}}) {
		if p.Category == CategoryDiscovery && strings.Contains(strings.ToLower(p.Text), "acme") {
			t.Errorf("discovery prompt names the brand: %q", p.Text)
		}
	}
}

func TestArticleAgreesWithTheCategory(t *testing.T) {
	// "a AI visibility tool" tells a user the list was written by a template.
	cases := map[string]string{
		"AI visibility tracking": "an AI visibility tracking tool",
		"CRM":                    "a CRM tool",
		"email marketing":        "an email marketing tool",
		"UX research":            "a UX research tool",
		"European payments":      "a European payments tool",
		"observability":          "an observability tool",
	}
	for category, want := range cases {
		var found bool
		for _, p := range Build(Input{Brand: "Acme", Category: category}) {
			if strings.Contains(p.Text, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("category %q: no prompt reads %q", category, want)
		}
	}
}

func TestCompetitorPromptsAreCapped(t *testing.T) {
	// The pack is a starter set, not a bill: five competitors must not mean
	// five more prompts on day one.
	got := Build(Input{Brand: "Acme", Category: "CRM", Competitors: []string{"A Co", "B Co", "C Co", "D Co", "E Co"}})
	var alternatives int
	for _, p := range got {
		if strings.HasSuffix(p.Text, "alternatives") {
			alternatives++
		}
	}
	if alternatives > 3 {
		t.Errorf("%d alternatives prompts, want at most 3", alternatives)
	}
}

func TestBuildWithoutCategoryReturnsNothing(t *testing.T) {
	if got := Build(Input{Brand: "Acme"}); len(got) != 0 {
		t.Errorf("Build with no category returned %d prompts", len(got))
	}
}

func TestBuildWithoutBrandOrCompetitors(t *testing.T) {
	got := Build(Input{Category: "CRM"})
	if len(got) == 0 {
		t.Fatal("a category alone produced no prompts")
	}
	for _, p := range got {
		if p.Branded {
			t.Errorf("prompt %q is branded with no brand configured", p.Text)
		}
	}
}

// TestTheCategoryReadsAsASentenceWhateverItsShape. The wizard pasted
// "Converting kindle books to pdf" into "What are the best ___ tools?" and
// every discovery prompt read as template output.
func TestTheCategoryReadsAsASentenceWhateverItsShape(t *testing.T) {
	for category, want := range map[string][]string{
		"Converting kindle books to pdf": {
			"What are the best tools for converting kindle books to pdf?",
			"How do I choose a tool for converting kindle books to pdf?",
			"Is there a free or open source tool for converting kindle books to pdf?",
		},
		"CRM for startups": {
			"What are the best CRM tools for startups?",
			"How do I choose a CRM tool for startups?",
		},
		"PDF converter tool": {"What are the best PDF converter tools?"},
		"AI visibility tracking software": {
			"What are the best AI visibility tracking tools?",
			"How do I choose an AI visibility tracking tool?",
		},
		"Kindle to PDF": {"Which Kindle to PDF tool should I use?"},
	} {
		texts := map[string]bool{}
		for _, p := range Build(Input{Brand: "Acme", Category: category}) {
			texts[p.Text] = true
		}
		for _, w := range want {
			if !texts[w] {
				t.Errorf("category %q: no prompt reads %q (got %v)", category, w, texts)
			}
		}
	}
}
