// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package target

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/provider/providertest"
)

func TestParseShape(t *testing.T) {
	tests := []struct {
		spec string
		want Target
	}{
		{"chatgpt:openai", Target{Engine: "chatgpt", Provider: "openai"}},
		{"chatgpt:openai:gpt-5.5", Target{Engine: "chatgpt", Provider: "openai", Model: "gpt-5.5"}},
		{"chatgpt:openai:online", Target{Engine: "chatgpt", Provider: "openai", Online: true}},
		{"chatgpt:openai:gpt-5.5:online", Target{Engine: "chatgpt", Provider: "openai", Model: "gpt-5.5", Online: true}},
		{"ai_overview:dataforseo", Target{Engine: "ai_overview", Provider: "dataforseo"}},
		{"  chatgpt : openai  ", Target{Engine: "chatgpt", Provider: "openai"}},
	}
	for _, tc := range tests {
		got, err := Parse(tc.spec)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.spec, got, tc.want)
		}
	}
}

func TestParseThirdSegmentIsOnlineOrModel(t *testing.T) {
	// The grammar is ambiguous here and the disambiguation is positional: a
	// third segment spelled exactly "online" is the flag, anything else is a
	// model pin. Both readings appear in docs/providers.md.
	flag, err := Parse("chatgpt:dataforseo:online")
	if err != nil {
		t.Fatal(err)
	}
	if !flag.Online || flag.Model != "" {
		t.Errorf(`"online" as the third segment parsed as model %q, online=%v`, flag.Model, flag.Online)
	}

	pin, err := Parse("perplexity:perplexity:sonar")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Model != "sonar" || pin.Online {
		t.Errorf("a model pin parsed as model %q, online=%v", pin.Model, pin.Online)
	}
}

func TestParseRejectsWithAReason(t *testing.T) {
	tests := []struct {
		spec       string
		wantReason string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"chatgpt", "no provider"},
		{"chatgpt:", "empty segment"},
		{":openai", "empty segment"},
		{"chatgpt:openai:gpt-5.5:maybe", "can only be \"online\""},
		{"chatgpt:openai:gpt-5.5:online:extra", "at most 4"},
	}
	for _, tc := range tests {
		_, err := Parse(tc.spec)
		if err == nil {
			t.Errorf("Parse(%q) was accepted", tc.spec)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantReason) {
			t.Errorf("Parse(%q) error %q does not mention %q", tc.spec, err, tc.wantReason)
		}
	}
}

func TestStringRoundTrips(t *testing.T) {
	// Targets are stored and compared as text, so a target that does not
	// round-trip would create two rows for one surface.
	for _, spec := range []string{
		"chatgpt:openai",
		"chatgpt:openai:gpt-5.5",
		"chatgpt:openai:online",
		"chatgpt:openai:gpt-5.5:online",
		"ai_overview:dataforseo",
		"bing_copilot:searchapi",
	} {
		parsed, err := Parse(spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec, err)
		}
		if got := parsed.String(); got != spec {
			t.Errorf("String() = %q, want %q", got, spec)
		}
		again, err := Parse(parsed.String())
		if err != nil || again != parsed {
			t.Errorf("re-parsing %q gave %+v, %v", parsed.String(), again, err)
		}
	}
}

func TestValidateAgainstRegistry(t *testing.T) {
	reg := providertest.Registry()
	for _, spec := range []string{
		"chatgpt:openai:gpt-5.5:online",
		"chatgpt:dataforseo:online",
		"claude:anthropic:online",
		"perplexity:perplexity:sonar",
		"gemini:google:online",
		"ai_overview:dataforseo",
		"ai_mode:dataforseo",
		"bing_copilot:searchapi",
	} {
		parsed, err := Parse(spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec, err)
		}
		if err := parsed.Validate(reg); err != nil {
			t.Errorf("Validate(%q): %v", spec, err)
		}
	}
}

