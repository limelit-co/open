// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// cloud is a stand-in for Limelit Cloud's relay. testdata/limelit_chatgpt.json
// is a relay answer in the shape Cloud's internal/ossrelay returns (the
// contract is documented in docs/providers.md).
func cloud(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("LIMELIT_CLOUD_API", srv.URL)
}

func TestLimelitReturnsTheRelayedAnswer(t *testing.T) {
	fixture, err := os.ReadFile("testdata/limelit_chatgpt.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	cloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/answer" || r.Header.Get("Authorization") != "Bearer lmlt_test" {
			t.Errorf("request %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write(fixture)
	})
	p, err := NewLimelit("lmlt_test")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Run(context.Background(), Request{Engine: ChatGPTEngine, Prompt: "best project management tool", LocationCountry: "US", LanguageCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if got["engine"] != "chatgpt" || got["prompt"] != "best project management tool" || got["location_country"] != "US" || got["language"] != "en" {
		t.Errorf("sent %v", got)
	}
	if !strings.Contains(resp.Text, "Linear") || len(resp.Citations) != 2 || resp.Citations[1].Position != 2 ||
		len(resp.FanOut) != 1 || resp.Calls != 1 || resp.Model != "searchapi-chatgpt" {
		t.Errorf("response = %+v", resp)
	}
	if p.Access() != AccessScraped || p.Name() != "limelit" {
		t.Errorf("limelit is %s/%s", p.Name(), p.Access())
	}
}

// TestLimelitSaysToAddYourOwnKeyWhenTheAllowanceIsUsedUp. Every limit Cloud
// enforces arrives as ErrQuota, carrying Cloud's sentence, so the failed
// answer tells the user what to do next.
func TestLimelitSaysToAddYourOwnKeyWhenTheAllowanceIsUsedUp(t *testing.T) {
	for code, status := range map[string]int{
		"allowance_exhausted":       http.StatusPaymentRequired,
		"daily_allowance_exhausted": http.StatusTooManyRequests,
		"daily_pool_exhausted":      http.StatusTooManyRequests,
		"relay_disabled":            http.StatusServiceUnavailable,
	} {
		cloud(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"error": code, "message": "Add your own provider key in Limelit Open's Settings to keep measuring."})
		})
		p, _ := NewLimelit("lmlt_test")
		_, err := p.Run(context.Background(), Request{Engine: ChatGPTEngine, Prompt: "x"})
		if !errors.Is(err, ErrQuota) || !strings.Contains(err.Error(), "your own provider key") {
			t.Errorf("%s: err = %v", code, err)
		}
	}
}

func TestLimelitMapsTheOtherRelayErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusUnauthorized, "auth_required", ErrAuth},
		{http.StatusForbidden, "engine_not_available", ErrUnsupportedEngine},
		{http.StatusTooManyRequests, "", ErrRateLimited},
	} {
		cloud(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			json.NewEncoder(w).Encode(map[string]any{"error": tc.code, "message": "m"})
		})
		p, _ := NewLimelit("lmlt_test")
		if _, err := p.Run(context.Background(), Request{Engine: ChatGPTEngine, Prompt: "x"}); !errors.Is(err, tc.want) {
			t.Errorf("%d %s: err = %v, want %v", tc.status, tc.code, err, tc.want)
		}
	}
}

// TestLimelitKeepsClaudeOut. Claude is not in the allowance: neither
// registration reaches it, and a request for it never leaves the machine.
func TestLimelitKeepsClaudeOut(t *testing.T) {
	cloud(t, func(w http.ResponseWriter, r *http.Request) { t.Error("a Claude request reached Cloud") })
	for _, newP := range []func(string) (Provider, error){NewLimelit, NewLimelitAPI} {
		p, _ := newP("lmlt_test")
		if _, ok := p.Engines()[ClaudeEngine]; ok {
			t.Errorf("%s reaches claude", p.Name())
		}
		if _, err := p.Run(context.Background(), Request{Engine: ClaudeEngine, Prompt: "x"}); !errors.Is(err, ErrUnsupportedEngine) {
			t.Errorf("%s: err = %v", p.Name(), err)
		}
	}
	api, _ := NewLimelitAPI("lmlt_test")
	if api.Access() != AccessAPI || api.Engines()[PerplexityEngine] == "" {
		t.Errorf("limelitapi = %s %v", api.Access(), api.Engines())
	}
}

func TestLimelitReportsAnAbsentSurface(t *testing.T) {
	cloud(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"text": "", "no_answer_surface": true})
	})
	p, _ := NewLimelit("lmlt_test")
	if _, err := p.Run(context.Background(), Request{Engine: AIOverviewEngine, Prompt: "x"}); !errors.Is(err, ErrNoAnswerSurface) {
		t.Errorf("err = %v", err)
	}
}

// TestLimelitTestReadsTheAllowanceAndSpendsNothing. Saving the key proves it
// against the allowance endpoint, never by asking an engine.
func TestLimelitTestReadsTheAllowanceAndSpendsNothing(t *testing.T) {
	cloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/relay/allowance" {
			t.Errorf("Test called %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"enabled": true, "monthly_credits": 1000})
	})
	p, _ := NewLimelit("lmlt_test")
	if err := p.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLimelit("  "); !errors.Is(err, ErrAuth) {
		t.Errorf("an empty key: err = %v", err)
	}
}
