// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/mcpserver"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/target"
)

// Credential status strings. The environment always wins over a stored value,
// and saying which one is in force stops a user editing a field that cannot
// take effect.
const (
	credNotSet = "not set"
	credEnv    = "set in the environment"
	credSaved  = "saved here"
)

// buildSettings assembles the settings page: what is tracked, what could be,
// and where each key comes from.
func (a *App) buildSettings(r *http.Request, base Base) (SettingsPage, error) {
	ctx := r.Context()
	var brand BrandForm
	if prop, err := a.db.Property(ctx); err == nil {
		brand = BrandForm{Name: prop.Name, Domain: prop.Domain, Aliases: strings.Join(prop.Aliases, ", ")}
	}
	stored, err := a.db.Targets(ctx, false)
	if err != nil {
		return SettingsPage{}, err
	}
	health, err := a.db.TargetHealths(ctx)
	if err != nil {
		return SettingsPage{}, err
	}
	today, err := a.db.RunsToday(ctx)
	if err != nil {
		return SettingsPage{}, err
	}
	counts, err := a.db.Counts(ctx)
	if err != nil {
		return SettingsPage{}, err
	}

	byEngine := map[string][]TargetView{}
	var flat []TargetView
	for _, t := range stored {
		h := health[t.ID]
		if a.demo {
			// A vendor's error text is the operator's business: it can name
			// an account state or quote a request. The demo keeps the timing
			// and drops the message.
			h.LastError = ""
		}
		v := TargetView{ID: t.ID, Spec: t.Spec, Access: t.Access, Enabled: t.Enabled, Health: healthLine(h)}
		if e, ok := engines.Lookup(t.Engine); ok {
			v.EngineLabel = e.Label
		}
		byEngine[t.Engine] = append(byEngine[t.Engine], v)
		flat = append(flat, v)
	}
	var tracked, paused int
	for _, v := range flat {
		if v.Enabled {
			tracked++
		} else {
			paused++
		}
	}

	mode := a.ScheduleMode(ctx)
	page := SettingsPage{
		Brand:         brand,
		Base:          base,
		Targets:       flat,
		Tracked:       tracked,
		Paused:        paused,
		EngineList:    strings.Join(engines.IDs(), ", "),
		ProviderNames: strings.Join(a.registry.Names(), ", "),
		RunsPerDay:    a.runsPerDay(ctx),
		RunsToday:     today,
		Schedule:      mode,
		NextRun:       nextRunLine(mode, counts.LastChatAt, time.Now().UTC()),
	}
	if !a.demo {
		page.MCP = a.mcpView(r)
	}

	for _, e := range engines.All() {
		card := EngineCard{ID: e.ID, Label: e.Label, Targets: byEngine[e.ID]}
		switch e.Kind {
		case engines.KindChat:
			card.Kind = "chat engine"
		default:
			card.Kind = "search surface"
		}
		var pending []string
		for _, c := range provider.CatalogFor(e.ID) {
			if _, built := a.registry.Lookup(c.Name); !built {
				pending = append(pending, c.Label)
				continue
			}
			card.Options = append(card.Options, TrackOption{
				Provider:  c.Name,
				Label:     c.Label,
				Access:    string(c.Access),
				Note:      c.Note,
				Available: true,
			})
		}
		card.Pending = strings.Join(pending, ", ")
		page.Engines = append(page.Engines, card)
	}

	page.Providers = a.providerCards(ctx)
	return page, nil
}

// providerCards lists every documented provider with where to get its key and
// where its current value is coming from.
func (a *App) providerCards(ctx context.Context) []ProviderKeyCard {
	var out []ProviderKeyCard
	// Entries that take the same key are one card: the Limelit Cloud key
	// enables two registrations, and two cards for one key would let Forget
	// on one silently disconnect the other.
	byKey := map[string]int{}
	for _, c := range provider.Catalog() {
		key := strings.Join(c.Credentials, ",")
		labels := make([]string, 0, len(c.Engines))
		for _, id := range sharedKeyEngines(c) {
			if e, ok := engines.Lookup(id); ok {
				labels = append(labels, e.Label)
			}
		}
		sort.Strings(labels)
		if _, seen := byKey[key]; seen {
			continue
		}
		byKey[key] = len(out)

		card := ProviderKeyCard{
			Name:       c.Name,
			Label:      c.Label,
			Access:     string(c.Access),
			EngineList: strings.Join(labels, ", "),
			Note:       c.Note,
			KeyURL:     c.KeyURL,
			Free:       c.Name == "limelit",
		}
		if _, built := a.registry.Lookup(c.Name); built {
			card.Available = true
		} else {
			card.Reason = "not built yet"
		}
		for _, name := range c.Credentials {
			view := a.credentialView(ctx, name)
			card.AnySaved = card.AnySaved || view.Saved
			card.Credentials = append(card.Credentials, view)
		}
		out = append(out, card)
	}
	return out
}