func TestValidateNamesTheProblemAndTheWayOut(t *testing.T) {
	reg := providertest.Registry()
	tests := []struct {
		spec  string
		wants []string
	}{
		{"bard:openai", []string{"unknown engine", "chatgpt"}},
		{"chatgpt:nosuchvendor", []string{"unknown provider", "openai"}},
		// An error that only says no is a dead end. Naming the providers that
		// CAN reach the engine turns it into the next thing to type.
		{"ai_overview:openai", []string{"cannot reach ai_overview", "dataforseo"}},
		{"claude:dataforseo", []string{"cannot reach claude", "anthropic"}},
		// A scraped surface has no model to choose, so accepting a pin would
		// let a user believe they had pinned something.
		{"ai_overview:dataforseo:some-model", []string{"scraped surface", "no model"}},
	}
	for _, tc := range tests {
		parsed, err := Parse(tc.spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.spec, err)
		}
		err = parsed.Validate(reg)
		if err == nil {
			t.Errorf("Validate(%q) accepted it", tc.spec)
			continue
		}
		for _, want := range tc.wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Validate(%q) error %q does not mention %q", tc.spec, err, want)
			}
		}
	}
}

func TestValidateWithAnEmptyRegistrySaysSo(t *testing.T) {
	// This is what a pre-release instance sees before any provider is
	// implemented. "no providers are registered" is actionable; "unknown
	// provider openai, known providers are " is not.
	parsed, _ := Parse("chatgpt:openai")
	err := parsed.Validate(provider.NewRegistry())
	if err == nil || !strings.Contains(err.Error(), "no providers are registered") {
		t.Errorf("empty-registry error = %v", err)
	}
}

func TestTwoTargetsOnOneEngineKeepSeparateIdentity(t *testing.T) {
	// The acceptance criterion for this, and the reason access travels with
	// every row: one measures a model API, the other measures what a person
	// sees. They must never collapse into one key.
	reg := providertest.Registry()
	api, err := Parse("chatgpt:openai:gpt-5.5:online")
	if err != nil {
		t.Fatal(err)
	}
	scraped, err := Parse("chatgpt:dataforseo:online")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Validate(reg); err != nil {
		t.Fatal(err)
	}
	if err := scraped.Validate(reg); err != nil {
		t.Fatal(err)
	}

	if api.String() == scraped.String() {
		t.Fatal("two targets on one engine produced the same key")
	}
	if api.Engine != scraped.Engine {
		t.Fatal("the test is not comparing two targets on the same engine")
	}

	apiAccess, _ := api.Access(reg)
	scrapedAccess, _ := scraped.Access(reg)
	if apiAccess != provider.AccessAPI {
		t.Errorf("openai access = %q", apiAccess)
	}
	if scrapedAccess != provider.AccessScraped {
		t.Errorf("dataforseo access = %q", scrapedAccess)
	}
}

func TestEffectiveOnline(t *testing.T) {
	// A scraped surface is always online whatever the spec says, but the flag
	// as written is preserved so String round-trips.
	bare, _ := Parse("ai_overview:dataforseo")
	if bare.Online {
		t.Error("the literal flag was invented")
	}
	if !bare.EffectiveOnline(provider.AccessScraped) {
		t.Error("a scraped surface reported itself offline")
	}

	api, _ := Parse("chatgpt:openai")
	if api.EffectiveOnline(provider.AccessAPI) {
		t.Error("an api target with no online flag reported itself online")
	}
	withFlag, _ := Parse("chatgpt:openai:online")
	if !withFlag.EffectiveOnline(provider.AccessAPI) {
		t.Error("an api target with the online flag reported itself offline")
	}
}

func TestDefaultModel(t *testing.T) {
	reg := providertest.Registry()
	pinned, _ := Parse("chatgpt:openai:gpt-4.1")
	if got := pinned.DefaultModel(reg); got != "gpt-4.1" {
		t.Errorf("a pin was ignored: %q", got)
	}
	unpinned, _ := Parse("chatgpt:openai")
	if got := unpinned.DefaultModel(reg); got == "" {
		t.Error("an unpinned target resolved to no model at all")
	}
	scraped, _ := Parse("ai_overview:dataforseo")
	if got := scraped.DefaultModel(reg); got != "" {
		t.Errorf("a scraped surface resolved to model %q, want none", got)
	}
}

