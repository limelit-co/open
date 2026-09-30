// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/mentions"
	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/siteinfo"
	"github.com/limelit-co/open/internal/store"
)

// Settings keys the dashboard writes.
const (
	settingCategory   = "category"
	settingRunsPerDay = "runs_per_day"
	settingSchedule   = "schedule"
	// settingMCPToken holds the HTTP bearer token, sealed with the same
	// keyring as a provider credential. It is a credential.
	settingMCPToken  = "mcp_token"
	credentialPrefix = credentials.Prefix
)

// cloudFeatures is the hosted-only list. It is the same list as the README's,
// and the upgrade screen is the only place in the product that shows it, so a
// user learns the boundary before they go looking for a missing feature.
var cloudFeatures = []string{
	"Query fan-out capture and rewrite analysis",
	"Prompt generation from your site, personas, competitor suggestion",
	"Discovery of brands you did not list",
	"Sentiment and framing, hallucination guard, correction drafts",
	"Perception audits",
	"Google Search Console and GA4",
	"Agents, sheets and blocks",
	"Portfolios and multi-brand, white-label, digest emails",
}

// App serves the dashboard.
type App struct {
	db       *store.DB
	registry *provider.Registry
	keys     *secrets.Keyring
	runner   *runner.Runner
	metrics  *metrics.Service
	views    *Renderer
	log      *slog.Logger
	version  string
	commit   string
	cfg      *config.Config
	// demo makes every mutating route refuse and hides the credential
	// surface. Set from LIMELIT_DEMO at construction.
	demo bool
	// siteLookup reads what a website calls itself, for the brand step.
	siteLookup func(ctx context.Context, domain string) (siteinfo.Info, error)
}

// New builds the dashboard handler set. run may be nil, which leaves the Run
// button reporting that there is nothing to run it with. commit is the
// revision the binary was built from, or "" when the build does not name one.
func New(db *store.DB, registry *provider.Registry, keys *secrets.Keyring, run *runner.Runner, log *slog.Logger, version, commit string, cfg *config.Config) (*App, error) {
	views, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	userAgent := "LimelitOpen/" + version + " (+https://github.com/limelit-co/open)"
	return &App{db: db, registry: registry, keys: keys, runner: run, metrics: metrics.New(db), views: views, log: log, version: version, commit: commit, cfg: cfg, demo: DemoMode(),
		siteLookup: func(ctx context.Context, domain string) (siteinfo.Info, error) {
			return siteinfo.Lookup(ctx, domain, userAgent)
		},
	}, nil
}

// Routes registers every dashboard route on mux.
func (a *App) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", StaticHandler())

	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /overview", a.overview)
	mux.HandleFunc("GET /prompts", a.prompts)
	mux.HandleFunc("POST /prompts/add", a.readOnly(a.addPrompt))
	mux.HandleFunc("POST /prompts/delete", a.readOnly(a.deletePrompt))
	mux.HandleFunc("POST /prompts/import-cloud", a.readOnly(a.importCloudPrompts))
	mux.HandleFunc("GET /competitors", a.competitors)
	mux.HandleFunc("POST /competitors/add", a.readOnly(a.addCompetitor))
	mux.HandleFunc("POST /competitors/delete", a.readOnly(a.deleteCompetitor))
	mux.HandleFunc("GET /chats", a.answers)
	mux.HandleFunc("GET /chats/{id}", a.answer)
	mux.HandleFunc("GET /citations", a.citations)
	mux.HandleFunc("GET /settings", a.settings)
	mux.HandleFunc("POST /settings/targets/track", a.readOnly(a.trackEngine))
	mux.HandleFunc("POST /settings/targets/add", a.readOnly(a.addTarget))
	mux.HandleFunc("POST /settings/targets/delete", a.readOnly(a.deleteTarget))
	mux.HandleFunc("POST /settings/targets/pause", a.readOnly(a.setTargetEnabled(false)))
	mux.HandleFunc("POST /settings/targets/resume", a.readOnly(a.setTargetEnabled(true)))
	mux.HandleFunc("POST /settings/brand", a.readOnly(a.saveBrandSettings))
	mux.HandleFunc("POST /settings/brand/lookup", a.readOnly(a.lookupBrandSettings))
	mux.HandleFunc("POST /settings/keys", a.readOnly(a.saveKeys))
	mux.HandleFunc("POST /settings/keys/test", a.readOnly(a.testKeys))
	mux.HandleFunc("POST /settings/keys/forget", a.readOnly(a.forgetKeys))
	mux.HandleFunc("POST /settings/limits", a.readOnly(a.saveLimits))
	mux.HandleFunc("POST /settings/schedule", a.readOnly(a.saveSchedule))
	mux.HandleFunc("POST /settings/mcp/rotate", a.readOnly(a.rotateMCPToken))
	mux.HandleFunc("POST /settings/mcp/forget", a.readOnly(a.forgetMCPToken))
	mux.HandleFunc("GET /upgrade", a.upgrade)
	mux.HandleFunc("POST /upgrade", a.readOnly(a.runUpgrade))
	mux.HandleFunc("POST /run", a.readOnly(a.run))

	mux.HandleFunc("GET /setup", a.noSetup(a.wizardBrand))
	mux.HandleFunc("POST /setup/brand", a.readOnly(a.saveBrand))
	mux.HandleFunc("POST /setup/brand/lookup", a.readOnly(a.lookupBrand))
	mux.HandleFunc("GET /setup/competitors", a.noSetup(a.wizardCompetitors))
	mux.HandleFunc("POST /setup/competitors", a.readOnly(a.saveCompetitors))
	mux.HandleFunc("GET /setup/prompts", a.noSetup(a.wizardPrompts))
	mux.HandleFunc("POST /setup/prompts", a.readOnly(a.savePrompts))
	mux.HandleFunc("POST /setup/prompts/import-cloud", a.readOnly(a.importCloudWizard))
	mux.HandleFunc("GET /setup/provider", a.noSetup(a.wizardProvider))
	mux.HandleFunc("POST /setup/provider", a.readOnly(a.saveProvider))
	mux.HandleFunc("POST /setup/finish", a.readOnly(a.finishSetup))
}

