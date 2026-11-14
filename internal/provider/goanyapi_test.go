// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The fixtures here are the vendor's published example responses as of
// 2026-11-14, not a call this repository made: there was no key available
// when the adapter was written. They pin the shapes the parser reads
// (data.result.content.results[].text, .links[], search_parameters
// .feature_available) and an empty-answer response. Replacing them with a
// self-recorded pair is welcome and changes nothing else.

func newGoAnyAPIAgainst(t *testing.T, h http.Handler) *goAnyAPIProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewGoAnyAPI("ga-test")
	if err != nil {
		t.Fatal(err)
	}
	gp := p.(*goAnyAPIProvider)
	gp.overview, gp.aiMode, gp.balance = srv.URL+"/ai-overview", srv.URL+"/ai-mode", srv.URL+"/balance"
	return gp
}

func TestGoAnyAPIParsesAnAIOverview(t *testing.T) {
	p := newGoAnyAPIAgainst(t, against(t, "goanyapi_ai_overview.json"))

	resp, err := p.Run(context.Background(), Request{
		Engine: AIOverviewEngine, Prompt: "how do solar panels work",
		LocationCountry: "US", LanguageCode: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "photovoltaic effect") {
		t.Errorf("answer text missing the overview body: %q", resp.Text)
	}
	// The fixture repeats the same link inside one block; a citation is
	// counted once, in the order the engine gave them.
	if len(resp.Citations) != 2 {
		t.Fatalf("got %d citations, want 2: %+v", len(resp.Citations), resp.Citations)
	}
	if resp.Citations[0].URL != "https://news.energysage.com/how-do-solar-panels-work/" {
		t.Errorf("first citation is %q", resp.Citations[0].URL)
	}
	if resp.Citations[0].Title != "EnergySage" {
		t.Errorf("citation kept no title: %q", resp.Citations[0].Title)
	}
	if resp.Citations[1].Position != 2 {
		t.Errorf("citations are not renumbered: %+v", resp.Citations)
	}
	// A scraped surface has no model to report and must not invent one.
	if resp.Model != "" {
		t.Errorf("scraped answer reported model %q", resp.Model)
	}
	if resp.Calls != 1 {
		t.Errorf("calls = %d, want 1", resp.Calls)
	}
}

func TestGoAnyAPIAbsentOverviewIsNotABrandMiss(t *testing.T) {
	// The common case: Google renders no overview. The vendor reports it as a
	// success and bills for it. It must surface as an absent surface, which
	// every metric denominator excludes, not as an answer that ignored the
	// brand.
	p := newGoAnyAPIAgainst(t, against(t, "goanyapi_ai_overview_no_surface.json"))

	_, err := p.Run(context.Background(), Request{
		Engine: AIOverviewEngine, Prompt: "acme widgets invoice portal login",
	})
	if !errors.Is(err, ErrNoAnswerSurface) {
		t.Fatalf("err = %v, want ErrNoAnswerSurface", err)
	}
}

func TestGoAnyAPIRequestShape(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth string
		gotBody                     goAnyAPIRequest
		gotContentType              string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotContentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		body, _ := json.Marshal(map[string]any{
			"code": "ok",
			"data": map[string]any{
				"endpoint": "google-ai-mode",
				"result": map[string]any{
					"format": "json",
					"content": map[string]any{
						"search_parameters": map[string]any{"feature_available": true},
						"results": []map[string]any{{
							"type": "ai_summary", "title": "AI Mode", "text": "An answer.",
							"links": []map[string]string{{"title": "X.com", "url": "https://x.com"}},
						}},
					},
				},
			},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	p, err := NewGoAnyAPI("ga-test")
	if err != nil {
		t.Fatal(err)
	}
	gp := p.(*goAnyAPIProvider)
	gp.aiMode = srv.URL + "/ai-mode"

	resp, err := gp.Run(context.Background(), Request{
		Engine: AIModeEngine, Prompt: "What is x.com",
		LocationCountry: "GB", LanguageCode: "en-GB",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/ai-mode" {
		t.Errorf("%s %s, want POST /ai-mode", gotMethod, gotPath)
	}
	if gotAuth != "Bearer ga-test" {
		t.Errorf("auth header %q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("content type %q", gotContentType)
	}
	// The country arrives as an ISO code from the target and the vendor
	// wants it lowercase; the language tag is passed through as given.
	if gotBody.Query != "What is x.com" || gotBody.Country != "gb" || gotBody.Language != "en-GB" {
		t.Errorf("request body %+v", gotBody)
	}
	if gotBody.Device != goAnyAPIDefaultDevice {
		t.Errorf("device = %q, want %q", gotBody.Device, goAnyAPIDefaultDevice)
	}
	if len(resp.Citations) != 1 || resp.Citations[0].URL != "https://x.com" {
		t.Errorf("citations %+v", resp.Citations)
	}
}

func TestGoAnyAPIRejectsAnEngineItCannotReach(t *testing.T) {
	p, err := NewGoAnyAPI("ga-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Run(context.Background(), Request{Engine: ChatGPTEngine, Prompt: "hi"})
	if !errors.Is(err, ErrUnsupportedEngine) {
		t.Fatalf("err = %v, want ErrUnsupportedEngine", err)
	}
}

func TestGoAnyAPIMapsFailures(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuth},
		{http.StatusForbidden, ErrAuth},
		{http.StatusPaymentRequired, ErrQuota},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusBadGateway, ErrRateLimited},
	}
	for _, c := range cases {
		p := newGoAnyAPIAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(`{"code":"request_failed","message":"nope"}`))
		}))
		_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
		if !errors.Is(err, c.want) {
			t.Errorf("status %d gave %v, want %v", c.status, err, c.want)
		}
	}
}

func TestGoAnyAPIErrorCodeInsideASuccessIsNotAnAnswer(t *testing.T) {
	// A 200 carrying code != "ok" is how this vendor also reports a problem.
	p := newGoAnyAPIAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"insufficient_credits","message":"balance is zero"}`))
	}))
	_, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "q"})
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("err = %v, want ErrQuota", err)
	}
}

func TestGoAnyAPITestUsesTheFreeBalanceCall(t *testing.T) {
	var hits int
	p := newGoAnyAPIAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/balance" {
			t.Errorf("%s %s, want GET /balance", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ga-test" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		hits++
		w.Write([]byte(`{"code":"ok","data":{"remainingCredits":900}}`))
	}))
	if err := p.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if hits != 1 {
		t.Errorf("balance endpoint hit %d times, want 1", hits)
	}
}

func TestGoAnyAPITestReportsABadKey(t *testing.T) {
	p := newGoAnyAPIAgainst(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if err := p.Test(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestGoAnyAPIRequiresAKey(t *testing.T) {
	if _, err := NewGoAnyAPI("  "); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}
