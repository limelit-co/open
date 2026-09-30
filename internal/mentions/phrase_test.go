// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mentions

import (
	"encoding/json"
	"os"
	"testing"
)

// TestPhraseNamesFromRealAnswers runs testdata/phrase_names.json, the case
// file Limelit Cloud's matcher also reads, so both count the same answers.
// "Kindle to PDF" counted in 122 of 200 answers where 44 named the product:
// rivals' product names and the task itself matched the brand.
func TestPhraseNamesFromRealAnswers(t *testing.T) {
	raw, err := os.ReadFile("testdata/phrase_names.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name, Domain, Text, Why string
			Found                   bool
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 20 {
		t.Fatalf("only %d cases", len(file.Cases))
	}
	for _, c := range file.Cases {
		m := NewMatcher([]Brand{{Name: c.Name, Domain: c.Domain}})
		if got := len(m.Find(c.Text)) > 0; got != c.Found {
			t.Errorf("%s (%s): found = %v, want %v\n  %s", c.Name, c.Why, got, c.Found, c.Text)
		}
	}
}

// TestIsBrandedReadsAPhraseNameAsTheCategory. "convert Kindle to PDF." is
// the category, not a question about the brand, so it stays in the headline.
func TestIsBrandedReadsAPhraseNameAsTheCategory(t *testing.T) {
	brand := Brand{Name: "kindletopdf.com", Aliases: []string{"Kindle to PDF"}, Domain: "kindletopdf.com"}
	for text, want := range map[string]bool{
		"convert Kindle to PDF.":         false,
		"One-click export kindle to PDF": false,
		"Best Kindle to PDF converter?":  false,
		"Is Kindle to PDF safe to use?":  true,
		"Is kindletopdf.com any good?":   true,
		"What is KindleToPDF?":           true,
	} {
		if got := IsBranded(text, brand); got != want {
			t.Errorf("IsBranded(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestTheDomainNameCountsWithoutItsDotCom, and only for a plain domain.
func TestTheDomainNameCountsWithoutItsDotCom(t *testing.T) {
	epubor := NewMatcher([]Brand{{Name: "epubor.com", Domain: "epubor.com"}})
	if len(epubor.Find("Epubor Ultimate removes DRM.")) != 1 {
		t.Error("Epubor was not found by its domain's name")
	}
	ms := NewMatcher([]Brand{{Name: "marketplace.microsoft.com", Domain: "marketplace.microsoft.com"}})
	if len(ms.Find("Browse the marketplace for converters.")) != 0 {
		t.Error("the word marketplace matched a subdomain's first label")
	}
}