// flashes maps a short code to the message it shows. Keeping the copy here
// rather than in the query string means a link cannot inject text into the
// page, and the wording stays in one place.
var flashes = map[string]Flash{
	"setup-done":        {Kind: "ok", Text: "Setup complete. Add a provider key in Settings when you are ready to run."},
	"setup-ready":       {Kind: "ok", Text: "Setup complete. Press Run now to fetch your first answers."},
	"prompt-added":      {Kind: "ok", Text: "Prompt added."},
	"prompt-removed":    {Kind: "ok", Text: "Prompt removed. Answers already recorded are kept."},
	"prompt-empty":      {Kind: "error", Text: "That prompt was empty."},
	"competitor-added":  {Kind: "ok", Text: "Competitor added. New answers will be searched for it."},
	"competitor-nodom":  {Kind: "error", Text: "A competitor needs a domain: it is how the same company is recognised across citations."},
	"competitor-remove": {Kind: "ok", Text: "Competitor removed. Answers already recorded keep their mentions."},
	"target-added":      {Kind: "ok", Text: "Target added."},
	"target-kept":       {Kind: "info", Text: "That target is already tracked."},
	"target-removed":    {Kind: "ok", Text: "Target removed, with its answers."},
	"target-paused":     {Kind: "ok", Text: "Target paused. Its answers are kept and the next run skips it."},
	"target-resumed":    {Kind: "ok", Text: "Target resumed. The next run includes it."},
	"limits-saved":      {Kind: "ok", Text: "Run ceiling saved."},
	"schedule-saved":    {Kind: "ok", Text: "Schedule saved. It takes effect within a minute, no restart needed."},
	"key-saved":         {Kind: "ok", Text: "Key saved on this machine."},
	"token-forgotten":   {Kind: "ok", Text: "Token forgotten. MCP over HTTP refuses every request until a new one is generated."},
	"run-started":       {Kind: "ok", Text: "Running. Answers appear as each engine replies; refresh to see them."},
	"demo-readonly":     {Kind: "info", Text: "This is a read-only demo. Run your own copy to change anything."},
	"run-busy":          {Kind: "warn", Text: "A run is already in progress."},
}

func (a *App) flash(r *http.Request) *Flash {
	code := r.URL.Query().Get("flash")
	if code == "" {
		return nil
	}
	if f, ok := flashes[code]; ok {
		return &f
	}
	// An unknown code is a stale or hand-edited link, not a message.
	return nil
}

