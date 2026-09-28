// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package runner

import (
	"context"

	"github.com/limelit-co/open/internal/citations"
	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

// storeAnalyzer is the open core's analyzer: a deterministic mention search
// and a citation classifier, both built from the brands this instance tracks.
//
// It is built once per pass, from the property and competitors as they are
// when the pass starts. Rebuilding per answer would let a competitor added
// mid-run apply to some answers and not others, which would make the same
// evaluation internally inconsistent.
type storeAnalyzer struct {
	matcher    *mentions.Matcher
	classifier *citations.Classifier
}

// NewStoreAnalyzer reads the tracked brands and returns an analyzer.
func NewStoreAnalyzer(ctx context.Context, db *store.DB) (Analyzer, error) {
	property, err := db.Property(ctx)
	if err != nil {
		return nil, err
	}
	competitors, err := db.Competitors(ctx)
	if err != nil {
		return nil, err
	}

	brands := []mentions.Brand{{
		Name:    property.Name,
		Aliases: property.Aliases,
		Domain:  property.Domain,
	}}
	competitorDomains := make([]string, 0, len(competitors))
	for _, c := range competitors {
		id := c.ID
		brands = append(brands, mentions.Brand{CompetitorID: &id, Name: c.Name, Domain: c.Domain})
		competitorDomains = append(competitorDomains, c.Domain)
	}

	return &storeAnalyzer{
		matcher:    mentions.NewMatcher(brands),
		classifier: citations.NewClassifier([]string{property.Domain}, competitorDomains),
	}, nil
}

// Analyze derives the rows stored alongside one answer.
func (a *storeAnalyzer) Analyze(text string, cited []provider.Citation) ([]store.Mention, []store.Citation) {
	var out []store.Mention
	for _, m := range a.matcher.Find(text) {
		out = append(out, store.Mention{
			CompetitorID: m.CompetitorID,
			BrandKey:     m.BrandKey,
			BrandName:    m.BrandName,
			OffsetStart:  m.Start,
			OffsetEnd:    m.End,
			ListRank:     m.ListRank,
		})
	}

	raw := make([]citations.Raw, 0, len(cited))
	for _, c := range cited {
		raw = append(raw, citations.Raw{URL: c.URL, Title: c.Title, Position: c.Position})
	}
	var cites []store.Citation
	for _, c := range a.classifier.Classify(raw) {
		cites = append(cites, store.Citation{
			URL: c.URL, Host: c.Host, Site: c.Site, Title: c.Title,
			Position: c.Position, SourceType: c.SourceType,
		})
	}
	return out, cites
}
