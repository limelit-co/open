// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

// Default returns the registry a running instance uses.
//
// A provider appears here when its implementation lands, not when it is
// documented. An instance that lists a target for a provider that is not yet
// implemented fails target validation at startup with a message naming what
// IS available, which is the honest failure: the alternative is a target that
// validates and then never produces a row.
//
// Catalog() is the plan, and the settings screen reads it so a user can see
// and prepare for what is coming. This function is the truth.
func Default() *Registry {
	reg := NewRegistry()

	reg.Register(Registration{
		Name:        "openai",
		Access:      AccessAPI,
		Engines:     map[string]string{ChatGPTEngine: OpenAIDefaultModel},
		Credentials: []string{"OPENAI_API_KEY"},
		New: func(src CredentialSource) (Provider, error) {
			return NewOpenAI(src("OPENAI_API_KEY"))
		},
	})

	reg.Register(Registration{
		Name:        "anthropic",
		Access:      AccessAPI,
		Engines:     map[string]string{ClaudeEngine: AnthropicDefaultModel},
		Credentials: []string{"ANTHROPIC_API_KEY"},
		New: func(src CredentialSource) (Provider, error) {
			return NewAnthropic(src("ANTHROPIC_API_KEY"))
		},
	})

	reg.Register(Registration{
		Name:        "perplexity",
		Access:      AccessAPI,
		Engines:     map[string]string{PerplexityEngine: PerplexityDefaultModel},
		Credentials: []string{"PERPLEXITY_API_KEY"},
		New: func(src CredentialSource) (Provider, error) {
			return NewPerplexity(src("PERPLEXITY_API_KEY"))
		},
	})

	reg.Register(Registration{
		Name:        "google",
		Access:      AccessAPI,
		Engines:     map[string]string{GeminiEngine: GoogleDefaultModel},
		Credentials: []string{"GOOGLE_API_KEY"},
		New: func(src CredentialSource) (Provider, error) {
			return NewGoogle(src("GOOGLE_API_KEY"))
		},
	})

	// The first scraped provider. It exists because Google AI Overview and AI
	// Mode have no API at all: a scraping vendor is the only way to see them.
	reg.Register(Registration{
		Name:        "dataforseo",
		Access:      AccessScraped,
		Engines:     map[string]string{AIOverviewEngine: "", AIModeEngine: ""},
		Credentials: []string{"DATAFORSEO_LOGIN", "DATAFORSEO_PASSWORD"},
		New: func(src CredentialSource) (Provider, error) {
			return NewDataForSEO(src("DATAFORSEO_LOGIN"), src("DATAFORSEO_PASSWORD"))
		},
	})

	// The only route to Bing Copilot in this build, and a second read on the
	// two Google AI surfaces. Two scrapers disagreeing about the same surface
	// is a signal about the surface, not noise.
	reg.Register(Registration{
		Name:   "searchapi",
		Access: AccessScraped,
		Engines: map[string]string{
			AIOverviewEngine: "", AIModeEngine: "", BingCopilotEngine: "",
		},
		Credentials: []string{"SEARCHAPI_KEY"},
		New: func(src CredentialSource) (Provider, error) {
			return NewSearchApi(src("SEARCHAPI_KEY"))
		},
	})

	// The free allowance: Limelit Cloud answers with Limelit's keys, inside a
	// monthly allowance, for a user with no vendor key. Two registrations,
	// one key, because access is fixed per provider.
	reg.Register(Registration{
		Name:        "limelit",
		Access:      AccessScraped,
		Engines:     LimelitScrapedEngines,
		Credentials: []string{LimelitKeyEnv},
		New: func(src CredentialSource) (Provider, error) {
			return NewLimelit(src(LimelitKeyEnv))
		},
	})
	reg.Register(Registration{
		Name:        "limelitapi",
		Access:      AccessAPI,
		Engines:     LimelitAPIEngines,
		Credentials: []string{LimelitKeyEnv},
		New: func(src CredentialSource) (Provider, error) {
			return NewLimelitAPI(src(LimelitKeyEnv))
		},
	})

	return reg
}