// base fills the chrome: the sidebar counts, the property, and whether Run is
// possible at all.
func (a *App) base(r *http.Request, title, current string) (Base, store.Counts, error) {
	ctx := r.Context()
	counts, err := a.db.Counts(ctx)
	if err != nil {
		return Base{}, counts, err
	}
	prop, err := a.db.Property(ctx)
	if err != nil && err != store.ErrNotFound {
		return Base{}, counts, err
	}

	b := Base{
		Title:    title,
		Version:  a.version,
		Property: PropertyView{Name: prop.Name, Domain: prop.Domain},
		Flash:    a.flash(r),
		// Run needs something to ask, somewhere to ask it, and a provider
		// that exists in this build. Enabling the button without all three
		// would produce a spinner and no explanation.
		CanRun: counts.Prompts > 0 && counts.Targets > 0 && len(a.registry.Names()) > 0,
		Demo:   a.demo,
	}
	// In a demo the Run button stays visible and disabled, so a visitor sees
	// that a run is a thing this software does, and the flash says why they
	// cannot press it here.
	if a.demo {
		b.CanRun = false
		b.DemoLive = a.scheduled(ctx)
	}
	b.Commit, b.CommitURL = commitLink(a.commit)
	if counts.LastChatAt != "" {
		b.LastRun = counts.LastChatAt
	}
	b.Nav = []NavItem{
		{Label: "Overview", Href: "/overview", Current: current == "overview"},
		{Label: "Prompts", Href: "/prompts", Count: count(counts.Prompts), Current: current == "prompts"},
		{Label: "Competitors", Href: "/competitors", Count: count(counts.Competitors), Current: current == "competitors"},
		{Section: "Evidence", Label: "Answers", Href: "/chats", Count: count(counts.Chats), Current: current == "chats"},
		{Label: "Citations", Href: "/citations", Current: current == "citations"},
		{Section: "Instance", Label: "Settings", Href: "/settings", Count: count(counts.Targets), Current: current == "settings"},
		{Label: "Limelit Cloud", Href: "/upgrade", Current: current == "upgrade"},
	}
	return b, counts, nil
}

// brandOf turns the stored property into the brand the matcher searches for,
// so a prompt is tagged branded by the same rule that decides whether an
// answer mentions the brand.
func brandOf(p store.Property) mentions.Brand {
	return mentions.Brand{Name: p.Name, Aliases: p.Aliases, Domain: p.Domain}
}

func count(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func (a *App) configured(ctx context.Context) bool {
	_, err := a.db.Property(ctx)
	return err == nil
}

// home sends a fresh instance into the wizard. That redirect is the whole
// first-run experience: there is no empty dashboard to be confused by.
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/overview", http.StatusSeeOther)
}

func (a *App) prompts(w http.ResponseWriter, r *http.Request) { a.promptsPage(w, r, nil) }

// promptsWithFlash renders the prompts page with an outcome, for an action
// whose message carries counts.
func (a *App) promptsWithFlash(w http.ResponseWriter, r *http.Request, flash Flash) {
	a.promptsPage(w, r, &flash)
}

func (a *App) promptsPage(w http.ResponseWriter, r *http.Request, flash *Flash) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	base, _, err := a.base(r, "Prompts", "prompts")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if flash != nil {
		base.Flash = flash
	}
	rows, err := a.db.Prompts(r.Context(), true)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page := PromptsPage{Base: base, CloudKey: !a.demo && a.hasCloudKey(r.Context())}
	for _, p := range rows {
		page.Prompts = append(page.Prompts, PromptView{ID: p.ID, Text: p.Text, Category: p.Category, Branded: p.Branded})
	}
	a.write(w, r, "prompts", page)
}

