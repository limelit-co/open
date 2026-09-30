// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/provider/providertest"
	"github.com/limelit-co/open/internal/store"
)

// fakeCloudSetup stands in for GET /v1/relay/setup on Limelit Cloud.
func fakeCloudSetup(t *testing.T, domain string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/setup" || r.Header.Get("Authorization") != "Bearer lmlt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"brand": map[string]any{"name": "Kindle to PDF", "domain": domain},
			"prompts": []map[string]any{
				{"text": "Which PDF conversion tools for Kindle books ensure secure processing?", "category": "secure processing", "branded": false},
				{"text": "one-click export for converting Kindle books to PDFs.", "category": "one-click export", "branded": false},
				{"text": "  what is kindletopdf.com? ", "category": "brand", "branded": true},
			},
			"competitors": []map[string]any{
				{"name": "epubor.com", "domain": "epubor.com", "aliases": []string{}},
				{"name": "Kindle to PDF", "domain": "www.kindletopdf.com", "aliases": []string{}},
				{"name": "reddit.com", "domain": "reddit.com", "aliases": []string{}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LIMELIT_CLOUD_API", srv.URL)
	t.Setenv(provider.LimelitKeyEnv, "lmlt_test")
}

func kindleApp(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	_, db, h := newApp(t, providertest.Registry())
	post(t, h, "/setup/brand", url.Values{"name": {"kindletopdf.com"}, "domain": {"kindletopdf.com"}})
	ctx := context.Background()
	db.AddPrompt(ctx, store.Prompt{Text: "What is kindletopdf.com?", Branded: true, Active: true})
	db.AddCompetitor(ctx, store.Competitor{Name: "reddit.com", Domain: "reddit.com"})
	return db, h
}

// TestImportingFromCloudBringsItsPromptsCompetitorsAndName. The owner's Cloud
// prompts are written from the product's features; the local brand gains the
// name answers actually use; nothing is duplicated, the brand is never its
// own competitor, and the flash says what happened.
func TestImportingFromCloudBringsItsPromptsCompetitorsAndName(t *testing.T) {
	fakeCloudSetup(t, "kindletopdf.com")
	db, h := kindleApp(t)
	ctx := context.Background()

	if !strings.Contains(get(t, h, "/prompts").Body.String(), "Import from Limelit Cloud") {
		t.Fatal("the prompts page does not offer the import with a Cloud key set")
	}
	body := post(t, h, "/prompts/import-cloud", nil).Body.String()
	if !strings.Contains(body, "Imported 2 prompts and 1 competitor from Limelit Cloud") || !strings.Contains(body, "Kindle to PDF") {
		t.Errorf("flash: %s", flashOf(body))
	}
	prompts, _ := db.Prompts(ctx, true)
	if len(prompts) != 3 {
		t.Errorf("prompts = %d, want the existing one plus two", len(prompts))
	}
	competitors, _ := db.Competitors(ctx)
	domains := map[string]bool{}
	for _, c := range competitors {
		domains[c.Domain] = true
	}
	if len(competitors) != 2 || !domains["epubor.com"] || domains["kindletopdf.com"] {
		t.Errorf("competitors = %+v", competitors)
	}
	prop, _ := db.Property(ctx)
	if len(prop.Aliases) != 1 || prop.Aliases[0] != "Kindle to PDF" {
		t.Errorf("aliases = %v", prop.Aliases)
	}

	if body := post(t, h, "/prompts/import-cloud", nil).Body.String(); !strings.Contains(body, "already here") {
		t.Errorf("a second import: %s", flashOf(body))
	}
}

// TestImportRefusesAnotherBrandsAccount. A key for a Cloud account that
// tracks a different domain must not fill this brand with its prompts.
func TestImportRefusesAnotherBrandsAccount(t *testing.T) {
	fakeCloudSetup(t, "rafay.co")
	db, h := kindleApp(t)
	body := post(t, h, "/prompts/import-cloud", nil).Body.String()
	if !strings.Contains(body, "not kindletopdf.com, so nothing was imported") {
		t.Errorf("flash: %s", flashOf(body))
	}
	if prompts, _ := db.Prompts(context.Background(), true); len(prompts) != 1 {
		t.Errorf("prompts = %d after a refused import", len(prompts))
	}
}

// TestTheWizardCanStartFromTheCloudSetup: the import replaces the starter
// pack and moves on to connecting the engines.
func TestTheWizardCanStartFromTheCloudSetup(t *testing.T) {
	fakeCloudSetup(t, "kindletopdf.com")
	_, h := kindleApp(t)
	if !strings.Contains(get(t, h, "/setup/prompts").Body.String(), "/setup/prompts/import-cloud") {
		t.Fatal("the prompts step does not offer the import")
	}
	rec := post(t, h, "/setup/prompts/import-cloud", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup/provider" {
		t.Errorf("import = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestNoCloudKeyNoImportButton(t *testing.T) {
	t.Setenv(provider.LimelitKeyEnv, "")
	_, h := kindleApp(t)
	if strings.Contains(get(t, h, "/prompts").Body.String(), "Import from Limelit Cloud") {
		t.Error("the import is offered without a key")
	}
}

func TestImportSaysWhenAPassWouldPassTheCeiling(t *testing.T) {
	ok := importSentence(cloudImport{Prompts: 6, PassAnswers: 40, Ceiling: 200})
	if !strings.Contains(ok, "A pass now asks 40 questions.") || strings.Contains(ok, "refused") {
		t.Errorf("under the ceiling: %q", ok)
	}
	over := importSentence(cloudImport{Prompts: 150, PassAnswers: 750, Ceiling: 200})
	if !strings.Contains(over, "over your daily limit of 200") {
		t.Errorf("over the ceiling: %q", over)
	}
}
