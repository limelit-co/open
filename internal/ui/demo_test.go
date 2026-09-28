// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/store"
)

// demoApp builds a configured instance and then turns demo mode on, which is
// the order an operator follows: the wizard once, then the flag. Seeding goes
// through the store because the wizard's POSTs are exactly what demo refuses.
func demoApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	t.Setenv(DemoEnv, "1")
	reg := provider.NewRegistry()
	provider.RegisterStub(reg, provider.StubConfig{Answer: "Acme leads."})
	app, db, h := newApp(t, reg)
	if err := db.SaveProperty(context.Background(), store.Property{Name: "Acme", Domain: "acme.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPrompt(context.Background(), store.Prompt{Text: "best widget tools", Active: true}); err != nil {
		t.Fatal(err)
	}
	return app, h
}

// TestDemoUnconfiguredIsAnOperatorErrorNotALoop. Without this, /overview says
// "not configured, go to /setup" and /setup says "demo, go to /overview".
func TestDemoUnconfiguredIsAnOperatorErrorNotALoop(t *testing.T) {
	t.Setenv(DemoEnv, "1")
	reg := provider.NewRegistry()
	_, _, h := newApp(t, reg)

	res := get(t, h, "/setup")
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /setup on an unconfigured demo returned %d, want 503", res.Code)
	}
	if !strings.Contains(res.Body.String(), DemoEnv) {
		t.Error("the error does not tell the operator what to do")
	}
}

// TestDemoRefusesEveryMutatingRoute is the whole point of demo mode: a public
// instance where nobody can spend the operator's keys or change what it
// tracks. Every POST in the router is exercised, so a new mutating route that
// forgets the wrapper fails here rather than in production.
func TestDemoRefusesEveryMutatingRoute(t *testing.T) {
	_, h := demoApp(t)
	for _, path := range []string{
		"/prompts/add", "/prompts/delete",
		"/competitors/add", "/competitors/delete",
		"/settings/targets/track", "/settings/targets/add", "/settings/targets/delete",
		"/settings/targets/pause", "/settings/targets/resume",
		"/settings/keys", "/settings/keys/test", "/settings/keys/forget", "/settings/limits",
		"/settings/schedule", "/settings/mcp/rotate", "/settings/mcp/forget",
		"/run", "/upgrade",
		"/setup/brand", "/setup/competitors", "/setup/prompts", "/setup/provider", "/setup/finish",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"text": {"x"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", "/prompts")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		if res.Code != http.StatusSeeOther {
			t.Errorf("POST %s returned %d in demo mode, want a redirect", path, res.Code)
			continue
		}
		if loc := res.Header().Get("Location"); !strings.Contains(loc, "flash=demo-readonly") {
			t.Errorf("POST %s redirected to %q without the demo flash", path, loc)
		}
	}
}

