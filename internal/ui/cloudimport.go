// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

// Importing from Limelit Cloud: an owner who already tracks this brand there
// gets the same prompts and competitors here in one click, instead of a
// template pack. Cloud's prompts are written from the product's own features,
// which is often the difference between a brand that never shows up and one
// that does. The Cloud brand name joins the brand's names, so answers that
// say "Kindle to PDF" count for kindletopdf.com.
//
// It only imports when the Cloud account tracks the same domain, adds and
// never removes, and reads the stored answers again afterwards so the new
// competitors count in them.

// cloudImport is what one import added, and what a pass now asks.
type cloudImport struct {
	Prompts, Competitors int
	Alias                string
	// PassAnswers is active prompts times enabled engines after the import,
	// and Ceiling the daily limit a pass is refused above.
	PassAnswers, Ceiling int
}

// errNoCloudKey means there is no Limelit Cloud key to import with.
var errNoCloudKey = errors.New("save a free Limelit Cloud key first, in Settings or with limelit login")

// hasCloudKey reports whether a Limelit Cloud key is set or saved.
func (a *App) hasCloudKey(ctx context.Context) bool {
	return a.keys != nil && a.credentials(ctx)(provider.LimelitKeyEnv) != ""
}

// importCloudSetup copies the Cloud account's brand name, prompts and
// competitors into this instance.
func (a *App) importCloudSetup(ctx context.Context) (cloudImport, error) {
	var res cloudImport
	key := a.credentials(ctx)(provider.LimelitKeyEnv)
	if key == "" {
		return res, errNoCloudKey
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	setup, err := provider.FetchLimelitSetup(fetchCtx, key)
	cancel()
	if errors.Is(err, provider.ErrAuth) {
		return res, errors.New("Limelit Cloud did not accept the saved key; get a new one at " + provider.LimelitKeyPage)
	}
	if err != nil {
		return res, fmt.Errorf("could not read your Limelit Cloud setup: %w", err)
	}
	prop, err := a.db.Property(ctx)
	if err != nil {
		return res, err
	}
	if store.NormalizeDomain(setup.Brand.Domain) != store.NormalizeDomain(prop.Domain) {
		return res, fmt.Errorf("that Limelit Cloud key belongs to %s (%s), not %s, so nothing was imported",
			setup.Brand.Name, setup.Brand.Domain, prop.Domain)
	}

	if name := strings.TrimSpace(setup.Brand.Name); name != "" && !strings.EqualFold(name, prop.Name) && !containsFold(prop.Aliases, name) {
		prop.Aliases = append(prop.Aliases, name)
		if err := a.db.SaveProperty(ctx, prop); err != nil {
			return res, err
		}
		res.Alias = name
	}
	brand := brandOf(prop)

	existing, err := a.db.Prompts(ctx, true)
	if err != nil {
		return res, err
	}
	have := map[string]bool{}
	for _, p := range existing {
		have[promptKey(p.Text)] = true
	}
	for _, p := range setup.Prompts {
		text := strings.TrimSpace(p.Text)
		if text == "" || have[promptKey(text)] {
			continue
		}
		have[promptKey(text)] = true
		if _, err := a.db.AddPrompt(ctx, store.Prompt{
			Text: text, Category: p.Category, Branded: mentions.IsBranded(text, brand), Active: true,
		}); err != nil {
			return res, err
		}
		res.Prompts++
	}

	competitors, err := a.db.Competitors(ctx)
	if err != nil {
		return res, err
	}
	tracked := map[string]bool{store.NormalizeDomain(prop.Domain): true}
	for _, c := range competitors {
		tracked[c.Domain] = true
	}
	for _, c := range setup.Competitors {
		domain := store.NormalizeDomain(c.Domain)
		if domain == "" || tracked[domain] {
			continue
		}
		tracked[domain] = true
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = domain
		}
		if _, err := a.db.AddCompetitor(ctx, store.Competitor{Name: name, Domain: domain}); err != nil {
			return res, err
		}
		res.Competitors++
	}
	a.reanalyze(ctx)
	if active, err := a.db.Prompts(ctx, false); err == nil {
		if targets, err := a.db.Targets(ctx, true); err == nil {
			res.PassAnswers = len(active) * len(targets)
		}
	}
	res.Ceiling = a.runsPerDay(ctx)
	return res, nil
}

// promptKey is a prompt's identity for de-duplication: case and spacing
// do not make a different question.
func promptKey(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

// importSentence says what an import did, in one line.
func importSentence(res cloudImport) string {
	if res.Prompts == 0 && res.Competitors == 0 && res.Alias == "" {
		return "Everything your Limelit Cloud account tracks is already here."
	}
	s := fmt.Sprintf("Imported %d %s and %d %s from Limelit Cloud", res.Prompts, plural(res.Prompts, "prompt", "prompts"),
		res.Competitors, plural(res.Competitors, "competitor", "competitors"))
	if res.Alias != "" {
		s += fmt.Sprintf(", and added “%s” as another name for your brand", res.Alias)
	}
	s += "."
	// A big account's prompts on every engine can pass the daily ceiling,
	// and a pass over it is refused whole, so say so now, not at Run.
	if res.PassAnswers > 0 {
		s += fmt.Sprintf(" A pass now asks %d questions.", res.PassAnswers)
		if res.Ceiling > 0 && res.PassAnswers > res.Ceiling {
			s += fmt.Sprintf(" That is over your daily limit of %d, so a pass would be refused: remove some prompts, or raise the limit in Settings.", res.Ceiling)
		}
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// importCloudPrompts handles POST /prompts/import-cloud.
func (a *App) importCloudPrompts(w http.ResponseWriter, r *http.Request) {
	res, err := a.importCloudSetup(r.Context())
	flash := Flash{Kind: "ok", Text: importSentence(res)}
	if err != nil {
		flash = Flash{Kind: "error", Text: err.Error()}
	}
	a.promptsWithFlash(w, r, flash)
}

// importCloudWizard handles POST /setup/prompts/import-cloud: the imported
// prompts replace the template pack, so setup moves on to the engines.
func (a *App) importCloudWizard(w http.ResponseWriter, r *http.Request) {
	if _, err := a.importCloudSetup(r.Context()); err != nil {
		a.wizardPromptsWith(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/setup/provider", http.StatusSeeOther)
}
