// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/engines"
)

func stubRegistration(name string, access Access, engineIDs map[string]string, creds ...string) Registration {
	return Registration{
		Name:        name,
		Access:      access,
		Engines:     engineIDs,
		Credentials: creds,
		New: func(src CredentialSource) (Provider, error) {
			for _, c := range creds {
				if strings.TrimSpace(src(c)) == "" {
					return nil, errors.New(c + " is not set: " + ErrAuth.Error())
				}
			}
			return NewStub(StubConfig{Access: access, Engines: engineIDs}), nil
		},
	}
}

func TestRegisterAndLookup(t *testing.T) {
	reg := NewRegistry()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: "gpt-5.5"}, "OPENAI_API_KEY"))

	got, ok := reg.Lookup("openai")
	if !ok {
		t.Fatal("openai was not found after registration")
	}
	if got.Access != AccessAPI {
		t.Errorf("access = %q", got.Access)
	}
	if _, ok := reg.Lookup("nope"); ok {
		t.Error("an unregistered provider resolved")
	}
}

func TestRegisterRejectsBadRegistrations(t *testing.T) {
	// These are programmer errors found at startup, so they panic rather than
	// returning an error a caller might log and continue past.
	cases := map[string]Registration{
		"no name":        {Access: AccessAPI, Engines: map[string]string{engines.ChatGPT: ""}, New: func(CredentialSource) (Provider, error) { return nil, nil }},
		"bad access":     {Name: "x", Access: "guessed", Engines: map[string]string{engines.ChatGPT: ""}, New: func(CredentialSource) (Provider, error) { return nil, nil }},
		"no engines":     {Name: "x", Access: AccessAPI, New: func(CredentialSource) (Provider, error) { return nil, nil }},
		"no constructor": {Name: "x", Access: AccessAPI, Engines: map[string]string{engines.ChatGPT: ""}},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("registering a provider with %s did not panic", name)
				}
			}()
			NewRegistry().Register(bad)
		})
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	reg := NewRegistry()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: ""}))
	defer func() {
		if recover() == nil {
			t.Error("registering the same provider twice did not panic")
		}
	}()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: ""}))
}

func TestProvidersForEngine(t *testing.T) {
	// The settings screen and the target error messages both need this: when
	// a user asks for a surface, say who can reach it.
	reg := NewRegistry()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: "gpt-5.5"}))
	reg.Register(stubRegistration("dataforseo", AccessScraped, map[string]string{engines.ChatGPT: "", engines.AIOverview: ""}))

	got := reg.ProvidersFor(engines.ChatGPT)
	if len(got) != 2 || got[0] != "dataforseo" || got[1] != "openai" {
		t.Errorf("ProvidersFor(chatgpt) = %v, want both, sorted", got)
	}
	if got := reg.ProvidersFor(engines.AIOverview); len(got) != 1 || got[0] != "dataforseo" {
		t.Errorf("ProvidersFor(ai_overview) = %v", got)
	}
	if got := reg.ProvidersFor(engines.Claude); len(got) != 0 {
		t.Errorf("ProvidersFor(claude) = %v, want none", got)
	}
}

func TestNewConstructsWithCredentials(t *testing.T) {
	reg := NewRegistry()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: "gpt-5.5"}, "OPENAI_API_KEY"))

	if _, err := reg.New("openai", StaticCredentials(nil)); err == nil {
		t.Error("a provider was constructed with no credentials")
	}
	p, err := reg.New("openai", StaticCredentials(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatalf("New with a credential: %v", err)
	}
	if p.Name() != StubName {
		t.Errorf("constructed provider name = %q", p.Name())
	}
}

func TestNewUnknownProviderListsTheKnownOnes(t *testing.T) {
	reg := NewRegistry()
	reg.Register(stubRegistration("openai", AccessAPI, map[string]string{engines.ChatGPT: ""}))
	_, err := reg.New("nope", nil)
	if err == nil || !strings.Contains(err.Error(), "openai") {
		t.Errorf("error = %v, want it to name the registered providers", err)
	}
}

func TestMissingCredentialsNamesTheVariable(t *testing.T) {
	// A bare authentication failure sends a user hunting. Naming the variable
	// is the difference between a fix and a support thread.
	reg := NewRegistry()
	reg.Register(stubRegistration("oxylabs", AccessScraped, map[string]string{engines.AIMode: ""}, "OXYLABS_USERNAME", "OXYLABS_PASSWORD"))

	missing := reg.MissingCredentials("oxylabs", StaticCredentials(map[string]string{"OXYLABS_USERNAME": "u"}))
	if len(missing) != 1 || missing[0] != "OXYLABS_PASSWORD" {
		t.Errorf("MissingCredentials = %v, want just the password", missing)
	}
	if got := reg.MissingCredentials("oxylabs", StaticCredentials(map[string]string{"OXYLABS_USERNAME": "u", "OXYLABS_PASSWORD": "p"})); len(got) != 0 {
		t.Errorf("MissingCredentials with both set = %v", got)
	}
	if got := reg.MissingCredentials("oxylabs", StaticCredentials(map[string]string{"OXYLABS_USERNAME": "   "})); len(got) != 2 {
		t.Errorf("a whitespace-only credential counted as set: %v", got)
	}
}

