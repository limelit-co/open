// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

// AnalyzerVersion changes whenever the rules that derive mentions, citations
// or the branded flag change, so answers stored under older rules are read
// again with the rules a new pass uses.
//
//	2: a name that is also a description ("Kindle to PDF") counts only where
//	   it reads as a name, and a plain domain's name part ("KindleToPDF")
//	   counts as the brand (2026-09-30).
const AnalyzerVersion = "2"

// analysisSignatureKey is the setting holding the signature stored answers
// were last analyzed under.
const analysisSignatureKey = "analysis_signature"

// analysisSignature names everything a stored answer's derived rows depend
// on: the rules and the brands they look for. Any change to either means the
// stored rows no longer say what a new pass would.
func analysisSignature(ctx context.Context, db *store.DB) (string, error) {
	property, err := db.Property(ctx)
	if err != nil {
		return "", err
	}
	competitors, err := db.Competitors(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "v%s\nproperty\t%s\t%s\t%s\n", AnalyzerVersion, property.Name, property.Domain, strings.Join(property.Aliases, "\x1f"))
	lines := make([]string, 0, len(competitors))
	for _, c := range competitors {
		lines = append(lines, fmt.Sprintf("competitor\t%d\t%s\t%s", c.ID, c.Name, c.Domain))
	}
	sort.Strings(lines)
	b.WriteString(strings.Join(lines, "\n"))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

// EnsureAnalyzed reads every stored answer again when the rules or the
// tracked brands have changed since it was last read: a competitor added
// today is counted in last month's answers, and a matcher fix corrects the
// history rather than only what comes next.
//
// It takes the same lock as Run, so it never overlaps a pass. When a pass is
// in flight it does nothing and says so; the pass checks again as it ends.
//
// Returns: what changed, whether a re-read happened, and any error.
func (r *Runner) EnsureAnalyzed(ctx context.Context) (store.ReanalyzeResult, bool, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return store.ReanalyzeResult{}, false, ErrAlreadyRunning
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()
	return r.ensureAnalyzed(ctx)
}

// ensureAnalyzed is EnsureAnalyzed for a caller that already holds the lock.
func (r *Runner) ensureAnalyzed(ctx context.Context) (store.ReanalyzeResult, bool, error) {
	if r.NewAnalyzer == nil {
		return store.ReanalyzeResult{}, false, nil
	}
	sig, err := analysisSignature(ctx, r.db)
	if errors.Is(err, store.ErrNotFound) {
		return store.ReanalyzeResult{}, false, nil // nothing set up, nothing stored
	}
	if err != nil {
		return store.ReanalyzeResult{}, false, err
	}
	if stored, err := r.db.Setting(ctx, analysisSignatureKey); err == nil && stored == sig {
		return store.ReanalyzeResult{}, false, nil
	}
	analyzer, err := r.NewAnalyzer(ctx)
	if err != nil {
		return store.ReanalyzeResult{}, false, err
	}
	if analyzer == nil {
		return store.ReanalyzeResult{}, false, nil // no derived rows, as in a pass
	}
	property, err := r.db.Property(ctx)
	if err != nil {
		return store.ReanalyzeResult{}, false, err
	}
	brand := mentions.Brand{Name: property.Name, Aliases: property.Aliases, Domain: property.Domain}
	res, err := r.db.Reanalyze(ctx,
		func(text string, cited []store.Citation) ([]store.Mention, []store.Citation) {
			raw := make([]provider.Citation, 0, len(cited))
			for _, c := range cited {
				raw = append(raw, provider.Citation{URL: c.URL, Title: c.Title, Position: c.Position})
			}
			return analyzer.Analyze(text, raw)
		},
		func(text string) bool { return mentions.IsBranded(text, brand) })
	if err != nil {
		return res, false, err
	}
	if err := r.db.SetSetting(ctx, analysisSignatureKey, sig); err != nil {
		return res, true, err
	}
	if r.log != nil {
		r.log.Info("stored answers read again with the current rules and brands",
			"answers", res.Answers, "mentions_before", res.MentionsBefore, "mentions_after", res.MentionsAfter,
			"prompts_reclassified", res.PromptsReclassified, "analyzer", AnalyzerVersion)
	}
	return res, true, nil
}