// TestDemoRefusalActuallyChangedNothing: a redirect is not proof. The data
// must be untouched afterwards.
func TestDemoRefusalActuallyChangedNothing(t *testing.T) {
	app, h := demoApp(t)
	before := get(t, h, "/prompts").Body.String()
	promptsBefore := strings.Count(before, `class="prompt-text"`) + strings.Count(before, "Remove")

	req := httptest.NewRequest(http.MethodPost, "/prompts/add", strings.NewReader("text=injected+prompt"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(httptest.NewRecorder(), req)

	after := get(t, h, "/prompts").Body.String()
	if strings.Contains(after, "injected prompt") {
		t.Fatal("a prompt was added through a demo instance")
	}
	promptsAfter := strings.Count(after, `class="prompt-text"`) + strings.Count(after, "Remove")
	if promptsAfter != promptsBefore {
		t.Fatalf("prompt count moved from %d to %d", promptsBefore, promptsAfter)
	}
	if !app.demo {
		t.Fatal("app is not in demo mode")
	}
}

// TestDemoHidesTheCredentialSurface. Not masked, absent: which variables are
// set would tell a visitor which vendor accounts the operator holds.
func TestDemoHidesTheCredentialSurface(t *testing.T) {
	app, h := demoApp(t)
	body := get(t, h, "/settings").Body.String()
	for _, leak := range []string{`action="/settings/keys"`, `type="password"`, "set in the environment", "saved, paste to replace", "Test this key",
		"Generate token", "Rotate token", "Forget token", "/mcp/rotate", "not set"} {
		if strings.Contains(body, leak) {
			t.Errorf("demo settings renders %q", leak)
		}
	}
	if !strings.Contains(body, "not shown on a public demo") {
		t.Error("the settings page does not say why credentials are absent")
	}
	// A target's last error is the operator's, not the visitor's: the demo
	// shows that a call failed and when, never what the vendor said.
	targetID, err := app.db.AddTarget(context.Background(), store.Target{Spec: "chatgpt:stub", Engine: "chatgpt", Provider: "stub", Access: "api"})
	if err != nil {
		t.Fatal(err)
	}
	prompts, _ := app.db.Prompts(context.Background(), false)
	if _, err := app.db.RecordChat(context.Background(), store.ChatRecord{TargetID: targetID, PromptID: prompts[0].ID, Status: "failed", Error: "openai: account sk-live-... has no credit"}); err != nil {
		t.Fatal(err)
	}
	body = get(t, h, "/settings").Body.String()
	if strings.Contains(body, "has no credit") || strings.Contains(body, "sk-live") {
		t.Error("demo settings renders a vendor error message")
	}
	if !strings.Contains(body, "last failure") {
		t.Error("demo settings hides that a call failed at all")
	}
	// The upgrade form asks for a Cloud key, so it is absent too.
	upgrade := get(t, h, "/upgrade").Body.String()
	if strings.Contains(upgrade, `type="password"`) || strings.Contains(upgrade, `action="/upgrade"`) {
		t.Error("demo renders the upgrade form")
	}
}

func TestDemoShowsTheBannerAndDisablesRun(t *testing.T) {
	_, h := demoApp(t)
	body := get(t, h, "/overview").Body.String()
	if !strings.Contains(body, "demo-banner") || !strings.Contains(body, "Live demo") {
		t.Error("no demo banner")
	}
	if !strings.Contains(body, "Run your own") {
		t.Error("the banner does not point at running your own copy")
	}
	// The button stays visible so a visitor sees that running is a thing the
	// software does, and disabled so they cannot spend the operator's keys.
	if !strings.Contains(body, `disabled title="Read-only demo`) {
		t.Error("Run is not disabled with the demo explanation")
	}
}

func TestDemoSendsTheWizardHome(t *testing.T) {
	_, h := demoApp(t)
	for _, path := range []string{"/setup", "/setup/competitors", "/setup/prompts", "/setup/provider"} {
		res := get(t, h, path)
		if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/overview" {
			t.Errorf("GET %s returned %d -> %q, want a redirect to /overview", path, res.Code, res.Header().Get("Location"))
		}
	}
}

// TestDemoIsTheSameBinary: nothing about demo mode is a different build. The
// same routes, the same templates, one environment variable.
func TestDemoIsTheSameBinary(t *testing.T) {
	t.Setenv(DemoEnv, "")
	if DemoMode() {
		t.Fatal("demo mode is on with the variable unset")
	}
	t.Setenv(DemoEnv, "1")
	if !DemoMode() {
		t.Fatal("demo mode is off with the variable set")
	}
	// Not in demo, the same POST works.
	reg := provider.NewRegistry()
	provider.RegisterStub(reg, provider.StubConfig{Answer: "x"})
	t.Setenv(DemoEnv, "")
	_, _, h := newApp(t, reg)
	seedProperty(t, h)
	req := httptest.NewRequest(http.MethodPost, "/prompts/add", strings.NewReader("text=a+real+prompt"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if strings.Contains(res.Header().Get("Location"), "demo-readonly") {
		t.Fatal("a normal instance refused a write as if it were a demo")
	}
}

func TestCommitLinkOnlyLinksACleanBuild(t *testing.T) {
	if c, u := commitLink("abcdef123456"); c != "abcdef123456" || !strings.HasSuffix(u, "/commit/abcdef123456") {
		t.Errorf("clean stamp: %q %q", c, u)
	}
	// A dirty build is not any commit in the repository.
	if c, u := commitLink("abcdef123456-dirty"); c == "" || u != "" {
		t.Errorf("dirty stamp should show but not link: %q %q", c, u)
	}
	if c, u := commitLink("dev"); c != "" || u != "" {
		t.Errorf("dev build: %q %q", c, u)
	}
}
