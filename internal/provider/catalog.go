// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"sort"
	"strings"
)

// CatalogEntry is what the dashboard needs to offer a provider before anyone
// has typed a key: what it reaches, what credential it wants, and where to go
// and get one.
//
// The catalog is separate from the Registry on purpose. The registry holds
// implementations, so a target for a provider that is not built yet cannot
// validate. The catalog holds the whole documented list, so the settings
// screen can show a user their options and say which are not built rather
// than presenting an empty page and a text box.
type CatalogEntry struct {
	// Name is the provider segment of a target string.
	Name string
	// Label is the vendor's own name, for a heading.
	Label string
	// Access is the mode every target through this provider measures in.
	Access Access
	// Engines maps engine id to default model, empty where a pin is
	// meaningless because a scraped surface has no model to choose.
	Engines map[string]string
	// Credentials are the environment-variable names this provider needs.
	Credentials []string
	// KeyURL is where a user goes to get the credential. Without it the
	// settings screen asks for a key and leaves the user to find it, which
	// is the step most likely to end a first session.
	KeyURL string
	// Note is one line of why a user would pick this provider.
	Note string
}

// catalog is the documented provider list. docs/providers.md describes the
// same set in prose, and a test reads that file to keep the two in step.
var catalog = []CatalogEntry{
	{
		Name: "openrouter", Label: "OpenRouter", Access: AccessAPI,
		Engines: map[string]string{
			ChatGPTEngine: "openai/gpt-5.5", ClaudeEngine: "anthropic/claude-sonnet-5",
			GeminiEngine: "google/gemini-2.5-flash", PerplexityEngine: "perplexity/sonar",
		},
		Credentials: []string{"OPENROUTER_API_KEY"},
		KeyURL:      "https://openrouter.ai/keys",
		Note:        "One key, four engines. The quickest way to a first number.",
	},
	{
		Name: "openai", Label: "OpenAI", Access: AccessAPI,
		Engines:     map[string]string{ChatGPTEngine: OpenAIDefaultModel},
		Credentials: []string{"OPENAI_API_KEY"},
		KeyURL:      "https://platform.openai.com/api-keys",
		Note:        "ChatGPT's model with web search on, billed per token.",
	},
	{
		Name: "anthropic", Label: "Anthropic", Access: AccessAPI,
		Engines:     map[string]string{ClaudeEngine: "claude-sonnet-5"},
		Credentials: []string{"ANTHROPIC_API_KEY"},
		KeyURL:      "https://console.anthropic.com/settings/keys",
		Note:        "Claude with web search on. Claude has no consumer surface to scrape.",
	},
	{
		Name: "perplexity", Label: "Perplexity", Access: AccessAPI,
		Engines:     map[string]string{PerplexityEngine: "sonar"},
		Credentials: []string{"PERPLEXITY_API_KEY"},
		KeyURL:      "https://www.perplexity.ai/account/api/keys",
		Note:        "Sonar models, always grounded in web results.",
	},
	{
		Name: "google", Label: "Google AI Studio", Access: AccessAPI,
		Engines:     map[string]string{GeminiEngine: "gemini-2.5-flash"},
		Credentials: []string{"GOOGLE_API_KEY"},
		KeyURL:      "https://aistudio.google.com/apikey",
		Note:        "Gemini with Google Search grounding.",
	},
	{
		Name: "dataforseo", Label: "DataForSEO", Access: AccessScraped,
		Engines: map[string]string{
			AIOverviewEngine: "", AIModeEngine: "",
			ChatGPTEngine: "", GeminiEngine: "", PerplexityEngine: "",
		},
		Credentials: []string{"DATAFORSEO_LOGIN", "DATAFORSEO_PASSWORD"},
		KeyURL:      "https://app.dataforseo.com/api-access",
		Note:        "The Google AI surfaces, which have no API at all.",
	},
	{
		Name: "searchapi", Label: "SearchApi", Access: AccessScraped,
		Engines:     map[string]string{AIOverviewEngine: "", AIModeEngine: "", BingCopilotEngine: ""},
		Credentials: []string{"SEARCHAPI_KEY"},
		KeyURL:      "https://www.searchapi.io/",
		Note:        "Google AI surfaces plus Bing Copilot.",
	},
	{
		Name: "cloro", Label: "Cloro", Access: AccessScraped,
		Engines:     map[string]string{ChatGPTEngine: "", AIModeEngine: "", PerplexityEngine: "", GeminiEngine: ""},
		Credentials: []string{"CLORO_API_KEY"},
		KeyURL:      "https://cloro.ai/",
		Note:        "Consumer answers as markdown with their sources.",
	},
	{
		Name: "brightdata", Label: "Bright Data", Access: AccessScraped,
		Engines: map[string]string{
			ChatGPTEngine: "", AIModeEngine: "", AIOverviewEngine: "",
			PerplexityEngine: "", GeminiEngine: "", BingCopilotEngine: "",
		},
		Credentials: []string{"BRIGHTDATA_API_TOKEN"},
		KeyURL:      "https://brightdata.com/",
		Note:        "The widest surface coverage of the scrapers.",
	},
	{
		Name: "oxylabs", Label: "Oxylabs", Access: AccessScraped,
		Engines:     map[string]string{ChatGPTEngine: "", AIModeEngine: "", AIOverviewEngine: "", PerplexityEngine: ""},
		Credentials: []string{"OXYLABS_USERNAME", "OXYLABS_PASSWORD"},
		KeyURL:      "https://oxylabs.io/",
		Note:        "Scraped consumer surfaces, priced per request.",
	},
	{
		Name: "olostep", Label: "Olostep", Access: AccessScraped,
		Engines:     map[string]string{ChatGPTEngine: "", AIModeEngine: "", PerplexityEngine: ""},
		Credentials: []string{"OLOSTEP_API_KEY"},
		KeyURL:      "https://www.olostep.com/",
		Note:        "Web data infrastructure, with the chat surfaces parsed.",
	},
	// The free allowance: no vendor key needed. Both entries take the same
	// Limelit Cloud key, and Settings shows them as one card.
	{
		Name: "limelit", Label: "Limelit Cloud free allowance", Access: AccessScraped,
		Engines:     LimelitScrapedEngines,
		Credentials: []string{LimelitKeyEnv},
		KeyURL:      LimelitKeyPage,
		Note: "No vendor keys: sign in at limelit.co with Google, press Create my key, paste it here. " +
			"A monthly allowance covers ChatGPT, Gemini, Google AI Overviews, AI Mode and Perplexity. " +
			"Prompts pass through Limelit Cloud; answers stay here.",
	},
	{
		Name: "limelitapi", Label: "Limelit Cloud free allowance (Perplexity)", Access: AccessAPI,
		Engines:     LimelitAPIEngines,
		Credentials: []string{LimelitKeyEnv},
		KeyURL:      LimelitKeyPage,
		Note:        "The Perplexity half of the free allowance, through Perplexity's API. Same key.",
	},
}