func (a *App) addPrompt(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		http.Redirect(w, r, "/prompts?flash=prompt-empty", http.StatusSeeOther)
		return
	}
	prop, _ := a.db.Property(r.Context())
	_, err := a.db.AddPrompt(r.Context(), store.Prompt{
		Text:    text,
		Branded: mentions.IsBranded(text, brandOf(prop)),
		Active:  true,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/prompts?flash=prompt-added", http.StatusSeeOther)
}

func (a *App) deletePrompt(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := a.db.DeletePrompt(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/prompts?flash=prompt-removed", http.StatusSeeOther)
}

func (a *App) competitors(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	base, _, err := a.base(r, "Competitors", "competitors")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.db.Competitors(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page := CompetitorsPage{Base: base}
	for _, c := range rows {
		page.Competitors = append(page.Competitors, CompetitorView{ID: c.ID, Name: c.Name, Domain: c.Domain})
	}
	page.Suggested = a.suggestedCompetitors(r.Context(), rows)
	a.write(w, r, "competitors", page)
}

func (a *App) addCompetitor(w http.ResponseWriter, r *http.Request) {
	domain := store.NormalizeDomain(r.FormValue("domain"))
	if domain == "" {
		http.Redirect(w, r, "/competitors?flash=competitor-nodom", http.StatusSeeOther)
		return
	}
	_, err := a.db.AddCompetitor(r.Context(), store.Competitor{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Domain: domain,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.reanalyze(r.Context())
	http.Redirect(w, r, "/competitors?flash=competitor-added", http.StatusSeeOther)
}

func (a *App) deleteCompetitor(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := a.db.DeleteCompetitor(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	a.reanalyze(r.Context())
	http.Redirect(w, r, "/competitors?flash=competitor-remove", http.StatusSeeOther)
}

// suggestedCompetitors are the sites the engines cite most for your prompts
// that you do not track: in a category, the answers' own sources are usually
// the rivals worth watching. Only "other" sites count (not your own, social
// or reference sites), each cited in at least two answers, top five.
// Tracking one is a click and reads the stored answers again.
func (a *App) suggestedCompetitors(ctx context.Context, tracked []store.Competitor) []SuggestedCompetitor {
	sources, err := a.metrics.Sources(ctx, metrics.Window{Days: 90}, 50)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, c := range tracked {
		have[c.Domain] = true
	}
	var out []SuggestedCompetitor
	for _, s := range sources {
		if s.SourceType != "other" || s.Answers < 2 || have[store.NormalizeDomain(s.Site)] {
			continue
		}
		out = append(out, SuggestedCompetitor{Site: s.Site, Answers: s.Answers})
	}
	// The column shown is answers, so that is the order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Answers > out[j].Answers })
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// reanalyze reads the stored answers again after the tracked brands changed,
// so a competitor added today is counted in last month's answers too. A pass
// in flight holds the lock and does it when it ends.
func (a *App) reanalyze(ctx context.Context) {
	if a.runner == nil {
		return
	}
	if _, _, err := a.runner.EnsureAnalyzed(ctx); err != nil && !errors.Is(err, runner.ErrAlreadyRunning) {
		a.log.Error("reading stored answers again", "error", err)
	}
}

// run starts one evaluation and returns immediately. A pass takes as long as
// the slowest engine, which is tens of seconds, so holding the request open
// would look like a hung browser.
func (a *App) run(w http.ResponseWriter, r *http.Request) {
	if a.runner == nil {
		http.Redirect(w, r, "/overview?flash=run-busy", http.StatusSeeOther)
		return
	}
	if a.runner.Running() {
		http.Redirect(w, r, "/overview?flash=run-busy", http.StatusSeeOther)
		return
	}

	opts := runner.Options{RunsPerDay: a.runsPerDay(r.Context())}
	// The run outlives this request: the user closing the tab must not
	// abandon answers that are already being paid for.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := a.runner.Run(ctx, opts)
		if err != nil {
			a.log.Error("run failed", "error", err)
			return
		}
		a.log.Info("run finished", "evaluation", res.EvaluationID,
			"completed", res.Completed, "failed", res.Failed, "took", res.Duration.Round(time.Second))
	}()

	http.Redirect(w, r, "/overview?flash=run-started", http.StatusSeeOther)
}

func (a *App) write(w http.ResponseWriter, r *http.Request, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.views.Render(w, page, data); err != nil {
		// The response is already partly written, so this cannot become a
		// clean 500. Log it loudly instead of pretending it rendered.
		a.log.Error("render failed", "page", page, "error", err)
	}
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("dashboard error", "path", r.URL.Path, "error", err)
	http.Error(w, "Something went wrong. The server log has the detail.", http.StatusInternalServerError)
}

// commitLink turns the build's commit into a short revision and a GitHub URL.
//
// The commit is a VCS stamp of the form <12-hex>[-dirty]. Only a clean stamp
// gets a link: a dirty build is not any commit in the repository, and linking
// it would claim otherwise.
func commitLink(commit string) (string, string) {
	v := strings.TrimSpace(commit)
	if v == "" || v == "dev" {
		return "", ""
	}
	if strings.HasSuffix(v, "-dirty") {
		return v, ""
	}
	return v, "https://github.com/limelit-co/open/commit/" + v
}
