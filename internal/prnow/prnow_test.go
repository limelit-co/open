// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package prnow_test

// The fixtures in testdata follow the shapes documented at
// https://prnow.io/api-docs. Nothing here touches the network.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/limelit-co/open/internal/prnow"
	"github.com/limelit-co/open/internal/prnow/prnowtest"
)

const key = "prnow_test_key"

func client(s *prnowtest.Server) *prnow.Client {
	c := prnow.New(key)
	c.BaseURL = s.BaseURL()
	return c
}

func validDraft() prnow.Draft {
	return prnow.Draft{
		Title:      "Example Co opens its first office in Berlin today",
		Content:    "<p>" + strings.Repeat("word ", 300) + "</p>",
		Categories: []string{"Business"},
		Country:    "Germany",
		City:       "Berlin",
		Plan:       "standard",
	}
}

func TestTestSendsTheKeyAsABearerTokenAndReadsTheAccount(t *testing.T) {
	s := prnowtest.New(t, key)
	acct, err := client(s).Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acct.Account.Company != "Example Co" || acct.Credits != 120 || acct.MaxSingleWalletCredits != 100 {
		t.Fatalf("account = %+v", acct)
	}
	if got := s.Requests()[0].Authorization; got != "Bearer "+key {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestAWrongKeyIsErrAuthAndNeverInTheError(t *testing.T) {
	s := prnowtest.New(t, key)
	c := client(s)
	c.Key = "prnow_wrong_secret"
	_, err := c.Test(context.Background())
	if !errors.Is(err, prnow.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "prnow_wrong_secret") {
		t.Fatalf("the key leaked into the error: %v", err)
	}
}

func TestSubmitSendsTheReleaseWithAnIdempotencyKey(t *testing.T) {
	s := prnowtest.New(t, key)
	rel, err := client(s).Submit(context.Background(), validDraft(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Ref() != "G9E1HJK" || rel.Status != "pending" {
		t.Fatalf("release = %+v", rel)
	}
	req := s.Requests()[0]
	if req.Method != "POST" || req.Path != "/submit" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if len(req.IdempotencyKey) != 36 {
		t.Fatalf("Idempotency-Key = %q, want a UUID", req.IdempotencyKey)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(req.Body), &sent); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"title", "content", "categories", "country", "city", "plan"} {
		if sent[field] == nil {
			t.Errorf("body has no %s: %s", field, req.Body)
		}
	}
	if _, ok := sent["api_key"]; ok {
		t.Error("the key belongs in the Authorization header, not the body")
	}
}

func TestAReplayedSubmitIsMarked(t *testing.T) {
	s := prnowtest.New(t, key)
	s.On("POST /submit", prnowtest.Reply{Status: 200, Fixture: "submit.json", Header: map[string]string{"Idempotent-Replayed": "true"}})
	rel, err := client(s).Submit(context.Background(), validDraft(), "fixed-key")
	if err != nil {
		t.Fatal(err)
	}
	if !rel.Replayed {
		t.Fatal("Replayed = false on an Idempotent-Replayed answer")
	}
	if got := s.Requests()[0].IdempotencyKey; got != "fixed-key" {
		t.Fatalf("Idempotency-Key = %q, want the one passed", got)
	}
}

func TestTheDocumentedFailuresMapToErrors(t *testing.T) {
	s := prnowtest.New(t, key)
	c := client(s)

	s.On("POST /submit", prnowtest.Reply{Status: 402, Fixture: "credits.json"})
	if _, err := c.Submit(context.Background(), validDraft(), ""); !errors.Is(err, prnow.ErrCredits) {
		t.Errorf("402: err = %v, want ErrCredits", err)
	}

	s.On("POST /submit", prnowtest.Reply{Status: 409, Fixture: "duplicate.json"})
	_, err := c.Submit(context.Background(), validDraft(), "")
	var apiErr *prnow.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != prnow.CodeDuplicate || !errors.Is(err, prnow.ErrConflict) {
		t.Fatalf("409: err = %v", err)
	}
	if apiErr.Existing == nil || apiErr.Existing.Ref() != "G9E1HJK" {
		t.Errorf("duplicate does not name the existing release: %+v", apiErr.Existing)
	}

	s.On("POST /submit", prnowtest.Reply{Status: 409, Fixture: "in_progress.json", Header: map[string]string{"Retry-After": "3"}})
	_, err = c.Submit(context.Background(), validDraft(), "")
	if !errors.As(err, &apiErr) || apiErr.Code != prnow.CodeInProgress || apiErr.RetryAfter != 3*time.Second {
		t.Errorf("in progress: err = %v, retry after %v", err, apiErr.RetryAfter)
	}

	s.On("GET /status/*", prnowtest.Reply{Status: 404, Fixture: "credits.json"})
	if _, err := c.Status(context.Background(), "NOPE"); !errors.Is(err, prnow.ErrNotFound) {
		t.Errorf("404: err = %v, want ErrNotFound", err)
	}
}

func TestStatusReadsPackagesKeyedByOutlet(t *testing.T) {
	s := prnowtest.New(t, key)
	rel, err := client(s).Status(context.Background(), "G9E1HJK")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Status != "in_progress" || len(rel.Packages) != 3 {
		t.Fatalf("release = %+v", rel)
	}
	a := rel.Packages[0]
	if a.Outlet != "news-outlet-a" || a.State != "live" || a.LiveURL == "" {
		t.Fatalf("first package = %+v (want outlets sorted, live with a URL)", a)
	}
	if c := rel.Packages[2]; c.State != "rejected" || c.CreditsRefunded != 4 {
		t.Fatalf("third package = %+v", c)
	}
	if got := s.Requests()[0].Path; got != "/status/G9E1HJK" {
		t.Fatalf("path = %q", got)
	}
}

func TestRetractAndCategoriesAndDraft(t *testing.T) {
	s := prnowtest.New(t, key)
	c := client(s)
	r, err := c.Retract(context.Background(), "G9E1HJK")
	if err != nil || r.CreditsRefunded != 19 || r.Status != "refunded" {
		t.Fatalf("retract = %+v, %v", r, err)
	}
	cats, err := c.Categories(context.Background())
	if err != nil || strings.Join(cats, ",") != "Business,Technology,Science" {
		t.Fatalf("categories = %v, %v", cats, err)
	}
	d, err := c.SaveDraft(context.Background(), validDraft(), "")
	if err != nil || d.Status != "draft" || d.Ref() != "D2RAFT1" {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	if _, err := c.Status(context.Background(), " "); err == nil {
		t.Fatal("an empty id was sent")
	}
}

func TestValidateCatchesEveryDocumentedLimitAtOnce(t *testing.T) {
	if p := prnow.Validate(validDraft()); !p.OK() {
		t.Fatalf("a valid draft failed: %v", p.Errors)
	}
	bad := prnow.Draft{
		Title:      "Too short",
		Content:    `<p>only a few words <a href="https://a">1</a><a href="https://b">2</a><a href="https://c">3</a><a href="https://d">4</a><a href="https://e">5</a><a href="https://f">6</a></p>`,
		Categories: []string{"A", "B", "C", "D", "E", "F"},
		MetaTitle:  strings.Repeat("x", 61),
		Summary:    strings.Repeat("w ", 50),
	}
	p := prnow.Validate(bad)
	want := []string{"title is 2 words", "content is", "6 links", "6 categories", "country is required", "city is required", "meta title"}
	all := strings.Join(p.Errors, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q in:\n%s", w, all)
		}
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "summary") {
		t.Errorf("warnings = %v", p.Warnings)
	}
}

func TestWordsIgnoresMarkup(t *testing.T) {
	if n := prnow.Words(`<p>one <strong>two</strong>&nbsp;three</p><img src="x">`); n != 3 {
		t.Fatalf("Words = %d, want 3", n)
	}
}

func TestHTMLFromText(t *testing.T) {
	got := prnow.HTMLFromText("First para\nstill first.\n\nSecond & last < here.")
	want := "<p>First para still first.</p>\n<p>Second &amp; last &lt; here.</p>"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if html := "<p>already</p>"; prnow.HTMLFromText(html) != html {
		t.Fatal("HTML input was rewritten")
	}
}

func TestIdempotencyKeysAreFreshUUIDs(t *testing.T) {
	a, b := prnow.NewIdempotencyKey(), prnow.NewIdempotencyKey()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Fatalf("keys %q %q", a, b)
	}
}