// sharedKeyEngines is every engine reached with c's key, across all the
// catalog entries that take exactly the same credentials.
func sharedKeyEngines(c provider.CatalogEntry) []string {
	var out []string
	for _, other := range provider.SharedKeyEntries(c) {
		for id := range other.Engines {
			out = append(out, id)
		}
	}
	return out
}

func (a *App) credentialView(ctx context.Context, name string) CredentialView {
	v := CredentialView{Name: name, Status: credNotSet}
	// Saved is reported even when the environment wins, so a stored value
	// that can no longer take effect can still be forgotten.
	if stored, err := a.db.Setting(ctx, credentialPrefix+name); err == nil && stored != "" {
		v.Saved, v.Status = true, credSaved
	}
	if config.Credential(name) != "" {
		v.FromEnv, v.Status = true, credEnv
	}
	return v
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	base, _, err := a.base(r, "Settings", "settings")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.buildSettings(r, base)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.write(w, r, "settings", page)
}

// settingsWithFlash re-renders settings carrying a message too specific to be
// a flash code, such as a parser error quoted back to the user.
func (a *App) settingsWithFlash(w http.ResponseWriter, r *http.Request, flash Flash) {
	base, _, err := a.base(r, "Settings", "settings")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	base.Flash = &flash
	page, err := a.buildSettings(r, base)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.write(w, r, "settings", page)
}

// settingsWithKeyResult re-renders settings with one provider's outcome shown
// inside that provider's card. A saved key that the vendor then rejects is
// the moment the user is looking at the field, so the answer goes there.
func (a *App) settingsWithKeyResult(w http.ResponseWriter, r *http.Request, name string, result Flash) {
	a.settingsWith(w, r, func(p *SettingsPage) {
		p.KeyResults = map[string]*Flash{name: &result}
	})
}

// settingsWith renders settings after letting the caller adjust the page.
func (a *App) settingsWith(w http.ResponseWriter, r *http.Request, adjust func(*SettingsPage)) {
	base, _, err := a.base(r, "Settings", "settings")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.buildSettings(r, base)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	adjust(&page)
	a.write(w, r, "settings", page)
}

// saveBrandSettings edits the brand's names after setup. Every stored answer
// is read again, so a name answers actually use ("Kindle to PDF") counts in
// last month's answers, not only in the next run's.
func (a *App) saveBrandSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	prop, err := a.db.Property(ctx)
	if err != nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = prop.Name
	}
	if domain := store.NormalizeDomain(r.FormValue("domain")); domain != "" {
		prop.Domain = domain
	}
	prop.Name, prop.Aliases = name, splitList(r.FormValue("aliases"))
	if err := a.db.SaveProperty(ctx, prop); err != nil {
		a.fail(w, r, err)
		return
	}
	a.reanalyze(ctx)
	a.settingsWith(w, r, func(p *SettingsPage) {
		p.BrandNote = &Flash{Kind: "ok", Text: "Saved. Every stored answer was read again with these names."}
	})
}

// lookupBrandSettings fills the brand name from the site, for review.
func (a *App) lookupBrandSettings(w http.ResponseWriter, r *http.Request) {
	domain := store.NormalizeDomain(r.FormValue("domain"))
	suggestion := a.suggestBrandName(r.Context(), domain)
	a.settingsWith(w, r, func(p *SettingsPage) {
		p.Brand.Aliases = r.FormValue("aliases")
		if suggestion == "" {
			p.BrandNote = &Flash{Kind: "warn", Text: "Could not read a name from " + domain + ". Type the name answers use for your product."}
			return
		}
		p.Brand.Name = suggestion
		p.BrandNote = &Flash{Kind: "info", Text: "Your site calls itself “" + suggestion + "”. Save to use it; " + domain + " still counts."}
	})
}

