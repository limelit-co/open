// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mentions

import (
	"os"
	"testing"
)

func id(n int64) *int64 { return &n }

func acmeAndRivals() *Matcher {
	return NewMatcher([]Brand{
		{Name: "Acme", Aliases: []string{"Acme Inc"}, Domain: "acme.com"},
		{CompetitorID: id(1), Name: "Globex", Domain: "globex.com"},
		{CompetitorID: id(2), Name: "Initech", Domain: "initech.com"},
	})
}

func TestNormalizeKeyCollapsesSpellings(t *testing.T) {
	// One brand written three ways is one brand, and this key is what links a
	// mention row back to a competitor.
	for _, in := range []string{"Run:AI", "RunAI", "run ai", "  Run-AI  "} {
		if got := NormalizeKey(in); got != "runai" {
			t.Errorf("NormalizeKey(%q) = %q", in, got)
		}
	}
	if got := NormalizeKey("!!!"); got != "" {
		t.Errorf("NormalizeKey of punctuation = %q", got)
	}
}

func TestNumberedListGivesEachBrandItsRank(t *testing.T) {
	// "Third in a ranked list" is a different fact from "mentioned in
	// passing", and it is the whole reason rank is stored.
	text := "Here are the leaders:\n\n1. Globex is the enterprise option.\n2. Acme is best for small teams.\n3. Initech rounds out the list.\n"
	got := acmeAndRivals().Find(text)
	if len(got) != 3 {
		t.Fatalf("found %d mentions: %+v", len(got), got)
	}
	want := map[string]int{"Globex": 1, "Acme": 2, "Initech": 3}
	for _, m := range got {
		if m.ListRank == nil {
			t.Errorf("%s has no rank", m.BrandName)
			continue
		}
		if *m.ListRank != want[m.BrandName] {
			t.Errorf("%s has rank %d, want %d", m.BrandName, *m.ListRank, want[m.BrandName])
		}
	}
}

func TestBulletedListAlsoRanks(t *testing.T) {
	// Models write their recommendation order into the sequence whether or
	// not they number it.
	text := "Top picks:\n\n- Globex\n- Acme\n- Initech\n"
	for _, m := range acmeAndRivals().Find(text) {
		if m.ListRank == nil {
			t.Fatalf("%s in a bulleted list has no rank", m.BrandName)
		}
	}
}

func TestProseMentionHasNoRank(t *testing.T) {
	text := "Acme and Globex both do this well, though Initech is cheaper."
	got := acmeAndRivals().Find(text)
	if len(got) != 3 {
		t.Fatalf("found %d mentions", len(got))
	}
	for _, m := range got {
		if m.ListRank != nil {
			t.Errorf("%s in prose was given rank %d", m.BrandName, *m.ListRank)
		}
	}
}

// ranksOf maps each brand found in text to its rank, -1 for none. A brand
// found twice keeps its last rank.
func ranksOf(text string) map[string]int {
	out := map[string]int{}
	for _, m := range acmeAndRivals().Find(text) {
		out[m.BrandName] = -1
		if m.ListRank != nil {
			out[m.BrandName] = *m.ListRank
		}
	}
	return out
}

// TestProseAfterAListHasNoRank. The paragraph after a list is prose. Without
// an end to the last item, a brand named there took that item's rank and
// entered average position as if it had been listed.
func TestProseAfterAListHasNoRank(t *testing.T) {
	text := "Top picks:\n\n1. Globex\n2. Initech\n\nAcme is also worth a look."
	got := ranksOf(text)
	want := map[string]int{"Globex": 1, "Initech": 2, "Acme": -1}
	for brand, rank := range want {
		if got[brand] != rank {
			t.Errorf("%s has rank %d, want %d (-1 is none)", brand, got[brand], rank)
		}
	}
}

// TestAnIndentedParagraphStaysInItsItem. A paragraph indented under an item
// is part of that item, as Markdown reads it, so a brand named there has the
// item's rank.
func TestAnIndentedParagraphStaysInItsItem(t *testing.T) {
	text := "1. Globex\n\n   It connects to Acme out of the box.\n2. Initech\n"
	got := ranksOf(text)
	want := map[string]int{"Globex": 1, "Acme": 1, "Initech": 2}
	for brand, rank := range want {
		if got[brand] != rank {
			t.Errorf("%s has rank %d, want %d (-1 is none)", brand, got[brand], rank)
		}
	}
}

// TestNestedBulletsAreNotItems. Models put sub-bullets under a numbered item
// all the time. They belong to that item; counting them as items made the
// second numbered item rank 4.
func TestNestedBulletsAreNotItems(t *testing.T) {
	text := "1. Acme\n   - fast\n   - integrates with Initech\n2. Globex\n"
	got := ranksOf(text)
	want := map[string]int{"Acme": 1, "Initech": 1, "Globex": 2}
	for brand, rank := range want {
		if got[brand] != rank {
			t.Errorf("%s has rank %d, want %d (-1 is none)", brand, got[brand], rank)
		}
	}
}