func TestRequestCarriesEffectiveOnline(t *testing.T) {
	reg := providertest.Registry()
	scraped, _ := Parse("ai_mode:dataforseo")
	req, err := scraped.Request(reg, "best crm for startups", "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !req.Online {
		t.Error("the request for a scraped surface was not marked online")
	}
	if req.Engine != engines.AIMode || req.Prompt != "best crm for startups" {
		t.Errorf("request = %+v", req)
	}
	if req.LocationCountry != "US" || req.LanguageCode != "en" {
		t.Errorf("locale did not reach the request: %+v", req)
	}
}

func TestParseAllReportsEveryProblemAtOnce(t *testing.T) {
	reg := providertest.Registry()
	_, err := ParseAll([]string{"bard:openai", "chatgpt:nosuchvendor", "chatgpt:openai"}, reg)
	if err == nil {
		t.Fatal("ParseAll accepted a list with two bad entries")
	}
	for _, want := range []string{"unknown engine", "unknown provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAll error %q does not mention %q", err, want)
		}
	}
}

func TestParseAllRejectsDuplicates(t *testing.T) {
	// Two identical specs would double every count they contribute to.
	reg := providertest.Registry()
	_, err := ParseAll([]string{"chatgpt:openai", "chatgpt:openai"}, reg)
	if err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Errorf("duplicate targets error = %v", err)
	}
}

func TestParseAllAcceptsAGoodList(t *testing.T) {
	reg := providertest.Registry()
	got, err := ParseAll([]string{
		"chatgpt:openai:gpt-5.5:online",
		"claude:anthropic:online",
		"ai_overview:dataforseo",
	}, reg)
	if err != nil {
		t.Fatalf("ParseAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d targets, want 3", len(got))
	}
}

// TestDocumentedTargetsParse reads the example block out of docs/providers.md
// and runs every line through the parser and the registry.
//
// This is the acceptance criterion for the issue, and it is also a drift
// guard: an example added to the docs that this parser cannot read fails the
// build rather than misleading the first person who copies it.
func TestDocumentedTargetsParse(t *testing.T) {
	raw, err := os.ReadFile("../../docs/providers.md")
	if err != nil {
		t.Fatalf("read docs/providers.md: %v", err)
	}
	specs := documentedTargets(string(raw))
	if len(specs) < 8 {
		t.Fatalf("found %d target examples in docs/providers.md, expected the full example block", len(specs))
	}
	reg := providertest.Registry()
	for _, spec := range specs {
		parsed, err := Parse(spec)
		if err != nil {
			t.Errorf("documented target %q does not parse: %v", spec, err)
			continue
		}
		if err := parsed.Validate(reg); err != nil {
			t.Errorf("documented target %q does not validate: %v", spec, err)
		}
		if got := parsed.String(); got != spec {
			t.Errorf("documented target %q round-trips to %q", spec, got)
		}
	}
}

// documentedTargets pulls target strings out of the markdown: lines inside a
// fenced block that look like a target, with any trailing comment removed.
var targetLine = regexp.MustCompile(`^([a-z_]+:[a-z0-9_.\-:]+)(\s{2,}.*)?$`)

func documentedTargets(md string) []string {
	var (
		out    []string
		inCode bool
		seen   = map[string]bool{}
	)
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if !inCode {
			continue
		}
		m := targetLine.FindStringSubmatch(strings.TrimRight(line, " \t"))
		if m == nil {
			continue
		}
		spec := m[1]
		// The yaml block lists targets under a "- " bullet, which the regexp
		// above already skips, and prose lines never contain a colon pair.
		if seen[spec] {
			continue
		}
		seen[spec] = true
		out = append(out, spec)
	}
	return out
}