// trackEngine is the click-select path: an engine and a provider, rather than
// a target string a user has to learn the grammar for.
func (a *App) trackEngine(w http.ResponseWriter, r *http.Request) {
	engine := strings.TrimSpace(r.FormValue("engine"))
	name := strings.TrimSpace(r.FormValue("provider"))

	entry, ok := provider.CatalogEntryFor(name)
	if !ok {
		a.settingsWithFlash(w, r, Flash{Kind: "error", Text: "That provider is not one this build knows about."})
		return
	}
	// The online flag is meaningful for an API provider and implied for a
	// scraped one, so the click-select path sets it rather than asking a
	// user to know the difference.
	spec := engine + ":" + name
	if entry.Access == provider.AccessAPI {
		spec += ":online"
	}
	a.addTargetSpec(w, r, spec)
}

// addTarget is the advanced path: a raw target string.
func (a *App) addTarget(w http.ResponseWriter, r *http.Request) {
	a.addTargetSpec(w, r, strings.TrimSpace(r.FormValue("spec")))
}

func (a *App) addTargetSpec(w http.ResponseWriter, r *http.Request, spec string) {
	parsed, err := target.Parse(spec)
	if err == nil {
		err = parsed.Validate(a.registry)
	}
	if err != nil {
		// The parser's message names the way out, so it is shown verbatim
		// rather than replaced with a generic "invalid target".
		a.settingsWithFlash(w, r, Flash{Kind: "error", Text: err.Error()})
		return
	}
	access, _ := parsed.Access(a.registry)
	// AddTarget re-enables a spec that is already stored, so the flash has
	// to say which of the three things happened: a paused target came back,
	// a tracked one was left alone, or a new one was added.
	flash := "target-added"
	if existing, err := a.db.Targets(r.Context(), false); err == nil {
		for _, t := range existing {
			if t.Spec != parsed.String() {
				continue
			}
			if t.Enabled {
				flash = "target-kept"
			} else {
				flash = "target-resumed"
			}
		}
	}
	if _, err := a.db.AddTarget(r.Context(), store.Target{
		Spec: parsed.String(), Engine: parsed.Engine, Provider: parsed.Provider,
		Model: parsed.Model, Online: parsed.Online, Access: string(access),
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash="+flash, http.StatusSeeOther)
}

func (a *App) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := a.db.DeleteTarget(r.Context(), id); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash=target-removed", http.StatusSeeOther)
}

// setTargetEnabled is Pause and Resume. Pausing keeps the target and every
// answer it recorded; only the runner stops asking it.
func (a *App) setTargetEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		err := a.db.SetTargetEnabled(r.Context(), id, enabled)
		if errors.Is(err, store.ErrNotFound) {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		flash := "target-paused"
		if enabled {
			flash = "target-resumed"
		}
		http.Redirect(w, r, "/settings?flash="+flash, http.StatusSeeOther)
	}
}

// healthLine is one sentence on a target's recent record: when it last
// answered, and when and why it last failed if that is more recent.
func healthLine(h store.TargetHealth) string {
	if h.LastOK == "" && h.LastFailed == "" {
		return "never run"
	}
	var parts []string
	if h.LastOK != "" {
		parts = append(parts, "last answer "+shortStamp(h.LastOK))
	} else {
		parts = append(parts, "no answer yet")
	}
	if h.LastFailed != "" && h.LastFailed >= h.LastOK {
		fail := "last failure " + shortStamp(h.LastFailed)
		if msg := strings.TrimSpace(h.LastError); msg != "" {
			fail += ": " + clip(msg, 90)
		}
		parts = append(parts, fail)
	}
	return strings.Join(parts, "; ")
}

// shortStamp renders a store timestamp ("2006-01-02 15:04:05", UTC) as a day
// and time a person can read at a glance.
func shortStamp(stamp string) string {
	t, err := time.Parse("2006-01-02 15:04:05", stamp)
	if err != nil {
		return stamp
	}
	return t.Format("Jan 2, 15:04") + " UTC"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}

