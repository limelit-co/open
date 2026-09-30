// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/provider/providertest"
)

func TestTrackEngineNeedsNoGrammar(t *testing.T) {
	// The whole point of the click-select path: a user picks an engine and a
	// provider, and never has to learn engine:provider[:model][:online].
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/settings/targets/track", url.Values{
		"engine": {"chatgpt"}, "provider": {"openrouter"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("track = %d, body %s", rec.Code, rec.Body.String())
	}
	targets, _ := db.Targets(context.Background(), false)
	if len(targets) != 1 {
		t.Fatalf("stored %d targets", len(targets))
	}
	if targets[0].Spec != "chatgpt:openrouter:online" {
		t.Errorf("spec = %q", targets[0].Spec)
	}
}

func TestTrackEngineSetsOnlineOnlyWhereItIsASwitch(t *testing.T) {
	// Web search is a switch on an API provider and implied on a scraped
	// surface. Asking a user to know which is which is the kind of detail
	// the click path exists to absorb.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"openai"}})
	post(t, h, "/settings/targets/track", url.Values{"engine": {"ai_overview"}, "provider": {"dataforseo"}})

	got := map[string]string{}
	targets, _ := db.Targets(context.Background(), false)
	for _, tg := range targets {
		got[tg.Engine] = tg.Spec
	}
	if got["chatgpt"] != "chatgpt:openai:online" {
		t.Errorf("api target = %q, want the online flag", got["chatgpt"])
	}
	if got["ai_overview"] != "ai_overview:dataforseo" {
		t.Errorf("scraped target = %q, want no redundant flag", got["ai_overview"])
	}
}

func TestTrackEngineRejectsAnUnknownProvider(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/settings/targets/track", url.Values{"engine": {"chatgpt"}, "provider": {"nosuchvendor"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("an unknown provider redirected instead of re-rendering: %d", rec.Code)
	}
	if targets, _ := db.Targets(context.Background(), false); len(targets) != 0 {
		t.Errorf("stored %d targets", len(targets))
	}
}

func TestSettingsSaysWhereToGetEveryKey(t *testing.T) {
	// A settings screen that asks for a key and leaves the user to find it
	// loses them at the step that decides whether the product ever runs.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	for _, c := range provider.Catalog() {
		if !strings.Contains(body, c.KeyURL) {
			t.Errorf("settings does not link %s at %s", c.Label, c.KeyURL)
		}
		if !strings.Contains(body, c.Label) {
			t.Errorf("settings does not name %s", c.Label)
		}
		for _, cred := range c.Credentials {
			if !strings.Contains(body, cred) {
				t.Errorf("settings does not name the variable %s", cred)
			}
		}
	}
	if !strings.Contains(body, `rel="noreferrer noopener"`) {
		t.Error("the vendor links do not carry rel=noreferrer noopener")
	}
}

func TestSettingsOffersAButtonPerReachableEngine(t *testing.T) {
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	for _, want := range []string{
		"Google AI Overview", "Bing Copilot", "Track via OpenRouter", "Track via DataForSEO",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings does not offer %q", want)
		}
	}
	// The raw string stays available for pinning a model, but behind a
	// disclosure rather than as the only way in.
	if !strings.Contains(body, "Advanced: add a target by hand") {
		t.Error("the raw target field is gone entirely")
	}
}

func TestSettingsNamesUnbuiltProvidersRatherThanHidingThem(t *testing.T) {
	// An engine with no button and no explanation reads as unreachable. One
	// muted line per card says the surface exists and the work is pending.
	_, _, h := newApp(t, provider.NewRegistry())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	if !strings.Contains(body, "Not built yet: OpenRouter") {
		t.Error("an unbuilt provider was hidden instead of named")
	}
	if strings.Contains(body, "Track via") {
		t.Error("a track button was offered with no provider built")
	}
}

func TestEnvironmentCredentialWinsAndSaysSo(t *testing.T) {
	// The environment always beats a stored value. A field that silently
	// cannot take effect is worse than a disabled one.
	t.Setenv("OPENROUTER_API_KEY", "sk-from-env")
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	if !strings.Contains(body, "set in the environment") {
		t.Error("settings does not say the environment is providing the key")
	}
	i := strings.Index(body, "OPENROUTER_API_KEY")
	if i < 0 {
		t.Fatal("the variable is not on the page")
	}
	if !strings.Contains(body[i:i+600], "disabled") {
		t.Error("the field for an environment-provided key is editable")
	}
}

