// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

// View models. These are the only shapes the templates see, which keeps
// formatting decisions in Go where they can be tested, rather than in
// template expressions where they cannot.

// NavItem is one sidebar link.
type NavItem struct {
	// Section starts a labelled group above this item when set.
	Section string
	Label   string
	Href    string
	Count   string
	Current bool
}

// Flash is a one-off message above the page body.
type Flash struct {
	// Kind is info, ok, warn or error, matching the notice styles.
	Kind string
	Text string
}

// PropertyView is the brand as the chrome shows it.
type PropertyView struct {
	Name   string
	Domain string
}

// Base is what every chrome template needs.
type Base struct {
	Title   string
	Version string
	// Theme is "" so the page follows the viewer's system setting. A stored
	// preference writes "light" or "dark" here.
	Theme    string
	Nav      []NavItem
	Property PropertyView
	Flash    *Flash
	LastRun  string
	// CanRun is false until there is something to run: a prompt, a target,
	// and a provider that exists in this build. The button says why.
	CanRun bool
	// Demo marks a public read-only instance. The chrome shows a banner
	// naming the exact commit it runs, so anyone can check it against the
	// repository: the demo is the open core, not a fork of it.
	Demo bool
	// DemoLive is true when the demo also runs itself on a schedule. The
	// banner says so only then; a demo showing a fixed history must not
	// claim to be measuring.
	DemoLive bool
	// Commit is the short revision and CommitURL its page on GitHub. Empty
	// on a dev build with no VCS stamp.
	Commit    string
	CommitURL string
	// Usage is what this instance has used against its limit, in the top
	// bar: the free Limelit Cloud credits when that key is set, otherwise
	// today's answers against the daily limit. Nil in a demo.
	Usage *UsageView
}

// UsageView is the top bar's usage meter.
type UsageView struct {
	Label       string
	Used, Limit int
	// Width is the meter's fill as a CSS percentage, capped at 100%.
	Width string
	// Detail is the tooltip: what is counted and when it resets.
	Detail string
	// Warn is set at 90% and above.
	Warn bool
}

// StatView is one number on the overview.
type StatView struct {
	Label string
	Value string
	Sub   string
}

// TargetView is one row in a target table.
type TargetView struct {
	ID          int64
	Spec        string
	EngineLabel string
	Access      string
	Chats       int
	// Enabled is false for a paused target: kept, with its history, but
	// skipped by the runner until it is resumed.
	Enabled bool
	// Health is one line on when the target last answered and last failed,
	// derived from its chats. "never run" before the first pass.
	Health string
}

// PromptView is one row in the prompts table.
type PromptView struct {
	ID       int64
	Text     string
	Category string
	Branded  bool
	Chats    int
}

// CompetitorView is one row in the competitors table.
type CompetitorView struct {
	ID       int64
	Name     string
	Domain   string
	Mentions int
}

// CountsView is the row counts the sidebar shows.
type CountsView struct {
	Prompts     int
	Competitors int
	Targets     int
	Chats       int
}

// PromptsPage lists tracked prompts.
type PromptsPage struct {
	Base
	Prompts []PromptView
	// CloudKey offers the import from Limelit Cloud.
	CloudKey bool
}

// CompetitorsPage lists tracked competitors.
type CompetitorsPage struct {
	Base
	Competitors []CompetitorView
	// Suggested are sites the engines cite often that are not tracked.
	Suggested []SuggestedCompetitor
}

// SuggestedCompetitor is one often-cited site offered as a competitor.
type SuggestedCompetitor struct {
	Site    string
	Answers int
}

// TrackOption is one provider a user can click to start tracking an engine.
type TrackOption struct {
	Provider string
	Label    string
	Access   string
	Note     string
	// Available is false for a provider that is documented but not built
	// yet. The button is disabled and Reason says why, which is more use
	// than hiding it and leaving the engine looking unreachable.
	Available bool
	Reason    string
}

// EngineCard is one tracked answer engine and how to reach it.
type EngineCard struct {
	ID      string
	Label   string
	Kind    string
	Targets []TargetView
	// Options are the providers a user can click today.
	Options []TrackOption
	// Pending names the documented providers this build does not implement
	// yet, collapsed to one line. Seven disabled buttons per engine would
	// bury the one that works.
	Pending string
}

// CredentialView is one environment variable a provider needs, and where its
// value is coming from.
type CredentialView struct {
	Name string
	// Status is "not set", "set in the environment", or "saved here". The
	// environment always wins, and saying so stops a user editing a field
	// that cannot take effect.
	Status  string
	FromEnv bool
	Saved   bool
}

