// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/promptpack"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/siteinfo"
	"github.com/limelit-co/open/internal/store"
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
	// A brand named by its domain ("kindletopdf.com") is missed every time an
	// answer writes the product's name ("Kindle to PDF"). Before saving one,
	// ask the site what it calls itself, once.
	if domain != "" && r.FormValue("confirmed") != "1" && namedByDomain(name, domain) {
		if suggestion := a.suggestBrandName(r.Context(), domain); suggestion != "" && suggestion != name {
			a.writeBrandSuggestion(w, r, suggestion, domain, r.FormValue("aliases"), fmt.Sprintf(
				"Your site calls itself “%s”, and answers use that name, so that is the name we will look for; %s still counts too. Change it if it is wrong, then continue.",
				suggestion, domain))
			return
		}
	}
	if name == "" && domain != "" {
		name = domain
	}
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
	a.reanalyze(r.Context())
	http.Redirect(w, r, "/setup/competitors", http.StatusSeeOther)
}

// lookupBrand handles the brand step's "Fill in from the site" button.
func (a *App) lookupBrand(w http.ResponseWriter, r *http.Request) {
	domain := store.NormalizeDomain(r.FormValue("domain"))
	name := strings.TrimSpace(r.FormValue("name"))
	if domain == "" {
		a.writeBrandSuggestion(w, r, name, r.FormValue("domain"), r.FormValue("aliases"), "Type the website domain first.")
		return
	}
	if suggestion := a.suggestBrandName(r.Context(), domain); suggestion != "" {
		a.writeBrandSuggestion(w, r, suggestion, domain, r.FormValue("aliases"),
			fmt.Sprintf("Filled in from %s: the site calls itself “%s”.", domain, suggestion))
		return
	}
	a.writeBrandSuggestion(w, r, name, domain, r.FormValue("aliases"),
		fmt.Sprintf("Could not read a name from %s. Type the name answers use for your product.", domain))
}

// namedByDomain reports whether name is only the domain ("kindletopdf.com")
// or its name part typed in lower case ("kindletopdf"). A name written like
// a name ("Acme" for acme.com) is taken as given.
func namedByDomain(name, domain string) bool {
	if name == "" {
		return true
	}
	if strings.Contains(name, ".") && store.NormalizeDomain(name) == domain {
		return true
	}
	return name == strings.ToLower(name) &&
		mentions.NormalizeKey(name) == mentions.NormalizeKey(mentions.DomainLabel(domain))
}

// suggestBrandName is the name the site uses for itself, or "".
func (a *App) suggestBrandName(ctx context.Context, domain string) string {
	if a.siteLookup == nil || a.demo {
		return ""
	}
	info, err := a.siteLookup(ctx, domain)
	if err != nil {
		return ""
	}
	return siteinfo.BrandName(info, domain)
}

// writeBrandSuggestion renders the brand step with a filled-in name and a
// note, marked confirmed so Continue saves it as shown.
func (a *App) writeBrandSuggestion(w http.ResponseWriter, r *http.Request, name, domain, aliases, note string) {
	page := WizardBrandPage{
		WizardBase: a.wizardBase(r, "Set up", 0),
		Form:       BrandForm{Name: name, Domain: domain, Aliases: aliases},
		Confirmed:  true,
	}
	page.Flash = &Flash{Kind: "info", Text: note}
	a.write(w, r, "wizard_brand", page)
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
	a.reanalyze(r.Context())
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

func (a *App) wizardPrompts(w http.ResponseWriter, r *http.Request) { a.wizardPromptsWith(w, r, "") }

// wizardPromptsWith renders the prompts step, with why an import from
// Limelit Cloud did not happen when cloudError is set.
func (a *App) wizardPromptsWith(w http.ResponseWriter, r *http.Request, cloudError string) {
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

	page := WizardPromptsPage{WizardBase: a.wizardBase(r, "Set up", 2), CloudKey: a.hasCloudKey(ctx), CloudError: cloudError}
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
	// The key may reach more than this entry: the Limelit Cloud key is two
	// registrations, one per access mode. Track everything it reaches.
	if _, err := credentials.Track(r.Context(), a.db, a.registry, entry); err != nil {
		a.fail(w, r, err)
		return
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
