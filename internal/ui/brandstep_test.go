// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/provider/providertest"
	"github.com/limelit-co/open/internal/siteinfo"
	"github.com/limelit-co/open/internal/store"
)

func kindleSite(context.Context, string) (siteinfo.Info, error) {
	return siteinfo.Info{SiteName: "Kindle to PDF", Title: "Kindle to PDF — Save Kindle Books as PDF"}, nil
}

// TestABrandNamedByItsDomainIsAskedForItsRealName. The instance that read 0%
// had its brand saved as "kindletopdf.com", so every answer that wrote "Kindle
// to PDF" was missed. The step now reads the site's own name and asks once.
func TestABrandNamedByItsDomainIsAskedForItsRealName(t *testing.T) {
	app, db, h := newApp(t, providertest.Registry())
	app.siteLookup = kindleSite

	rec := post(t, h, "/setup/brand", url.Values{"name": {"kindletopdf.com"}, "domain": {"kindletopdf.com"}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="Kindle to PDF"`) || !strings.Contains(body, `name="confirmed" value="1"`) {
		t.Fatalf("no suggestion: %d %s", rec.Code, flashOf(body))
	}
	if _, err := db.Property(context.Background()); err == nil {
		t.Fatal("the brand was saved before the suggestion was confirmed")
	}

	rec = post(t, h, "/setup/brand", url.Values{"name": {"Kindle to PDF"}, "domain": {"kindletopdf.com"}, "confirmed": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	if prop, _ := db.Property(context.Background()); prop.Name != "Kindle to PDF" || prop.Domain != "kindletopdf.com" {
		t.Errorf("saved %+v", prop)
	}
}

func TestARealNameIsSavedWithoutAsking(t *testing.T) {
	app, db, h := newApp(t, providertest.Registry())
	asked := false
	app.siteLookup = func(ctx context.Context, d string) (siteinfo.Info, error) { asked = true; return kindleSite(ctx, d) }
	if rec := post(t, h, "/setup/brand", url.Values{"name": {"Acme"}, "domain": {"acme.com"}}); rec.Code != http.StatusSeeOther || asked {
		t.Errorf("status %d, asked the site: %v", rec.Code, asked)
	}
	if prop, _ := db.Property(context.Background()); prop.Name != "Acme" {
		t.Errorf("saved %+v", prop)
	}
}

// TestAnUnreadableSiteKeepsTheDomainAsTheName: no suggestion, no blocking.
func TestAnUnreadableSiteKeepsTheDomainAsTheName(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	if rec := post(t, h, "/setup/brand", url.Values{"domain": {"acme.com"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if prop, _ := db.Property(context.Background()); prop.Name != "acme.com" {
		t.Errorf("saved %+v", prop)
	}
}

func TestFillInFromTheSite(t *testing.T) {
	app, _, h := newApp(t, providertest.Registry())
	app.siteLookup = kindleSite
	body := post(t, h, "/setup/brand/lookup", url.Values{"domain": {"https://www.kindletopdf.com/"}}).Body.String()
	if !strings.Contains(body, `value="Kindle to PDF"`) || !strings.Contains(body, `value="kindletopdf.com"`) {
		t.Errorf("lookup did not fill the form: %s", flashOf(body))
	}
}

func TestNamedByDomain(t *testing.T) {
	for name, want := range map[string]bool{
		"":                true,
		"kindletopdf.com": true,
		"kindletopdf":     true,
		"Acme":            false,
		"KindleToPDF":     false,
		"Kindle to PDF":   false,
	} {
		domain := "kindletopdf.com"
		if name == "Acme" {
			domain = "acme.com"
		}
		if got := namedByDomain(name, domain); got != want {
			t.Errorf("namedByDomain(%q) = %v", name, got)
		}
	}
}

// TestCompetitorsPageSuggestsTheSitesAnswersCite. The instance that read
// "1 of 1 brands" had no competitors while its answers cited epubor.com 29
// times; the page offers the most cited untracked sites, one click each.
func TestCompetitorsPageSuggestsTheSitesAnswersCite(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	ctx := context.Background()
	promptID, _ := db.AddPrompt(ctx, store.Prompt{Text: "best pdf tools", Active: true})
	targetID, _ := db.AddTarget(ctx, store.Target{Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api"})
	db.AddCompetitor(ctx, store.Competitor{Name: "Tracked", Domain: "tracked.example"})
	for i := 0; i < 3; i++ {
		db.RecordChat(ctx, store.ChatRecord{PromptID: promptID, TargetID: targetID, Status: store.ChatOK, Text: "x",
			Citations: []store.Citation{
				{URL: "https://epubor.com/a", Host: "epubor.com", Site: "epubor.com", Position: 1, SourceType: "other"},
				{URL: "https://tracked.example/a", Host: "tracked.example", Site: "tracked.example", Position: 2, SourceType: "competitor"},
				{URL: "https://reddit.com/r/x", Host: "reddit.com", Site: "reddit.com", Position: 3, SourceType: "social"},
			}})
	}
	body := get(t, h, "/competitors").Body.String()
	_, suggested, ok := strings.Cut(body, "Often cited, not tracked")
	if !ok || !strings.Contains(suggested, "epubor.com") {
		t.Fatal("epubor.com was not suggested")
	}
	for _, not := range []string{"tracked.example", "reddit.com"} {
		if strings.Contains(suggested, not) {
			t.Errorf("%s was suggested", not)
		}
	}
}

// TestTheBrandCanBeRenamedAfterSetup, and the stored answers follow.
func TestTheBrandCanBeRenamedAfterSetup(t *testing.T) {
	app, db, h := newApp(t, providertest.Registry())
	app.siteLookup = kindleSite
	post(t, h, "/setup/brand", url.Values{"name": {"KindleToPDF"}, "domain": {"kindletopdf.com"}})

	body := post(t, h, "/settings/brand/lookup", url.Values{"domain": {"kindletopdf.com"}}).Body.String()
	if !strings.Contains(body, `id="brand-name" name="name" value="Kindle to PDF"`) {
		t.Fatal("the lookup did not fill the settings form")
	}
	body = post(t, h, "/settings/brand", url.Values{"name": {"Kindle to PDF"}, "domain": {"kindletopdf.com"}, "aliases": {"KindleToPDF"}}).Body.String()
	if !strings.Contains(body, "read again") {
		t.Errorf("no confirmation: %s", body[:200])
	}
	prop, _ := db.Property(context.Background())
	if prop.Name != "Kindle to PDF" || len(prop.Aliases) != 1 {
		t.Errorf("saved %+v", prop)
	}
}

// TestGridSaysNoAnswerWhenTheEngineShowedNone. Google AI Overviews was asked
// ten times and showed nothing; the grid drew that as "-" under a legend
// saying "not asked", which read as broken. It now says "no answer".
func TestGridSaysNoAnswerWhenTheEngineShowedNone(t *testing.T) {
	_, db, h := newApp(t, providertest.Registry())
	seedProperty(t, h)
	ctx := context.Background()
	promptID, _ := db.AddPrompt(ctx, store.Prompt{Text: "best pdf tools", Active: true})
	aio, _ := db.AddTarget(ctx, store.Target{Spec: "ai_overview:dataforseo", Engine: "ai_overview", Provider: "dataforseo", Access: "scraped"})
	gpt, _ := db.AddTarget(ctx, store.Target{Spec: "chatgpt:openai:online", Engine: "chatgpt", Provider: "openai", Online: true, Access: "api"})
	other, _ := db.AddPrompt(ctx, store.Prompt{Text: "which pdf tool", Active: true})
	db.RecordChat(ctx, store.ChatRecord{PromptID: promptID, TargetID: aio, Status: store.ChatNoAnswerSurface})
	db.RecordChat(ctx, store.ChatRecord{PromptID: promptID, TargetID: gpt, Status: store.ChatOK, Text: "Calibre is free."})
	db.RecordChat(ctx, store.ChatRecord{PromptID: other, TargetID: gpt, Status: store.ChatOK, Text: "Epubor works."})

	body := get(t, h, "/overview").Body.String()
	_, grid, _ := strings.Cut(body, "Every prompt, every engine")
	if !strings.Contains(grid, ">no answer<") || !strings.Contains(grid, "showed no AI answer") {
		t.Error("the asked-but-empty cell is not labelled")
	}
	if !strings.Contains(grid, "not asked yet") {
		t.Error("the unasked cell lost its label")
	}
	if !strings.Contains(body, "Nothing to compare with yet") {
		t.Error("an instance with no competitors shows an empty share of voice")
	}
}