func TestSavedKeyIsEncryptedAtRest(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	const key = "sk-or-v1-secret-value"
	rec := post(t, h, "/settings/keys", url.Values{
		"provider": {"openrouter"}, "cred_OPENROUTER_API_KEY": {key},
	})
	// Save is save-then-test, answered on the page rather than by redirect,
	// so the vendor's verdict lands under the field that was just filled.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "accepted the key") {
		t.Fatalf("saving a key = %d, notice %s", rec.Code, flashOf(rec.Body.String()))
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Error("the response to saving a key echoed it")
	}

	stored, err := db.Setting(context.Background(), credentialPrefix+"OPENROUTER_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if stored == "" {
		t.Fatal("nothing was stored")
	}
	if strings.Contains(stored, key) {
		t.Error("the key is stored in plaintext")
	}
	// And it is never handed back, on any surface.
	if strings.Contains(get(t, h, "/settings").Body.String(), key) {
		t.Error("the saved key was rendered back into the page")
	}
	if !strings.Contains(get(t, h, "/settings").Body.String(), "saved here") {
		t.Error("settings does not report that a key is saved")
	}
}

func TestSavingAnEmptyKeySaysNothingHappened(t *testing.T) {
	// Silently accepting an empty form would leave a user believing the key
	// was stored.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	rec := post(t, h, "/settings/keys", url.Values{"provider": {"openrouter"}, "cred_OPENROUTER_API_KEY": {"   "}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Nothing was saved") {
		t.Errorf("empty save = %d, body did not explain", rec.Code)
	}
}

func TestWizardProviderStepLinksVendorPages(t *testing.T) {
	// The step that asks for a key is the one most likely to end a first
	// session, so it carries the link rather than assuming the user knows.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/setup/provider").Body.String()

	if !strings.Contains(body, "https://openrouter.ai/keys") {
		t.Error("the wizard does not link a vendor key page")
	}
	if !strings.Contains(body, "Get a key") {
		t.Error("the wizard has no way to go and get one")
	}
	if !strings.Contains(body, "Skip for now") {
		t.Error("the wizard cannot be finished without a key")
	}
}

func TestConnectingInTheWizardStartsTracking(t *testing.T) {
	// Finishing setup with a key and no target would leave the Run button
	// disabled for a reason the user could not see.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/setup/provider", url.Values{
		"provider": {"openrouter"}, "cred_OPENROUTER_API_KEY": {"sk-or-test"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("connect = %d", rec.Code)
	}
	targets, _ := db.Targets(context.Background(), false)
	if len(targets) != 4 {
		t.Fatalf("connecting OpenRouter produced %d targets, want one per engine it reaches", len(targets))
	}
	for _, tg := range targets {
		if tg.Access != string(provider.AccessAPI) {
			t.Errorf("target %q access = %q", tg.Spec, tg.Access)
		}
	}
}

