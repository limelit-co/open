// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package target parses and validates a tracked target: one way of asking one
// engine, written engine:provider[:model][:online].
//
// A target is the unit everything downstream is keyed by. Two targets can name
// the same engine (chatgpt:openai and chatgpt:dataforseo), and they stay
// separate all the way to the dashboard, because one measures a model API with
// web search on and the other measures what a person sees in the product.
// Averaging them would produce a number that describes neither.
package target

import (
	"fmt"
	"sort"
	"strings"

	"github.com/limelit-co/open/internal/engines"
	"github.com/limelit-co/open/internal/provider"
)

// OnlineFlag is the literal final segment that turns web search on.
const OnlineFlag = "online"

// Target is one parsed target string.
type Target struct {
	// Engine is an id from package engines.
	Engine string
	// Provider is a provider name from the registry.
	Provider string
	// Model is the optional pin. Empty means the provider's default.
	Model string
	// Online is the flag as written. Scraped surfaces are always online and
	// ignore it; see EffectiveOnline.
	Online bool
}

// Parse splits a target string. It checks shape only: that the engine id is
// real is a registry question, answered by Validate.
//
// The grammar is ambiguous in one place and the disambiguation is positional:
// in engine:provider:X, X is the online flag when it is literally "online",
// and a model pin otherwise. That is why "online" cannot be a model name, and
// why a provider that ever ships a model called "online" would need a new
// spelling rather than a parser change.
func Parse(spec string) (Target, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Target{}, fmt.Errorf("target is empty, want engine:provider[:model][:%s]", OnlineFlag)
	}
	parts := strings.Split(trimmed, ":")
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			return Target{}, fmt.Errorf("target %q has an empty segment at position %d, want engine:provider[:model][:%s]", spec, i+1, OnlineFlag)
		}
		parts[i] = strings.TrimSpace(p)
	}

	t := Target{}
	switch len(parts) {
	case 1:
		return Target{}, fmt.Errorf("target %q names an engine but no provider, want engine:provider[:model][:%s]", spec, OnlineFlag)
	case 2:
		t.Engine, t.Provider = parts[0], parts[1]
	case 3:
		t.Engine, t.Provider = parts[0], parts[1]
		if parts[2] == OnlineFlag {
			t.Online = true
		} else {
			t.Model = parts[2]
		}
	case 4:
		t.Engine, t.Provider, t.Model = parts[0], parts[1], parts[2]
		if parts[3] != OnlineFlag {
			return Target{}, fmt.Errorf("target %q ends with %q, but the fourth segment can only be %q", spec, parts[3], OnlineFlag)
		}
		t.Online = true
	default:
		return Target{}, fmt.Errorf("target %q has %d segments, want at most 4: engine:provider[:model][:%s]", spec, len(parts), OnlineFlag)
	}
	return t, nil
}

// String rebuilds the target string. Parse(t.String()) == t for every target
// Parse produced, which is what lets a target be stored as text and compared
// as text without a normalisation step that could disagree with itself.
func (t Target) String() string {
	parts := []string{t.Engine, t.Provider}
	if t.Model != "" {
		parts = append(parts, t.Model)
	}
	if t.Online {
		parts = append(parts, OnlineFlag)
	}
	return strings.Join(parts, ":")
}

// Validate checks a parsed target against a registry: the engine exists, the
// provider is registered, and that provider can reach that engine.
//
// It deliberately does not need credentials. A typo in limelit.yaml has to be
// reportable at load time, on a machine that may hold no keys at all, rather
// than surfacing halfway through a scheduled run.
func (t Target) Validate(reg *provider.Registry) error {
	if !engines.Known(t.Engine) {
		return fmt.Errorf("unknown engine %q, known engines are %s", t.Engine, strings.Join(engines.IDs(), ", "))
	}
	p, ok := reg.Lookup(t.Provider)
	if !ok {
		known := reg.Names()
		if len(known) == 0 {
			return fmt.Errorf("unknown provider %q, no providers are registered", t.Provider)
		}
		return fmt.Errorf("unknown provider %q, known providers are %s", t.Provider, strings.Join(known, ", "))
	}
	if _, ok := p.Engines[t.Engine]; !ok {
		alternatives := reg.ProvidersFor(t.Engine)
		if len(alternatives) == 0 {
			return fmt.Errorf("provider %q cannot reach %s, and no registered provider can", t.Provider, t.Engine)
		}
		return fmt.Errorf("provider %q cannot reach %s, try %s", t.Provider, t.Engine, strings.Join(alternatives, " or "))
	}
	if t.Model != "" && p.Access == provider.AccessScraped {
		// A scraped surface is whatever the product served; there is no model
		// to choose. Accepting the pin silently would let a user believe they
		// had pinned something.
		return fmt.Errorf("target %q pins model %q, but %s is a scraped surface with no model to choose", t.String(), t.Model, t.Provider)
	}
	return nil
}

// Access is the mode this target measures in.
func (t Target) Access(reg *provider.Registry) (provider.Access, error) {
	p, ok := reg.Lookup(t.Provider)
	if !ok {
		return "", fmt.Errorf("unknown provider %q", t.Provider)
	}
	return p.Access, nil
}

// EffectiveOnline reports whether the answer will be grounded in web results.
//
// The flag as written is kept verbatim so String round-trips, but a scraped
// surface is always online whatever the spec says, and the runner needs the
// effective value rather than the literal one.
func (t Target) EffectiveOnline(access provider.Access) bool {
	return t.Online || access == provider.AccessScraped
}

// DefaultModel is the model this target will use: its pin, or the provider's
// default for that engine.
func (t Target) DefaultModel(reg *provider.Registry) string {
	if t.Model != "" {
		return t.Model
	}
	p, ok := reg.Lookup(t.Provider)
	if !ok {
		return ""
	}
	return p.Engines[t.Engine]
}

// Request builds the provider request for one prompt.
func (t Target) Request(reg *provider.Registry, prompt, country, language string) (provider.Request, error) {
	access, err := t.Access(reg)
	if err != nil {
		return provider.Request{}, err
	}
	return provider.Request{
		Engine:          t.Engine,
		Model:           t.Model,
		Online:          t.EffectiveOnline(access),
		Prompt:          prompt,
		LocationCountry: country,
		LanguageCode:    language,
	}, nil
}

// ParseAll parses and validates a whole target list, reporting every problem
// at once so a user fixes limelit.yaml in one pass. Duplicates are rejected:
// two identical specs would double every count they contribute to.
func ParseAll(specs []string, reg *provider.Registry) ([]Target, error) {
	var (
		out      []Target
		problems []string
		seen     = make(map[string]bool, len(specs))
	)
	for _, spec := range specs {
		t, err := Parse(spec)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if err := t.Validate(reg); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		key := t.String()
		if seen[key] {
			problems = append(problems, fmt.Sprintf("target %q is listed twice", key))
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("targets: %s", strings.Join(problems, "; "))
	}
	return out, nil
}