// Engine ids repeated here rather than imported, because package engines
// would then import this package for its own validation and the two would
// form a cycle. The values are pinned by a test in package engines.
const (
	ChatGPTEngine     = "chatgpt"
	ClaudeEngine      = "claude"
	PerplexityEngine  = "perplexity"
	GeminiEngine      = "gemini"
	AIOverviewEngine  = "ai_overview"
	AIModeEngine      = "ai_mode"
	BingCopilotEngine = "bing_copilot"
)

// Catalog returns every documented provider, in recommended order: the one
// key that reaches the most engines first, then the rest of the APIs, then
// the scrapers.
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}

// CatalogEntryFor returns one documented provider.
func CatalogEntryFor(name string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// CatalogFor returns the documented providers that can reach an engine, in
// catalog order so the recommended one is first.
func CatalogFor(engine string) []CatalogEntry {
	var out []CatalogEntry
	for _, e := range catalog {
		if _, ok := e.Engines[engine]; ok {
			out = append(out, e)
		}
	}
	return out
}

// CatalogNames returns every documented provider name, sorted.
func CatalogNames() []string {
	out := make([]string, 0, len(catalog))
	for _, e := range catalog {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// SharedKeyEntries is c and every other catalog entry that takes exactly the
// same credentials. The Limelit Cloud key is two registrations, one per
// access mode, so saving it means tracking what both reach.
func SharedKeyEntries(c CatalogEntry) []CatalogEntry {
	key := strings.Join(c.Credentials, ",")
	var out []CatalogEntry
	for _, other := range Catalog() {
		if strings.Join(other.Credentials, ",") == key {
			out = append(out, other)
		}
	}
	return out
}