// saveKeys stores the credentials for one provider, encrypted.
func (a *App) saveKeys(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(r.FormValue("provider"))
	entry, ok := provider.CatalogEntryFor(name)
	if !ok {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	saved, err := a.storeCredentials(r, entry)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if saved == 0 {
		a.settingsWithKeyResult(w, r, name, Flash{Kind: "warn", Text: "Nothing was saved: the field was empty."})
		return
	}
	// Saved, now proved. A key that the vendor rejects is reported here,
	// under the field, rather than at the next run when nobody is watching.
	// It stays saved either way: the usual cause of a rejection is an
	// account problem the user fixes on the vendor's side, and Forget is one
	// click away if the key itself was wrong.
	if _, built := a.registry.Lookup(name); !built {
		a.settingsWithKeyResult(w, r, name, Flash{Kind: "ok",
			Text: "Saved. " + entry.Label + " is not built into this binary yet, so the key could not be tested."})
		return
	}
	if err := a.proveKey(r.Context(), name); err != nil {
		a.settingsWithKeyResult(w, r, name, Flash{Kind: "error", Text: "Saved, but " + entry.Label + " did not accept the key: " + err.Error()})
		return
	}
	a.settingsWithKeyResult(w, r, name, Flash{Kind: "ok", Text: "Saved. " + entry.Label + " accepted the key."})
}

// forgetKeys removes a provider's stored credentials. An environment value is
// untouched: it is not this page's to remove, and the card says so.
func (a *App) forgetKeys(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("provider"))
	entry, ok := provider.CatalogEntryFor(name)
	if !ok {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	for _, cred := range entry.Credentials {
		if err := a.db.DeleteSetting(r.Context(), credentialPrefix+cred); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	a.settingsWithKeyResult(w, r, name, Flash{Kind: "ok", Text: "Forgotten. " + entry.Label + " has no saved key on this machine."})
}

// proveKey presses the provider's cheapest authenticated call with the
// credentials a run would use. The error is the provider's own typed one.
func (a *App) proveKey(ctx context.Context, name string) error {
	if missing := a.registry.MissingCredentials(name, a.credentials(ctx)); len(missing) > 0 {
		return errors.New(strings.Join(missing, " and ") + " is not set yet.")
	}
	p, err := a.registry.New(name, a.credentials(ctx))
	if err != nil {
		return err
	}
	return p.Test(ctx)
}

// storeCredentials seals and stores whatever fields the form carried for one
// provider, and reports how many landed.
func (a *App) storeCredentials(r *http.Request, entry provider.CatalogEntry) (int, error) {
	var saved int
	for _, cred := range entry.Credentials {
		value := strings.TrimSpace(r.FormValue("cred_" + cred))
		if cred == provider.LimelitKeyEnv {
			value = provider.CleanLimelitKey(value)
		}
		if value == "" {
			continue
		}
		if err := credentials.Save(r.Context(), a.db, a.keys, cred, value); err != nil {
			return saved, err
		}
		saved++
	}
	return saved, nil
}

// credentials reads a credential the way the runner does: the environment
// first, then the encrypted settings store. Shared, so the Test button proves
// the same value a run would use rather than a different one.
func (a *App) credentials(ctx context.Context) provider.CredentialSource {
	return credentials.Source(ctx, a.db, a.keys, a.log)
}

// testKeys presses the provider's own cheapest authenticated call, so a wrong
// key is reported at the moment it is pasted rather than at the next run.
func (a *App) testKeys(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("provider"))
	entry, ok := provider.CatalogEntryFor(name)
	if !ok {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if missing := a.registry.MissingCredentials(name, a.credentials(r.Context())); len(missing) > 0 {
		a.settingsWithKeyResult(w, r, name, Flash{Kind: "error", Text: strings.Join(missing, " and ") + " is not set yet."})
		return
	}
	if err := a.proveKey(r.Context(), name); err != nil {
		a.settingsWithKeyResult(w, r, name, Flash{Kind: "error", Text: entry.Label + " did not accept the key: " + err.Error()})
		return
	}
	a.settingsWithKeyResult(w, r, name, Flash{Kind: "ok", Text: entry.Label + " accepted the key."})
}

func (a *App) saveLimits(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("runs_per_day")))
	if err != nil || n <= 0 {
		a.settingsWithFlash(w, r, Flash{Kind: "error", Text: "The run ceiling has to be a positive number."})
		return
	}
	if err := a.db.SetSetting(r.Context(), settingRunsPerDay, strconv.Itoa(n)); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash=limits-saved", http.StatusSeeOther)
}

// RunsPerDay is the ceiling in force: Settings, else limelit.yaml, else the
// default. Exported for the scheduler.
func (a *App) RunsPerDay(ctx context.Context) int { return a.runsPerDay(ctx) }

func (a *App) runsPerDay(ctx context.Context) int {
	if v, err := a.db.Setting(ctx, settingRunsPerDay); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if a.cfg != nil && a.cfg.Limits.RunsPerDay > 0 {
		return a.cfg.Limits.RunsPerDay
	}
	return config.DefaultRunsPerDay
}