// TestAnIndentedListAfterProseIsItsOwnList. Only a marker directly under an
// item is nested; after a paragraph of prose, an indented list is a new list.
func TestAnIndentedListAfterProseIsItsOwnList(t *testing.T) {
	text := "Enterprise:\n\n- Globex\n\nFor small teams:\n\n  - Acme\n  - Initech\n"
	got := ranksOf(text)
	want := map[string]int{"Globex": 1, "Acme": 1, "Initech": 2}
	for brand, rank := range want {
		if got[brand] != rank {
			t.Errorf("%s has rank %d, want %d (-1 is none)", brand, got[brand], rank)
		}
	}
}

func TestASecondListRestartsTheRanking(t *testing.T) {
	// Without the restart, a brand the model put first in its second list
	// would be reported as rank 4.
	text := "Enterprise:\n\n1. Globex\n2. Initech\n\nThat is the enterprise end. For small teams the picture is different:\n\n1. Acme\n"
	for _, m := range acmeAndRivals().Find(text) {
		if m.BrandName == "Acme" {
			if m.ListRank == nil || *m.ListRank != 1 {
				t.Errorf("Acme has rank %v, want 1 in its own list", m.ListRank)
			}
		}
	}
}

func TestOffsetsPointAtTheMatch(t *testing.T) {
	// The dashboard highlights exactly these bytes, so an offset that is off
	// by one highlights the wrong word.
	text := "The leader is Globex today."
	got := acmeAndRivals().Find(text)
	if len(got) != 1 {
		t.Fatalf("found %d mentions", len(got))
	}
	if text[got[0].Start:got[0].End] != "Globex" {
		t.Errorf("offsets select %q", text[got[0].Start:got[0].End])
	}
}

func TestAnAbsenceStatementIsStillAMention(t *testing.T) {
	// This one is deliberate and worth reading twice. An answer that says it
	// could not find the brand DOES contain the brand string, and this
	// matcher reports it, because the matcher's only job is where a string
	// occurs. Deciding that "I could not find Acme" is not evidence of
	// visibility requires reading the sentence, which needs a model, and that
	// is a hosted feature. What the open core does instead is never invent a
	// mention: every row here can be checked against the stored text.
	text := "I could not find any information about Acme."
	got := acmeAndRivals().Find(text)
	if len(got) != 1 {
		t.Fatalf("found %d mentions, want the literal occurrence", len(got))
	}
	if text[got[0].Start:got[0].End] != "Acme" {
		t.Errorf("matched %q", text[got[0].Start:got[0].End])
	}
}

func TestDomainInProseCounts(t *testing.T) {
	// An answer that links a brand without naming it has still put that brand
	// in front of the reader.
	text := "See acme.com for pricing."
	got := acmeAndRivals().Find(text)
	if len(got) != 1 || got[0].BrandName != "Acme" {
		t.Fatalf("mentions = %+v", got)
	}
}

func TestPartialWordsAreNotMentions(t *testing.T) {
	// Without a boundary test "Acme" matches inside "Acmeify" and every
	// count in the product drifts up.
	text := "Acmeify and Globexity are unrelated products. So is initechnology."
	if got := acmeAndRivals().Find(text); len(got) != 0 {
		t.Errorf("matched inside longer words: %+v", got)
	}
}

func TestPunctuationIsAValidBoundary(t *testing.T) {
	// A word-character boundary alone would never match a domain, because a
	// dot is not a word character.
	text := "Try Acme, Globex; or acme.com."
	got := acmeAndRivals().Find(text)
	if len(got) != 3 {
		t.Errorf("found %d mentions in punctuated prose: %+v", len(got), got)
	}
}

func TestLongestBrandWins(t *testing.T) {
	// "Acme Analytics" must not also be reported as "Acme", which would make
	// one company look like two.
	m := NewMatcher([]Brand{
		{Name: "Acme", Domain: "acme.com"},
		{CompetitorID: id(1), Name: "Acme Analytics", Domain: "acmeanalytics.com"},
	})
	got := m.Find("Acme Analytics is the leader.")
	if len(got) != 1 {
		t.Fatalf("found %d mentions: %+v", len(got), got)
	}
	if got[0].BrandName != "Acme Analytics" {
		t.Errorf("matched the shorter brand: %q", got[0].BrandName)
	}
}