func TestTheFreeCloudKeyTracksEveryEngineItReaches(t *testing.T) {
	// One Limelit Cloud key is two registrations, because access is fixed
	// per provider. Pasting it must track both halves, or Perplexity would
	// silently never run.
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)

	rec := post(t, h, "/setup/provider", url.Values{
		"provider": {"limelit"}, "cred_LIMELIT_CLOUD_KEY": {"lmlt_test"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("connect = %d", rec.Code)
	}
	targets, _ := db.Targets(context.Background(), false)
	got := map[string]string{}
	for _, tg := range targets {
		got[tg.Spec] = tg.Access
	}
	want := map[string]string{
		"chatgpt:limelit": "scraped", "gemini:limelit": "scraped",
		"ai_overview:limelit": "scraped", "ai_mode:limelit": "scraped",
		"perplexity:limelitapi:online": "api",
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for spec, access := range want {
		if got[spec] != access {
			t.Errorf("target %q access = %q, want %q (all: %v)", spec, got[spec], access, got)
		}
	}
}

func TestOneCloudKeyIsOneCard(t *testing.T) {
	// Two cards for one key would let Forget on one disconnect the other.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	if n := strings.Count(body, `name="cred_LIMELIT_CLOUD_KEY"`); n != 1 {
		t.Errorf("the Cloud key has %d fields on Settings, want one", n)
	}
	_, card, ok := strings.Cut(body, "<h3>Limelit Cloud free allowance")
	card, _, _ = strings.Cut(card, "</form>")
	for _, engine := range []string{"ChatGPT", "Google AI Mode", "Perplexity"} {
		if !ok || !strings.Contains(card, engine) {
			t.Errorf("the Cloud card does not say it reaches %s", engine)
		}
	}

	// Each half is tracked through the registration that reaches it.
	for engine, via := range map[string]string{"perplexity": "limelitapi", "chatgpt": "limelit"} {
		if rec := post(t, h, "/settings/targets/track", url.Values{"engine": {engine}, "provider": {via}}); rec.Code != http.StatusSeeOther {
			t.Errorf("tracking %s via %s = %d", engine, via, rec.Code)
		}
	}
}

func TestWizardOffersTheFreeAllowanceFirst(t *testing.T) {
	// Two options, in order: the free allowance needs no vendor account, so
	// it is the one a first visitor can finish.
	_, _, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	body := get(t, h, "/setup/provider").Body.String()

	free := strings.Index(body, "Free: use Limelit")
	own := strings.Index(body, "Or use your own keys")
	vendor := strings.Index(body, "https://openrouter.ai/keys")
	if free < 0 || own < 0 || !(free < own && own < vendor) {
		t.Errorf("free at %d, own keys at %d, a vendor at %d: want them in that order", free, own, vendor)
	}
	if n := strings.Count(body, `name="cred_LIMELIT_CLOUD_KEY"`); n != 1 {
		t.Errorf("the Cloud key has %d fields in the wizard, want one", n)
	}
	if !strings.Contains(body, "pass through Limelit Cloud") {
		t.Error("the wizard does not say where the prompts go")
	}
}

func TestTestButtonReportsABadKey(t *testing.T) {
	// A wrong key reported at the moment it is pasted, rather than at the
	// next scheduled run when nobody is watching.
	_, _, h := newApp(t, provider.Default())
	seedProperty(t, h)

	post(t, h, "/settings/keys", url.Values{"provider": {"openai"}, "cred_OPENAI_API_KEY": {"sk-not-a-real-key"}})
	rec := post(t, h, "/settings/keys/test", url.Values{"provider": {"openai"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("test = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "did not accept the key") {
		t.Error("a rejected key was not reported on the page")
	}
}

func TestTestButtonSaysWhichVariableIsMissing(t *testing.T) {
	// "Authentication failed" sends a user hunting. Naming the variable is
	// the difference between a fix and a support thread.
	_, _, h := newApp(t, provider.Default())
	seedProperty(t, h)

	rec := post(t, h, "/settings/keys/test", url.Values{"provider": {"openai"}})
	if !strings.Contains(rec.Body.String(), "OPENAI_API_KEY is not set yet") {
		t.Errorf("the missing credential was not named: %s", flashOf(rec.Body.String()))
	}
}

func TestCredentialSourcePrefersTheEnvironment(t *testing.T) {
	// The runner and the Test button must read a credential the same way, or
	// Test would prove a value a run never uses.
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	app, _, h := newApp(t, provider.Default())
	seedProperty(t, h)
	post(t, h, "/settings/keys", url.Values{"provider": {"openai"}, "cred_OPENAI_API_KEY": {"sk-from-store"}})

	if got := app.credentials(context.Background())("OPENAI_API_KEY"); got != "sk-from-env" {
		t.Errorf("credential = %q, want the environment to win", got)
	}
}

func TestCredentialSourceFallsBackToTheStore(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	app, _, h := newApp(t, provider.Default())
	seedProperty(t, h)
	post(t, h, "/settings/keys", url.Values{"provider": {"openai"}, "cred_OPENAI_API_KEY": {"sk-from-store"}})

	if got := app.credentials(context.Background())("OPENAI_API_KEY"); got != "sk-from-store" {
		t.Errorf("credential = %q, want the stored value decrypted", got)
	}
}

func TestOpenAIIsOfferedAsARealButton(t *testing.T) {
	// The first implemented provider turns its engine card from a pending
	// line into something a user can click.
	_, _, h := newApp(t, provider.Default())
	seedProperty(t, h)
	body := get(t, h, "/settings").Body.String()

	if !strings.Contains(body, "Track via OpenAI") {
		t.Error("settings does not offer OpenAI as a track button")
	}
	if !strings.Contains(body, "Test this key") {
		t.Error("settings has no way to check the key")
	}
}

// flashOf pulls the notice text out of a rendered page, for error messages
// that are easier to read than a whole document.
func flashOf(body string) string {
	i := strings.Index(body, `class="notice`)
	if i < 0 {
		return "(no notice on the page)"
	}
	tail := body[i:]
	if j := strings.Index(tail, "</div>"); j > 0 {
		return tail[:j]
	}
	return tail[:min(len(tail), 200)]
}