// saveSchedule stores the cadence. The scheduler re-reads it, so the change
// takes effect within a minute and without a restart.
func (a *App) saveSchedule(w http.ResponseWriter, r *http.Request) {
	mode := config.NormalizeSchedule(r.FormValue("schedule"))
	if !config.ValidSchedule(mode) {
		a.settingsWithFlash(w, r, Flash{Kind: "error", Text: "The schedule has to be daily, hourly or off."})
		return
	}
	if err := a.db.SetSetting(r.Context(), settingSchedule, mode); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash=schedule-saved", http.StatusSeeOther)
}

// ScheduleMode is the cadence in force: what Settings stored, else what
// limelit.yaml says, else off. Exported because the scheduler in cmd/limelit
// reads it on every wake, so a change made here is picked up without a
// restart.
func (a *App) ScheduleMode(ctx context.Context) string {
	if v, err := a.db.Setting(ctx, settingSchedule); err == nil && v != "" {
		return config.NormalizeSchedule(v)
	}
	if a.cfg != nil {
		return config.NormalizeSchedule(a.cfg.Schedule)
	}
	return config.ScheduleOff
}

// nextRunLine says when the next automatic pass is due, from the last
// recorded answer plus the interval, which is the same rule the scheduler
// uses. "" when nothing runs on its own.
func nextRunLine(mode, lastChatAt string, now time.Time) string {
	every, ok := config.ScheduleInterval(mode)
	if !ok {
		return ""
	}
	last, err := time.Parse("2006-01-02 15:04:05", lastChatAt)
	if lastChatAt == "" || err != nil {
		return "The first pass runs shortly after the server starts."
	}
	due := last.Add(every)
	if !due.After(now) {
		return "A pass is due now and starts within a minute while the server is running."
	}
	return "Next pass around " + shortStamp(due.UTC().Format("2006-01-02 15:04:05")) + ", while the server is running."
}

// mcpView reports the bearer token's status without the token.
func (a *App) mcpView(r *http.Request) MCPView {
	v := MCPView{Status: credNotSet, URL: mcpURL(r), TokenEnv: mcpserver.TokenEnv}
	if strings.TrimSpace(os.Getenv(mcpserver.TokenEnv)) != "" {
		v.FromEnv, v.Status = true, credEnv
		return v
	}
	ctx := r.Context()
	if sealed, err := a.db.Setting(ctx, settingMCPToken); err == nil && sealed != "" {
		v.Saved, v.Status = true, credSaved
		if at, err := a.db.SettingUpdatedAt(ctx, settingMCPToken); err == nil && at != "" {
			v.SavedAt = shortStamp(at)
		}
	}
	return v
}

// mcpURL is the endpoint as this request reached it, so the snippet a user
// copies points at the address they are already using.
func mcpURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:1515"
	}
	return scheme + "://" + host + "/mcp"
}

// MCPToken is the bearer token MCP over HTTP checks on every request: the
// environment when set, else the token generated in Settings, else "" which
// keeps the endpoint closed. Exported for the HTTP server.
func (a *App) MCPToken() string {
	if v := strings.TrimSpace(os.Getenv(mcpserver.TokenEnv)); v != "" {
		return v
	}
	sealed, err := a.db.Setting(context.Background(), settingMCPToken)
	if err != nil || sealed == "" {
		return ""
	}
	token, err := a.keys.Unseal(sealed)
	if err != nil {
		a.log.Error("the stored MCP token could not be decrypted", "error", err)
		return ""
	}
	return token
}

// rotateMCPToken generates a token, or replaces the one stored. The value is
// shown on the page that answers this request and nowhere else afterwards:
// it is stored sealed, and there is no route that reads it back.
func (a *App) rotateMCPToken(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv(mcpserver.TokenEnv)) != "" {
		a.settingsWithFlash(w, r, Flash{Kind: "warn", Text: "The token is set in the environment (" + mcpserver.TokenEnv + "), so it is changed there, not here."})
		return
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		a.fail(w, r, err)
		return
	}
	token := hex.EncodeToString(raw)
	sealed, err := a.keys.Seal(token)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.db.SetSetting(r.Context(), settingMCPToken, sealed); err != nil {
		a.fail(w, r, err)
		return
	}
	a.settingsWith(w, r, func(p *SettingsPage) {
		p.MCP.NewToken = token
	})
}

// forgetMCPToken closes the HTTP endpoint again.
func (a *App) forgetMCPToken(w http.ResponseWriter, r *http.Request) {
	if err := a.db.DeleteSetting(r.Context(), settingMCPToken); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash=token-forgotten", http.StatusSeeOther)
}