func TestDefaultRegistryIsHonestAboutWhatExists(t *testing.T) {
	// Default() holds implementations, not intentions. Every provider it
	// lists must be constructible, or a target would validate at startup and
	// then never produce a row.
	reg := Default()
	for _, name := range reg.Names() {
		entry, _ := reg.Lookup(name)
		if entry.New == nil {
			t.Errorf("provider %q is in Default() with no constructor", name)
		}
		if len(entry.Engines) == 0 {
			t.Errorf("provider %q is in Default() reaching no engines", name)
		}
		for engine := range entry.Engines {
			if !engines.Known(engine) {
				t.Errorf("provider %q claims unknown engine %q", name, engine)
			}
		}
	}
}

func TestStubAnswersAndCounts(t *testing.T) {
	stub := NewStub(StubConfig{
		Answer:    "Acme and Globex lead the category.",
		Citations: []Citation{{URL: "https://acme.com/pricing", Position: 1}},
	})
	resp, err := stub.Run(context.Background(), Request{Engine: engines.ChatGPT, Prompt: "best crm"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(resp.Text, "Acme") {
		t.Errorf("text = %q", resp.Text)
	}
	if len(resp.Citations) != 1 || resp.Citations[0].Position != 1 {
		t.Errorf("citations = %+v", resp.Citations)
	}
	if resp.Calls != 1 {
		t.Errorf("calls = %d", resp.Calls)
	}
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Error("an api stub reported zero tokens")
	}
	if stub.Calls() != 1 || len(stub.Requests()) != 1 {
		t.Errorf("stub recorded %d calls", stub.Calls())
	}
}

func TestStubReportsUnsupportedEngine(t *testing.T) {
	stub := NewStub(StubConfig{Engines: map[string]string{engines.ChatGPT: ""}})
	_, err := stub.Run(context.Background(), Request{Engine: engines.AIOverview, Prompt: "q"})
	if !errors.Is(err, ErrUnsupportedEngine) {
		t.Errorf("err = %v, want ErrUnsupportedEngine", err)
	}
}

func TestStubCanReturnTypedErrors(t *testing.T) {
	// The runner branches on these, so a test needs to be able to produce
	// each one without a network.
	for _, want := range []error{ErrAuth, ErrRateLimited, ErrQuota, ErrNoAnswerSurface} {
		stub := NewStub(StubConfig{Err: want})
		_, err := stub.Run(context.Background(), Request{Engine: engines.ChatGPT, Prompt: "q"})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
}

func TestScrapedStubReportsNoTokens(t *testing.T) {
	// Scraped providers bill per request, not per token. Reporting invented
	// tokens would make the usage screen lie.
	stub := NewStub(StubConfig{Access: AccessScraped, Engines: map[string]string{engines.AIOverview: ""}})
	resp, err := stub.Run(context.Background(), Request{Engine: engines.AIOverview, Prompt: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Errorf("scraped tokens = %d/%d, want 0/0", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Calls != 1 {
		t.Errorf("calls = %d", resp.Calls)
	}
}

func TestStubHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewStub(StubConfig{}).Run(ctx, Request{Engine: engines.ChatGPT, Prompt: "q"}); err == nil {
		t.Error("a cancelled context still produced an answer")
	}
}

func TestRegisterStubIsResolvableThroughTheRegistry(t *testing.T) {
	reg := NewRegistry()
	stub := RegisterStub(reg, StubConfig{Answer: "canned"})
	p, err := reg.New(StubName, nil)
	if err != nil {
		t.Fatalf("New(stub): %v", err)
	}
	if _, err := p.Run(context.Background(), Request{Engine: engines.ChatGPT, Prompt: "q"}); err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 1 {
		t.Error("the registry handed back a different stub than RegisterStub returned")
	}
}

func TestAccessValid(t *testing.T) {
	if !AccessAPI.Valid() || !AccessScraped.Valid() {
		t.Error("a known access mode reported itself invalid")
	}
	if Access("").Valid() || Access("guessed").Valid() {
		t.Error("an unknown access mode reported itself valid")
	}
}
