// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"net/http"
	"strings"

	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/promptpack"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/target"
)

// wizardSteps label the progress bar. Four steps, and nothing is spent until
// the last one: a user who abandons setup has cost themselves nothing.
var wizardSteps = []string{"Brand", "Competitors", "Prompts", "Connect an engine"}

const maxWizardCompetitors = 5

func (a *App) wizardBase(r *http.Request, title string, step int) WizardBase {
	return WizardBase{
		Title:     title,
		Version:   a.version,
		Steps:     wizardSteps,
		StepIndex: step,
		Flash:     a.flash(r),
	}
}

func (a *App) wizardBrand(w http.ResponseWriter, r *http.Request) {
	form := BrandForm{}
	// Re-entering the wizard shows what was saved, so a user who came back to
	// fix a typo is not retyping everything.
	if prop, err := a.db.Property(r.Context()); err == nil {
		form = BrandForm{Name: prop.Name, Domain: prop.Domain, Aliases: strings.Join(prop.Aliases, ", ")}
	}
	a.write(w, r, "wizard_brand", WizardBrandPage{
		WizardBase: a.wizardBase(r, "Set up", 0),
		Form:       form,
	})
}

func (a *App) saveBrand(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	domain := store.NormalizeDomain(r.FormValue("domain"))
	if name == "" || domain == "" {
		page := WizardBrandPage{
			WizardBase: a.wizardBase(r, "Set up", 0),
			Form:       BrandForm{Name: name, Domain: r.FormValue("domain"), Aliases: r.FormValue("aliases")},
		}
		page.Flash = &Flash{Kind: "error", Text: "A brand name and a domain are both needed: the domain is how a citation is recognised as yours."}
		a.write(w, r, "wizard_brand", page)
		return
	}
	if err := a.db.SaveProperty(r.Context(), store.Property{
		Name:    name,
		Domain:  domain,
		Aliases: splitList(r.FormValue("aliases")),
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/setup/competitors", http.StatusSeeOther)
}

func (a *App) wizardCompetitors(w http.ResponseWriter, r *http.Request) {
	category, _ := a.db.Setting(r.Context(), settingCategory)
	existing, _ := a.db.Competitors(r.Context())

	fields := make([]CompetitorField, 0, maxWizardCompetitors)
	for _, c := range existing {
		if len(fields) == maxWizardCompetitors {
			break
		}
		fields = append(fields, CompetitorField{Name: c.Name, Domain: c.Domain})
	}
	for len(fields) < maxWizardCompetitors {
		fields = append(fields, CompetitorField{})
	}

	a.write(w, r, "wizard_competitors", WizardCompetitorsPage{
		WizardBase: a.wizardBase(r, "Set up", 1),
		Form:       CompetitorsForm{Category: category, Competitors: fields},
	})
}

func (a *App) saveCompetitors(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, err)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	names, domains := r.Form["competitor_name"], r.Form["competitor_domain"]

	if category == "" {
		a.write(w, r, "wizard_competitors", a.competitorsPageWithError(r, category, names, domains,
			"The category phrase is needed: it is what the starter prompts are written from."))
		return
	}
	if err := a.db.SetSetting(r.Context(), settingCategory, category); err != nil {
		a.fail(w, r, err)
		return
	}

	for i := range domains {
		domain := store.NormalizeDomain(domains[i])
		if domain == "" {
			continue
		}
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		if name == "" {
			// A row with a domain and no name is still a competitor. The
			// first label of the domain is a better guess than refusing.
			name = strings.Title(strings.SplitN(domain, ".", 2)[0]) //nolint:staticcheck // ASCII brand names only
		}
		if _, err := a.db.AddCompetitor(r.Context(), store.Competitor{Name: name, Domain: domain}); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/setup/prompts", http.StatusSeeOther)
}

func (a *App) competitorsPageWithError(r *http.Request, category string, names, domains []string, msg string) WizardCompetitorsPage {
	fields := make([]CompetitorField, 0, maxWizardCompetitors)
	for i := 0; i < maxWizardCompetitors; i++ {
		f := CompetitorField{}
		if i < len(names) {
			f.Name = names[i]
		}
		if i < len(domains) {
			f.Domain = domains[i]
		}
		fields = append(fields, f)
	}
	page := WizardCompetitorsPage{
		WizardBase: a.wizardBase(r, "Set up", 1),
		Form:       CompetitorsForm{Category: category, Competitors: fields},
	}
	page.Flash = &Flash{Kind: "error", Text: msg}
	return page
}

func (a *App) wizardPrompts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	prop, err := a.db.Property(ctx)
	if err != nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	category, _ := a.db.Setting(ctx, settingCategory)
	competitors, _ := a.db.Competitors(ctx)

	names := make([]string, 0, len(competitors))
	for _, c := range competitors {
		names = append(names, c.Name)
	}
	built := promptpack.Build(promptpack.Input{Brand: prop.Name, Category: category, Competitors: names})

	page := WizardPromptsPage{WizardBase: a.wizardBase(r, "Set up", 2)}
	for _, p := range built {
		page.Prompts = append(page.Prompts, WizardPromptView{Text: p.Text, Category: p.Category, Branded: p.Branded})
	}
	a.write(w, r, "wizard_prompts", page)
}

func (a *App) savePrompts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()
	prop, err := a.db.Property(ctx)
	if err != nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	brand := brandOf(prop)

	// Unticked boxes are simply absent from the form, so the categories
	// submitted alongside cannot be matched by index. Rebuilding the pack and
	// looking the text up is what keeps a prompt's category correct when a
	// user unticks one in the middle.
	category, _ := a.db.Setting(ctx, settingCategory)
	competitors, _ := a.db.Competitors(ctx)
	compNames := make([]string, 0, len(competitors))
	for _, c := range competitors {
		compNames = append(compNames, c.Name)
	}
	categoryOf := map[string]string{}
	for _, p := range promptpack.Build(promptpack.Input{Brand: prop.Name, Category: category, Competitors: compNames}) {
		categoryOf[p.Text] = p.Category
	}

	add := func(text, cat string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		_, err := a.db.AddPrompt(ctx, store.Prompt{
			Text:     text,
			Category: cat,
			Branded:  mentions.IsBranded(text, brand),
			Active:   true,
		})
		return err
	}

	for _, text := range r.Form["prompt"] {
		if err := add(text, categoryOf[strings.TrimSpace(text)]); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	for _, line := range strings.Split(r.FormValue("extra"), "\n") {
		if err := add(line, ""); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/setup/provider", http.StatusSeeOther)
}

func (a *App) wizardProvider(w http.ResponseWriter, r *http.Request) {
	cards := a.providerCards(r.Context())
	page := WizardProviderPage{
		WizardBase: a.wizardBase(r, "Set up", 3),
		Providers:  cards,
	}
	for _, c := range cards {
		if c.Available {
			page.AnyAvailable = true
			break
		}
	}
	a.write(w, r, "wizard_provider", page)
}

func (a *App) saveProvider(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(r.FormValue("provider"))
	entry, ok := provider.CatalogEntryFor(name)
	if !ok {
		http.Redirect(w, r, "/setup/provider", http.StatusSeeOther)
		return
	}
	if _, err := a.storeCredentials(r, entry); err != nil {
		a.fail(w, r, err)
		return
	}
	// Connecting a provider in the wizard means wanting to track what it
	// reaches. Leaving the user on a finished setup with no target, and a
	// Run button that stays disabled, would be the obvious next complaint.
	if _, built := a.registry.Lookup(name); built {
		for engine := range entry.Engines {
			spec := engine + ":" + name
			if entry.Access == provider.AccessAPI {
				spec += ":online"
			}
			parsed, err := target.Parse(spec)
			if err != nil || parsed.Validate(a.registry) != nil {
				continue
			}
			access, _ := parsed.Access(a.registry)
			if _, err := a.db.AddTarget(r.Context(), store.Target{
				Spec: parsed.String(), Engine: parsed.Engine, Provider: parsed.Provider,
				Model: parsed.Model, Online: parsed.Online, Access: string(access),
			}); err != nil {
				a.fail(w, r, err)
				return
			}
		}
	}
	a.finish(w, r)
}

func (a *App) finishSetup(w http.ResponseWriter, r *http.Request) { a.finish(w, r) }

func (a *App) finish(w http.ResponseWriter, r *http.Request) {
	counts, err := a.db.Counts(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// Two endings, because promising a Run button that is disabled would be
	// the first thing this product got wrong.
	if counts.Prompts > 0 && counts.Targets > 0 && len(a.registry.Names()) > 0 {
		http.Redirect(w, r, "/overview?flash=setup-ready", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/overview?flash=setup-done", http.StatusSeeOther)
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
