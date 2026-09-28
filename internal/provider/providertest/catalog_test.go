// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package providertest

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/provider"
)

// TestCatalogMatchesDocs reads the provider tables out of docs/providers.md
// and checks that this catalog names the same providers with the same access
// modes.
//
// The point is drift. This catalog is what every offline test validates
// targets against, so if the docs gain a provider and the catalog does not,
// the examples a user copies out of the README would fail against a registry
// the tests called complete.
func TestCatalogMatchesDocs(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/providers.md")
	if err != nil {
		t.Fatalf("read docs/providers.md: %v", err)
	}
	documented := documentedProviders(string(raw))
	if len(documented) < 10 {
		t.Fatalf("found %d providers in docs/providers.md, expected the full table", len(documented))
	}

	catalog := make(map[string]provider.CatalogEntry, len(provider.Catalog()))
	for _, e := range provider.Catalog() {
		catalog[e.Name] = e
	}

	for name, access := range documented {
		entry, ok := catalog[name]
		if !ok {
			t.Errorf("docs/providers.md lists provider %q, the catalog does not", name)
			continue
		}
		if entry.Access != access {
			t.Errorf("provider %q: docs say %q, catalog says %q", name, access, entry.Access)
		}
	}
	for name := range catalog {
		if _, ok := documented[name]; !ok {
			t.Errorf("the catalog has provider %q, docs/providers.md does not list it", name)
		}
	}
}

func TestDocsScannerActuallyFindsProviders(t *testing.T) {
	// A drift guard that silently finds nothing passes forever. This pins the
	// scanner itself: it must read the access-modes table, and it must not
	// read the target-format table's column labels as provider names.
	raw, err := os.ReadFile("../../../docs/providers.md")
	if err != nil {
		t.Fatal(err)
	}
	got := documentedProviders(string(raw))
	if got["openai"] != provider.AccessAPI {
		t.Errorf("scanner read openai as %q, want api", got["openai"])
	}
	if got["dataforseo"] != provider.AccessScraped {
		t.Errorf("scanner read dataforseo as %q, want scraped", got["dataforseo"])
	}
	for _, notAProvider := range []string{"engine", "provider", "model", "online"} {
		if _, ok := got[notAProvider]; ok {
			t.Errorf("scanner read the target-format column label %q as a provider", notAProvider)
		}
	}
}

func TestCatalogIsInternallyConsistent(t *testing.T) {
	for _, e := range provider.Catalog() {
		if !e.Access.Valid() {
			t.Errorf("provider %q: access %q", e.Name, e.Access)
		}
		if len(e.Engines) == 0 {
			t.Errorf("provider %q reaches no engines", e.Name)
		}
		if len(e.Credentials) == 0 {
			t.Errorf("provider %q declares no credentials", e.Name)
		}
		for engine, model := range e.Engines {
			if !engines.Known(engine) {
				t.Errorf("provider %q claims unknown engine %q", e.Name, engine)
			}
			// A scraped surface is whatever the product served, so a default
			// model there would be a fiction the settings screen would show.
			if e.Access == provider.AccessScraped && model != "" {
				t.Errorf("scraped provider %q gives engine %q a default model %q", e.Name, engine, model)
			}
			if e.Access == provider.AccessAPI && model == "" {
				t.Errorf("api provider %q gives engine %q no default model", e.Name, engine)
			}
		}
	}
}

// TestEveryProviderSaysWhereToGetAKey is the reason the catalog exists at
// all. A settings screen that asks for a key and leaves the user to find it
// loses them at the step that decides whether the product ever runs.
func TestEveryProviderSaysWhereToGetAKey(t *testing.T) {
	for _, e := range provider.Catalog() {
		if e.Label == "" {
			t.Errorf("provider %q has no vendor label", e.Name)
		}
		if e.Note == "" {
			t.Errorf("provider %q has no note saying why a user would pick it", e.Name)
		}
		if !strings.HasPrefix(e.KeyURL, "https://") {
			t.Errorf("provider %q has key URL %q, want an https link", e.Name, e.KeyURL)
		}
	}
}