// ProviderKeyCard is one provider in the keys section.
type ProviderKeyCard struct {
	Name        string
	Label       string
	Access      string
	EngineList  string
	Note        string
	KeyURL      string
	Credentials []CredentialView
	// AnySaved is true when at least one credential is stored here, which
	// is when Forget has something to do.
	AnySaved  bool
	Available bool
	Reason    string
	// Free marks the Limelit Cloud allowance: no vendor key, so setup shows
	// it as its own option rather than one provider among many.
	Free bool
}

// MCPView is the state of MCP over HTTP: whether a bearer token is in force
// and where it comes from. The token itself appears exactly once, in the
// response to the request that generated it, and never again.
type MCPView struct {
	// Status is "not set", "set in the environment" or "saved here".
	Status  string
	FromEnv bool
	Saved   bool
	// SavedAt is when the stored token was generated, for a user deciding
	// whether to rotate it.
	SavedAt string
	// NewToken is set only on the page that answers a generate or rotate,
	// and Snippet is the client configuration carrying it.
	NewToken string
	// URL is the endpoint as this request reached it.
	URL string
	// TokenEnv names the variable that overrides the stored token.
	TokenEnv string
}

// SettingsPage is targets, keys, limits, the schedule and the MCP token.
type SettingsPage struct {
	Base
	// Brand is what the matcher looks for; BrandNote is the outcome of an
	// edit or a lookup, shown inside the brand card.
	Brand     BrandForm
	BrandNote *Flash
	Engines   []EngineCard
	Providers []ProviderKeyCard
	Targets   []TargetView
	// Tracked and Paused split Targets the way the sidebar count does, so
	// the two never disagree on the page.
	Tracked, Paused int
	EngineList      string
	ProviderNames   string
	RunsPerDay      int
	RunsToday       int
	// KeyResults carries the outcome of a save, test or forget, keyed by
	// provider name, so it renders inside that provider's card rather than
	// as a page-level flash the user has to connect to a field.
	KeyResults map[string]*Flash
	// Schedule is the mode in force: daily, hourly or off.
	Schedule string
	// NextRun says when the next automatic pass is due, "" when off.
	NextRun string
	MCP     MCPView
}

// UpgradePage is the honest boundary with the hosted product, and the form
// that moves an instance across it.
type UpgradePage struct {
	Base
	CloudFeatures []string
	// KeySaved is true when a Limelit Cloud key is saved in Settings (the
	// free-allowance key), which the upload uses when the field is blank.
	KeySaved bool
	// KeyFromEnv is true when LIMELIT_CLOUD_KEY is set, so the form can say
	// the key is already known rather than asking for it again.
	KeyFromEnv bool
	// Prompts, Competitors and Chats are what would move.
	Prompts, Competitors, Chats int
	// Error is the upgrade's own message when the last attempt failed.
	Error string
	// Result is set after a successful upload.
	Result *UpgradeResult
}

// UpgradeResult is what Cloud reported back, shaped for the page.
type UpgradeResult struct {
	Prompts, Competitors, Chats, Mentions, Citations, Skipped int
	WorkspaceURL, MCPURL                                      string
}

// WizardBase is the chrome for the setup flow.
type WizardBase struct {
	Title     string
	Version   string
	Theme     string
	Steps     []string
	StepIndex int
	Flash     *Flash
}

// StepNumber is the 1-based step, for "Step 2 of 4".
func (w WizardBase) StepNumber() int { return w.StepIndex + 1 }

// BrandForm is step one.
type BrandForm struct {
	Name    string
	Domain  string
	Aliases string
}

// CompetitorField is one name and domain pair in step two.
type CompetitorField struct {
	Name   string
	Domain string
}

// CompetitorsForm is step two.
type CompetitorsForm struct {
	Category    string
	Competitors []CompetitorField
}

// WizardBrandPage is step one.
type WizardBrandPage struct {
	WizardBase
	Form BrandForm
	// Confirmed is set once a name was suggested from the site, so the next
	// Continue saves what the user sees instead of suggesting again.
	Confirmed bool
}

// WizardCompetitorsPage is step two.
type WizardCompetitorsPage struct {
	WizardBase
	Form CompetitorsForm
}

// WizardPromptView is one proposed prompt in step three.
type WizardPromptView struct {
	Text     string
	Category string
	Branded  bool
}

// WizardPromptsPage is step three.
type WizardPromptsPage struct {
	WizardBase
	Prompts []WizardPromptView
	// CloudKey offers the import from Limelit Cloud; CloudError says why
	// one did not happen.
	CloudKey   bool
	CloudError string
}

// ProviderOption is one provider a user could connect in step four.
type ProviderOption struct {
	Name       string
	Access     string
	EngineList string
}

// WizardProviderPage is step four.
type WizardProviderPage struct {
	WizardBase
	Providers []ProviderKeyCard
	// AnyAvailable is false when no provider is built into this binary yet,
	// which turns the step into an honest exit rather than a dead form.
	AnyAvailable bool
}