func TestSameNameDifferentDomain(t *testing.T) {
	// Two real companies share a name often enough that this matters. They
	// are separate brands with separate domains, and each domain must only
	// credit its own.
	m := NewMatcher([]Brand{
		{Name: "Acme", Domain: "acme.com"},
		{CompetitorID: id(1), Name: "Acme Cloud", Domain: "acme.io"},
	})
	got := m.Find("Look at acme.io rather than acme.com.")
	if len(got) != 2 {
		t.Fatalf("found %d mentions: %+v", len(got), got)
	}
	byName := map[string]bool{}
	for _, mention := range got {
		byName[mention.BrandName] = true
	}
	if !byName["Acme"] || !byName["Acme Cloud"] {
		t.Errorf("the two domains did not credit their own brands: %+v", got)
	}
}

func TestEveryOccurrenceIsKept(t *testing.T) {
	// The count is evidence. Collapsing repeats here would throw away how
	// prominent a brand was in one answer.
	text := "Acme leads. Later, Acme again. And Acme once more."
	if got := acmeAndRivals().Find(text); len(got) != 3 {
		t.Errorf("found %d occurrences, want all three", len(got))
	}
}

func TestShortBrandNamesAreNotSearched(t *testing.T) {
	// A two-character term matches inside ordinary words constantly, and a
	// wrong mention is worse than a missing one because it inflates the
	// number the whole product is about. The domain still works.
	m := NewMatcher([]Brand{{Name: "Go", Domain: "golang.org"}})
	if got := m.Find("I am going to the good category."); len(got) != 0 {
		t.Errorf("a two-letter brand matched inside words: %+v", got)
	}
	if got := m.Find("See golang.org."); len(got) != 1 {
		t.Errorf("the domain did not match: %+v", got)
	}
}

func TestBrandWithNothingSearchableIsDropped(t *testing.T) {
	m := NewMatcher([]Brand{{Name: "", Domain: ""}, {Name: "Acme", Domain: "acme.com"}})
	if got := m.Find("Acme leads."); len(got) != 1 {
		t.Errorf("mentions = %+v", got)
	}
}

func TestEmptyInputs(t *testing.T) {
	if got := acmeAndRivals().Find(""); got != nil {
		t.Errorf("an empty answer produced %+v", got)
	}
	if got := NewMatcher(nil).Find("Acme leads."); got != nil {
		t.Errorf("no brands produced %+v", got)
	}
}

func TestFindIsDeterministic(t *testing.T) {
	// Recomputing from a stored answer has to give the same rows, or the
	// audit trail the whole design buys is worthless.
	text := "1. Globex\n2. Acme\n3. Initech\n\nAcme also appears here."
	first := acmeAndRivals().Find(text)
	for i := 0; i < 20; i++ {
		got := acmeAndRivals().Find(text)
		if len(got) != len(first) {
			t.Fatalf("run %d found %d mentions, first found %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j].Start != first[j].Start || got[j].BrandName != first[j].BrandName {
				t.Fatalf("run %d differs at %d: %+v vs %+v", i, j, got[j], first[j])
			}
		}
	}
}

func TestIsBranded(t *testing.T) {
	acme := Brand{Name: "Acme", Aliases: []string{"Acme Inc"}, Domain: "acme.com"}
	for _, text := range []string{"What is Acme?", "is acme.com any good", "Acme Inc vs Globex"} {
		if !IsBranded(text, acme) {
			t.Errorf("IsBranded(%q) = false", text)
		}
	}
	for _, text := range []string{"best CRM tools", "Globex alternatives"} {
		if IsBranded(text, acme) {
			t.Errorf("IsBranded(%q) = true", text)
		}
	}
}

// TestAgainstARealAnswer runs the matcher over a recorded ChatGPT answer, so
// the ranking logic is exercised against prose a model actually wrote rather
// than prose shaped to pass.
func TestAgainstARealAnswer(t *testing.T) {
	raw, err := os.ReadFile("testdata/real_answer.txt")
	if err != nil {
		t.Skipf("no recorded answer: %v", err)
	}
	m := NewMatcher([]Brand{
		{Name: "Limelit", Domain: "limelit.co"},
		{CompetitorID: id(1), Name: "Profound", Domain: "tryprofound.com"},
		{CompetitorID: id(2), Name: "Peec AI", Domain: "peec.ai"},
	})
	got := m.Find(string(raw))
	if len(got) == 0 {
		t.Fatal("no brands found in a real answer about this category")
	}
	var ranked int
	for _, mention := range got {
		if mention.ListRank != nil {
			ranked++
		}
		if string(raw)[mention.Start:mention.End] == "" {
			t.Errorf("mention %+v selects nothing", mention)
		}
	}
	if ranked == 0 {
		t.Error("a listing answer produced no ranked mentions")
	}
	t.Logf("%d mentions, %d of them ranked", len(got), ranked)
}