// TestCatalogEngineIDsMatchTheEngineRegistry pins the constants the catalog
// declares locally to avoid an import cycle. A rename in package engines
// that missed them would silently make every provider unreachable.
func TestCatalogEngineIDsMatchTheEngineRegistry(t *testing.T) {
	for id, name := range map[string]string{
		provider.ChatGPTEngine:     engines.ChatGPT,
		provider.ClaudeEngine:      engines.Claude,
		provider.PerplexityEngine:  engines.Perplexity,
		provider.GeminiEngine:      engines.Gemini,
		provider.AIOverviewEngine:  engines.AIOverview,
		provider.AIModeEngine:      engines.AIMode,
		provider.BingCopilotEngine: engines.BingCopilot,
	} {
		if id != name {
			t.Errorf("catalog engine id %q does not match the registry's %q", id, name)
		}
	}
}

func TestCatalogForOrdersTheRecommendationFirst(t *testing.T) {
	// OpenRouter reaches four engines with one key, so it is the answer to
	// "what do I connect first" and has to come first in the list a user
	// picks from.
	got := provider.CatalogFor(engines.ChatGPT)
	if len(got) == 0 {
		t.Fatal("no provider reaches chatgpt")
	}
	if got[0].Name != "openrouter" {
		t.Errorf("first provider for chatgpt is %q, want openrouter", got[0].Name)
	}
}

func TestEveryEngineIsReachable(t *testing.T) {
	// An engine nobody can reach is a dead entry in the registry and a dead
	// column in the dashboard.
	reg := Registry()
	for _, e := range engines.All() {
		if got := reg.ProvidersFor(e.ID); len(got) == 0 {
			t.Errorf("no documented provider reaches engine %q", e.ID)
		}
	}
}

func TestRegistryBuildsEveryProvider(t *testing.T) {
	reg := Registry()
	for _, name := range reg.Names() {
		p, err := reg.New(name, provider.StaticCredentials(nil))
		if err != nil {
			t.Errorf("New(%q): %v", name, err)
			continue
		}
		if !p.Access().Valid() {
			t.Errorf("provider %q built with access %q", name, p.Access())
		}
	}
}

// documentedProviders pulls provider names and access modes out of the two
// markdown sections that carry them.
//
// It is section-aware on purpose: the target-format table at the top of the
// document has `engine`, `provider`, `model` and `online` in its first
// column, and a naive scan reads those as provider names.
func documentedProviders(md string) map[string]provider.Access {
	out := map[string]provider.Access{}
	backticked := regexp.MustCompile("`([a-z]+)`")

	section := ""
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			section = strings.ToLower(strings.TrimLeft(trimmed, "# "))
			continue
		}
		inAccessModes := section == "access modes"
		inProviderList := strings.HasPrefix(section, "api") || strings.HasPrefix(section, "scraped")
		if !inAccessModes && !inProviderList {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		first := strings.TrimSpace(cells[0])

		// The access-modes table: | `api` | ... | ... | `openai`, `anthropic` |
		if inAccessModes && (first == "`api`" || first == "`scraped`") {
			access := provider.Access(strings.Trim(first, "`"))
			for _, m := range backticked.FindAllStringSubmatch(cells[len(cells)-1], -1) {
				out[m[1]] = access
			}
			continue
		}

		// The per-provider tables under "### API" and "### Scraped ...":
		// | `openai` | `chatgpt` | notes |
		if inProviderList {
			if m := backticked.FindStringSubmatch(first); m != nil && first == "`"+m[1]+"`" {
				if _, known := out[m[1]]; !known {
					// Access comes from the access-modes table, which appears
					// first. Landing here means the two disagree, and the
					// empty access makes the comparison above fail loudly.
					out[m[1]] = ""
				}
			}
		}
	}
	return out
}
